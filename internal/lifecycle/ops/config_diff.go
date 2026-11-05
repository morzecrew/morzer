package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// ConfigDiffReport is where the configuration files on disk differ from what
// the installed release renders (RFC 0044 §5.1).
type ConfigDiffReport struct {
	Release string `json:"release"`
	Targets int    `json:"targets"`

	// Redacted is false only when a file differs and its lines could not
	// be scrubbed, so they are not shown.
	Redacted bool `json:"redacted"`

	Drift      []TargetDiff       `json:"drift"`
	Unreadable []UnreadableTarget `json:"unreadable"`

	// Services are the services created from another definition than the
	// release declares, which the next `up` re-creates (§5.4).
	// ServicesCompared is false when the runtime could not be asked, and
	// ServicesSkipped says why.
	Services         []ServiceDrift `json:"services"`
	ServicesCompared bool           `json:"services_compared"`
	ServicesSkipped  string         `json:"-"`
}

// ServiceDrift is one service whose containers were created from another
// definition: the hash the release declares, and one per container.
type ServiceDrift struct {
	Service  string   `json:"service"`
	Declared string   `json:"declared"`
	Running  []string `json:"running"`
}

// ConfigDiff compares the files on disk with what the installed release renders
// from the recorded parameters. Read-only: no lock, no journal.
func ConfigDiff(ctx context.Context, d *Deps) (ConfigDiffReport, error) {
	inst, rel, err := d.currentInstallationAndRelease(ctx)
	if err != nil {
		return ConfigDiffReport{}, err
	}
	comparison, err := configComparison(ctx, d, inst, rel, "")
	if err != nil {
		return ConfigDiffReport{}, err
	}
	comparison, shown := d.scrubbed(ctx, comparison)
	services, skipped := d.definitionDrift(ctx, inst, rel)

	out := ConfigDiffReport{
		Release:          rel.Version().String(),
		Targets:          comparison.Targets,
		Redacted:         shown,
		Drift:            comparison.Diffs,
		Unreadable:       comparison.Unreadable,
		Services:         services,
		ServicesCompared: skipped == "",
		ServicesSkipped:  skipped,
	}
	// Lists rather than nulls, for a script that ranges over them.
	if out.Drift == nil {
		out.Drift = []TargetDiff{}
	}
	if out.Unreadable == nil {
		out.Unreadable = []UnreadableTarget{}
	}
	if out.Services == nil {
		out.Services = []ServiceDrift{}
	}
	return out, nil
}

// definitionDrift asks the runtime which services the next `up` would
// re-create (RFC 0044 §5.4). skipped says why nothing was compared: a runtime
// without the capability, or one that cannot answer, is never drift (decision
// 11).
func (d *Deps) definitionDrift(
	ctx context.Context, inst domain.Installation, rel domain.Release,
) (drifted []ServiceDrift, skipped string) {
	drifter, ok := d.Runtime.(ports.DefinitionDrifter)
	if !ok {
		return nil, "this runtime records no service definitions"
	}
	cfg, err := d.runtimeConfig(rel, inst, "")
	if err != nil {
		return nil, domain.AsError(err).Message
	}
	defs, err := drifter.DefinitionDrift(ctx, cfg)
	if err != nil {
		return nil, domain.AsError(err).Message
	}
	for _, def := range defs {
		if def.Drifted() {
			drifted = append(drifted, ServiceDrift{Service: def.Service, Declared: def.Declared, Running: def.Running})
		}
	}
	return drifted, ""
}

// Text is the diffs, the unreadable targets and the drifted services, for
// stdout.
func (r ConfigDiffReport) Text() string {
	text := ConfigComparison{Diffs: r.Drift, Unreadable: r.Unreadable}.text(r.Redacted)
	if len(r.Services) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	if text != "" {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "services created from another definition than %s declares:\n", r.Release)
	for _, s := range r.Services {
		differs := 0
		for _, hash := range s.Running {
			if hash != s.Declared {
				differs++
			}
		}
		fmt.Fprintf(&b, "  %s (%d of %d containers)\n", s.Service, differs, len(s.Running))
	}
	return b.String()
}

