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

	if len(changed) == 0 {
		return Result{Summary: "no change: every value already matches"}, nil
	}
	sort.Strings(changed)

	if opts.DryRun {
		return Result{Summary: "would set " + strings.Join(changed, ", ")}, nil
	}

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
		for _, name := range opts.Unset {
			settings[name].Clear(&fresh)
		}
		for _, name := range sortedSettingNames(opts.Set) {
			if err := settings[name].Apply(ctx, d, &fresh, opts.Set[name]); err != nil {
				return err
			}
		}
		return d.saveInstallation(ctx, fresh)
	})
	if err != nil {
		return Result{}, err
	}

	return Result{Summary: "set " + strings.Join(changed, ", ")}, nil
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
