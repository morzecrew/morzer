package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/test/fakes"
)

// What follows a secret change is a restart, and by then the change is
// written. These pin the two ways that restart can go wrong for an operator:
// running when there is nothing to restart, and failing in a way that reads as
// though the value had not been stored.

// restartApp is an installation of the example release, with `session_key` --
// which the release requires -- never set.
func restartApp(t *testing.T, running bool) (*App, *fakes.Runtime) {
	t.Helper()
	ctx := context.Background()

	paths := domain.PathsUnder(t.TempDir(), "demo")
	st := state.New(paths)
	require.NoError(t, st.SaveInstallation(ctx, domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_01",
		Product:       "demo",
		Profile:       "embedded",
	}))

	bundle, err := filepath.Abs(filepath.Join("..", "..", "testdata", "bundle"))
	require.NoError(t, err)
	rel, err := release.Load(bundle)
	require.NoError(t, err)
	require.NoError(t, st.SetCurrentRelease(ctx, domain.ReleaseRecord{
		SchemaVersion: domain.InstallationSchemaVersion,
		Name:          rel.Name(),
		Version:       rel.Version(),
		Root:          rel.Root,
	}))

	rt := fakes.NewRuntime()
	if running {
		rt.Services["app"] = ports.ServiceState{Name: "app", State: ports.StateRunning}
	}
	store := fakes.NewSecretStore()
	store.Seed(map[string]string{"db_password": "a-stored-password"})

	return &App{
		Stream: ui.Streams{Out: os.Stderr, Err: os.Stderr},
		Deps: &ops.Deps{
			Paths:   paths,
			State:   st,
			Secrets: store,
			Runtime: rt,
		},
	}, rt
}

func TestRestartDependentsWithNothingRunningRendersNothing(t *testing.T) {
	app, rt := restartApp(t, false)

	restarted, err := app.restartDependents(context.Background(), []string{"db_password"})
	require.NoError(t, err, "before the first apply there is nothing to restart, "+
		"and nothing to render for")
	assert.Empty(t, restarted)
	assert.NotContains(t, rt.Calls, "Restart:app,db")
	assert.NoDirExists(t, app.Deps.Paths.SecretsRenderDir())
}

func TestRestartDependentsSaysTheChangeWasStored(t *testing.T) {
	app, rt := restartApp(t, true)

	_, err := app.restartDependents(context.Background(), []string{"db_password"})
	require.Error(t, err)

	e := domain.AsError(err)
	assert.Contains(t, e.Message, "the change to db_password was stored",
		"exit 6 alone reads as the value not being saved, and a script retries "+
			"or gives up on a change that is already in force")
	assert.Contains(t, e.Message, "session_key", "what failed is still named")
	assert.Equal(t, domain.ExitSecrets, domain.ExitCode(err),
		"the exit code still says what failed: the render")
	assert.NotContains(t, rt.Calls, "Restart:app,db")
}
