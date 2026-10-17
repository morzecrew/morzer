package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// stageUpgradeSource copies the 1.3.0 fixture somewhere the harness can update
// from, retargeting its absolute paths into the test root exactly as the
// harness does for the installed release.
func stageUpgradeSource(t *testing.T, h *harness) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "bundle-1.3.0")
	copyBundle(t, filepath.Join(testBundlePath(t), "..", "bundle-1.3.0"), src)
	retargetManifest(t, src, h.Root)
	return src
}

// stageRecoverableUpgradeSource is the 1.3.0 fixture with migrations that stop
// at schema 12, which 1.2.0 can still read, so a failure after them may still
// compensate back. The fixture as shipped migrates to 14, past the point of no
// return.
func stageRecoverableUpgradeSource(t *testing.T, h *harness) string {
	t.Helper()
	src := stageUpgradeSource(t, h)
	for file, edit := range map[string][2]string{
		"hooks/migrate": {"target=14", "target=12"},
		"manifest.yaml": {"database_schema_produces: 14", "database_schema_produces: 12"},
	} {
		path := filepath.Join(src, file)
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		patched := strings.Replace(string(raw), edit[0], edit[1], 1)
		require.NotEqual(t, string(raw), patched, "%s no longer says %q", file, edit[0])
		require.NoError(t, os.WriteFile(path, []byte(patched), 0o755))
	}
	return src
}

// applyBaseline brings the harness to a converged 1.2.0 deployment, which is
// the state every update test starts from.
func applyBaseline(t *testing.T, h *harness) {
	t.Helper()
	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
}

func TestUpdateMovesBetweenReleases(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	src := stageUpgradeSource(t, h)
	ctx := context.Background()

	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)

	// The journal records the transition, which is most of why `update`
	// exists rather than an operator editing the symlink.
	assert.Equal(t, domain.OpTypeUpdate, result.Record.Type)
	assert.Equal(t, "1.2.0", result.Record.From.String())
	assert.Equal(t, "1.3.0", result.Record.To.String())

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String())

	previous, err := h.Deps.State.PreviousRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", previous.Version.String(),
		"the displaced release must become previous, or rollback has nothing to return to")

	// Staged into the release store, and the symlink follows.
	assert.DirExists(t, filepath.Join(h.Paths.ReleasesDir(), "1.3.0"))
	target, err := os.Readlink(h.Paths.CurrentLink())
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(h.Paths.ReleasesDir(), "1.3.0"), target)
}

func TestUpdateTakesAPreUpdateBackup(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.NoError(t, err)

	backups, err := h.Deps.Backup.List(ctx)
	require.NoError(t, err)
	require.Len(t, backups, 1, "an update must back up before it changes anything")

	manifest, err := h.Deps.Backup.Inspect(ctx, backups[0])
	require.NoError(t, err)
	assert.Equal(t, "pre-update", manifest.Reason,
		"the reason is what exempts this backup from retention pruning")
	assert.Equal(t, "1.2.0", manifest.Labels["from"])
	assert.Equal(t, "1.3.0", manifest.Labels["to"])
}

func TestUpdateSkipBackupIsRecorded(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{
		Options: ops.Options{Force: true, SkipBackup: true},
		Ref:     stageUpgradeSource(t, h),
	})
	require.NoError(t, err)

	backups, err := h.Deps.Backup.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, backups)

	assert.Equal(t, "true", result.Record.Flags["skip_backup"],
		"an incident review must be able to see the choice was made")
}

func TestUpdateRefusesAnIncompatibleRelease(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	// 1.3.0 declares upgrade_from ">=1.2.0". Pretend the installed release
	// is older than that.
	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	current.Version = domain.MustParseVersion("1.0.0")
	require.NoError(t, h.Deps.State.SetCurrentRelease(ctx, current))

	h.Runtime.Calls = nil

	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.Error(t, err)
	assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err))

	assert.Empty(t, h.Runtime.Calls,
		"an incompatible release must be refused before anything is pulled or started")
	assert.NoDirExists(t, filepath.Join(h.Paths.ReleasesDir(), "1.3.0"),
		"and before it is staged")
}

func TestUpdateForceDoesNotBypassCompatibility(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	current.Version = domain.MustParseVersion("1.0.0")
	require.NoError(t, h.Deps.State.SetCurrentRelease(ctx, current))

	// --force authorises destructive actions, not incorrect ones. A release
	// stating it cannot be installed over 1.0.0 is stating a fact about its
	// migrations.
	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{
		Options: ops.Options{Force: true},
		Ref:     stageUpgradeSource(t, h),
	})
	require.Error(t, err)
	assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err))
}

