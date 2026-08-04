package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/internal/ui"
)

// The wizard is a front-end over the flags, never a second path.
//
// It fills the same InitOptions struct the flags do and then prints the command
// line that would have produced it, so an operator who ran it once can script
// it thereafter. Where the two could ever disagree, the flags are correct --
// that is what makes this safe to add to a tool whose whole value is being
// reproducible.
//
// It runs only when nothing else could reasonably be meant: an interactive
// terminal, no `--yes`, and a required value actually missing. A CI run, a
// systemd unit, or a scripted install never reaches it.

// wizardApplies reports whether the interactive path should run at all.
func wizardApplies(app *App, opts ops.InitOptions) bool {
	switch {
	case app.Flags.yes:
		// The flag means "do not ask me anything". Asking anyway would
		// make it a lie in the one place it matters.
		return false
	case app.Flags.json, app.Flags.quiet:
		return false
	case !isInteractive():
		return false
	default:
		return missingRequired(opts)
	}
}

// missingRequired reports whether anything the wizard collects is still unset.
//
// A fully-specified command line is a command line that means what it says, so
// it runs untouched even at a terminal.
func missingRequired(opts ops.InitOptions) bool {
	if opts.Product == "" {
		return true
	}
	// The recovery decision has to be made one way or the other; `init`
	// refuses without it, and being refused is a worse first run than being
	// asked.
	return opts.RecoveryRecipient == "" && !opts.NoRecoveryKey
}

func isInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// form builds a huh form bound to this run's own streams.
//
// Three things it fixes, all of which were `os.Stdin`/`os.Stdout` before:
// the prompts now go to stderr like every other piece of narration this
// program emits, the input is the one the App was given, and a stream that
// cannot be put in raw mode gets huh's accessible renderer instead of a
// terminal UI that would have nothing to draw on.
//
// That last is not a test affordance. Accessible mode is what huh ships for
// screen readers, and "the input is not a terminal" is the same condition from
// the form's point of view: prompt on a line, read a line.
func (a *App) form(fields ...huh.Field) *huh.Form {
	return huh.NewForm(huh.NewGroup(fields...)).
		WithTheme(huh.ThemeBase()).
		WithInput(a.Stream.In).
		WithOutput(a.Stream.Err).
		WithAccessible(a.accessibleForms())
}

// accessibleForms reports whether forms should render line by line.
func (a *App) accessibleForms() bool {
	// An operator who exported ACCESSIBLE means it, whatever the streams
	// look like.
	if _, set := os.LookupEnv("ACCESSIBLE"); set {
		return true
	}
	return !ui.IsTerminal(a.Stream.In)
}

// runInitWizard collects what is missing and returns the completed options.
//
// Everything already supplied on the command line is left alone: the wizard
// fills gaps, it does not re-ask questions the operator has answered.
func runInitWizard(ctx context.Context, app *App, opts ops.InitOptions) (ops.InitOptions, error) {
	profiles := profilesFrom(opts.ReleasePath)

	var (
		domainsInput = strings.Join(opts.Domains, ", ")
		recovery     = recoveryChoiceExisting
	)
	if opts.NoRecoveryKey {
		recovery = recoveryChoiceNone
	} else if opts.RecoveryRecipient == "" {
		recovery = recoveryChoiceGenerate
	}

	fields := []huh.Field{}

	if opts.Product == "" {
		fields = append(fields, huh.NewInput().
			Title("Product name").
			Description("Drives /etc/<name>, /var/lib/<name>, /run/<name> and the hook environment.").
			Value(&opts.Product).
			Validate(func(s string) error { return domain.ValidateProductName(s) }))
	}

	if opts.Profile == "" && len(profiles) > 0 {
		fields = append(fields, huh.NewSelect[string]().
			Title("Deployment profile").
			Description("The topology this release declares.").
			Options(huh.NewOptions(profiles...)...).
			Value(&opts.Profile))
	}

	if len(opts.Domains) == 0 {
		fields = append(fields, huh.NewInput().
			Title("Public domains").
			Description("Comma-separated. The first is canonical. Leave empty for none.").
			Value(&domainsInput))
	}

	if opts.RecoveryRecipient == "" && !opts.NoRecoveryKey {
		// The consequence is in the title, not only the description.
		// huh's accessible renderer -- the one a screen reader gets --
		// prints titles and drops descriptions, and this is the one
		// question where not hearing the consequence is expensive.
		fields = append(fields, huh.NewSelect[string]().
			Title("Offline recovery key (without one, losing this machine loses its secrets)").
			Description("Without one, losing this machine loses its secrets permanently.").
			Options(
				huh.NewOption("Generate one now (recommended)", recoveryChoiceGenerate),
				huh.NewOption("I have a public key to paste", recoveryChoiceExisting),
				huh.NewOption("Proceed without one", recoveryChoiceNone),
			).
			Value(&recovery))
	}

	if len(fields) == 0 {
		return opts, nil
	}

	if err := app.form(fields...).RunWithContext(ctx); err != nil {
		return opts, domain.Interrupted("setup was cancelled")
	}

	opts.Domains = splitDomains(domainsInput)

	filled, err := resolveRecoveryChoice(ctx, app, opts, recovery)
	if err != nil {
		return opts, err
	}
	return filled, nil
}

