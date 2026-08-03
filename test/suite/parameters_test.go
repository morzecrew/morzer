package suite

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
)

// Parameters are the only supported way an operator value reaches a Compose
// file. These assert the two halves a fake Runtime can see: what the manager
// exports, and what it refuses to export.

func TestRuntimeConfigExportsEveryDeclaredParameter(t *testing.T) {
	h := newHarness(t)
	inst := h.install()

	cfg, err := h.Deps.RuntimeConfigFor(h.Release, inst)
	require.NoError(t, err)

	// Named exactly as the example bundle's Compose file interpolates it.
	// A rename here silently returns the deployment to whatever default
	// that file carries, which is why the assertion is on the literal name.
	assert.Equal(t, "18080", cfg.Env["DEMO_PARAM_HTTP_PORT"],
		"an unset parameter must still be exported, holding the release's default")
	assert.Equal(t, "info", cfg.Env["DEMO_PARAM_LOG_LEVEL"])

	for name := range h.Release.Manifest.Parameters {
		assert.Contains(t, cfg.Env, "DEMO_PARAM_"+upper(name),
			"every declared parameter must be reachable from the topology file")
	}
}

func TestAnOperatorValueReachesCompose(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	inst.Parameters = map[string]string{"http_port": "9000"}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	cfg, err := h.Deps.RuntimeConfigFor(h.Release, inst)
	require.NoError(t, err)
	assert.Equal(t, "9000", cfg.Env["DEMO_PARAM_HTTP_PORT"])
	assert.Equal(t, "info", cfg.Env["DEMO_PARAM_LOG_LEVEL"],
		"a parameter the operator did not set keeps its default")
}

// TestAParameterCannotShadowAManagedVariable pins the namespacing decision.
//
// With a flat <PRODUCT>_<NAME>, a parameter named `data_dir` would overwrite
// DEMO_DATA_DIR and take the deployment's storage with it. PARAM_ makes that
// structurally impossible rather than a rule somebody remembers.
func TestAParameterCannotShadowAManagedVariable(t *testing.T) {
	h := newHarness(t)
	inst := h.install()

	rel := h.Release
	rel.Manifest.Parameters = map[string]domain.ParameterSpec{
		"data_dir":    {Type: domain.ParamString, Default: "/tmp/attacker"},
		"secrets_dir": {Type: domain.ParamString, Default: "/tmp/attacker"},
		"config_file": {Type: domain.ParamString, Default: "/tmp/attacker"},
	}

	cfg, err := h.Deps.RuntimeConfigFor(rel, inst)
	require.NoError(t, err)

	for _, managed := range []string{"DEMO_DATA_DIR", "DEMO_SECRETS_DIR", "DEMO_CONFIG_FILE"} {
		assert.NotEqual(t, "/tmp/attacker", cfg.Env[managed],
			"%s is the manager's, and a parameter must not be able to repoint it", managed)
		assert.NotEmpty(t, cfg.Env[managed])
	}
	assert.Equal(t, "/tmp/attacker", cfg.Env["DEMO_PARAM_DATA_DIR"],
		"the parameter still arrives, under its own namespace")
}

// TestAnUndeclaredEnvironmentVariableDoesNotReachCompose is the back door this
// closes.
//
// The Compose subprocess used to inherit the whole parent environment, so any
// DEMO_* variable in the operator's shell interpolated into a Compose file —
// undocumented, unvalidated, unrecorded, and invisible to the manifest. That is
// how a deployment ended up published on one port while preflight checked, and
// the health probe asked for, another.
func TestAnUndeclaredEnvironmentVariableDoesNotReachCompose(t *testing.T) {
	t.Setenv("DEMO_PARAM_HTTP_PORT", "31337")
	t.Setenv("DEMO_DATA_DIR", "/tmp/attacker")
	t.Setenv("SOME_UNRELATED_THING", "1")

	env := exec.FilteredEnv(exec.PassthroughEnv, map[string]string{
		"DEMO_PARAM_HTTP_PORT": "9000",
	})

	got := envMap(env)
	assert.Equal(t, "9000", got["DEMO_PARAM_HTTP_PORT"],
		"the manager's own value wins over whatever is in the shell")
	assert.NotContains(t, got, "DEMO_DATA_DIR",
		"an undeclared product variable from the shell must not reach the runtime")
	assert.NotContains(t, got, "SOME_UNRELATED_THING")

	// And what a tool genuinely needs still arrives, or nothing runs.
	assert.Equal(t, os.Getenv("PATH"), got["PATH"])
}

func TestTheDockerClientKeepsWhatItNeeds(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///run/user/1000/docker.sock")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")

	got := envMap(exec.FilteredEnv(exec.PassthroughEnv, nil))

	// Dropping either of these does not fail a unit test; it fails on the
	// one machine that needed it, during an update, at three in the
	// morning.
	assert.Equal(t, "unix:///run/user/1000/docker.sock", got["DOCKER_HOST"])
	assert.Equal(t, "http://proxy.example:3128", got["HTTPS_PROXY"])
}

// TestTheExampleBundleTiesItsPortToItsParameter is a fixture test.
//
// The example bundle is the source of every code sample on the documentation
// site, so an incoherent one teaches the wrong thing to everybody who reads it.
func TestTheExampleBundleTiesItsPortToItsParameter(t *testing.T) {
	h := newHarness(t)

	params, err := domain.ResolveParameters(h.Release.Manifest.Parameters,
		map[string]string{"http_port": "9000"})
	require.NoError(t, err)

	ports, err := h.Release.Manifest.ResolvePorts(params)
	require.NoError(t, err)
	assert.Equal(t, []int{9000}, ports,
		"the bundle's requirements.ports must follow its http_port parameter")

	checks, err := h.Release.Manifest.ResolveHealthChecks(params)
	require.NoError(t, err)

	var api *string
	for i := range checks {
		if checks[i].Name == "api" {
			api = &checks[i].URL
		}
	}
	require.NotNil(t, api, "the example bundle declares an http check named api")
	assert.Contains(t, *api, ":9000/",
		"the bundle's health probe must follow its http_port parameter")
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		for i := range kv {
			if kv[i] == '=' {
				out[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	return out
}
