package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// A mistyped flag is answered with the error and where to look. The command's
// whole usage followed it, thirty lines that buried the one that was wrong, and
// under --json it was prose on stderr beside the envelope.
func TestAFlagErrorPointsAtHelpRatherThanPrintingIt(t *testing.T) {
	r := clitest.New(t)

	r.Run("status", "--bogus").ExitCode(2).
		OutputContains("unknown flag: --bogus", "morzer status --help").
		NoOutputContains("Usage:", "Global Flags:")

	out := r.Run("status", "--bogus", "--json").ExitCode(2)
	if out.Stderr != "" {
		t.Errorf("under --json only the envelope is written, got stderr:\n%s", out.Stderr)
	}
}

// Cobra's "accepts 1 arg(s), received 0" counts arguments without naming them;
// the usage line already says what each one is.
func TestAWrongArgumentCountNamesWhatTheCommandTakes(t *testing.T) {
	r := clitest.New(t)

	r.Run("release", "verify").ExitCode(2).
		OutputContains("morzer release verify takes <path>", "morzer release verify --help").
		NoOutputContains("arg(s)")
	r.Run("exec", "db").ExitCode(2).
		OutputContains("<service> -- <command>").
		NoOutputContains("arg(s)")
	r.Run("status", "extra").ExitCode(2).
		OutputContains("morzer status takes no arguments")
}