func TestUpdateRefusesADigestMismatch(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref:          stageUpgradeSource(t, h),
		ExpectDigest: "sha256:" + "00000000000000000000000000000000000000000000000000000000000000ff",
	})
	require.Error(t, err)
	assert.Equal(t, domain.ExitUsage, domain.ExitCode(err),
		"a bundle that is not what was published must be refused before it is staged")
}

// TestTheInstallationCanTurnOffThePreUpdateBackup.
//
// The policy field existed, was written by init, was compared by doctor, and
// was read by nothing that decides anything -- an unhooked intent-guard. It was
// also spelled in the unsafe direction: BackupBeforeUpdate, whose zero value
// meant "do not back up", so a record written before the field existed or a
// hand-edited file turned the backup off by omission.
func TestTheInstallationCanTurnOffThePreUpdateBackup(t *testing.T) {
	// Two machines rather than two updates on one: the second update would
	// have to be 1.3.0 over itself, and a same-version update short-circuits
	// enough that "no backup was taken" would prove nothing. Each of these
	// is the same real 1.2.0 → 1.3.0 transition, differing only in the
	// policy under test.
	converged := func(t *testing.T) *harness {
		t.Helper()
		h := newHarness(t)
		h.install()
		h.setHookEnv()
		applyBaseline(t, h)
		return h
	}
	ctx := context.Background()

	t.Run("the default is the safe direction", func(t *testing.T) {
		h := converged(t)

		_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
		require.NoError(t, err)

		backups, err := h.Deps.Backup.List(ctx)
		require.NoError(t, err)
		assert.Len(t, backups, 1, "the pre-update backup was not taken by default")
	})

	t.Run("the policy suppresses it, without --skip-backup --force", func(t *testing.T) {
		h := converged(t)

		inst, err := h.Deps.State.LoadInstallation(ctx)
		require.NoError(t, err)
		inst.Policy.SkipBackupBeforeUpdate = true
		require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

		result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
		require.NoError(t, err)

		backups, err := h.Deps.Backup.List(ctx)
		require.NoError(t, err)
		assert.Empty(t, backups, "the policy was set and a backup was taken anyway")

		// Recorded, because the journal is where an incident review looks
		// for why there is no backup from just before this update.
		assert.Equal(t, "true", result.Record.Flags["skip_backup_policy"],
			"the journal does not record that the policy skipped the backup")
	})
}

// TestUpdateRetiresAParameterTheNewReleaseDropped.
//
// A vendor may stop declaring a parameter, and the operator should be told
// rather than refused. Nothing enforced that: the recorded value stayed, and
// every resolve after the update validates the whole map against the new
// declarations -- so the first `apply`, `config` or `status` afterwards would
// refuse over a parameter the operator never typed, with the update already
// applied and no obvious way back.
func TestUpdateRetiresAParameterTheNewReleaseDropped(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	// The operator set a value for a parameter 1.3.0 is about to drop.
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Parameters = map[string]string{"log_level": "debug"}
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	src := stageUpgradeSource(t, h)
	dropParameter(t, src, "log_level")

	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err, "dropping a parameter is the vendor's decision, not a failure")
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)

	after, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.NotContains(t, after.Parameters, "log_level",
		"the retired value is still recorded, so the next command resolves against "+
			"a declaration that no longer exists")

	// Told, not silently dropped: the value was the operator's, and the
	// product's behaviour changes when it stops being applied.
	var warned bool
	for _, e := range h.Events.Events() {
		if e.Level == events.LevelWarn && strings.Contains(e.Message, "log_level") {
			warned = true
		}
	}
	assert.True(t, warned,
		"nothing warned that a parameter the operator had set was retired")

	// The whole point of retiring it: the next operation resolves.
	_, err = ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err, "the update left an installation later commands refuse")
}

// TestAFailedUpdateKeepsTheParametersItWouldHaveRetired.
//
// The retirement is the last step of the update, and that ordering is the whole
// safety argument. Anything that fails before it unwinds with the values still
// recorded -- which is correct, because what it unwinds *to* is the release
// that declares them. Persisting the drop earlier would need a compensation to
// put them back, and a resumed run could not: it reloads an installation the
// values are already gone from, and has nothing to restore.
func TestAFailedUpdateKeepsTheParametersItWouldHaveRetired(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Parameters = map[string]string{"log_level": "debug"}
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	src := stageRecoverableUpgradeSource(t, h)
	dropParameter(t, src, "log_level")

	// 1.3.0 never becomes healthy, so the update compensates; 1.2.0, put
	// back, does.
	h.Health.FailingWaits = 1

	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	require.Equal(t, domain.StatusCompensated, result.Record.Status)

	after, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Equal(t, "debug", after.Parameters["log_level"],
		"the update unwound and the operator's value went with it, though the "+
			"release now running is the one that declares it")

	// And the release that declares it still resolves against that value.
	h.Health.Healthy = true
	_, err = ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err, "the value the update left recorded no longer resolves")
}

