package clitest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// Shell completion as the shell asks for it: cobra's hidden `__complete`
// command, one candidate per line, then `:<directive>`. 4 is
// ShellCompDirectiveNoFileComp -- a machine with nothing to offer must not fall
// back to the file names in the current directory (#210).

func TestProductCompletesTheInstallationsOnTheMachine(t *testing.T) {
	r := withTwoInstallations(t)

	r.Run("__complete", "--product", "").ExitCode(0).
		OutputContains("demo\t", "sandbox\t", ":4")
}

func TestProductCompletesNothingOnABareMachine(t *testing.T) {
	r := clitest.New(t)

	out := r.Run("__complete", "--product", "").ExitCode(0)
	if got := strings.TrimSpace(out.Stdout); !strings.HasPrefix(got, ":4") {
		t.Errorf("a bare machine offered candidates or allowed file names:\n%s", got)
	}
}

// Under sudo the completion runs as the operator, who cannot open an
// installation's 0750 directory. Only a unit the manager wrote for this root
// names it then, and the host has none for a test's root: the matching itself
// is TestTheFallbackRecognisesTheUnitsTheManagerWrites.
func TestProductOffersNoDirectoryItCannotOpenWithoutAUnit(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens every directory, so nothing is unreadable to fall back from")
	}
	r := withTwoInstallations(t)

	sandbox := filepath.Join(r.Root, "etc", "sandbox")
	if err := os.Chmod(sandbox, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sandbox, 0o750) })

	r.Run("__complete", "--product", "").ExitCode(0).
		OutputContains("demo\t", ":4").
		NoOutputContains("sandbox")
}
