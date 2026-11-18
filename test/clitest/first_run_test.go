package clitest_test

import (
	"os"
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

// `init` on an empty root leaves the directories above the layout listable:
// on a Mac the user who runs Docker Desktop lists each one on the way to a
// bind (RFC 0055 decision 16), and init sets only the layout's own modes.
func TestInitLeavesTheDirectoriesAboveTheLayoutListable(t *testing.T) {
	r := clitest.NewInstalled(t)

	for _, dir := range []string{r.Path("var"), r.Path("var", "lib")} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Errorf("%s: mode %04o, want 0755", dir, got)
		}
	}
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
