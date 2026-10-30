package clitest

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeShell writes a $SHELL that reports the variable it was started with and
// exits with the given status, which is all `morzer shell` promises about the
// shell it starts.
func fakeShell(t *testing.T, status string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sh")
	script := "#!/bin/sh\nprintf 'pinned:%s\\n' \"$MORZER_PRODUCT\"\nexit " + status + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", path)
	t.Setenv("MORZER_PRODUCT", "")
}

func TestShellStartsAShellPinnedToTheInstallation(t *testing.T) {
	r := NewInstalled(t)
	fakeShell(t, "0")

	r.Run("shell", "demo").ExitCode(0).
		OutputContains("pinned:demo", "demo: a shell pinned to this installation")
}

// The shell's status is the invocation's, as `exec`'s command's is: the last
// thing typed before `exit` decides `$?`, not morzer.
func TestShellExitsWithTheShellsStatus(t *testing.T) {
	r := NewInstalled(t)
	fakeShell(t, "3")

	r.Run("shell", "demo").ExitCode(3).NoOutputContains("error:")
}

// A typo must not become a shell in which every command is refused.
func TestShellRefusesAnInstallationThisMachineDoesNotHave(t *testing.T) {
	r := NewInstalled(t)
	fakeShell(t, "0")

	r.Run("shell", "dmeo").ExitCode(2).
		OutputContains(`no installation named "dmeo"`, "demo").
		NoOutputContains("pinned:")
}

// One answer to "which installation is this shell on": a pin inside a pin is
// refused rather than nested.
func TestShellRefusesToNestInsideAPinnedShell(t *testing.T) {
	r := NewInstalled(t)
	fakeShell(t, "0")
	t.Setenv("MORZER_PRODUCT", "demo")

	r.Run("shell", "demo").ExitCode(2).
		OutputContains("already pinned to demo", "exit it first").
		NoOutputContains("pinned:")
}

// The shell owns stdout while it runs, so there is no single JSON object to
// promise; refused before anything starts rather than corrupted.
func TestShellRefusesJSON(t *testing.T) {
	r := NewInstalled(t)
	fakeShell(t, "0")

	r.Run("--json", "shell", "demo").ExitCode(2).
		OutputContains("--json").
		NoOutputContains("pinned:")
}

// A shell killed by a signal exits as shells report one: 128 plus the signal.
func TestShellKilledBySignalExitsAsAShellWould(t *testing.T) {
	r := NewInstalled(t)
	path := filepath.Join(t.TempDir(), "sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nkill -TERM $$\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", path)
	t.Setenv("MORZER_PRODUCT", "")

	r.Run("shell", "demo").ExitCode(128 + 15)
}

// A directory `ls` could not open is not an installation it counted, so it is
// no name to pin to -- but when it is the one asked for, the way in is sudo.
func TestShellRefusesADirectoryItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens every directory")
	}
	r := NewInstalled(t)
	fakeShell(t, "0")
	locked := filepath.Join(r.Root, "etc", "credstore")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	r.Run("shell", "credstore").ExitCode(2).
		OutputContains("cannot be read", "sudo morzer shell credstore").
		NoOutputContains("pinned:")
}
