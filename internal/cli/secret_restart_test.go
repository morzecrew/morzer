package cli

import (
	"bytes"
	"context"
	"errors"
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
	bundle, err := filepath.Abs(filepath.Join("..", "..", "testdata", "bundle"))
	require.NoError(t, err)
	return restartAppAt(t, bundle, services...)
}

func restartAppAt(t *testing.T, bundle string, services ...ports.ServiceState) (*App, *fakes.Runtime) {
	t.Helper()
	ctx := context.Background()

	paths := domain.PathsUnder(t.TempDir(), "demo")
	st := state.New(paths)
	require.NoError(t, st.SaveInstallation(ctx, domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_01",
		Product:       "demo",
		Profiles:      []string{"embedded"},
	}))

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

	// Nor does it need the runtime to answer: the engine may be down along
	// with the product, and nothing is restarted either way.
	down := domain.RuntimeError(errors.New("cannot connect to the Docker daemon"), "the engine is down")
	rt.Fail = map[string]error{"Status": down, "Validate": down}
	restarted, err = app.restartDependents(ctx, []string{"db_password"})
	require.NoError(t, err, "a stopped installation failed a stored change on an engine that is down")
	assert.Empty(t, restarted)
}

// A secret no service declares is still rendered when it changes, so whatever
// reads the rendered file is not left with the old value, and the operator is
// told what picks the new one up (#238).
func TestASecretNoServiceDeclaresIsStillRendered(t *testing.T) {
	ctx := context.Background()
	bundle := t.TempDir()
	require.NoError(t, os.CopyFS(bundle, os.DirFS(filepath.Join("..", "..", "testdata", "bundle"))))
	schemaPath := filepath.Join(bundle, "secrets.schema.yaml")
	schema, err := os.ReadFile(schemaPath)
	require.NoError(t, err)
	schema = append(schema, []byte("  - name: report_token\n    description: read only by a configuration template\n    required: false\n")...)
	require.NoError(t, os.WriteFile(schemaPath, schema, 0o600))

	app, rt := restartAppAt(t, bundle, ports.ServiceState{Name: "app", State: ports.StateRunning})
	store, ok := app.Deps.Secrets.(*fakes.SecretStore)
	require.True(t, ok)
	store.Seed(map[string]string{
		"db_password": "a-stored-password", "session_key": "a-stored-session-key", "report_token": "old",
	})
	_, err = app.restartDependents(ctx, []string{"report_token"})
	require.NoError(t, err)
	rendered := filepath.Join(app.Deps.Paths.SecretsRenderDir(), "report_token")
	require.FileExists(t, rendered)

	var told bytes.Buffer
	app.Stream.Err = &told
	require.NoError(t, store.Set(ctx, "report_token", domain.NewSecret("new")))
	restarted, err := app.restartDependents(ctx, []string{"report_token"})
	require.NoError(t, err)
	assert.Empty(t, restarted)
	assert.NotContains(t, rt.Calls, "Restart")
	got, err := os.ReadFile(rendered)
	require.NoError(t, err)
	assert.Equal(t, "new", string(got), "the rendered file kept the old value")
	assert.Contains(t, told.String(), "No service declares report_token")
	assert.Contains(t, told.String(), "until it restarts")
	assert.NotContains(t, told.String(), "morzer apply", "a template holds the path, so apply changes nothing")

	// A name the release does not declare is stored and never rendered, so
	// nothing may claim a file holds it.
	told.Reset()
	require.NoError(t, store.Set(ctx, "not_declared", domain.NewSecret("v")))
	_, err = app.restartDependents(ctx, []string{"not_declared"})
	require.NoError(t, err)
	assert.NotContains(t, told.String(), "rendered file", "a file that does not exist was said to hold the value")
}

// A declared service behind an inactive profile is not in the configuration,
// and Compose refuses to restart a service it does not know. The secret is
// stored and rendered, so it is not a failure: the service reads the file when
// the profile is turned on and it is created. Only the active ones restart
// (#324).
func TestRestartDependentsSkipsAServiceBehindAnInactiveProfile(t *testing.T) {
	ctx := context.Background()
	bundle := t.TempDir()
	require.NoError(t, os.CopyFS(bundle, os.DirFS(filepath.Join("..", "..", "testdata", "bundle"))))
	schemaPath := filepath.Join(bundle, "secrets.schema.yaml")
	schema, err := os.ReadFile(schemaPath)
	require.NoError(t, err)
	schema = append(schema, []byte(
		"  - name: shared_token\n    description: read by app and by vpn\n    required: false\n    services: [app, vpn]\n"+
			"  - name: upstream_openvpn\n    description: read only by vpn\n    required: false\n    services: [vpn]\n")...)
	require.NoError(t, os.WriteFile(schemaPath, schema, 0o600))

	// vpn is left out of the configured services: its profile is off.
	app, rt := restartAppAt(t, bundle, ports.ServiceState{Name: "app", State: ports.StateRunning})
	store, ok := app.Deps.Secrets.(*fakes.SecretStore)
	require.True(t, ok)
	store.Seed(map[string]string{
		"db_password": "a-stored-password", "session_key": "a-stored-session-key",
		"shared_token": "t", "upstream_openvpn": "ovpn",
	})

	restarted, err := app.restartDependents(ctx, []string{"shared_token"})
	require.NoError(t, err, "a service behind an inactive profile failed a change that was stored")
	assert.Equal(t, []string{"app"}, restarted)
	assert.Contains(t, rt.Calls, "Restart:app")

	var told bytes.Buffer
	app.Stream.Err = &told
	rt.Calls = nil
	restarted, err = app.restartDependents(ctx, []string{"upstream_openvpn"})
	require.NoError(t, err, "a secret read only behind an inactive profile could not be set")
	assert.Empty(t, restarted)
	assert.NotContains(t, rt.Calls, "Restart", "a restart naming no service restarts every one")
	assert.NotContains(t, told.String(), "No service declares", "the release does declare one")
	got, err := os.ReadFile(filepath.Join(app.Deps.Paths.SecretsRenderDir(), "upstream_openvpn"))
	require.NoError(t, err, "the file vpn reads when its profile is turned on was not rendered")
	assert.Equal(t, "ovpn", string(got))
}

// A runtime that cannot say which services the configuration defines fails
// the change it would have restarted for, still saying it was stored: a
// restart of the unfiltered services could name one Compose refuses.
func TestRestartDependentsFailsWhenTheConfigurationCannotBeRead(t *testing.T) {
	app, rt := restartApp(t, ports.ServiceState{Name: "app", State: ports.StateRunning})
	store, ok := app.Deps.Secrets.(*fakes.SecretStore)
	require.True(t, ok)
	store.Seed(map[string]string{
		"db_password": "a-stored-password", "session_key": "a-stored-session-key",
	})
	rt.Fail = map[string]error{"Validate": domain.RuntimeError(errors.New("exit status 15"), "compose configuration is invalid")}

	restarted, err := app.restartDependents(context.Background(), []string{"db_password"})
	require.Error(t, err)
	assert.Empty(t, restarted)
	assert.Contains(t, domain.AsError(err).Message, "the change to db_password was stored")
	assert.Equal(t, domain.ExitRuntime, domain.ExitCode(err))
	assert.NotContains(t, rt.Calls, "Restart")
}
