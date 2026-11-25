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

// doctor asks a user installation's runtime whether the engine is the user's
// own, and fails it on the system's (RFC 0023 row 32). A system installation is
// not asked.
func TestDoctorAsksWhoseTheEngineIs(t *testing.T) {
	engineScope := func(h *harness) *events.CheckResult {
		report, err := ops.Doctor(context.Background(), h.Deps)
		require.NoError(t, err)
		i := slices.IndexFunc(report.Results, func(c events.CheckResult) bool { return c.ID == "runtime.engine-scope" })
		if i < 0 {
			return nil
		}
		return &report.Results[i]
	}

	user := newHarnessAt(t, "demo", userLayout)
	user.install()
	check := engineScope(user)
	require.NotNil(t, check, "a user installation's engine was not asked about")
	assert.Equal(t, events.CheckOK, check.Status)

	user.Runtime.Fail["ProbeEngineScope"] = domain.Preflight(nil, "the Docker daemon is the system's")
	assert.Equal(t, events.CheckFail, engineScope(user).Status)

	system := newHarness(t)
	system.install()
	assert.Nil(t, engineScope(system), "a system installation's engine was asked whose it is")
}
