package exec_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
)

// A Mac job's manager stamps its own lines, and a child it starts must not
// inherit that: a hook that captures `morzer config get` would capture a time
// with the value. Everything else the parent has still reaches it.
func TestAChildDoesNotInheritLineStamping(t *testing.T) {
	t.Setenv(domain.StampLinesEnv, "1")
	t.Setenv("MORZER_PROBE_KEPT", "yes")

	env := exec.BaseEnv(map[string]string{"PROBE_OVERRIDE": "1"})
	for _, kv := range env {
		if strings.HasPrefix(kv, domain.StampLinesEnv+"=") {
			t.Errorf("a child inherits %s", kv)
		}
	}
	assert.Contains(t, env, "MORZER_PROBE_KEPT=yes")
	assert.Contains(t, env, "PROBE_OVERRIDE=1")
}
