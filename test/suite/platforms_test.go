package suite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// The platforms a release and its profiles are made for (RFC 0055 §5.1). The
// harness runs on Linux and says it is darwin, which is what a Mac's manager
// compares a release with.

// madeForDarwin makes a bundle declare both platforms, and narrows the given
// profiles to Linux, at the manager version that reads either key.
func madeForDarwin(t *testing.T, src string, linuxOnly ...string) string {
	t.Helper()
	patchManifest(t, src, "requirements:\n", "requirements:\n  platforms: [linux, darwin]\n")
	patchManifest(t, src, `min_manager_version: "0.0.0"`,
		`min_manager_version: "`+domain.PlatformsMinManagerVersion+`"`)
	if len(linuxOnly) > 0 {
		narrowed := "    platforms:\n"
		for _, profile := range linuxOnly {
			narrowed += "      " + profile + ": [linux]\n"
		}
		patchManifest(t, src, "      external-db: [compose/compose.external-db.yaml]\n",
			"      external-db: [compose/compose.external-db.yaml]\n"+narrowed)
	}
	return src
}

// asVersion renames a 1.3.0 bundle, so one test can stage two releases after
// the installed one.
func asVersion(t *testing.T, src, version string) string {
	t.Helper()
	patchManifest(t, src, "version: 1.3.0", "version: "+version)
	require.NoError(t, os.WriteFile(filepath.Join(src, "VERSION"), []byte(version+"\n"), 0o644))
	return src
}

// onDarwin is an installation on a Mac running 1.2.0, which declares no
// platforms: the state a Linux backup restored onto a Mac leaves.
func onDarwin(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	h.Deps.Platform = "darwin"
	return h
}

// `init` refuses a release not made for this platform before anything is
// written, and so does its plan; one that declares darwin installs.
func TestInitRefusesAReleaseNotMadeForThisPlatform(t *testing.T) {
	ctx := context.Background()
	fresh := func(declares bool) (*harness, string) {
		h := newHarness(t)
		h.setHookEnv()
		realIdentity(t, h)
		h.Deps.Platform = "darwin"
		src := filepath.Join(t.TempDir(), "bundle-1.2.0")
		copyBundle(t, testBundlePath(t), src)
		retargetManifest(t, src, h.Root)
		if declares {
			madeForDarwin(t, src)
		}
		return h, src
	}
	initOpts := func(src string, dryRun bool) ops.InitOptions {
		return ops.InitOptions{
			Product: "demo", ReleasePath: src, NoRecoveryKey: true, GenerateSecrets: true,
			Options: ops.Options{DryRun: dryRun},
		}
	}

	// The plan refuses before it starts. The operation refuses at its stage
	// step, once it has the release, and undoes the copy, as it does a `--set`
	// the release does not declare (logs/macos-host.md).
	for dryRun, exit := range map[bool]int{true: domain.ExitIncompatible, false: domain.ExitCompensated} {
		refused, src := fresh(false)
		_, err := ops.Init(ctx, refused.Deps, initOpts(src, dryRun))
		require.Error(t, err, "dry run %t", dryRun)
		require.ErrorIs(t, err, domain.ErrIncompatible)
		assert.Equal(t, exit, domain.ExitCode(err), "dry run %t", dryRun)
		assert.Contains(t, err.Error(), "release 1.2.0 is not made for darwin")
		assert.Contains(t, domain.AsError(err).Hint, "which means linux alone")
		exists, err := refused.Deps.State.InstallationExists(ctx)
		require.NoError(t, err)
		assert.False(t, exists, "a refused release left an installation behind")
	}

	made, src := fresh(true)
	_, err := ops.Init(ctx, made.Deps, initOpts(src, false))
	require.NoError(t, err)

	// A profile the release runs on Linux alone is refused by the plan as by
	// the operation.
	narrowed, src := fresh(true)
	patchManifest(t, src, "      external-db: [compose/compose.external-db.yaml]\n",
		"      external-db: [compose/compose.external-db.yaml]\n    platforms:\n      embedded: [linux]\n")
	for _, dryRun := range []bool{true, false} {
		opts := initOpts(src, dryRun)
		opts.Profiles = []string{"embedded"}
		_, err := ops.Init(ctx, narrowed.Deps, opts)
		require.Error(t, err, "dry run %t", dryRun)
		assert.Contains(t, err.Error(), `profile "embedded" does not run on darwin`, "dry run %t", dryRun)
	}
}

