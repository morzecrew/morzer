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
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// userLayout is a user installation's layout under a test's directory (RFC
// 0023 row 30).
func userLayout(dir, product string) domain.Paths {
	return domain.UserPaths(filepath.Join(dir, "data"), filepath.Join(dir, "run"), product)
}

// A user installation applies as a system one does: everything in the user's
// tree, and its secrets rendered on the user's tmpfs, never in the tree (RFC
// 0023 rows 26 and 30).
func TestAUserInstallationApplies(t *testing.T) {
	h := newHarnessAt(t, "demo", userLayout)
	h.install()
	h.setHookEnv()

	result, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)

	assert.FileExists(t, filepath.Join(h.Paths.SecretsRenderDir(), "db_password"))
	assert.False(t, strings.HasPrefix(h.Paths.SecretsRenderDir(), h.Root+"/"), "secrets rendered in the tree, on disk")
	_, err = os.Stat(filepath.Join(h.Root, "run"))
	assert.True(t, os.IsNotExist(err), "the tree has a run directory, where secrets would be on disk")
}

// `init` of a user installation is refused on an engine that is not the user's
// own, before anything is created, and goes ahead when the probe cannot run,
// since an installation with no release needs no engine yet (RFC 0023 row 32).
func TestInitOfAUserInstallationRefusesTheSystemsEngine(t *testing.T) {
	ctx := context.Background()
	opts := ops.InitOptions{Product: "demo", NoRecoveryKey: true, GenerateSecrets: true}

	refused := newHarnessAt(t, "demo", userLayout)
	refused.Runtime.Fail["ProbeEngineScope"] = domain.Preflight(nil, "the Docker daemon is the system's")
	_, err := ops.Init(ctx, refused.Deps, opts)
	require.ErrorIs(t, err, domain.ErrPreflight)
	assert.NoFileExists(t, refused.Paths.AgeIdentityFile())

	unasked := newHarnessAt(t, "demo", userLayout)
	realIdentity(t, unasked)
	unasked.Runtime.Fail["ProbeEngineScope"] = domain.RuntimeError(nil, "cannot ask the Docker daemon whose it is")
	_, err = ops.Init(ctx, unasked.Deps, opts)
	require.NoError(t, err)
	exists, err := unasked.Deps.State.InstallationExists(ctx)
	require.NoError(t, err)
	assert.True(t, exists, "a probe that could not run refused init")
}

// doctor runs a user installation's own checks: whose the engine is, failed on
// the system's (RFC 0023 row 32), whether the account lingers, and whether a
// published port needs privilege. A system installation runs none of them.
func TestDoctorRunsAUserInstallationsOwnChecks(t *testing.T) {
	ids := []string{"runtime.engine-scope", "system.linger", "network.unprivileged-ports"}
	checks := func(h *harness) map[string]events.CheckResult {
		report, err := ops.Doctor(context.Background(), h.Deps)
		require.NoError(t, err)
		out := map[string]events.CheckResult{}
		for _, c := range report.Results {
			if slices.Contains(ids, c.ID) {
				out[c.ID] = c
			}
		}
		return out
	}

	user := newHarnessAt(t, "demo", userLayout)
	user.install()
	got := checks(user)
	for _, id := range ids {
		assert.Contains(t, got, id, "a user installation did not run %s", id)
	}
	assert.Equal(t, events.CheckOK, got["runtime.engine-scope"].Status)

	user.Runtime.Fail["ProbeEngineScope"] = domain.Preflight(nil, "the Docker daemon is the system's")
	assert.Equal(t, events.CheckFail, checks(user)["runtime.engine-scope"].Status)

	system := newHarness(t)
	system.install()
	assert.Empty(t, checks(system), "a system installation ran a user installation's checks")
}

// doctor asks whether the metrics directory is writable as the user the units
// write as, which for a user installation is the user running it: a system
// installation asked by anyone but root is not asked.
func TestAUserInstallationsMetricsDirectoryIsAskedAsItsUser(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a directory whatever its mode")
	}
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o555))       //nolint:gosec // a directory nobody may write is the case under test
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) //nolint:gosec // so the test's own cleanup can remove it

	metrics := func(h *harness) events.CheckResult {
		inst := h.install()
		inst.Policy.MetricsTextfile = dir
		require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
		report, err := ops.Doctor(ctx, h.Deps)
		require.NoError(t, err)
		i := slices.IndexFunc(report.Results, func(c events.CheckResult) bool { return c.ID == "metrics.textfile" })
		require.GreaterOrEqual(t, i, 0)
		return report.Results[i]
	}

	assert.Contains(t, metrics(newHarnessAt(t, "demo", userLayout)).Message, "is not writable")
	assert.NotContains(t, metrics(newHarness(t)).Message, "is not writable")
}