// dropParameter removes a parameter block from a bundle's manifest, which is
// what a vendor does when a knob stops existing.
func dropParameter(t *testing.T, bundle, name string) {
	t.Helper()
	path := filepath.Join(bundle, "manifest.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	lines := strings.Split(string(data), "\n")
	var out []string
	dropping := false
	for _, line := range lines {
		switch {
		case line == "  "+name+":":
			dropping = true
		case dropping && strings.HasPrefix(line, "    "):
			// still inside the block
		case dropping:
			dropping = false
			out = append(out, line)
		default:
			out = append(out, line)
		}
	}
	require.NotEqual(t, len(lines), len(out), "manifest declares no parameter %q", name)
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644))
}

func TestUpdateRestoresThePointerWhenConvergenceFails(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	// 1.3.0 comes up but never becomes ready; 1.2.0, put back, does.
	h.Health.FailingWaits = 1

	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
	require.Error(t, err)
	assert.Equal(t, domain.StatusCompensated, result.Record.Status)

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", current.Version.String(),
		"a failed update must leave the release that was running current")

	target, err := os.Readlink(h.Paths.CurrentLink())
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(h.Paths.ReleasesDir(), "1.2.0"), target,
		"and the symlink must follow the pointer")

	// The staged directory stays: it is immutable and digest-addressed, and
	// removing it would destroy evidence.
	assert.DirExists(t, filepath.Join(h.Paths.ReleasesDir(), "1.3.0"))

	// The pre-update backup stays too. It is the most valuable artifact in
	// the system at this moment.
	backups, err := h.Deps.Backup.List(ctx)
	require.NoError(t, err)
	assert.Len(t, backups, 1, "compensation must not delete the pre-update backup")
}

// TestAFailedUpdatePutsThePreviousReleaseBackUp.
//
// Compensation used to put back files and nothing else, so the new release's
// containers went on running against the old release's configuration until
// somebody ran `apply` -- on an unattended machine, until the next boot (RFC
// 0001 decision 19). The release that was running is brought back up, with its
// own secrets and its configuration in the mode it had, and waited for.
func TestAFailedUpdatePutsThePreviousReleaseBackUp(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	// A mode the manifest does not declare, so only a restore of the file
	// as it was can bring it back.
	config := filepath.Join(h.Root, "etc", "demo", "application.yaml")
	require.NoError(t, os.Chmod(config, 0o644))
	ups := len(h.Runtime.UpConfigs)

	h.Health.FailingWaits = 1
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
	require.Error(t, err)
	require.Equal(t, domain.StatusCompensated, result.Record.Status, "%v", err)

	require.Len(t, h.Runtime.UpConfigs, ups+2, "1.3.0 was brought up and 1.2.0 was not brought back")
	assert.Equal(t, "1.3.0", h.Runtime.UpConfigs[ups].Env["DEMO_VERSION"])
	assert.Equal(t, "1.2.0", h.Runtime.UpConfigs[ups+1].Env["DEMO_VERSION"],
		"the project was brought back up from the wrong release")
	assert.Zero(t, h.Health.FailingWaits, "the restored release was never waited for")

	assert.DirExists(t, h.Paths.SecretsRenderDir(), "1.2.0 was brought up with no secrets rendered")
	info, err := os.Stat(config)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "the configuration came back in another mode")
}

// TestAConfigFileThatCannotBeReadIsNotReplaced.
//
// Rendering backs each file up before replacing it. A file that exists but
// cannot be read was replaced with no backup, so the rollback put nothing back,
// flagged nothing, and the previous release came back up against the new
// release's configuration -- the state the restore exists to prevent.
func TestAConfigFileThatCannotBeReadIsNotReplaced(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	config := filepath.Join(h.Root, "etc", "demo", "application.yaml")
	before, err := os.ReadFile(config)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(config, 0))

	h.Health.FailingWaits = 1
	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
	require.Error(t, err)

	require.NoError(t, os.Chmod(config, 0o600))
	after, err := os.ReadFile(config)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after),
		"a file with no backup was replaced, and the rollback could not put it back")
}

