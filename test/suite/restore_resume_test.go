package suite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// RFC 0051 P1: `morzer restore --resume` continues a restore from the step
// that was cut off, without restoring again what already went back.

// restoreHarness is an applied installation with one backup to restore.
func restoreHarness(t *testing.T) (*harness, domain.Installation, ports.BackupRef) {
	t.Helper()
	h := newHarness(t)
	// Ids in the order they are minted, as a running clock mints them: the
	// harness's clock is frozen, and the journal's newest-first order falls
	// back on the id within a second.
	minted := 0
	h.Deps.NewOpID = func(time.Time) string {
		minted++
		return fmt.Sprintf("op_%06d", minted)
	}
	inst := h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{Reason: "manual"})
	require.NoError(t, err)
	refs, err := h.Backup.List(context.Background())
	require.NoError(t, err)
	return h, inst, refs[0]
}

// interruptRestore runs a restore that a ctrl-C cuts off as it enters a part
// of the backup.
func interruptRestore(
	t *testing.T, h *harness, inst domain.Installation, part ports.RestorePart, opts ops.RestoreOptions,
) (ops.Result, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Backup.OnRestore = func(ctx context.Context, o ports.RestoreOptions) error {
		if o.Part != part {
			return nil
		}
		cancel()
		return ctx.Err()
	}
	defer func() { h.Backup.OnRestore = nil }()

	opts.Force = true
	opts.ConfirmedInstallationID = inst.ID
	result, err := ops.Restore(ctx, h.Deps, opts)
	require.Error(t, err)
	require.Equal(t, domain.StatusInterrupted, result.Record.Status)
	return result, err
}

// interruptReapply cuts a restore off while it brings the release back up,
// after both data steps finished.
func interruptReapply(t *testing.T, h *harness, inst domain.Installation) (ops.Result, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Runtime.OnCall = func(method string) {
		if method == "Up" {
			cancel()
		}
	}
	h.Runtime.Fail["Up"] = context.Canceled
	defer func() {
		h.Runtime.OnCall = nil
		delete(h.Runtime.Fail, "Up")
	}()

	result, err := ops.Restore(ctx, h.Deps, ops.RestoreOptions{
		Options: ops.Options{Force: true}, ConfirmedInstallationID: inst.ID,
	})
	require.Error(t, err)
	require.Equal(t, domain.StatusInterrupted, result.Record.Status)
	return result, err
}

func resumeRestore(h *harness, inst domain.Installation) (ops.Result, error) {
	return ops.Restore(context.Background(), h.Deps, ops.RestoreOptions{
		Options:                 ops.Options{Force: true, Resume: true},
		ConfirmedInstallationID: inst.ID,
	})
}

// The case it exists for: hours of data back, then the session drops. The
// resume brings the release up and runs the smoke test, and restores nothing
// again. It continues the record, so the statement it files replaces the
// interrupted run's.
func TestAResumeAfterTheDataWentBackDoesNotRestoreItAgain(t *testing.T) {
	h, inst, _ := restoreHarness(t)
	before := statements(t, h)
	interrupted, err := interruptReapply(t, h, inst)
	require.Equal(t, []ports.RestorePart{ports.RestoreVolumes, ports.RestoreHook}, h.Backup.Restores)
	hint := domain.AsError(err).Hint
	assert.Contains(t, hint, "morzer restore --resume --force --confirm "+inst.ID)
	assert.Contains(t, hint, "`morzer apply` starts the current release instead and abandons the restore")

	// Done once, the checks of the backup are not done again: a backup
	// gone since must not fail a resume that no longer reads it.
	h.Backup.Fail["Verify"] = domain.BackupError(domain.ErrNotFound, "injected: pruned")
	h.Backup.Fail["Inspect"] = domain.BackupError(domain.ErrNotFound, "injected: pruned")
	resumed, err := resumeRestore(h, inst)

	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, resumed.Record.Status)
	assert.Equal(t, interrupted.Record.ID, resumed.Record.ID, "the resume started a record of its own")
	assert.Equal(t, []ports.RestorePart{ports.RestoreVolumes, ports.RestoreHook}, h.Backup.Restores,
		"the resume restored the data again")
	for _, s := range resumed.Record.Steps {
		assert.Equal(t, domain.StepSucceeded, s.Status, "step %s", s.ID)
	}

	assert.ElementsMatch(t, append(before, interrupted.Record.ID+".json"), statements(t, h),
		"the resume did not file its statement over the interrupted run's")
}

