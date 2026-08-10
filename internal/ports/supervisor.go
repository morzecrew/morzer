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

	// Units renders the unit set this supervisor manages for a product.
	//
	// Unit *content* is supervisor-specific, so the lifecycle layer asks for
	// it rather than composing systemd stanzas it would have to rewrite for
	// any other init system.
	Units(params UnitParams) ([]Unit, error)

	// ManagedUnitNames lists the units this supervisor owns for a product,
	// so they can be queried or removed without the caller knowing their
	// naming scheme.
	ManagedUnitNames(product string) []string

	// InstalledUnits lists which of those are actually present.
	//
	// It answers a question nothing else can: whether this installation
	// manages units at all. `init --install-units=false` is a supported
	// choice, and a later reconciliation that installed units into a
	// machine that deliberately has none would be the manager overruling a
	// decision the operator already made -- on a host where it may not even
	// be root.
	InstalledUnits(ctx context.Context, product string) ([]string, error)
}

// UnitParams is what a supervisor needs to render its units.
type UnitParams struct {
	Product string

	// ManagerPath is the absolute path of the manager binary. A supervisor
	// must not rely on PATH: an init system's environment is not a login
	// shell's.
	ManagerPath string

	// ConfigPath is passed to the manager so a unit keeps working if the
	// default configuration location ever changes.
	ConfigPath string

	Description string

	// BackupSchedule is a supervisor-specific schedule expression.
	BackupSchedule string

	// UpdateSchedule is the schedule expression for the update timer, and
	// is also the maintenance window: an operator who wants updates only on
	// Sunday mornings says so here, rather than through a second mechanism
	// that would express it worse.
	UpdateSchedule string

	// UpdateTimer asks for the update pair at all. False on a machine that
	// follows no channel: a timer that polls nothing would still appear in
	// the supervisor's own listing as though it did.
	UpdateTimer bool
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
