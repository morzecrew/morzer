package ports

import "context"

// Supervisor manages the host init system's view of the product: the unit
// that starts it at boot and the timer that backs it up.
//
// v1 is systemd. The port exists so a container-only or non-systemd host is a
// new adapter rather than a fork of the lifecycle layer.
type Supervisor interface {
	// Available reports whether this supervisor can be used on this host.
	// A machine without systemd is not an error -- `init` simply skips
	// unit installation and says so.
	Available(ctx context.Context) bool

	// InstallUnits writes unit files atomically and reloads the daemon.
	InstallUnits(ctx context.Context, units []Unit) error

	// RemoveUnits deletes previously installed units and reloads.
	RemoveUnits(ctx context.Context, names []string) error

	Enable(ctx context.Context, unit string) error
	Disable(ctx context.Context, unit string) error
	Start(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error

	Status(ctx context.Context, unit string) (UnitState, error)
}

// Unit is a supervisor unit to install.
type Unit struct {
	// Name includes the suffix: "morzer.service", "morzer-backup.timer".
	Name string

	// Contents is the rendered unit file.
	Contents []byte

	// Enable requests the unit be enabled at boot after installation.
	Enable bool
}

// UnitState is what the supervisor reports about a unit.
type UnitState struct {
	Name string `json:"name"`

	// Loaded is false when the unit file is absent or unparseable.
	Loaded bool `json:"loaded"`

	// Active is the high-level state: active, inactive, failed,
	// activating, deactivating.
	Active string `json:"active"`

	// Sub is the fine-grained state: running, dead, exited, waiting.
	Sub string `json:"sub,omitempty"`

	Enabled bool `json:"enabled"`

	// ExitCode is the last exit status, which is how a
	// requires-manual-intervention (12) surfaces through systemd.
	ExitCode int `json:"exit_code,omitempty"`
}

func (u UnitState) Failed() bool { return u.Active == "failed" }
