package clitest

import (
	"os"
	"strings"
	"testing"
)

// The global flag surface: parsing failures and their streams, not any one
// command's behaviour.

func TestVersionFlagPrintsTheVersion(t *testing.T) {
	r := New(t)

	r.Run("--version").
		ExitCode(0).
		StdoutContains("morzer test")
}

func TestAFlagTypoPrintsUsageOnStderrOnly(t *testing.T) {
	r := New(t)

	res := r.Run("--definitely-not-a-flag").ExitCode(2).
		StderrContains("Usage:")
	// stdout belongs to results. A piped consumer must never receive
	// help text because of a typo.
	if strings.Contains(res.Stdout, "Usage:") {
		t.Errorf("usage text leaked onto stdout:\n%s", res.Stdout)
	}
}

func TestLogFormatRefusesAValueNobodyDefined(t *testing.T) {
	r := New(t)

	r.Run("--log-format", "banana", "version").
		ExitCode(2).
		StderrContains("invalid --log-format", "text, json")

	// Explicitly empty is a mistake too, not a silent alias for text.
	r.Run("--log-format=", "version").ExitCode(2)
}

func TestVerboseAndQuietRefuseToCombine(t *testing.T) {
	r := New(t)

	// Before this was stated, `-v -q` silently meant quiet.
	r.Run("-v", "-q", "version").ExitCode(2)
}

// TestAJSONRunAlwaysProducesAnEnvelope.
//
// The presenter is built in the persistent pre-run, which never runs when the
// failure is the parse itself. So exactly the mistakes a script makes -- a
// mistyped flag, a mistyped command, an invalid --log-format -- produced no
// output on stdout at all, and a consumer reading `morzer --json ... | jq`
// received empty input rather than an error it could act on.
func TestAJSONRunAlwaysProducesAnEnvelope(t *testing.T) {
	r := New(t)

	for _, args := range [][]string{
		{"--json", "--definitely-not-a-flag"},
		{"--json", "definitely-not-a-command"},
		{"--json", "--log-format", "banana", "version"},
	} {
		res := r.Run(args...).ExitCode(2)
		res.FieldEquals("ok", false)
		res.FieldEquals("exit_code", float64(2))
		if msg, _ := res.Field("error.message").(string); msg == "" {
			t.Errorf("%v: the envelope carries no error message:\n%s", args, res.Stdout)
		}
	}
}

// TestTheRootProductFlagReachesInit. `morzer --product demo init` is a
// spelling the flag's own placement invites, and init's local --product
// shadowed it with an empty string: the name was dropped, and a non-interactive
// run failed asking for the flag that had just been given.
func TestTheRootProductFlagReachesInit(t *testing.T) {
	r := New(t)

	r.Run("--product", "custom", "init",
		"--no-recovery-recipient", "--install-units=false").ExitCode(0)

	if _, err := os.Stat(r.Path("etc", "custom", "installation.yaml")); err != nil {
		t.Errorf("no installation was created under the name that was given: %v", err)
	}
}

// TestConfigSelectsTheInstallationItNames is the case the flag exists for: a
// host with more than one installation, where discovery refuses to guess.
func TestConfigSelectsTheInstallationItNames(t *testing.T) {
	r := NewInstalled(t)

	r.Run("--product", "second", "init",
		"--no-recovery-recipient", "--install-units=false").ExitCode(0)

	byProduct := r.Run("--product", "demo", "status", "--json").ExitCode(0).
		Field("data.installation_id")
	byConfig := r.Run("--config", r.Path("etc", "demo", "installation.yaml"), "status", "--json").
		ExitCode(0).Field("data.installation_id")

	if byConfig != byProduct {
		t.Errorf("--config reported installation %v, want %v -- it named one "+
			"deployment and the command acted on another", byConfig, byProduct)
	}

	second := r.Run("--product", "second", "status", "--json").ExitCode(0).
		Field("data.installation_id")
	if second == byProduct {
		t.Fatal("the fixture built one installation, so this proves nothing")
	}
}

func TestBareClearInterventionSelectsRatherThanFailing(t *testing.T) {
	r := NewInstalled(t)

	// The bare form arrives as NoOptDefVal (a single space) and used to
	// fall into the "named operation not found" branch with a blank ID in
	// the message. With nothing flagged, the documented empty form is a
	// clean no-op.
	r.Run("status", "--clear-intervention").
		ExitCode(0).
		StderrContains("no operations require attention")
}
