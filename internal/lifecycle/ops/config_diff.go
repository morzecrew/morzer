package ops

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
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

	out := ConfigDiffReport{
		Release:    rel.Version().String(),
		Targets:    comparison.Targets,
		Redacted:   shown,
		Drift:      comparison.Diffs,
		Unreadable: comparison.Unreadable,
	}
	// Lists rather than nulls, for a script that ranges over them.
	if out.Drift == nil {
		out.Drift = []TargetDiff{}
	}
	if out.Unreadable == nil {
		out.Unreadable = []UnreadableTarget{}
	}
	return out, nil
}

// Text is the diffs and the unreadable targets, for stdout.
func (r ConfigDiffReport) Text() string {
	return ConfigComparison{Diffs: r.Drift, Unreadable: r.Unreadable}.text(r.Redacted)
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