// TestAPartlyWrittenConfigurationIsPutBack.
//
// Two files, the second in a directory nothing can write to. The first was
// replaced before the second failed, and the backups were recorded only once
// every write had succeeded -- so the failed step's own rollback found nothing
// to restore and left the first file as the new release wrote it.
func TestAPartlyWrittenConfigurationIsPutBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	config := filepath.Join(h.Root, "etc", "demo", "application.yaml")
	before, err := os.ReadFile(config)
	require.NoError(t, err)

	// Sorted after the first target, so it is written second.
	locked := filepath.Join(h.Root, "etc", "zz")
	require.NoError(t, os.MkdirAll(locked, 0o755))
	require.NoError(t, os.Chmod(locked, 0o555))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	src := stageRecoverableUpgradeSource(t, h)
	path := filepath.Join(src, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	first := "    target: " + config + "\n    mode: \"0640\"\n"
	patched := strings.Replace(string(raw), first, first+
		"  - template: templates/application.yaml.tmpl\n"+
		"    target: "+filepath.Join(locked, "extra.yaml")+"\n"+
		"    mode: \"0640\"\n", 1)
	require.NotEqual(t, string(raw), patched, "the fixture's configuration entry is not the one this extends")
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))

	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	after, err := os.ReadFile(config)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after),
		"the file written before the failure was left as the new release wrote it")
}

// TestARestoreThatFailsNeedsAPerson.
//
// Bringing the old release back can fail too, and then nothing is running that
// anybody chose. Exit 12, with both failures in the error the operator reads:
// the update's, and the rollback's, which decides what to do next.
func TestARestoreThatFailsNeedsAPerson(t *testing.T) {
	for name, breakBoth := range map[string]func(h *harness){
		"neither release becomes healthy": func(h *harness) { h.Health.Healthy = false },
		"neither release starts": func(h *harness) {
			h.Runtime.Fail["Up"] = domain.RuntimeError(nil, "injected: daemon refused")
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.install()
			h.setHookEnv()
			applyBaseline(t, h)
			ctx := context.Background()

			breakBoth(h)
			result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
			require.Error(t, err)
			assert.Equal(t, domain.StatusManualIntervention, result.Record.Status)
			assert.Equal(t, domain.ExitManualIntervention, domain.ExitCode(err))
			assert.Contains(t, err.Error(), "rollback did not complete (restore-point:",
				"the rollback's own failure is not in the error")
		})
	}
}

// TestAFailureBeforeTheServicesLeavesThemAlone: an update that never replaced
// a container has none to put back, and starting the old release again would
// restart a deployment that was never touched.
func TestAFailureBeforeTheServicesLeavesThemAlone(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()
	ups := len(h.Runtime.UpConfigs)

	h.Runtime.Fail["Pull"] = domain.RuntimeError(nil, "injected: registry unreachable")
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
	require.Error(t, err)
	assert.Equal(t, domain.StatusCompensated, result.Record.Status)
	assert.Len(t, h.Runtime.UpConfigs, ups, "a deployment the update never touched was restarted")
}

// TestAnUpdateToAStagedReleasePutsThePreviousOneBackToo.
//
// `update --to` installs a release already in the store -- which is how the
// unattended tick installs what the channel staged -- and the staging step
// finds it there and is skipped. A skipped step's compensator never runs, so a
// restore that lived in it never ran either, on exactly the path it was for.
func TestAnUpdateToAStagedReleasePutsThePreviousOneBackToo(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	// Into the store, by a first attempt that fails the same way.
	h.Health.FailingWaits = 1
	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
	require.Error(t, err)

	ups := len(h.Runtime.UpConfigs)
	h.Health.FailingWaits = 1
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{To: "1.3.0"})
	require.Error(t, err)
	require.Equal(t, domain.StatusCompensated, result.Record.Status, "%v", err)
	require.Len(t, h.Runtime.UpConfigs, ups+2, "1.2.0 was not brought back after an update --to")
	assert.Equal(t, "1.2.0", h.Runtime.UpConfigs[ups+1].Env["DEMO_VERSION"])
}

