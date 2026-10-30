package suite

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// RFC 0040: `morzer stop` keeps a product down across a reboot and an
// unattended update until `morzer start`.

// stoppedHarness is an applied installation an operator has stopped.
func stoppedHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	_, err := ops.Stop(context.Background(), h.Deps, ops.StopOptions{Reason: "moving to a new disk"})
	require.NoError(t, err)
	return h
}

func (h *harness) stopRecord() *domain.Stop {
	h.t.Helper()
	inst, err := h.Deps.State.LoadInstallation(context.Background())
	require.NoError(h.t, err)
	return inst.Stopped
}

// callsSince is what the runtime was asked to do after the first n calls.
func (h *harness) callsSince(n int) []string {
	return slices.Clone(h.Runtime.Calls[n:])
}

func TestStopRecordsTheStopAndNeverReachesTheVolumes(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	before := len(h.Runtime.Calls)

	res, err := ops.Stop(context.Background(), h.Deps, ops.StopOptions{Reason: "moving to a new disk"})
	require.NoError(t, err)
	assert.Contains(t, res.Summary, "stays down until `morzer start`")

	stop := h.stopRecord()
	require.NotNil(t, stop, "the stop was not recorded")
	assert.Equal(t, "moving to a new disk", stop.Reason)
	assert.Equal(t, h.Release.Version(), stop.Release)
	assert.Equal(t, res.Record.ID, stop.OperationID)

	calls := h.callsSince(before)
	assert.Contains(t, calls, "Stop:", "the whole project was not stopped")
	// Decision 1, LOCKED: the volumes are out of reach because Down is.
	assert.NotContains(t, calls, "Down")
	assert.NotContains(t, calls, "Down(volumes)")
	for name, svc := range h.Runtime.Services {
		assert.False(t, svc.Running(), "%s is still running", name)
	}
}

// Boot is an `apply --startup`, and it is what brought a stopped product back
// before this: it must leave it down, succeed, and not touch the runtime.
func TestBootLeavesAStoppedProductDown(t *testing.T) {
	h := stoppedHarness(t)
	before := len(h.Runtime.Calls)

	res, err := ops.Apply(context.Background(), h.Deps, ops.Options{Startup: true})
	require.NoError(t, err, "boot must exit 0 while stopped")
	assert.Contains(t, res.Summary, "stopped by the operator since")
	assert.Contains(t, res.Summary, "moving to a new disk")
	assert.Empty(t, h.callsSince(before), "boot called the runtime while stopped")
	assert.NotNil(t, h.stopRecord())
}

// Decisions 3 and 5: each of these would start the product, and is refused
// with exit 2 and the way back rather than undoing the stop.
func TestWhatWouldStartAStoppedProductIsRefused(t *testing.T) {
	h := stoppedHarness(t)
	ctx := context.Background()
	before := len(h.Runtime.Calls)

	for name, run := range map[string]func() error{
		"apply": func() error { _, err := ops.Apply(ctx, h.Deps, ops.Options{}); return err },
		"update": func() error {
			_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{To: "1.2.0"})
			return err
		},
		"rollback": func() error { _, err := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{}); return err },
		"task run": func() error {
			_, err := ops.RunTask(ctx, h.Deps, ops.TaskOptions{Name: "migrate"})
			return err
		},
	} {
		err := run()
		require.Error(t, err, "%s ran while stopped", name)
		assert.Equal(t, domain.ExitUsage, domain.ExitCode(err), "%s: %v", name, err)
		assert.Contains(t, domain.AsError(err).Hint, "morzer start", name)
	}
	assert.Empty(t, h.callsSince(before), "a refused operation called the runtime")
	assert.NotNil(t, h.stopRecord())
}

