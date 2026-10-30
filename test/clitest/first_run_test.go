package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// `init` starts nothing, and an operator new to the tool was left at a prompt
// with no word of what comes next. A plan and a repair create nothing to start,
// so they say nothing about it. The hint is narration, so it is on stderr.
func TestInitSaysWhatToRunNext(t *testing.T) {
	r := clitest.New(t)
	args := []string{"init", "--release", r.Bundle, "--profile", "embedded",
		"--domain", "demo.example", "--no-recovery-recipient", "--install-units=false"}

	r.Run(append(args, "--dry-run")...).ExitCode(0).NoOutputContains("morzer apply")
	r.Run(args...).ExitCode(0).StderrContains("morzer --product demo apply --dry-run", "morzer --product demo apply")
	r.Run("init", "--repair", "--no-recovery-recipient", "--install-units=false").ExitCode(0).NoOutputContains("morzer apply")

	r.Run("status").ExitCode(0).StdoutContains("next", "morzer --product demo apply")
	r.Run("status", "--json").ExitCode(0).StdoutContains(`"next"`)
}

// Without --release nothing is installed, and `apply` refuses with "no release
// is installed": the next step is the command that installs one.
func TestInitWithoutAReleaseSaysToInstallOne(t *testing.T) {
	r := clitest.New(t)

	r.Run("init", "--product", "demo", "--domain", "demo.example",
		"--no-recovery-recipient", "--install-units=false").ExitCode(0).
		StderrContains("morzer --product demo update <bundle>").
		NoOutputContains("morzer apply")
	r.Run("status").ExitCode(0).StdoutContains("next", "morzer --product demo update <bundle>")
}
