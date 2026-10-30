package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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
// which the release requires -- never set, and services as the runtime reports
// them.
func restartApp(t *testing.T, services ...ports.ServiceState) (*App, *fakes.Runtime) {
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
	for _, svc := range services {
		rt.Services[svc.Name] = svc
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
	app, rt := restartApp(t)

	restarted, err := app.restartDependents(context.Background(), []string{"db_password"})
	require.NoError(t, err, "before the first apply there is nothing to restart, "+
		"and nothing to render for")
	assert.Empty(t, restarted)
	assert.NotContains(t, rt.Calls, "Restart:app,db")
	assert.NoDirExists(t, app.Deps.Paths.SecretsRenderDir())
}

func TestRestartDependentsSaysTheChangeWasStored(t *testing.T) {
	app, rt := restartApp(t, ports.ServiceState{Name: "app", State: ports.StateRunning})

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

// Only a project with no containers at all has nothing to update. An unhealthy
// service is the one an operator is most likely changing a credential for, and
// a stopped or paused one still reads the files when it comes back -- so each
// of these renders, which here fails on the unset `session_key`.
func TestRestartDependentsRendersForAnyContainerThatExists(t *testing.T) {
	for _, svc := range []ports.ServiceState{
		{Name: "app", State: ports.StateRunning, Health: ports.HealthUnhealthy},
		{Name: "app", State: ports.StatePaused},
		{Name: "app", State: ports.StateRestarting},
		{Name: "app", State: "exited"},
	} {
		app, _ := restartApp(t, svc)

		_, err := app.restartDependents(context.Background(), []string{"db_password"})
		require.Error(t, err, "a %s %s service was treated as nothing to restart, "+
			"and kept the old credentials", svc.State, svc.Health)
		assert.Contains(t, domain.AsError(err).Message, "session_key")
	}
}

// While an operator has the product stopped, the change is rendered for the
// containers that exist and nothing is restarted: a restart starts a stopped
// container, and `morzer start` is what brings it back (RFC 0040).
func TestRestartDependentsRestartsNothingWhileStopped(t *testing.T) {
	ctx := context.Background()
	app, rt := restartApp(t, ports.ServiceState{Name: "app", State: "exited"})
	store, ok := app.Deps.Secrets.(*fakes.SecretStore)
	require.True(t, ok)
	store.Seed(map[string]string{
		"db_password": "a-stored-password", "session_key": "a-stored-session-key",
	})
	inst, err := app.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Stopped = &domain.Stop{At: domain.NewTime(time.Now()), OperationID: "op_stop"}
	require.NoError(t, app.Deps.State.SaveInstallation(ctx, inst))

	restarted, err := app.restartDependents(ctx, []string{"db_password"})
	require.NoError(t, err)
	assert.Empty(t, restarted)
	assert.NotContains(t, rt.Calls, "Restart")
	assert.DirExists(t, app.Deps.Paths.SecretsRenderDir(), "the change was not rendered for start to pick up")
}
