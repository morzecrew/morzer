package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// A setting is an installation-level knob, as opposed to a release parameter.
//
// The two are different things wearing similar words. A parameter is declared by
// the vendor, resolved into the deployment and re-created when it changes; a
// setting is the operator's arrangement with the manager -- whether it may
// contact a registry, which reference it watches -- and changes nothing that is
// running.
//
// They share `morzer config` because that is where an operator looks for "change
// a value", and they cannot collide: a parameter name is
// [a-z][a-z0-9_]* (domain.ValidateParameters), so a dotted name is never one.
//
// Until this existed, `update.check` was settable by nothing at all. It shipped
// with an error message telling operators to enable it with `morzer config`,
// which read and wrote release parameters exclusively -- so the feature had an
// off switch, a documented name, and no way to turn it on.
type setting struct {
	Description string

	// Read renders the current value, for `config get`.
	Read func(domain.Installation) string

	// Apply validates the raw text and records it. It takes a pointer
	// because a setting may touch more than the field it names.
	Apply func(context.Context, *Deps, *domain.Installation, string) error

	// Clear returns the setting to its absent state, which is always the
	// conservative one: nothing here defaults to "on".
	Clear func(*domain.Installation)
}

var settings = map[string]setting{
	"update.check": {
		Description: "contact the vendor's registry unprompted, for `doctor` and `status`",
		Read:        func(i domain.Installation) string { return strconv.FormatBool(i.Update.Check) },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			v, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return domain.Usage("%q is not a boolean (true or false)", raw)
			}
			i.Update.Check = v
			return nil
		},
		Clear: func(i *domain.Installation) { i.Update.Check = false },
	},
	"update.auto_apply": {
		Description: "install what the channel offers, when the release declares it is recoverable",
		Read:        func(i domain.Installation) string { return strconv.FormatBool(i.Update.AutoApply) },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			v, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return domain.Usage("%q is not a boolean (true or false)", raw)
			}
			// The refusal for an installation with no signature policy
			// lives in Installation.Validate rather than here, so it
			// fires wherever the state is written -- including a path
			// that sets the two in the other order.
			i.Update.AutoApply = v
			return nil
		},
		Clear: func(i *domain.Installation) { i.Update.AutoApply = false },
	},
	"update.channel": {
		Description: "a mutable reference to follow, e.g. oci://registry.example/demo/bundle:stable",
		Read:        func(i domain.Installation) string { return i.Update.Channel },
		Apply: func(ctx context.Context, d *Deps, i *domain.Installation, raw string) error {
			raw = strings.TrimSpace(raw)
			ref, err := ports.ParseRef(raw)
			if err != nil {
				return err
			}
			if err := d.checkChannelIsFollowable(ctx, ref); err != nil {
				return err
			}
			i.Update.Channel = raw
			return nil
		},
		Clear: func(i *domain.Installation) { i.Update.Channel = "" },
	},
}

// checkChannelIsFollowable refuses a reference this manager could never watch.
//
// At configuration time rather than at the first tick, which is the same shape
// as `--skip-backup` requiring `--force`: a machine that accepts a setting and
// then silently does nothing with it is worse than one that refuses the setting.
//
// Only a *capability* answer refuses. A registry that is unreachable right now
// says nothing about whether the reference is followable, and refusing on it
// would make configuring a channel depend on the network being up at that
// moment -- which is exactly the situation an operator is often configuring
// their way out of.
func (d *Deps) checkChannelIsFollowable(ctx context.Context, ref ports.Ref) error {
	peeker, ok := d.Source.(ports.ChannelPeeker)
	if !ok {
		return nil
	}
	if _, err := peeker.Peek(ctx, ref); err != nil && errors.Is(err, domain.ErrUnsupported) {
		return err
	}
	return nil
}