const (
	recoveryChoiceGenerate = "generate"
	recoveryChoiceExisting = "existing"
	recoveryChoiceNone     = "none"
)

// resolveRecoveryChoice acts on the answer, including generating a key.
func resolveRecoveryChoice(
	ctx context.Context,
	app *App,
	opts ops.InitOptions,
	choice string,
) (ops.InitOptions, error) {
	switch choice {
	case recoveryChoiceNone:
		opts.NoRecoveryKey = true
		return opts, nil

	case recoveryChoiceExisting:
		var key string
		field := huh.NewInput().
			Title("Recovery public key").
			Description("The age public key, starting age1.").
			Value(&key).
			Validate(app.Deps.Secrets.ValidateRecipient)

		if err := app.form(field).RunWithContext(ctx); err != nil {
			return opts, domain.Interrupted("setup was cancelled")
		}
		opts.RecoveryRecipient = strings.TrimSpace(key)
		return opts, nil

	default:
		return generateRecoveryKey(ctx, app, opts)
	}
}

// generateRecoveryKey writes an offline identity and tells the operator the one
// thing that makes it worth having.
func generateRecoveryKey(ctx context.Context, app *App, opts ops.InitOptions) (ops.InitOptions, error) {
	path := defaultRecoveryKeyPath(opts.Product)

	field := huh.NewInput().
		Title("Where to write the recovery key").
		Description("The private half. Move it off this machine afterwards.").
		Value(&path)

	if err := app.form(field).RunWithContext(ctx); err != nil {
		return opts, domain.Interrupted("setup was cancelled")
	}

	public, err := sopsage.GenerateIdentity(path)
	if err != nil {
		return opts, err
	}
	opts.RecoveryRecipient = public

	out := app.Stream.Err
	fmt.Fprintf(out, "\nrecovery key written to %s (0400)\n", path)
	fmt.Fprintf(out, "public key: %s\n", public)
	fmt.Fprintf(out, "\n  MOVE THE PRIVATE HALF OFF THIS MACHINE.\n")
	fmt.Fprintf(out, "  A recovery key stored on the machine it is meant to recover protects nothing.\n")
	return opts, nil
}

func defaultRecoveryKeyPath(product string) string {
	if home, err := os.UserHomeDir(); err == nil {
		return home + "/" + product + "-recovery.key"
	}
	return "./" + product + "-recovery.key"
}

// profilesFrom reads the deployment profiles a bundle declares, so the wizard
// offers what exists rather than asking the operator to remember it.
func profilesFrom(releasePath string) []string {
	if releasePath == "" {
		return nil
	}
	manifest, err := release.LoadManifest(releasePath + "/" + release.ManifestFileName)
	if err != nil {
		return nil
	}

	out := make([]string, 0, len(manifest.Runtime.Profiles))
	for name := range manifest.Runtime.Profiles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func splitDomains(input string) []string {
	var out []string
	for _, part := range strings.Split(input, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// EquivalentCommand renders the command line that would produce these options.
//
// Printed after every wizard run, and the reason the wizard does not erode the
// scriptability the rest of the tool depends on: an operator who ran it once
// can put the result in a provisioning script and never run it again.
func EquivalentCommand(opts ops.InitOptions) string {
	args := []string{"morzer init"}

	add := func(flag, value string) {
		if value != "" {
			args = append(args, fmt.Sprintf("--%s %s", flag, shellQuote(value)))
		}
	}

	add("product", opts.Product)
	add("release", opts.ReleasePath)
	add("profile", opts.Profile)
	for _, d := range opts.Domains {
		add("domain", d)
	}
	add("recovery-recipient", opts.RecoveryRecipient)
	if opts.NoRecoveryKey {
		args = append(args, "--no-recovery-recipient")
	}
	add("backup-schedule", opts.BackupSchedule)
	for _, k := range opts.SigningKeys {
		add("signing-key", k)
	}
	if opts.RequireSignature {
		args = append(args, "--require-signature")
	}
	if !opts.InstallUnits {
		args = append(args, "--install-units=false")
	}
	if !opts.GenerateSecrets {
		args = append(args, "--generate-secrets=false")
	}

	return strings.Join(args, " \\\n    ")
}

// shellQuote quotes a value only when it needs it, so the common case stays
// readable enough to copy.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\$`*?[]{}();&|<>#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
