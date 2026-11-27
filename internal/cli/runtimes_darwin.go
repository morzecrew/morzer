package cli

import (
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// hostRuntimes is runtimes_linux.go's counterpart: a Mac runs the incumbent
// alone, since Quadlet needs systemd.
func hostRuntimes(exec.Runner, func(exec.Line), []string, int) []ports.Runtime { return nil }
