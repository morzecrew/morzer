package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
)

// On a Mac a docker command runs only once a CLI is settled, which it cannot be
// before `init` creates the manager directory; and until then a registry is
// reached with no config at all, never root's own (RFC 0055 decision 22,
// logs/macos-host.md).
func TestOnAMacNoEngineCommandRunsUnsettled(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", "")
	t.Setenv("PATH", os.Getenv("PATH"))
	paths := domain.Paths{VarDir: t.TempDir() + "/missing"}

	runner, settle := engineRunner(exec.New(), paths)

	if settle == nil {
		t.Fatal("no engine was wired")
	}
	_, err := runner.Run(context.Background(), exec.Command{Argv: []string{"docker", "version"}})
	if err == nil || !strings.Contains(err.Error(), "which `init` creates") {
		t.Errorf("a docker command ran with no CLI settled: %v", err)
	}
	if got := os.Getenv("DOCKER_CONFIG"); got == "" || strings.HasPrefix(got, os.Getenv("HOME")) {
		t.Errorf("DOCKER_CONFIG is %q, which reads root's own config", got)
	}
}
