package clitest

import (
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