// Summary is what `config diff` says beneath them, on stderr.
func (r ConfigDiffReport) Summary() string {
	var lines []string
	switch {
	case r.Targets == 0:
		lines = append(lines, r.Release+" renders no configuration files")
	case len(r.Drift) == 0 && len(r.Unreadable) == 0:
		lines = append(lines, fmt.Sprintf("no drift: %s what %s renders",
			matchVerb(r.Targets), r.Release))
	case len(r.Drift) == 0:
		lines = append(lines, fmt.Sprintf("no drift in the files compared with what %s renders.", r.Release))
	default:
		lines = append(lines, fmt.Sprintf("%d of %s from what %s renders.",
			len(r.Drift), configFiles(r.Targets), r.Release))
		if !r.Redacted {
			lines = append(lines,
				"Their lines are not shown: the secret values could not be loaded to scrub them.")
		}
		lines = append(lines, "`morzer apply` replaces them; a change meant to last belongs in a parameter.")
	}
	switch n := len(r.Unreadable); n {
	case 0:
	case 1:
		lines = append(lines, "1 could not be read, so it was not compared.")
	default:
		lines = append(lines, fmt.Sprintf("%d could not be read, so they were not compared.", n))
	}
	switch n := len(r.Services); {
	case !r.ServicesCompared:
		lines = append(lines, "The services were not compared: "+r.ServicesSkipped+".")
	case n == 1:
		lines = append(lines, "1 service was created from another definition; the next `apply` re-creates it.")
	case n > 1:
		lines = append(lines, fmt.Sprintf(
			"%d services were created from another definition; the next `apply` re-creates them.", n))
	}
	return strings.Join(lines, "\n")
}

// configFiles counts configuration files, with the verb for a difference.
func configFiles(n int) string {
	if n == 1 {
		return "1 configuration file differs"
	}
	return fmt.Sprintf("%d configuration files differ", n)
}

func matchVerb(n int) string {
	if n == 1 {
		return "the configuration file matches"
	}
	return fmt.Sprintf("the %d configuration files match", n)
}

// checkConfigDrift warns while a configuration file differs from what the
// release renders (RFC 0044 §5.2).
//
// A warning, not a failure, as secrets.rotation is: a hand edit can be
// deliberate, and an exit code that pages over one trains people to ignore it.
// It is the notice that the next `apply` replaces the file.
//
// A target that cannot be read is named and never counted, as in the fleet
// row: an unreadable `/etc` is a permission fault, not a change.
func (d *Deps) checkConfigDrift(inst domain.Installation, rel domain.Release, skip string) preflight.Check {
	return preflight.Check{
		ID:          "config.drift",
		Category:    preflight.CategoryConfig,
		Description: "the configuration files match what the release renders",
		Run: func(ctx context.Context) events.CheckResult {
			if skip == "" && d.Renderer == nil {
				skip = "this manager renders no templates"
			}
			if skip != "" {
				return preflight.OK("not checked: %s", skip)
			}
			comparison, err := configComparison(ctx, d, inst, rel, "")
			if err != nil {
				return preflight.Warn("`morzer config diff` says the same",
					"cannot render the configuration to compare it: %s", domain.AsError(err).Message)
			}
			var unread string
			if len(comparison.Unreadable) > 0 {
				names := make([]string, len(comparison.Unreadable))
				for i, u := range comparison.Unreadable {
					names[i] = u.Target
				}
				unread = "; not compared, because they cannot be read: " + strings.Join(names, ", ")
			}
			if len(comparison.Diffs) == 0 {
				matched := "every configuration file matches"
				if unread != "" {
					matched = "every file that could be read matches"
				}
				return preflight.OK("%s what %s renders%s", matched, rel.Version(), unread)
			}
			names := make([]string, len(comparison.Diffs))
			for i, diff := range comparison.Diffs {
				names[i] = diff.Target
			}
			return preflight.Warn(
				"`morzer config diff` shows them; `morzer apply` replaces them, "+
					"and a change meant to last belongs in a parameter",
				"%s from what %s renders: %s%s",
				configFiles(len(names)), rel.Version(), strings.Join(names, ", "), unread)
		},
	}
}

