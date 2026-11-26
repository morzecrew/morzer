package cli

import (
	"os"

	"github.com/morzecrew/morzer/internal/adapters/runtime/quadlet"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// hostRuntimes are the runtimes this host can run beside the incumbent:
// Quadlet, on Linux only, since it drives the user's own systemd manager (RFC
// 0023 P3b). runtimes_darwin.go has none.
func hostRuntimes(runner exec.Runner, sink func(exec.Line), redact []string) []ports.Runtime {
	return []ports.Runtime{quadlet.New(runner,
		quadlet.WithOutputSink(sink),
		quadlet.WithRedaction(redact),
		quadlet.WithHelperImage(os.Getenv(VolumeHelperImageEnv)),
	)}
}