// TestAConfigurationThatCannotBePutBackIsNotRestartedAgainst.
//
// The configuration compensator wrote back what it could and discarded what it
// could not. That was a best effort while nothing restarted; now the previous
// release is brought back up after it, and would be started against the new
// release's files. A file that cannot be put back stops the restore, and the
// update ends needing a person, with both failures named.
func TestAConfigurationThatCannotBePutBackIsNotRestartedAgainst(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	// The migration runs after the configuration is rendered and before
	// anything is compensated, which is the window to take the directory's
	// write permission away in.
	src := stageRecoverableUpgradeSource(t, h)
	hook := filepath.Join(src, "hooks", "migrate")
	raw, err := os.ReadFile(hook)
	require.NoError(t, err)
	locked := strings.Replace(string(raw), "set -eu\n",
		"set -eu\nchmod 0555 \"$(dirname \"${DEMO_CONFIG_FILE:?}\")\"\n", 1)
	require.NotEqual(t, string(raw), locked)
	require.NoError(t, os.WriteFile(hook, []byte(locked), 0o755))
	configDir := filepath.Join(h.Root, "etc", "demo")
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o755) })

	ups := len(h.Runtime.UpConfigs)
	h.Health.FailingWaits = 1
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	assert.Equal(t, domain.StatusManualIntervention, result.Record.Status, "%v", err)
	assert.Contains(t, err.Error(), "render-configuration:", "the configuration's own failure is not named")
	assert.Len(t, h.Runtime.UpConfigs, ups+1,
		"1.2.0 was brought back up against configuration that was never put back")
}

// TestAConfigurationFileOnlyTheNewReleaseHadIsRemoved.
//
// The compensator put back files that existed before the render and kept
// nothing for the ones the render created, so a file only the new release
// declares stayed behind while the previous release was brought back up beside
// it.
func TestAConfigurationFileOnlyTheNewReleaseHadIsRemoved(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	src := stageRecoverableUpgradeSource(t, h)
	manifest := filepath.Join(src, "manifest.yaml")
	raw, err := os.ReadFile(manifest)
	require.NoError(t, err)
	existing := "    target: " + h.Root + "/etc/demo/application.yaml\n    mode: \"0640\"\n"
	added := strings.Replace(string(raw), existing, existing+
		"  - template: templates/application.yaml.tmpl\n"+
		"    target: "+h.Root+"/etc/demo/extra.yaml\n    mode: \"0640\"\n", 1)
	require.NotEqual(t, string(raw), added, "the fixture's configuration block is not the one this extends")
	require.NoError(t, os.WriteFile(manifest, []byte(added), 0o644))
	extra := filepath.Join(h.Root, "etc", "demo", "extra.yaml")

	h.Health.FailingWaits = 1
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	require.Equal(t, domain.StatusCompensated, result.Record.Status, "%v", err)
	assert.NoFileExists(t, extra, "the new release's own configuration file outlived its rollback")
}

// TestAReleaseRecordWithNoRootStillRollsBack.
//
// A release record from before roots were recorded names its release only
// through the `current` link. Restoring the link from the record's empty root
// failed, so a failed update over such an installation could not compensate --
// though the link had never moved, because only record-state moves it.
func TestAReleaseRecordWithNoRootStillRollsBack(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	current.Root = ""
	require.NoError(t, h.Deps.State.SetCurrentRelease(ctx, current))

	h.Health.FailingWaits = 1
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
	require.Error(t, err)
	assert.Equal(t, domain.StatusCompensated, result.Record.Status, "%v", err)
}

// TestAFailureAfterAnUnreadableMigrationIsNotRolledBack.
//
// The 1.3.0 fixture migrates to schema 14, and 1.2.0 reads at most 12. A
// failure after that compensated back toward 1.2.0 over the migrated database
// and called it a rollback: exit 11, "earlier changes were rolled back", 1.2.0
// still recorded at schema 12, nothing flagged -- and the next boot's `apply`
// running 1.2.0's migrations against 14. Past the migration, the only honest
// outcomes are forward or a restore, and choosing between them is a person's.
func TestAFailureAfterAnUnreadableMigrationIsNotRolledBack(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	h.Health.Healthy = false
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.Error(t, err)
	assert.Equal(t, domain.ExitManualIntervention, domain.ExitCode(err))
	assert.Equal(t, domain.StatusManualIntervention, result.Record.Status)
	assert.Contains(t, err.Error(), `after step "migrate"`, "the error does not say where the way back closed")
	assert.Contains(t, err.Error(), "database restore", "the error does not say why")
	assert.Contains(t, err.Error(), "update --to 1.3.0", "the error does not say how to finish")

	// What came before the migration stays. Compensating render-secrets
	// would have deleted the rendered secrets, which the release now
	// running needs, so their presence is the evidence nothing crossed.
	assert.DirExists(t, h.Paths.SecretsRenderDir(), "a step before the migration was rolled back")

	// The way forward the error names is one that works.
	h.Health.Healthy = true
	_, err = ops.ClearIntervention(ctx, h.Deps, result.Record.ID)
	require.NoError(t, err)
	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{To: "1.3.0"})
	require.NoError(t, err)
	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String())
	assert.Equal(t, 14, current.SchemaAtInstall)
}