// SettingNames lists what may be set, sorted.
func SettingNames() []string {
	out := make([]string, 0, len(settings))
	for name := range settings {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// IsSetting reports whether a name addresses an installation setting rather
// than a release parameter.
func IsSetting(name string) bool {
	_, ok := settings[name]
	return ok
}

// LooksLikeSetting reports whether a name was *meant* as one.
//
// A dotted name can never be a parameter, so `update.chanel` is a typo in a
// setting rather than an unknown parameter -- and telling an operator "the
// release declares no parameter update.chanel" would send them to the manifest
// to look for something that was never going to be there.
func LooksLikeSetting(name string) bool { return strings.Contains(name, ".") }

// IsSettingName is the routing question `config` asks: does this name belong to
// the settings side at all, known or mistyped.
//
// One predicate rather than the pair spelled out at each of `get`, `set` and
// `unset`. They must route a name identically -- an operator who sets
// `update.chanel` and then unsets it has to reach the same refusal both times --
// and three copies of a two-term condition is how that stops being true.
func IsSettingName(name string) bool { return IsSetting(name) || LooksLikeSetting(name) }

// SettingsReport is every installation setting and its current value.
type SettingsReport struct {
	Settings []SettingEntry `json:"settings"`
}

// SettingEntry is one setting as an operator sees it.
type SettingEntry struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

// ListSettings reads them all. Read-only: no lock, no journal.
func ListSettings(ctx context.Context, d *Deps) (SettingsReport, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return SettingsReport{}, err
	}

	out := SettingsReport{}
	for _, name := range SettingNames() {
		out.Settings = append(out.Settings, SettingEntry{
			Name:        name,
			Value:       settings[name].Read(inst),
			Description: settings[name].Description,
		})
	}
	return out, nil
}

// GetSetting reads one.
func GetSetting(ctx context.Context, d *Deps, name string) (SettingEntry, error) {
	s, ok := settings[name]
	if !ok {
		return SettingEntry{}, unknownSetting(name)
	}
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return SettingEntry{}, err
	}
	return SettingEntry{Name: name, Value: s.Read(inst), Description: s.Description}, nil
}

// SetSettingsOptions is a change to the installation's own knobs.
type SetSettingsOptions struct {
	Options

	Set   map[string]string
	Unset []string
}

// SetSettings records installation settings.
//
// Not an engine operation, and the difference from ConfigSet is the reason: a
// parameter change alters what the containers are running, so it plans, renders,
// re-creates and unwinds. A setting changes what the *manager* does next time.
// There is nothing to converge and nothing to compensate, so wrapping it in a
// six-step pipeline would be ceremony that makes the journal harder to read
// rather than the change safer.
//
// It still takes the deployment lock: two writers of installation state at once
// is how one of them loses their change silently.
func SetSettings(ctx context.Context, d *Deps, opts SetSettingsOptions) (Result, error) {
	if len(opts.Set) == 0 && len(opts.Unset) == 0 {
		return Result{}, domain.Usage("nothing to change")
	}

	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}

	next := inst
	var changed []string

	for _, name := range opts.Unset {
		s, ok := settings[name]
		if !ok {
			return Result{}, unknownSetting(name)
		}
		if s.Read(next) == s.Read(zeroFor(next, s)) {
			continue
		}
		s.Clear(&next)
		changed = append(changed, name)
	}

	for _, name := range sortedSettingNames(opts.Set) {
		s, ok := settings[name]
		if !ok {
			return Result{}, unknownSetting(name)
		}
		before := s.Read(next)
		if err := s.Apply(ctx, d, &next, opts.Set[name]); err != nil {
			return Result{}, err
		}
		if s.Read(next) == before {
			continue
		}
		changed = append(changed, name)
	}

	// This pass exists to refuse an unknown name, to reject a value before
	// anything is locked, and to answer a plan. It is *not* the authority on
	// what changed: a plan is the only caller that can be, because it takes
	// no lock and so has nothing fresher to compare against. The real run
	// recomputes the list under the lock.
	sort.Strings(changed)

	if opts.DryRun {
		if len(changed) == 0 {
			return Result{Summary: "no change: every value already matches"}, nil
		}
		return Result{Summary: "would set " + strings.Join(changed, ", ")}, nil
	}

	// A run that changes no value still reconciles the units, and that is
	// the only reason it does any work at all. Unit installation is the half
	// of a setting change that can fail after the state has been written --
	// leaving a machine whose state says it follows a channel and whose
	// supervisor has no timer -- and without this, repeating the command
	// that failed would match every value, report "no change", and never
	// reach the step that did not finish. Reconciliation is idempotent, so
	// the cost of the ordinary no-op case is one unit comparison.

	opID := d.newOpID()
	err = d.withLock(ctx, opID, domain.OpTypeConfig, opts.Options, func(ctx context.Context) error {
		// Re-read under the lock and re-apply, so a concurrent writer's
		// change to a *different* setting is not reverted by this one's
		// copy of the state -- the same reason every other operation
		// re-checks after taking the lock.
		fresh, err := d.loadInstallation(ctx)
		if err != nil {
			return err
		}
		// Recomputed against the reloaded state, and it is this answer
		// that decides both the write and the summary. The pre-lock list
		// was computed from a copy another operation may have changed
		// since: `update.check=false` that read false, was set to true
		// by somebody else, and is being set back to false here would
		// look like "no change", skip the write, and leave the operator
		// told their value was already in place while the file says
		// otherwise.
		changed = changed[:0]
		for _, name := range opts.Unset {
			s := settings[name]
			before := s.Read(fresh)
			s.Clear(&fresh)
			if s.Read(fresh) != before {
				changed = append(changed, name)
			}
		}
		for _, name := range sortedSettingNames(opts.Set) {
			s := settings[name]
			before := s.Read(fresh)
			if err := s.Apply(ctx, d, &fresh, opts.Set[name]); err != nil {
				return err
			}
			if s.Read(fresh) != before {
				changed = append(changed, name)
			}
		}
		sort.Strings(changed)

		if len(changed) > 0 {
			if err := d.saveInstallation(ctx, fresh); err != nil {
				return err
			}
		}
		// After the state, not before: the units are derived from it,
		// and a crash between the two leaves a timer that the next
		// setting change reconciles rather than a state nothing polls.
		return d.refreshUnits(ctx, fresh)
	})
	if err != nil {
		return Result{}, err
	}

	if len(changed) == 0 {
		return Result{Summary: "no change: every value already matches"}, nil
	}
	return Result{Summary: "set " + strings.Join(changed, ", ")}, nil
}

