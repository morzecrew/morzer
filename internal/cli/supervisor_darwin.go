package cli

import (
	"github.com/morzecrew/morzer/internal/adapters/supervisor/launchd"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// newSupervisor writes a Mac's LaunchDaemons, which log under the
// installation's VarDir (RFC 0055 §5.6).
func newSupervisor(runner exec.Runner, paths domain.Paths) ports.Supervisor {
	return launchd.New(runner, paths)
}

// supervisedProducts names the products whose boot job the manager wrote, for a
// completion that cannot read the installations themselves.
func supervisedProducts(root string) []string {
	return launchd.BootJobProducts(launchd.DaemonDir, root)
}
