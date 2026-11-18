package cli

import (
	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// newSupervisor is supervisor_darwin.go's counterpart: systemd's units.
func newSupervisor(runner exec.Runner, _ domain.Paths) ports.Supervisor { return systemd.New(runner) }

// supervisedProducts names the products whose service unit the manager wrote,
// for a completion that cannot read the installations themselves.
func supervisedProducts(root string) []string { return unitProducts(systemd.UnitDir, root) }