func TestStartConvergesThenClearsTheStop(t *testing.T) {
	h := stoppedHarness(t)
	ctx := context.Background()
	ups := h.Runtime.UpCount

	res, err := ops.Start(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Contains(t, res.Summary, "started")
	assert.Greater(t, h.Runtime.UpCount, ups, "start did not converge the release")
	assert.Nil(t, h.stopRecord(), "start left the stop recorded")
	assert.Equal(t, domain.OpTypeStart, res.Record.Type)
	assert.Equal(t, "clear-stop", res.Record.Steps[len(res.Record.Steps)-1].ID,
		"the stop must be cleared last, once the release has converged")

	// A script may call it twice.
	again, err := ops.Start(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Contains(t, again.Summary, "was not stopped")
}

// Cleared only once `apply` has converged: a start that fails keeps boot and
// the update timer away from a half-converged machine.
func TestAFailedStartKeepsTheStop(t *testing.T) {
	h := stoppedHarness(t)
	ctx := context.Background()
	h.Runtime.Fail = map[string]error{"Up": domain.RuntimeError(nil, "the daemon refused")}

	_, err := ops.Start(ctx, h.Deps, ops.Options{})
	require.Error(t, err)
	require.NotNil(t, h.stopRecord(), "a failed start cleared the stop")

	h.Runtime.Fail = nil
	before := len(h.Runtime.Calls)
	_, err = ops.Apply(ctx, h.Deps, ops.Options{Startup: true})
	require.NoError(t, err)
	assert.Empty(t, h.callsSince(before), "boot after a failed start brought the product up")
}

// The record is written first, so a stop killed between its steps leaves a
// product marked stopped and still running, which the next boot leaves alone.
func TestAStopKilledBetweenItsStepsLeavesTheRecord(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Runtime.OnCall = func(method string) {
		if method == "Stop" {
			cancel()
		}
	}
	h.Runtime.Fail = map[string]error{"Stop": context.Canceled}

	res, err := ops.Stop(ctx, h.Deps, ops.StopOptions{})
	require.Error(t, err)
	require.Equal(t, domain.StatusInterrupted, res.Record.Status)
	require.NotNil(t, h.stopRecord(), "the record was not written before the runtime call")
	for name, svc := range h.Runtime.Services {
		assert.True(t, svc.Running(), "%s was stopped by a stop that never reached the runtime", name)
	}

	h.Runtime.OnCall, h.Runtime.Fail = nil, nil
	before := len(h.Runtime.Calls)
	_, err = ops.Apply(context.Background(), h.Deps, ops.Options{Startup: true})
	require.NoError(t, err)
	assert.Empty(t, h.callsSince(before), "boot touched a product marked stopped")

	// Running services under a stop record are still a stopped product to
	// `config set`: re-creating them would be starting what the operator
	// asked to keep down.
	ups := h.Runtime.UpCount
	_, err = ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"log_level": "debug"},
	})
	require.NoError(t, err)
	assert.Equal(t, ups, h.Runtime.UpCount, "config set re-created services under a stop")

	// And --resume finishes it, which the unfinished record otherwise
	// blocks.
	_, err = ops.Stop(context.Background(), h.Deps, ops.StopOptions{Options: ops.Options{Resume: true}})
	require.NoError(t, err)
	for name, svc := range h.Runtime.Services {
		assert.False(t, svc.Running(), "%s is still running after the stop was resumed", name)
	}
}

func TestNothingAppliedIsNothingToStop(t *testing.T) {
	h := newHarness(t)
	h.install()
	// A journal with an operation in it, none of which converged.
	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{Reason: "manual"})
	_ = err

	_, err = ops.Stop(context.Background(), h.Deps, ops.StopOptions{})
	require.Error(t, err)
	assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
	assert.Nil(t, h.stopRecord())
}

func TestAReasonIsOneBoundedLine(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	for _, reason := range []string{"two\nlines", string(make([]byte, 201))} {
		_, err := ops.Stop(context.Background(), h.Deps, ops.StopOptions{Reason: reason})
		require.Error(t, err)
		assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
	}
	assert.Nil(t, h.stopRecord())
}

