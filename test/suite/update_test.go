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
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	// The default is the safe direction: a backup is taken.
	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.NoError(t, err)

	backups, err := h.Deps.Backup.List(ctx)
	require.NoError(t, err)
	require.Len(t, backups, 1, "the pre-update backup was not taken by default")

	// Set, and it applies without --skip-backup --force on every run.
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Policy.SkipBackupBeforeUpdate = true
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	back := stageUpgradeSource(t, h)
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: back, To: ""})
	require.NoError(t, err)

	after, err := h.Deps.Backup.List(ctx)
	require.NoError(t, err)
	assert.Len(t, after, 1, "the policy was set and a backup was taken anyway")

	// Recorded, because the journal is where an incident review looks for
	// why there is no backup from just before this update.
	assert.Equal(t, "true", result.Record.Flags["skip_backup_policy"],
		"the journal does not record that the policy skipped the backup")
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

	src := stageUpgradeSource(t, h)
	dropParameter(t, src, "log_level")

	// The product never becomes healthy, so the update compensates.
	h.Health.Healthy = false

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

	// The product comes up but never becomes ready.
	h.Health.Healthy = false

	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
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
			h.Health.Healthy = false
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
