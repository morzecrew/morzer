package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/morzecrew/morzer/internal/adapters/dockerdesktop"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
)

// On a Mac every docker command goes through the engine's runner, and every
// process the manager starts gets its DOCKER_CONFIG from the start, before
// anything is settled (RFC 0055 decision 22, logs/macos-host.md).
func TestOnAMacTheEngineRunnerOwnsTheCLIAndItsConfig(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", "")
	paths := domain.Paths{VarDir: t.TempDir()}

	runner, settle := engineRunner(exec.New(), paths)

	if _, ok := runner.(dockerdesktop.Runner); !ok || settle == nil {
		t.Fatalf("the runner is %T and settle %v; want the engine's", runner, settle != nil)
	}
	if got, want := os.Getenv("DOCKER_CONFIG"), filepath.Join(paths.ManagerDir(), "docker"); got != want {
		t.Errorf("DOCKER_CONFIG is %q, want %q", got, want)
	}
}