// statements names the attestations on disk.
func statements(t *testing.T, h *harness) []string {
	t.Helper()
	entries, err := os.ReadDir(h.Paths.AttestationsDir())
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			out = append(out, e.Name())
		}
	}
	return out
}

// A volume half-written is wiped and written again from the start.
func TestAResumeInterruptedWritingVolumesWritesThemAgain(t *testing.T) {
	h, inst, _ := restoreHarness(t)
	_, err := interruptRestore(t, h, inst, ports.RestoreVolumes, ops.RestoreOptions{})
	assert.Contains(t, domain.AsError(err).Hint, "restore --resume")

	resumed, err := resumeRestore(h, inst)

	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, resumed.Record.Status)
	assert.Equal(t,
		[]ports.RestorePart{ports.RestoreVolumes, ports.RestoreVolumes, ports.RestoreHook},
		h.Backup.Restores)
}

// Nothing tells how far a hook got, so a restore cut off there is not resumed:
// the hint says so and names a fresh restore, and the resume is refused before
// anything runs.
func TestARestoreInterruptedInItsHookIsNotResumed(t *testing.T) {
	h, inst, ref := restoreHarness(t)
	_, err := interruptRestore(t, h, inst, ports.RestoreHook, ops.RestoreOptions{})
	hint := domain.AsError(err).Hint
	assert.Contains(t, hint, "would be refused")
	assert.Contains(t, hint, "morzer restore --backup "+ref.ID+" --force --confirm "+inst.ID)
	runs := len(h.Backup.Restores)

	_, err = resumeRestore(h, inst)

	require.Error(t, err)
	assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
	assert.Len(t, h.Backup.Restores, runs, "a refused resume restored something")
}

// The backup, the components and the cross-installation flag come from the
// record, and a flag that would name another restore is refused.
func TestAResumeRestoresWhatTheRestoreWasStartedWith(t *testing.T) {
	h, inst, older := restoreHarness(t)
	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{Reason: "manual"})
	require.NoError(t, err)

	interrupted, _ := interruptRestore(t, h, inst, ports.RestoreVolumes, ops.RestoreOptions{
		BackupID:               older.ID,
		Components:             []ports.Component{ports.ComponentDatabase, ports.ComponentVolumes},
		AllowCrossInstallation: true,
	})
	assert.Equal(t, older.ID, interrupted.Record.Flags["backup_id"])

	for name, opts := range map[string]ops.RestoreOptions{
		"--backup":                   {BackupID: older.ID},
		"--component":                {Components: []ports.Component{ports.ComponentDatabase}},
		"--allow-cross-installation": {AllowCrossInstallation: true},
	} {
		opts.Options = ops.Options{Force: true, Resume: true}
		opts.ConfirmedInstallationID = inst.ID
		_, err := ops.Restore(context.Background(), h.Deps, opts)
		require.Error(t, err, name)
		assert.Equal(t, domain.ExitUsage, domain.ExitCode(err), name)
		assert.Contains(t, err.Error(), name)
	}

	var asked []ports.RestoreOptions
	h.Backup.OnRestore = func(_ context.Context, o ports.RestoreOptions) error {
		asked = append(asked, o)
		return nil
	}
	resumed, err := resumeRestore(h, inst)

	require.NoError(t, err)
	assert.Equal(t, older.ID, resumed.Record.Flags["backup_id"])
	require.Len(t, asked, 2)
	for _, o := range asked {
		assert.Equal(t, []ports.Component{ports.ComponentDatabase, ports.ComponentVolumes}, o.Components)
		assert.True(t, o.Force, "the cross-installation authorisation was not carried")
	}
}

