package compose

import (
	"os"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// TestComposeDoesNotInheritTheOperatorsEnvironment is the back door this
// closes, asserted where it is actually wired rather than on the helper.
//
// The subprocess used to inherit the whole parent environment, so any
// <PRODUCT>_* variable in the operator's shell interpolated into a Compose
// file: undocumented, unvalidated, unrecorded, and invisible to the manifest.
// That is how a deployment ended up published on one port while preflight
// checked, and the health probe asked for, another.
func TestComposeDoesNotInheritTheOperatorsEnvironment(t *testing.T) {
	t.Setenv("DEMO_PARAM_HTTP_PORT", "31337")
	t.Setenv("DEMO_DATA_DIR", "/tmp/attacker")

	r := New(nil)
	cmd := r.command(ports.RuntimeConfig{
		Env: map[string]string{"DEMO_PARAM_HTTP_PORT": "9000"},
	}, 0, "docker", "compose", "up")

	env := envMap(cmd.Env)

	if got := env["DEMO_PARAM_HTTP_PORT"]; got != "9000" {
		t.Errorf("the manager's own value must win over the shell's, got %q", got)
	}
	if _, present := env["DEMO_DATA_DIR"]; present {
		t.Error("an undeclared product variable from the shell reached the runtime; " +
			"a declared parameter is the only supported channel")
	}

	// And what docker genuinely needs still arrives. Dropping PATH does not
	// fail a unit test; it fails on the machine that needed it.
	if env["PATH"] != os.Getenv("PATH") {
		t.Error("PATH did not survive the filter, so docker would not be found")
	}
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

var _ = exec.PassthroughEnv