// checkDefinitionDrift warns while a service's containers were created from
// another definition than the release declares (RFC 0044 §5.4): the next `up`,
// and so `apply`, re-creates them. A warning, as config.drift is: a service
// created by hand, or by an older manager, can be what an operator meant.
func (d *Deps) checkDefinitionDrift(inst domain.Installation, rel domain.Release, skip string) preflight.Check {
	return preflight.Check{
		ID:          "runtime.definition",
		Category:    preflight.CategoryRuntime,
		Description: "the services run the definition the release declares",
		Run: func(ctx context.Context) events.CheckResult {
			if skip != "" {
				return preflight.OK("not checked: %s", skip)
			}
			drifted, skipped := d.definitionDrift(ctx, inst, rel)
			if skipped != "" {
				return preflight.OK("not compared: %s", skipped)
			}
			if len(drifted) == 0 {
				return preflight.OK("every service runs the definition %s declares", rel.Version())
			}
			names := make([]string, len(drifted))
			for i, s := range drifted {
				names[i] = s.Service
			}
			return preflight.Warn(
				"the next `morzer apply` re-creates them; `morzer config diff` lists them",
				"%d service(s) created from another definition than %s declares: %s",
				len(drifted), rel.Version(), strings.Join(names, ", "))
		},
	}
}

// heldForDrift is why an unattended tick leaves a release staged rather than
// replace configuration files somebody edited, or empty when nothing holds it
// (RFC 0044 decision 4). A hand edit replaced with nobody watching cannot be
// recovered, and a staged release can be installed later.
//
// A tick that cannot compare the files holds too: it cannot tell that no edit
// would be lost. An unreadable target does not, as it is never drift, and nor
// does an absent one: there is no edit in it to lose, and an installation
// nothing has applied yet has every target absent.
func (d *Deps) heldForDrift(ctx context.Context, inst domain.Installation) string {
	const staged = ", so the release is staged and not installed; `morzer config diff` "
	n, err := d.driftedFiles(ctx, inst)
	switch {
	case err != nil:
		return "the configuration files could not be compared with what the installed release renders (" +
			domain.AsError(err).Message + ")" + staged + "says why"
	case n > 0:
		return configFiles(n) + " from what the installed release renders" + staged + "shows them"
	}
	return ""
}

func (d *Deps) driftedFiles(ctx context.Context, inst domain.Installation) (int, error) {
	current, err := d.State.CurrentRelease(ctx)
	if err != nil || current.IsZero() {
		return 0, err
	}
	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		return 0, err
	}
	comparison, err := configComparison(ctx, d, inst, rel, "")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, diff := range comparison.Diffs {
		if _, err := os.Stat(diff.Target); err == nil {
			n++
		}
	}
	return n, nil
}

// existingTargets are the files a release would replace although the
// installed release never rendered them (RFC 0048 §5.1): a target the
// installed release does not declare, where something already is and holds
// other bytes. The drift checks cannot see these, as they compare against
// the installed release only.
type existingTargets struct {
	owner      ownedTargets
	replaced   []string
	unreadable []string
}

func (e existingTargets) any() bool { return len(e.replaced)+len(e.unreadable) > 0 }

// lines says what happens to each, one line per file, for a plan and for the
// tick's reason.
func (e existingTargets) lines(next string, held bool) []string {
	by := e.owner.installed + " does not render it"
	switch {
	case e.owner.unknown:
		by = "the installed release cannot be read to tell whether it renders it"
	case e.owner.installed == "":
		by = "no release has rendered it here yet"
	}
	what := next + " replaces it and keeps a copy"
	if held {
		what = next + " would replace it"
	}
	var out []string
	for _, target := range e.replaced {
		out = append(out, fmt.Sprintf("%s exists, and %s: %s", target, by, what))
	}
	for _, target := range e.unreadable {
		out = append(out, fmt.Sprintf("%s exists and cannot be read, so it cannot be kept", target))
	}
	return out
}

// ownedTargets are the paths the installed release renders, for telling a
// file it put there from one it did not.
type ownedTargets struct {
	// all is a release that is itself the installed one: it owns every
	// target it renders, and nothing it renders is new.
	all bool
	// installed is the version that owns declared, empty when no release
	// has converged here.
	installed string
	// unknown is an installed release that could not be loaded. It owns
	// nothing, so every differing file is kept rather than none.
	unknown  bool
	declared map[string]bool
}