// Decision 3: restoring during a maintenance window is the natural use, and it
// leaves the product stopped. Nothing re-applies or brings it up.
func TestARestoreWhileStoppedLeavesItStopped(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()
	_, err := ops.Backup(ctx, h.Deps, ops.BackupOptions{Reason: "manual"})
	require.NoError(t, err)
	_, err = ops.Stop(ctx, h.Deps, ops.StopOptions{})
	require.NoError(t, err)
	ups := h.Runtime.UpCount

	res, err := ops.Restore(ctx, h.Deps, ops.RestoreOptions{
		Options:                 ops.Options{Force: true},
		ConfirmedInstallationID: inst.ID,
	})
	require.NoError(t, err)
	assert.Contains(t, res.Summary, "stays stopped")
	assert.Equal(t, ups, h.Runtime.UpCount, "a restore brought a stopped product up")
	assert.NotNil(t, h.stopRecord())
}

// Decision 4, and the unit's `--unattended` that tells the two apart.
func TestTheScheduledBackupIsSkippedWhileStoppedAndAnExplicitOneRuns(t *testing.T) {
	h := stoppedHarness(t)
	ctx := context.Background()
	before, err := h.Backup.List(ctx)
	require.NoError(t, err)

	res, err := ops.Backup(ctx, h.Deps, ops.BackupOptions{Reason: "manual", Unattended: true})
	require.NoError(t, err, "a skipped scheduled backup must exit 0")
	assert.Contains(t, res.Summary, "scheduled backup skipped")
	after, err := h.Backup.List(ctx)
	require.NoError(t, err)
	assert.Len(t, after, len(before), "the timer's backup ran while stopped")

	_, err = ops.Backup(ctx, h.Deps, ops.BackupOptions{Reason: "manual"})
	require.NoError(t, err)
	after, err = h.Backup.List(ctx)
	require.NoError(t, err)
	assert.Len(t, after, len(before)+1, "an operator's backup did not run while stopped")
}

// recoverableUpdate makes the channel's 1.3.0 one the unattended gate may
// install, as TestAnUnattendedTickInstallsWhatDeclaresItselfRecoverable does.
func recoverableUpdate(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	patched := strings.Replace(string(raw), "database_schema_produces: 14", "database_schema_produces: 12", 1)
	require.NotEqual(t, string(raw), patched, "the fixture no longer declares what this patches")
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))
}

// Decision 9: the timer stages and never installs while stopped, whatever
// auto_apply says; `start` does not install what it staged, and the first tick
// after it does.
func TestTheUpdateTimerStagesWhileStoppedAndInstallsAfterStart(t *testing.T) {
	h, _, _, _ := followingHarnessWith(t, recoverableUpdate)
	h.setHookEnv()
	applyBaseline(t, h)
	armAutoApply(t, h)
	ctx := context.Background()
	_, err := ops.Stop(ctx, h.Deps, ops.StopOptions{})
	require.NoError(t, err)

	res, err := ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err, "a tick while stopped must exit 0")
	assert.False(t, res.Applied, "the timer installed while stopped")
	assert.Contains(t, res.Assessment.Why(), "stopped by the operator")
	candidate, err := h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	require.True(t, candidate.IsStaged(), "the release was not staged")

	_, err = ops.Start(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", current.Version.String(), "start installed the staged release")
	candidate, err = h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	assert.True(t, candidate.IsStaged(), "start dropped the staged release")

	res, err = ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)
	assert.True(t, res.Applied, "the first tick after start did not install: %s", res.Assessment.Why())
}

// A parameter change is stored and applied by `start`, not by re-creating the
// services the operator stopped.
func TestAConfigChangeWhileStoppedStartsNothing(t *testing.T) {
	h := stoppedHarness(t)
	ups := h.Runtime.UpCount

	res, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"log_level": "debug"},
	})
	require.NoError(t, err)
	assert.Contains(t, res.Summary, "morzer start")
	assert.Equal(t, ups, h.Runtime.UpCount, "config set brought a stopped service up")
}