// TestAMigrationServiceClosesTheWayBackByWhatItDeclares is the same failure
// with the migration run as a one-shot service. A service reports no schema,
// so what the release declares its migrations produce is what decides.
func TestAMigrationServiceClosesTheWayBackByWhatItDeclares(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	src := stageUpgradeSource(t, h)
	path := filepath.Join(src, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	patched := strings.Replace(string(raw),
		"  migrate:\n    kind: hook\n    command: [\"hooks/migrate\"]",
		"  migrate:\n    kind: runtime-service\n    service: app", 1)
	require.NotEqual(t, string(raw), patched, "the fixture no longer declares a migrate hook")
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))

	h.Health.Healthy = false
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	assert.Equal(t, domain.StatusManualIntervention, result.Record.Status,
		"a declared schema of 14 over a release reading 12 was compensated back over")
	assert.Contains(t, h.Runtime.Calls, "RunOneShot", "the migration service never ran")
}

// TestAnUnreadableFallbackIsNotFallenBackTo.
//
// The release a failure would return to has a manifest that no longer loads,
// so nothing can say what it reads. The update still runs -- its runtime
// options are on record, which is all it needed from that manifest -- but past
// the migration the way back is treated as closed, even for migrations that
// stop at a schema the old release could have read.
func TestAnUnreadableFallbackIsNotFallenBackTo(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	require.NotNil(t, inst.RuntimeOptions,
		"without recorded options the update refuses before reaching what this tests")

	src := stageRecoverableUpgradeSource(t, h)
	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(current.Root, "manifest.yaml"),
		[]byte("not: [a manifest"), 0o644))

	h.Health.Healthy = false
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	assert.Equal(t, domain.StatusManualIntervention, result.Record.Status,
		"the update fell back to a release nothing could say it was safe to fall back to")
	assert.Contains(t, err.Error(), "could not be read")
}

// TestAFailedUpdateKeepsTheSecretsOfTheRunningRelease: an update that stops
// before start-services leaves the previous release's containers running, and
// they read their secrets from the same directory.
func TestAFailedUpdateKeepsTheSecretsOfTheRunningRelease(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ups := len(h.Runtime.CallsMatching("Up"))

	h.Runtime.Fail["Pull"] = domain.RuntimeError(nil, "injected: registry unreachable")

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.Error(t, err)
	require.Len(t, h.Runtime.CallsMatching("Up"), ups, "the failed update must stop before start-services")

	assert.FileExists(t, filepath.Join(h.Paths.SecretsRenderDir(), "db_password"),
		"compensation removed the secrets of the release that is still running")
}

// TestUpdateFaultInjection is RFC 0001 §6's extension of the engine's
// fault-injection loop to the update pipeline: fail at each step in turn and
// assert the release pointer always ends where it started.
//
// The engine's own loop covers compensation ordering with synthetic steps.
// This covers the property that matters operationally -- that no partial
// update leaves the manager pointing at a release it did not finish
// installing.
func TestUpdateFaultInjection(t *testing.T) {
	injections := []struct {
		name   string
		break_ func(h *harness)
	}{
		{"runtime validate", func(h *harness) {
			h.Runtime.Fail["Validate"] = domain.RuntimeError(nil, "injected: compose config invalid")
		}},
		{"image pull", func(h *harness) {
			h.Runtime.Fail["Pull"] = domain.RuntimeError(nil, "injected: registry unreachable")
		}},
		{"service start", func(h *harness) {
			h.Runtime.Fail["Up"] = domain.RuntimeError(nil, "injected: daemon refused")
		}},
		{"health checks", func(h *harness) {
			h.Health.FailingWaits = 1
		}},
		{"secret rendering", func(h *harness) {
			h.Secrets.Fail["Render"] = domain.SecretsError(nil, "injected: cannot render")
		}},
		{"pre-update backup", func(h *harness) {
			h.Backup.Fail["Create"] = domain.BackupError(nil, "injected: no space for a backup")
		}},
	}

	for _, inj := range injections {
		t.Run(inj.name, func(t *testing.T) {
			h := newHarness(t)
			h.install()
			h.setHookEnv()
			applyBaseline(t, h)
			ctx := context.Background()

			before, err := h.Deps.State.CurrentRelease(ctx)
			require.NoError(t, err)
			require.Equal(t, "1.2.0", before.Version.String())

			inj.break_(h)

			_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
			require.Error(t, err, "the injected failure must fail the update")

			after, err := h.Deps.State.CurrentRelease(ctx)
			require.NoError(t, err)
			assert.Equal(t, before.Version.String(), after.Version.String(),
				"the release pointer must end where it started")
			assert.Equal(t, before.Digest, after.Digest)

			link, err := os.Readlink(h.Paths.CurrentLink())
			require.NoError(t, err)
			assert.Equal(t, before.Root, link, "the symlink must agree with the pointer")
		})
	}
}

