package ops

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
)

// InstallationEntry is one installation as `ls` reports it.
type InstallationEntry struct {
	Product       string         `json:"product"`
	Path          string         `json:"path"`
	SchemaVersion int            `json:"schema_version,omitempty"`
	Mode          domain.Mode    `json:"mode,omitempty"`
	Release       domain.Version `json:"release,omitzero"`
	Units         int            `json:"units"`

	// Problem is why this row is incomplete, when it is. An installation
	// whose state will not parse is still an installation, and the one
	// thing an operator must not be told is that it is absent. A
	// schema_version this manager is too old to read is one of these:
	// refusing to interpret it is the same rule LoadInstallation already
	// applies, and a partially-read future installation reported as fact
	// would be worse than a row that says it cannot be read.
	Problem string `json:"problem,omitempty"`

	// Services is what --status found, and nil when it was not asked for.
	// ServicesProblem is per row rather than per command: one wedged
	// daemon must not blank the column for every other installation.
	Services        *ServiceCounts `json:"services,omitempty"`
	ServicesProblem string         `json:"services_problem,omitempty"`
}

// ServiceCounts is the summary --status adds, not the full service list:
// `morzer status --product X` is where the detail lives, and a listing that
// reprinted it would be a worse version of that command.
type ServiceCounts struct {
	Running int `json:"running"`
	Total   int `json:"total"`
}

// ListOptions is one flag today and a struct anyway: `--status` is the
// difference between a listing that reads files and one that talks to a
// daemon, and a bool parameter at the call site would not say which.
type ListOptions struct {
	Status        bool
	StatusTimeout time.Duration // per installation; DefaultStatusTimeout when zero
}

// DefaultStatusTimeout bounds one installation's runtime query.
//
// Per row rather than for the command, and short: one wedged daemon must not
// turn a machine listing into a hang, which is the failure mode that makes
// people stop running a listing command at all. The overall --timeout still
// governs the whole run, as it does everywhere else.
const DefaultStatusTimeout = 5 * time.Second

// ListInstallations reports every installation on this machine.
//
// Read from state files alone unless --status is asked for: no Docker call, no
// lock, no network. That is the whole point of the command -- it answers on a
// machine whose daemon is down, which is exactly when somebody is trying to
// find out what is on the box.
//
// It is not an engine operation. Nothing here mutates, so there is nothing to
// journal and nothing to compensate.
func ListInstallations(ctx context.Context, d *Deps, opts ListOptions) ([]InstallationEntry, error) {
	if d.StateFor == nil {
		// A build that wired no way to read another installation's
		// state. Loud, because the alternative is a listing that
		// reports every installation as unreadable and looks like a
		// broken machine rather than a broken binary.
		return nil, domain.Internal(nil, "this build cannot read installation state")
	}

	products, err := DiscoverProducts(d.Paths.Root())
	if err != nil {
		return nil, err
	}

	out := make([]InstallationEntry, 0, len(products))
	for _, product := range products {
		out = append(out, d.installationEntry(ctx, product, opts))
	}
	return out, nil
}

// installationEntry reads one installation, degrading field by field.
//
// Every failure below is recorded on the row rather than returned: a machine
// listing whose second installation is broken must still describe the first,
// and dropping the broken one would report it as absent at the exact moment
// its state stopped parsing.
func (d *Deps) installationEntry(ctx context.Context, product string, opts ListOptions) InstallationEntry {
	scoped := d.forInstallation(product)

	entry := InstallationEntry{Product: product, Path: scoped.Paths.EtcDir}

	// Asked before the state is read, and kept even on a row that has a
	// problem: "its units are installed and its state will not load" is
	// the condition `doctor`'s machine.installations warns about, and the
	// listing is where an operator sees it first. The units are named from
	// the product, so this answers whether or not the state does.
	if d.Supervisor != nil {
		if units, err := d.Supervisor.InstalledUnits(ctx, product); err == nil {
			entry.Units = len(units)
		}
	}

	inst, err := scoped.State.LoadInstallation(ctx)
	if err != nil {
		// Nothing interpreted beside it. A future schema is the case
		// this rule exists for: the message names the version, and
		// reporting fields this manager does not understand as fact
		// would be worse than a row saying it cannot read them.
		entry.Problem = domain.AsError(err).Message
		return entry
	}
	entry.SchemaVersion = inst.SchemaVersion
	entry.Mode = inst.Mode

	current, err := scoped.State.CurrentRelease(ctx)
	switch {
	case err != nil:
		// The installation is readable and its release pointer is not,
		// which is a narrower fault than the one above: the row keeps
		// what it knows and says what it could not read.
		entry.Problem = "cannot read the current release: " + domain.AsError(err).Message
		return entry
	case !current.IsZero():
		entry.Release = current.Version
	}

	if opts.Status {
		scoped.fillServiceCounts(ctx, &entry, inst, current, opts.timeout())
	}
	return entry
}