// A fresh restore still runs over an unfinished one: it starts a record of its
// own, and the old one is left for --clear-intervention (RFC 0051 decision
// 14).
func TestAFreshRestoreRunsOverAnUnfinishedOne(t *testing.T) {
	h, inst, _ := restoreHarness(t)
	interrupted, _ := interruptRestore(t, h, inst, ports.RestoreHook, ops.RestoreOptions{})

	fresh, err := ops.Restore(context.Background(), h.Deps, ops.RestoreOptions{
		Options: ops.Options{Force: true}, ConfirmedInstallationID: inst.ID,
	})

	require.NoError(t, err)
	assert.NotEqual(t, interrupted.Record.ID, fresh.Record.ID)
	assert.Equal(t, domain.StatusSucceeded, fresh.Record.Status)
}

// A resume is confirmed again, as the restore was.
func TestAResumeIsConfirmedAgain(t *testing.T) {
	h, inst, _ := restoreHarness(t)
	_, _ = interruptRestore(t, h, inst, ports.RestoreVolumes, ops.RestoreOptions{})

	for _, opts := range []ops.RestoreOptions{
		{Options: ops.Options{Resume: true}, ConfirmedInstallationID: inst.ID},
		{Options: ops.Options{Resume: true, Force: true}},
	} {
		_, err := ops.Restore(context.Background(), h.Deps, opts)
		require.Error(t, err)
		assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
	}
	assert.Equal(t, []ports.RestorePart{ports.RestoreVolumes}, h.Backup.Restores)
}

// Anything but a backup since the restore began refuses the resume, naming a
// fresh restore: an apply may have written to the volumes already put back,
// and a stop changes the steps.
func TestAResumeIsRefusedOverAnOperationThatCameAfter(t *testing.T) {
	for name, tc := range map[string]struct {
		between func(t *testing.T, h *harness)
		refused bool
	}{
		"apply": {func(t *testing.T, h *harness) {
			_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
			require.NoError(t, err)
		}, true},
		"stop": {func(t *testing.T, h *harness) {
			_, err := ops.Stop(context.Background(), h.Deps, ops.StopOptions{})
			require.NoError(t, err)
		}, true},
		"backup": {func(t *testing.T, h *harness) {
			_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{Reason: "manual"})
			require.NoError(t, err)
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			h, inst, ref := restoreHarness(t)
			_, _ = interruptReapply(t, h, inst)
			tc.between(t, h)

			_, err := resumeRestore(h, inst)

			if !tc.refused {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
			assert.Contains(t, err.Error(), name)
			assert.Contains(t, domain.AsError(err).Hint, "morzer restore --backup "+ref.ID)
		})
	}
}

// A restore that crashed blocks every converge, and the gate names the command
// that finishes it -- unless it crashed in the hook, which the resume refuses.
func TestTheGateNamesTheResumeOfAnUnfinishedRestore(t *testing.T) {
	for name, tc := range map[string]struct {
		steps  []domain.StepRecord
		resume bool
	}{
		"in the volumes": {[]domain.StepRecord{
			{ID: "verify-backup", Status: domain.StepSucceeded, Restartable: true},
			{ID: "restore-volumes", Status: domain.StepRunning, Restartable: true},
		}, true},
		"in the hook": {[]domain.StepRecord{
			{ID: "restore-volumes", Status: domain.StepSucceeded, Restartable: true},
			{ID: "restore-data", Status: domain.StepRunning},
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			inst := h.install()
			h.setHookEnv()
			applyBaseline(t, h)
			require.NoError(t, h.Deps.State.AppendOperation(context.Background(), domain.OperationRecord{
				ID: "op-crashed", Type: domain.OpTypeRestore, Status: domain.StatusRunning,
				StartedAt: domain.NewTime(time.Now()), InstallationID: inst.ID, Steps: tc.steps,
			}))

			_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})

			require.Error(t, err)
			named := "morzer restore --resume --force --confirm " + inst.ID
			if tc.resume {
				assert.Contains(t, err.Error()+domain.AsError(err).Hint, named)
			} else {
				assert.NotContains(t, err.Error()+domain.AsError(err).Hint, "--resume")
			}
		})
	}
}