func (o ownedTargets) owns(target string) bool {
	return o.all || o.declared[filepath.Clean(target)]
}

// ownedTargetsFor reads what the installed release renders when it is asked,
// so a resume past the new release's record finds it owning its own files. A
// release recorded and never converged, as `init --release` leaves one, has
// rendered nothing: until an apply, update, rollback or restore succeeds, no
// release is installed for this purpose (RFC 0048 decision 1). Rollback moves
// the pointer before its render and does not resume, so it passes the release
// it leaves as leaving.
func (d *Deps) ownedTargetsFor(
	ctx context.Context, rel domain.Release, leaving domain.ReleaseRecord,
) (ownedTargets, error) {
	owned := ownedTargets{declared: map[string]bool{}}
	current := leaving
	if current.IsZero() {
		var err error
		if current, err = d.State.CurrentRelease(ctx); err != nil || current.IsZero() {
			return owned, err
		}
	}
	converged, err := d.everConverged(ctx)
	if err != nil || !converged {
		return owned, err
	}
	if current.Root == rel.Root {
		owned.all = true
		return owned, nil
	}
	owned.installed = current.Version.String()
	installed, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		owned.unknown = true
		return owned, nil
	}
	for _, cfg := range installed.Manifest.Configuration {
		owned.declared[filepath.Clean(d.configTarget(cfg.Target))] = true
	}
	return owned, nil
}

// everConverged is whether any operation that renders the configuration has
// succeeded here. Unlike neverApplied, an operation still running does not
// count: the first apply is the one asking.
func (d *Deps) everConverged(ctx context.Context) (bool, error) {
	recs, err := d.State.Operations(ctx, ports.Filter{Status: domain.StatusSucceeded})
	if err != nil {
		return false, err
	}
	for _, rec := range recs {
		switch rec.Type {
		case domain.OpTypeApply, domain.OpTypeUpdate, domain.OpTypeRollback, domain.OpTypeRestore:
			return true, nil
		}
	}
	return false, nil
}

// existingTargetsFor is the one list the plan and the tick use (RFC 0048
// decision 2), with the profile the render uses.
func (d *Deps) existingTargetsFor(
	ctx context.Context, inst domain.Installation, rel domain.Release, profile string,
) (existingTargets, error) {
	var out existingTargets
	if len(rel.Manifest.Configuration) == 0 {
		return out, nil
	}
	owned, err := d.ownedTargetsFor(ctx, rel, domain.ReleaseRecord{})
	if err != nil || owned.all {
		return out, err
	}
	out.owner = owned
	comparison, err := configComparison(ctx, d, inst, rel, profile)
	if err != nil {
		return out, err
	}
	for _, diff := range comparison.Diffs {
		if _, err := os.Lstat(diff.Target); err == nil && !owned.owns(diff.Target) {
			out.replaced = append(out.replaced, diff.Target)
		}
	}
	for _, u := range comparison.Unreadable {
		if !owned.owns(u.Target) {
			out.unreadable = append(out.unreadable, u.Target)
		}
	}
	return out, nil
}

// heldForExisting is why an unattended tick leaves a candidate staged rather
// than replace a file no installed release rendered (RFC 0048 decision 4), or
// empty. The copy an attended update keeps saves the bytes and not the
// behaviour: the product would read the vendor's file from then on, with
// nobody watching.
//
// A candidate whose files cannot be rendered is not held here: its update
// fails at the render, before anything is replaced, and says why.
func (d *Deps) heldForExisting(ctx context.Context, inst domain.Installation, root string) string {
	rel, err := release.Load(root)
	if err != nil {
		return ""
	}
	if retired := domain.Parameters(inst.Parameters).ValidateAgainst(rel.Manifest.Parameters); len(retired) > 0 {
		inst = withoutParameters(inst, retired)
	}
	existing, err := d.existingTargetsFor(ctx, inst, rel, "")
	if err != nil || !existing.any() {
		return ""
	}
	return strings.Join(existing.lines(rel.Version().String(), true), "; ") +
		", so the release is staged and not installed; `morzer update --dry-run` shows it"
}