// Every reader tells "down on purpose" from "broken" (decisions 7, 10, 12).
func TestStatusDoctorAndLsSayTheProductIsStopped(t *testing.T) {
	h := stoppedHarness(t)
	ctx := context.Background()

	status, err := ops.GetStatus(ctx, h.Deps)
	require.NoError(t, err)
	require.NotNil(t, status.Stopped)
	assert.Equal(t, "moving to a new disk", status.Stopped.Reason)
	assert.Contains(t, status.Next, "start")

	report, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	for _, res := range report.Results {
		switch res.ID {
		case "runtime.services", "runtime.health":
			assert.Equal(t, events.CheckOK, res.Status, "%s: %s", res.ID, res.Message)
			assert.Contains(t, res.Message, "stopped by the operator", res.ID)
		}
	}

	h.Deps.StateFor = func(p domain.Paths) ports.StateStore { return state.New(p) }
	entries, err := ops.ListInstallations(ctx, h.Deps, ops.ListOptions{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].Stopped)
	assert.False(t, entries[0].Stopped.At.IsZero())
}

// A repair is not a start: it keeps the stop, and only `morzer start` clears
// one.
func TestARepairKeepsTheStop(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()
	_, recoveryPub := generateRecoveryKey(t)
	h := initOriginMachine(t, ctx, recoveryPub)

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Stopped = &domain.Stop{At: domain.NewTime(h.Deps.Now()), Reason: "maintenance", OperationID: "op_stop"}
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{Product: "demo", Repair: true, RecoveryRecipient: recoveryPub})
	require.NoError(t, err)
	after, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Equal(t, inst.Stopped, after.Stopped, "a repair cleared the stop")
}

// An export carries the whole installation, and the stop in it belongs to the
// machine that was stopped: the rebuilt one is started by the `apply` that
// follows the import, which a carried stop would refuse.
func TestAnImportDoesNotCarryTheStop(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()
	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)

	inst, err := origin.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Stopped = &domain.Stop{At: domain.NewTime(origin.Deps.Now()), OperationID: "op_stop"}
	require.NoError(t, origin.Deps.State.SaveInstallation(ctx, inst))

	exportPath := filepath.Join(t.TempDir(), "demo.export.yaml")
	_, err = ops.Export(ctx, origin.Deps, ops.ExportOptions{Path: exportPath})
	require.NoError(t, err)
	export, err := ops.LoadExport(exportPath)
	require.NoError(t, err)
	require.NotNil(t, export.Installation.Stopped, "the export no longer carries the stop this guards against")

	rebuilt := newMachine(t, t.TempDir())
	_, err = ops.Import(ctx, rebuilt.Deps, ops.ImportOptions{
		SourcePath: exportPath, Export: export, IdentityFile: recoveryPath,
	})
	require.NoError(t, err)
	got, err := rebuilt.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Nil(t, got.Stopped, "the import carried the old machine's stop")
}

// A `stop` that commits while `config set` waits for the lock survives it.
// `config set` saves a whole installation, so steps built from the copy it read
// before the lock would write the stop record away, and the next boot would
// start the product.
func TestAStopThatLandsWhileConfigSetWaitsIsKept(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	landed := false
	h.Locker.BeforeAcquire = func() {
		if landed {
			return
		}
		landed = true
		inst, err := h.Deps.State.LoadInstallation(ctx)
		require.NoError(t, err)
		inst.Stopped = &domain.Stop{At: domain.NewTime(h.Deps.Now()), OperationID: "op_stop"}
		require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	}
	ups := h.Runtime.UpCount

	_, err := ops.ConfigSet(ctx, h.Deps, ops.ConfigSetOptions{Set: map[string]string{"log_level": "debug"}})
	require.NoError(t, err)
	require.NotNil(t, h.stopRecord(), "config set wrote away a stop that landed while it waited")
	assert.Equal(t, ups, h.Runtime.UpCount, "config set re-created services under a stop that landed while it waited")

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Equal(t, "debug", inst.Parameters["log_level"], "the change itself was lost")
}
