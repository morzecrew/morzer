package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
)

// The wizard's contract is mostly about when it does *not* run. A tool whose
// value is being reproducible cannot grow an interactive path that a script, a
// systemd unit or a CI job might fall into.

func TestWizardNeverRunsWhenSomethingElseCouldBeMeant(t *testing.T) {
	// Nothing supplied at all -- the case the wizard exists for -- so any
	// false here is the flag under test doing the work.
	empty := ops.InitOptions{}

	cases := []struct {
		name  string
		flags globalFlags
	}{
		{"--yes means do not ask", globalFlags{yes: true}},
		{"--json output has no room for a form", globalFlags{json: true}},
		{"--quiet asked for silence", globalFlags{quiet: true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{Flags: tc.flags}
			assert.False(t, wizardApplies(app, empty))
		})
	}

	// And without a terminal, which is how every non-interactive caller
	// reaches `init`. The test binary has no TTY, so this is the real check
	// rather than a simulated one.
	assert.False(t, wizardApplies(&App{}, empty),
		"a run with no terminal must never wait for input")
}

func TestWizardDoesNotAskWhatWasAlreadyAnswered(t *testing.T) {
	// A fully-specified command line means what it says, so it runs
	// untouched even at a terminal. Re-asking would make `init` unscriptable
	// for anyone who happened to run it by hand.
	complete := ops.InitOptions{Product: "demo", RecoveryRecipient: "age1abc"}
	assert.False(t, missingRequired(complete))

	waived := ops.InitOptions{Product: "demo", NoRecoveryKey: true}
	assert.False(t, missingRequired(waived),
		"declining a recovery key is an answer, not a gap")

	assert.True(t, missingRequired(ops.InitOptions{RecoveryRecipient: "age1abc"}),
		"no product name is a gap")
	assert.True(t, missingRequired(ops.InitOptions{Product: "demo"}),
		"the recovery decision has to be made one way or the other")
}

// TestInteractiveAsksTheStreamsTheFormActuallyUses.
//
// The check used to consult the process's own stdin and stdout: an embedder
// driving the CLI through buffers would have been handed a form nobody could
// answer, and a `morzer init > log` at a terminal still counted as interactive
// because the form draws on stderr, which nothing looked at.
func TestInteractiveAsksTheStreamsTheFormActuallyUses(t *testing.T) {
	buffers := &App{Stream: ui.Streams{
		Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, In: strings.NewReader(""),
	}}
	assert.False(t, buffers.interactive(),
		"an embedder running against buffers would be asked a question it cannot answer")

	// A real file that is not a terminal is not one either -- the case a
	// redirect produces.
	f, err := os.CreateTemp(t.TempDir(), "log")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	redirected := &App{Stream: ui.Streams{Out: f, Err: f, In: f}}
	assert.False(t, redirected.interactive())
}

// TestAFormFailureIsNotAlwaysACancellation. Everything used to become "setup
// was cancelled" and exit 130, which tells an operator their own keystroke
// broke a setup they never touched and discards the cause.
func TestAFormFailureIsNotAlwaysACancellation(t *testing.T) {
	assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(formError(huh.ErrUserAborted)),
		"ctrl-C is a cancellation")
	assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(formError(context.Canceled)))

	broken := formError(errors.New("could not open /dev/tty"))
	assert.Equal(t, domain.ExitInternal, domain.ExitCode(broken),
		"a form that could not run is not an operator changing their mind")
	assert.Contains(t, domain.AsError(broken).Hint, "--help",
		"the hint does not say what to do instead")
}

