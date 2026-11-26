package cli

import (
	"path/filepath"

	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// newSupervisor is supervisor_darwin.go's counterpart: systemd's units, the
// user's own for a user installation (RFC 0023 row 26). Those carry the data
// home this command resolved, so they resolve the same tree
// (logs/quadlet-adapter.md, U6).
func newSupervisor(runner exec.Runner, paths domain.Paths) ports.Supervisor {
	if !paths.UserLayout() {
		return systemd.New(runner)
	}
	return systemd.New(runner, systemd.WithUserScope(userUnitDir(),
		"XDG_DATA_HOME="+filepath.Dir(paths.Root())))
}

// supervisedProducts names the products whose service unit the manager wrote,
// for a completion that cannot read the installations themselves: the user's
// own units for the user's tree.
func supervisedProducts(root string, user bool) []string {
	if user {
		return unitProducts(userUnitDir(), root)
	}
	return unitProducts(systemd.UnitDir, root)
}