func TestUpdateIsIdempotentOnAnAlreadyStagedRelease(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	src := stageUpgradeSource(t, h)

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)

	// Updating to the release that is already current: the staging step's
	// Check sees a byte-identical tree and skips, and the apply pipeline
	// converges to no change.
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)

	var staged domain.StepRecord
	for _, s := range result.Record.Steps {
		if s.ID == "stage-release" {
			staged = s
		}
	}
	assert.Equal(t, domain.StepSkipped, staged.Status,
		"a byte-identical release must not be re-copied")

	previous, err := h.Deps.State.PreviousRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", previous.Version.String(),
		"re-updating to the current release must not shift the rollback target")
}

func TestUpdateRefusesTheSameVersionWithDifferentContent(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	src := stageUpgradeSource(t, h)
	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)

	// Same version, different bytes. Content-addressed identity exists to
	// catch exactly this.
	require.NoError(t, os.WriteFile(filepath.Join(src, "hooks", "smoke-test"),
		[]byte("#!/bin/sh\necho tampered\n"), 0o755))

	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "different digest")
}

func TestUpdateToInstallsFromTheReleaseStore(t *testing.T) {
	h := updatedHarness(t) // 1.2.0 -> 1.3.0, both now in the store
	ctx := context.Background()
	setSchema(t, h, 12)

	// Roll back so 1.3.0 is in the store but not current.
	_, err := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
	require.NoError(t, err)
	require.Equal(t, "1.2.0", currentVersion(t, h))

	// --to installs it again without an operator knowing the store layout.
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{To: "1.3.0"})
	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)
	assert.Equal(t, "1.3.0", currentVersion(t, h))

	// Nothing was copied: source and destination are the same directory.
	var staged domain.StepRecord
	for _, s := range result.Record.Steps {
		if s.ID == "stage-release" {
			staged = s
		}
	}
	assert.Equal(t, domain.StepSkipped, staged.Status)
}

func TestUpdateRejectsBothARefAndTo(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: "./somewhere", To: "1.3.0",
	})
	require.Error(t, err)
	assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
}

func TestUpdateWithNeitherRefNorTo(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{})
	require.Error(t, err)
	assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
	assert.Contains(t, domain.AsError(err).Hint, "--to")
}

func currentVersion(t *testing.T, h *harness) string {
	t.Helper()
	c, err := h.Deps.State.CurrentRelease(context.Background())
	require.NoError(t, err)
	return c.Version.String()
}

// TestUpdateFromAnArchive is the end of the chain the archive source exists
// for: a vendor publishes a `tar.zst`, an operator points `update` at it, and
// everything downstream -- the compatibility gate, the pre-update backup, the
// convergence pipeline -- behaves exactly as it does for an unpacked directory.
//
// It is the only test that exercises materialising a reference that cannot be
// read where it lies, which is the part that will carry HTTPS and OCI later.
func TestUpdateFromAnArchive(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	src := stageUpgradeSource(t, h)
	archive := filepath.Join(t.TempDir(), "demo-1.3.0.tar.zst")
	writeTarZst(t, src, archive)

	ctx := context.Background()
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: archive})
	require.NoError(t, err, "an archive reference must install like a directory one")
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String())

	// The digest recorded in the release pointer is the tree digest, not
	// the archive's. An operator who pinned it from the unpacked bundle can
	// pin the archive with the same value.
	unpacked, err := atomicfs.DigestTree(src)
	require.NoError(t, err)
	assert.Equal(t, unpacked, current.Digest,
		"the transport must not change the identity of the release")

	// Staging is scratch and must not accumulate: an update that left its
	// unpacked copy behind would double the disk cost of every release.
	entries, err := os.ReadDir(h.Paths.StagingDir())
	require.NoError(t, err)
	assert.Empty(t, entries, "the staging directory must be empty once the update finishes")
}