// A refused repair leaves the installation it was asked to repair: the step
// that writes it undoes only what it created.
func TestARefusedRepairKeepsTheInstallation(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	realIdentity(t, h)
	src := filepath.Join(t.TempDir(), "bundle-1.2.0")
	copyBundle(t, testBundlePath(t), src)
	retargetManifest(t, src, h.Root)

	_, err := ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", ReleasePath: src, Profiles: []string{"undeclared"}, Repair: true,
		NoRecoveryKey: true, GenerateSecrets: true,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown deployment profile "undeclared"`)
	exists, err := h.Deps.State.InstallationExists(ctx)
	require.NoError(t, err)
	assert.True(t, exists, "a refused repair deleted the installation")
}

// The remedy the refusals name works: a recorded profile this platform does
// not run can be replaced, since replacing it reads the old selection to see
// what runs, and so can what runs be stopped (decision 3).
func TestAProfileThisPlatformDoesNotRunCanBeReplaced(t *testing.T) {
	ctx := context.Background()
	h := onDarwin(t)
	// `embedded` is recorded, and the installed release narrows it to Linux,
	// as a release installed elsewhere and restored here would.
	h.Deps.Platform = "linux"
	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{
		Ref: madeForDarwin(t, stageUpgradeSource(t, h), "embedded"),
	})
	require.NoError(t, err)
	h.Deps.Platform = "darwin"

	_, err = setSetting(t, h, false, map[string]string{"installation.profile": "external-db"})
	require.NoError(t, err)
	assert.Equal(t, []string{"external-db"}, installation(t, h).Profiles)
}

// A machine whose state names a release not made for it, as one restored
// from a Linux backup does, is refused by preflight and named by doctor
// (logs/macos-host.md).
func TestApplyRefusesAnInstalledReleaseNotMadeForThisPlatform(t *testing.T) {
	h := onDarwin(t)

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the release is not made for darwin")
	assert.Empty(t, h.Runtime.UpOptions, "a release not made for this platform was deployed")
	check := doctorCheck(t, h, "system.platform")
	assert.Equal(t, events.CheckFail, check.Status)
	assert.Equal(t, "the release is not made for darwin", check.Message)
	assert.Contains(t, check.Remedy, "Its vendor adds darwin to that list")
}

// An update is refused a release not made for this platform, and one that
// does not run the recorded profile here, before any step; a rollback is
// refused a release not made for it (decisions 2 and 3).
func TestUpdateAndRollbackRefuseWhatThisPlatformCannotRun(t *testing.T) {
	ctx := context.Background()
	h := onDarwin(t)
	current := func() string {
		t.Helper()
		rec, err := h.Deps.State.CurrentRelease(ctx)
		require.NoError(t, err)
		return rec.Version.String()
	}

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.ErrorIs(t, err, domain.ErrIncompatible)
	assert.Contains(t, err.Error(), "release 1.3.0 is not made for darwin")
	assert.Equal(t, "1.2.0", current())

	// The installation records `embedded`, which this release runs on Linux
	// alone.
	narrowed := madeForDarwin(t, stageUpgradeSource(t, h), "embedded")
	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: narrowed})
	require.ErrorIs(t, err, domain.ErrIncompatible)
	assert.Contains(t, err.Error(),
		`release 1.3.0 does not run the profile "embedded" this installation uses on darwin`)
	hint := domain.AsError(err).Hint
	assert.Contains(t, hint, "it runs it on linux only")
	assert.Contains(t, hint, "morzer config set installation.profile=")
	assert.Equal(t, "1.2.0", current())

	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{
		Ref: madeForDarwin(t, asVersion(t, stageUpgradeSource(t, h), "1.3.1"), "external-db"),
	})
	require.NoError(t, err)
	assert.Equal(t, "1.3.1", current())

	// A one-run `--profile` of a Linux-only profile is refused by the same
	// rule, and so is storing it.
	_, err = ops.Apply(ctx, h.Deps, ops.Options{Profiles: []string{"external-db"}})
	require.ErrorIs(t, err, domain.ErrValidation)
	assert.Contains(t, err.Error(), `profile "external-db" does not run on darwin`)
	_, err = setSetting(t, h, false, map[string]string{"installation.profile": "external-db"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `profile "external-db" does not run on darwin`)
	assert.Equal(t, "release 1.3.1 runs it on linux only", domain.AsError(err).Hint)

	_, err = ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
	require.ErrorIs(t, err, domain.ErrIncompatible)
	assert.Contains(t, err.Error(), "release 1.2.0 is not made for darwin")
	assert.Equal(t, "1.3.1", current())
}