// TestEquivalentCommandReproducesTheRun is what keeps the wizard from becoming
// the only way anyone knows how to install this.
func TestEquivalentCommandReproducesTheRun(t *testing.T) {
	got := EquivalentCommand(ops.InitOptions{
		Product:           "demo",
		ReleasePath:       "./bundle",
		Profile:           "embedded",
		Domains:           []string{"demo.example", "www.demo.example"},
		RecoveryRecipient: "age1abc",
		SigningKeys:       []string{"RWQabc"},
		RequireSignature:  true,
		InstallUnits:      true,
		GenerateSecrets:   true,
	})

	for _, want := range []string{
		"morzer init",
		"--product demo",
		"--release ./bundle",
		"--profile embedded",
		"--domain demo.example",
		"--domain www.demo.example",
		"--recovery-recipient age1abc",
		"--signing-key RWQabc",
		"--require-signature",
	} {
		assert.Contains(t, got, want)
	}

	// Defaults are not restated. A command line carrying every default is
	// one nobody reads, and the two that matter are printed only when they
	// are *not* the default.
	assert.NotContains(t, got, "--install-units")
	assert.NotContains(t, got, "--generate-secrets")
}

func TestEquivalentCommandPrintsTheChoicesThatDifferFromDefaults(t *testing.T) {
	got := EquivalentCommand(ops.InitOptions{
		Product:       "demo",
		NoRecoveryKey: true,
		// Both false, which is not what `init` does unless asked.
		InstallUnits:    false,
		GenerateSecrets: false,
	})

	assert.Contains(t, got, "--no-recovery-recipient")
	assert.Contains(t, got, "--install-units=false")
	assert.Contains(t, got, "--generate-secrets=false")
}

// TestEquivalentCommandCarriesTheParametersAndTheRepairFlag.
//
// Both were dropped, and both change what the command does: a --set the line
// omits is a deployment that comes up on the release's defaults instead of the
// operator's values, and a missing --repair turns a repair into `init`'s
// refusal to touch an existing installation.
func TestEquivalentCommandCarriesTheParametersAndTheRepairFlag(t *testing.T) {
	got := EquivalentCommand(ops.InitOptions{
		Product:       "demo",
		NoRecoveryKey: true,
		Parameters:    map[string]string{"log_level": "debug", "http_port": "8443"},
		Repair:        true,
	})

	assert.Contains(t, got, "--set http_port=8443")
	assert.Contains(t, got, "--set log_level=debug")
	assert.Contains(t, got, "--repair")

	// Sorted: this goes into a provisioning script, and a command that
	// reorders itself between runs is a diff nobody can read.
	assert.Less(t, strings.Index(got, "http_port"), strings.Index(got, "log_level"))
}

func TestEquivalentCommandQuotesWhatAShellWouldMangle(t *testing.T) {
	got := EquivalentCommand(ops.InitOptions{
		Product:        "demo",
		BackupSchedule: "*-*-* 02:00:00",
	})

	// A systemd OnCalendar expression contains a glob and a space. Pasted
	// unquoted it becomes several arguments and a filename expansion.
	assert.Contains(t, got, `--backup-schedule '*-*-* 02:00:00'`)

	// And the common case stays readable enough to copy by eye.
	assert.Contains(t, got, "--product demo")
}

func TestSplitDomainsIgnoresSpacingAndEmpties(t *testing.T) {
	assert.Equal(t, []string{"a.example", "b.example"},
		splitDomains(" a.example , b.example ,, "))
	assert.Empty(t, splitDomains("   "))
}

func TestProfilesComeFromTheBundle(t *testing.T) {
	// Offering what the release declares, rather than asking an operator to
	// remember it, is most of what the profile question is worth.
	got := profilesFrom("../../testdata/bundle")
	assert.Equal(t, []string{"embedded", "external-db"}, got)

	assert.Empty(t, profilesFrom(""), "no bundle, no profiles to offer")
	assert.Empty(t, profilesFrom(t.TempDir()), "an unreadable bundle is not an error here")
}

func TestShellQuoteLeavesOrdinaryValuesAlone(t *testing.T) {
	require.Equal(t, "demo", shellQuote("demo"))
	require.Equal(t, "age1abcdef", shellQuote("age1abcdef"))
	require.Equal(t, "./bundle", shellQuote("./bundle"))

	quoted := shellQuote("it's here")
	assert.True(t, strings.HasPrefix(quoted, "'"))
	assert.Contains(t, quoted, `'\''`)
}