// refreshUnits makes the installed unit set match what the installation
// declares.
//
// It exists because the update timer is generated from a setting rather than
// from a flag at `init`: an operator who configures a channel a month later must
// get a timer, and one who clears it must stop having one. Units installed only
// at creation would make the second impossible, and a machine polling a channel
// nobody has configured any more is a phone-home with no owner.
//
// Reconciliation, not installation: the units this installation should have are
// written, and every *managed* unit not among them is removed. ManagedUnitNames
// is deliberately the superset for exactly this -- it is what lets a machine
// that once followed a channel have its timer taken away without the lifecycle
// layer knowing what a timer is called.
//
// A host with no supervisor is not an error. That is the same host that gets no
// units at `init`, and a container deployment is a legitimate one.
func (d *Deps) refreshUnits(ctx context.Context, inst domain.Installation) error {
	if d.Supervisor == nil || !d.Supervisor.Available(ctx) {
		return nil
	}

	// A machine that manages no units keeps managing none. `init
	// --install-units=false` is a supported choice -- a container, a host
	// where this does not run as root -- and installing units into it now
	// would be the manager overruling a decision somebody already made.
	installed, err := d.Supervisor.InstalledUnits(ctx, inst.Product)
	if err != nil || len(installed) == 0 {
		return err
	}

	units, err := d.Supervisor.Units(ports.UnitParams{
		Product:     inst.Product,
		ManagerPath: d.ManagerPath,
		ConfigPath:  d.Paths.InstallationFile(),
		// Both, not either. A timer exists to poll, and polling is
		// gated by `update.check` (RFC 0016 §5.6) -- so a machine with a
		// channel and checking off would install a unit that fails every
		// night on a refusal, which is how an operator learns to ignore
		// the unit.
		UpdateTimer: inst.Update.FollowsChannel() && inst.Update.Check,

		// A target to publish to is the whole precondition (RFC 0026
		// P4). There is no second flag gating this the way `update.check`
		// gates the poll: a fleet row is derived entirely from what this
		// machine already computes, goes to a target the operator chose,
		// and reveals nothing to anybody who could not already read that
		// target. The phone-home question that made update checking
		// opt-in does not arise.
		FleetTimer: inst.Backup.HasTargets(),
	})
	if err != nil {
		return err
	}

	wanted := make(map[string]bool, len(units))
	for _, unit := range units {
		wanted[unit.Name] = true
	}
	var stale []string
	for _, name := range d.Supervisor.ManagedUnitNames(inst.Product) {
		if !wanted[name] {
			stale = append(stale, name)
		}
	}

	if err := d.Supervisor.InstallUnits(ctx, units); err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}
	return d.Supervisor.RemoveUnits(ctx, stale)
}

// zeroFor returns the installation with one setting cleared, so "already at its
// absent value" can be asked without a second Read function per setting.
func zeroFor(inst domain.Installation, s setting) domain.Installation {
	s.Clear(&inst)
	return inst
}

// sortedSettingNames orders a change so two runs report the same list, and so
// a failure part-way through fails on the same setting each time.
func sortedSettingNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func unknownSetting(name string) error {
	return domain.Usage("no installation setting named %q", name).
		WithHint("settable: %s", strings.Join(SettingNames(), ", "))
}

// DescribeSettings renders the list for a human, one per line.
func DescribeSettings(report SettingsReport) string {
	var sb strings.Builder
	for _, entry := range report.Settings {
		value := entry.Value
		if value == "" {
			value = "(unset)"
		}
		fmt.Fprintf(&sb, "%-16s %-24s %s\n", entry.Name, value, entry.Description)
	}
	return strings.TrimRight(sb.String(), "\n")
}