// A restore that crashed while it wrote the volumes blocks every converge until
// a person looks; once they cleared it, the resume writes the volumes again
// and finishes.
func TestAClearedRestoreResumes(t *testing.T) {
	h, inst, _ := restoreHarness(t)
	interrupted, _ := interruptRestore(t, h, inst, ports.RestoreVolumes, ops.RestoreOptions{})
	crashed := interrupted.Record
	crashed.Status = domain.StatusRunning
	crashed.Steps[3].Status = domain.StepRunning
	require.Equal(t, "restore-volumes", crashed.Steps[3].ID)
	require.NoError(t, h.Deps.State.AppendOperation(context.Background(), crashed))

	_, err := ops.ClearIntervention(context.Background(), h.Deps, crashed.ID)
	require.NoError(t, err)
	resumed, err := resumeRestore(h, inst)

	require.NoError(t, err)
	assert.Equal(t, crashed.ID, resumed.Record.ID)
	assert.Equal(t,
		[]ports.RestorePart{ports.RestoreVolumes, ports.RestoreVolumes, ports.RestoreHook},
		h.Backup.Restores)
}

// Retention keeps the backup an unfinished restore reads, including one waiting
// on intervention.
func TestRetentionKeepsTheBackupAnUnfinishedRestoreReads(t *testing.T) {
	for name, interrupt := range map[string]func(t *testing.T, h *harness, inst domain.Installation){
		"interrupted": func(t *testing.T, h *harness, inst domain.Installation) {
			_, _ = interruptRestore(t, h, inst, ports.RestoreVolumes, ops.RestoreOptions{})
		},
		"waiting on intervention": func(t *testing.T, h *harness, inst domain.Installation) {
			h.Backup.OnRestore = func(context.Context, ports.RestoreOptions) error {
				return domain.BackupError(nil, "injected")
			}
			defer func() { h.Backup.OnRestore = nil }()
			result, err := ops.Restore(context.Background(), h.Deps, ops.RestoreOptions{
				Options: ops.Options{Force: true}, ConfirmedInstallationID: inst.ID,
			})
			require.Error(t, err)
			require.Equal(t, domain.StatusManualIntervention, result.Record.Status)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, inst, read := restoreHarness(t)
			inst.Policy.RetainBackups = 1
			require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
			interrupt(t, h, inst)

			for range 2 {
				_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{Reason: "manual", Prune: true})
				require.NoError(t, err)
			}

			assert.Contains(t, backupIDs(t, h), read.ID, "retention pruned the backup the restore reads")
			assert.Len(t, backupIDs(t, h), 2, "more than one backup outlived the policy")
		})
	}
}

// Once the restore finishes, its backup is pruned like any other.
func TestRetentionLetsTheBackupGoOnceTheRestoreFinishes(t *testing.T) {
	h, inst, read := restoreHarness(t)
	inst.Policy.RetainBackups = 1
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
	_, _ = interruptRestore(t, h, inst, ports.RestoreVolumes, ops.RestoreOptions{})
	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{Reason: "manual", Prune: true})
	require.NoError(t, err)
	require.Contains(t, backupIDs(t, h), read.ID)

	_, err = resumeRestore(h, inst)
	require.NoError(t, err)
	_, err = ops.Backup(context.Background(), h.Deps, ops.BackupOptions{Reason: "manual", Prune: true})
	require.NoError(t, err)

	assert.NotContains(t, backupIDs(t, h), read.ID)
}

func backupIDs(t *testing.T, h *harness) []string {
	t.Helper()
	refs, err := h.Backup.List(context.Background())
	require.NoError(t, err)
	ids := make([]string, len(refs))
	for i, r := range refs {
		ids[i] = r.ID
	}
	return ids
}