func (o ListOptions) timeout() time.Duration {
	if o.StatusTimeout <= 0 {
		return DefaultStatusTimeout
	}
	return o.StatusTimeout
}

// fillServiceCounts asks the runtime what this installation is running.
//
// Bounded on its own clock and reported on its own row. Every failure here is
// a string in one column: an installation whose release directory was deleted,
// or whose daemon is not answering, costs the reader that row's service count
// and nothing else.
func (d *Deps) fillServiceCounts(
	ctx context.Context,
	entry *InstallationEntry,
	inst domain.Installation,
	current domain.ReleaseRecord,
	timeout time.Duration,
) {
	if d.Runtime == nil {
		entry.ServicesProblem = "no container runtime is configured"
		return
	}
	if current.IsZero() {
		entry.ServicesProblem = "no release is installed"
		return
	}

	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		entry.ServicesProblem = domain.AsError(err).Message
		return
	}
	cfg, err := d.runtimeConfig(rel, inst, "")
	if err != nil {
		entry.ServicesProblem = domain.AsError(err).Message
		return
	}

	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	services, err := d.Runtime.Status(queryCtx, cfg)
	if err != nil {
		// The deadline is reported as itself rather than as whatever
		// the adapter said about a killed subprocess: "timed out" is
		// the answer an operator can act on, and it is the one this
		// bound exists to produce.
		if errors.Is(queryCtx.Err(), context.DeadlineExceeded) {
			entry.ServicesProblem = "timed out after " + timeout.String()
			return
		}
		entry.ServicesProblem = domain.AsError(err).Message
		return
	}

	counts := ServiceCounts{Total: len(services)}
	for _, s := range services {
		if s.Running() {
			counts.Running++
		}
	}
	entry.Services = &counts
}

// forInstallation points a copy of these dependencies at another installation
// on the same machine.
//
// Only the state store and the paths are re-derived, and everything that holds
// state for *this* installation is dropped rather than carried: a lock, a
// secret store or a backup engine belonging to one installation, used against
// another, would act on the wrong deployment while every path in the log said
// otherwise. Nil is the fail-safe reading -- a later caller that reaches for
// one gets a panic in a test rather than a silently misdirected operation.
//
// What survives is what is genuinely per-machine or per-call: the runtime
// (which takes its project in every call), the supervisor (which takes its
// product), and the clocks and registries.
func (d *Deps) forInstallation(product string) *Deps {
	scoped := *d
	scoped.Paths = domain.PathsUnder(d.Paths.Root(), product)
	scoped.State = d.StateFor(scoped.Paths)

	scoped.Locker = nil
	scoped.Secrets = nil
	scoped.Backup = nil
	scoped.Targets = nil
	scoped.Notifier = nil

	// The inventory travels with it: an entry read this way was selected
	// by name, so a refusal about an unchosen machine must not fire
	// underneath it.
	scoped.ProductNamed = true
	return &scoped
}

// DiscoverProducts lists the products that have an installation under root.
//
// The filesystem is the registry: `<root>/etc/*/installation.yaml` is the
// truth, and a machine-level index would be a second source to keep in sync --
// wrong exactly when a machine was rebuilt by hand.
//
// The list is returned whole rather than reduced to "exactly one, or nothing".
// Every caller needs it: one to select an installation, one to refuse naming
// the alternatives, one to list them. Reducing it early is what made a machine
// with two installations report that it had none.
//
// A missing `etc` is an empty machine and not an error -- that is a bare host,
// or a --root that has never been written to. Anything else is refused: "I
// cannot look" and "there is nothing there" are different answers, and a
// listing that conflated them would report an unreadable /etc as a machine
// with nothing on it.
func DiscoverProducts(root string) ([]string, error) {
	base := filepath.Join(root, "/etc")

	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, domain.InstallationError(err, "cannot read %s", base).
			WithHint("installations are discovered from %s/*/%s; check its permissions",
				base, domain.InstallationFileName)
	}

	var found []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		marker := filepath.Join(base, e.Name(), domain.InstallationFileName)
		if _, err := os.Stat(marker); err != nil {
			continue
		}
		// A directory whose name is not a legal product name cannot be
		// one of ours: every path the manager owns is derived from a
		// validated name, so a `/etc/Foo Bar/installation.yaml` someone
		// left there is not an installation this manager could have
		// made.
		if domain.ValidateProductName(e.Name()) != nil {
			continue
		}
		found = append(found, e.Name())
	}
	sort.Strings(found)
	return found, nil
}