// TestUpdateFromAnArchivePinsTheSameDigest asserts the pinning story an
// operator actually uses: record the digest once, pass it whatever shape the
// bundle arrives in.
func TestUpdateFromAnArchivePinsTheSameDigest(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	src := stageUpgradeSource(t, h)
	digest, err := atomicfs.DigestTree(src)
	require.NoError(t, err)

	archive := filepath.Join(t.TempDir(), "demo-1.3.0.tar.zst")
	writeTarZst(t, src, archive)

	_, err = ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref:          archive,
		ExpectDigest: digest,
	})
	require.NoError(t, err,
		"a digest recorded from the unpacked bundle must verify against its archive")
}

// TestUpdateRecoversFromAPartiallyStagedRelease: a crash mid-extraction used
// to leave a partial tree at the digest-addressed path, and the retry then
// failed on it -- worst case as "installed with a different digest", an error
// whose only remedy was hand-deleting a directory everything else treats as
// immutable. Staging into a hidden sibling and renaming into place makes the
// retry self-healing.
func TestUpdateRecoversFromAPartiallyStagedRelease(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	// The debris a killed extraction leaves: a directory at the final path
	// holding half a bundle, and an abandoned staging dir beside it.
	partial := filepath.Join(h.Paths.ReleasesDir(), "1.3.0")
	require.NoError(t, os.MkdirAll(partial, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(partial, "manifest.yaml"),
		[]byte("api_version: morze.dev/v1alpha1\n"), 0o644))
	stale := filepath.Join(h.Paths.ReleasesDir(), ".staging-12345")
	require.NoError(t, os.MkdirAll(stale, 0o700))
	// An older kept-debris tree: creating new debris replaces it, so the
	// advertised bound of one tree holds.
	oldDebris := filepath.Join(h.Paths.ReleasesDir(), ".staging-debris-111")
	require.NoError(t, os.MkdirAll(oldDebris, 0o755))

	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.NoError(t, err, "an update could not recover from its own crash debris")
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String())
	assert.NoDirExists(t, stale, "the abandoned staging directory must be swept")

	assert.NoDirExists(t, oldDebris, "creating new debris must replace the old kept tree")
	debris, err := filepath.Glob(filepath.Join(h.Paths.ReleasesDir(), ".staging-debris-*"))
	require.NoError(t, err)
	assert.Len(t, debris, 1, "exactly one debris tree is kept: the partial 1.3.0 moved aside")
}

// TestUpdateRefusesWhenTheCurrentReleaseTreeIsUnreadable: an unreadable tree
// at the staged path is normally crash debris and gets moved aside -- but when
// that path IS the live release, moving it would leave `current` dangling with
// a failed fetch's compensation pointing at nothing. The refusal is the fix.
func TestUpdateRefusesWhenTheCurrentReleaseTreeIsUnreadable(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	// A same-version bundle, so the staged path is the live release's own.
	src := filepath.Join(t.TempDir(), "bundle-1.2.0")
	copyBundle(t, testBundlePath(t), src)
	retargetManifest(t, src, h.Root)

	// The live tree stops loading, the way a transient I/O fault looks.
	live := filepath.Join(h.Paths.ReleasesDir(), "1.2.0")
	require.NoError(t, os.Remove(filepath.Join(live, "manifest.yaml")))

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err, "the update proceeded over an unreadable live release")
	assert.Contains(t, domain.AsError(err).Error(), "currently installed release",
		"the refusal must name the real problem, not generic staging debris")
	assert.DirExists(t, live, "the live tree must not be moved or deleted")
}

// TestAnUnreadableCandidateDoesNotStopAnUpdateHalfWayThrough.
//
// Retiring the staged candidate runs after the release has been recorded as
// current and before the symlink swap that makes `current` true for anything
// reading the filesystem. It used to return its error, so a failure to forget
// one bookkeeping file left the deployment naming 1.3.0 while `current` still
// pointed at 1.2.0 -- a machine whose state and filesystem disagree about what
// is installed, over a record whose entire cost of loss is one fetch.
//
// An advisory write must not outrank the outcome it trails. The corrupt file is
// the reachable way to make that write fail: it is written by a poll and read
// back by an apply, and nothing about an update depends on it being readable.
func TestAnUnreadableCandidateDoesNotStopAnUpdateHalfWayThrough(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	src := stageUpgradeSource(t, h)
	require.NoError(t, os.WriteFile(h.Paths.UpdateCandidateFile(),
		[]byte("{\"version\": \n"), 0o640))

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	require.Equal(t, "1.3.0", current.Version.String())

	target, err := os.Readlink(h.Paths.CurrentLink())
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(h.Paths.ReleasesDir(), "1.3.0"), target,
		"state says 1.3.0 is installed and the current symlink says otherwise")
}
