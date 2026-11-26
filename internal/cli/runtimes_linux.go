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
//
// None for root, whose euid is 0. A Quadlet installation runs as the user who
// owns it (row 26), so `init` as root refuses the runtime rather than create an
// installation whose first `apply` the adapter would refuse.
func hostRuntimes(runner exec.Runner, sink func(exec.Line), redact []string, euid int) []ports.Runtime {
	if euid == 0 {
		return nil
	}
	return []ports.Runtime{quadlet.New(runner,
		quadlet.WithOutputSink(sink),
		quadlet.WithRedaction(redact),
		quadlet.WithHelperImage(os.Getenv(VolumeHelperImageEnv)),
	)}
}
