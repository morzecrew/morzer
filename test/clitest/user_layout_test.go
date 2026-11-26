package clitest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// initUser runs `init` as a user other than root, with no flag naming a layout.
func initUser(t *testing.T) *clitest.Runner {
	t.Helper()
	r := clitest.NewUser(t)
	r.Run("init",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
	).ExitCode(0)
	return r
}

// A user other than root who runs `init` gets an installation in their own
// tree, with its render directory on their tmpfs and never in the tree (RFC
// 0023 rows 30 and 31). Every later command that names no layout manages it.
func TestInitWithoutRootLandsInTheUsersTree(t *testing.T) {
	r := initUser(t)
	tree := r.Path("data", "morzer")

	if _, err := os.Stat(filepath.Join(tree, "etc", "demo", "installation.yaml")); err != nil {
		t.Fatalf("the installation is not in the user's tree: %v", err)
	}
	if _, err := os.Stat(r.Path("run", "morzer", "demo")); err != nil {
		t.Errorf("the render directory is not under XDG_RUNTIME_DIR: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree, "run")); !os.IsNotExist(err) {
		t.Errorf("the tree has a run directory, where secrets would land on disk: %v", err)
	}

	r.Run("--json", "ls").ExitCode(0).
		FieldEquals("data.0.product", "demo").
		FieldEquals("data.0.layout", "user").
		FieldEquals("data.0.path", filepath.Join(tree, "etc", "demo"))
	r.Run("ls").ExitCode(0).OutputContains("your own installations, in the user layout")

	// What the user's own units pass: the tree is the user layout, so its
	// render directory stays the user's tmpfs.
	r.Run("--json", "--config", filepath.Join(tree, "etc", "demo", "installation.yaml"), "ls").
		ExitCode(0).FieldEquals("data.0.layout", "user")
}

// The user's tree is the user layout by any path that resolves to it, and with
// no HOME while $XDG_DATA_HOME names it. Read as a tree of its own, a --config
// into it would render its secrets inside it, on disk. With no home for its
// units either, the command is refused rather than writing them relative to
// wherever it ran.
func TestTheUsersTreeIsRecognisedByAnyPathToIt(t *testing.T) {
	r := clitest.NewUser(t)
	if err := os.MkdirAll(r.Path("real"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(r.Path("real"), r.Path("data")); err != nil {
		t.Fatal(err)
	}
	r.Run("init", "--release", r.Bundle, "--profile", "embedded",
		"--domain", "demo.example", "--no-recovery-recipient", "--install-units=false").ExitCode(0)

	config := r.Path("real", "morzer", "etc", "demo", "installation.yaml")
	r.Run("--json", "--config", config, "ls").ExitCode(0).FieldEquals("data.0.layout", "user")

	t.Setenv("HOME", "")
	r.Run("--json", "ls").ExitCode(0).FieldEquals("data.0.layout", "user")
	t.Setenv("XDG_CONFIG_HOME", "")
	r.Run("ls").ExitCode(3).StderrContains("HOME is not set")
}

// Without $XDG_RUNTIME_DIR a user installation has no tmpfs to render its
// secrets into, and the command is refused rather than given a directory on
// disk (logs/quadlet-adapter.md, U1).
func TestAUserLayoutWithoutARuntimeDirectoryIsRefused(t *testing.T) {
	r := clitest.NewUser(t)
	t.Setenv("XDG_RUNTIME_DIR", "")

	r.Run("init", "--release", r.Bundle, "--profile", "embedded",
		"--domain", "demo.example", "--no-recovery-recipient", "--install-units=false").
		ExitCode(3).StderrContains("XDG_RUNTIME_DIR is not set")
	if _, err := os.Stat(r.Path("data", "morzer")); !os.IsNotExist(err) {
		t.Errorf("a refused init created the tree: %v", err)
	}
}

// `init` of a user installation is refused on the system's daemon, before it
// creates anything: the installation would share a Compose project with the
// system's (RFC 0023 row 32).
func TestInitOfAUserInstallationRefusesTheSystemsDaemon(t *testing.T) {
	r := clitest.NewUser(t)
	t.Setenv("MORZER_TEST_DOCKER_INFO", `["name=seccomp,profile=builtin","name=cgroupns"]`)

	r.Run("init", "--release", r.Bundle, "--profile", "embedded",
		"--domain", "demo.example", "--no-recovery-recipient", "--install-units=false").
		ExitCode(3).StderrContains("the Docker daemon is the system's", "docker context use rootless")
	if _, err := os.Stat(r.Path("data", "morzer", "etc", "demo", "installation.yaml")); !os.IsNotExist(err) {
		t.Errorf("a refused init wrote the installation: %v", err)
	}
}

// A user whose tree holds no installation still manages the system layout, as
// before (RFC 0023 row 31): an empty tree selects nothing.
func TestAUserWithNoInstallationKeepsTheSystemLayout(t *testing.T) {
	r := clitest.NewUser(t)
	if err := os.MkdirAll(r.Path("data", "morzer", "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	rows, _ := r.Run("--json", "ls").ExitCode(0).Field("data").([]any)
	for _, row := range rows {
		if fields, _ := row.(map[string]any); fields["layout"] == "user" {
			t.Errorf("an empty tree was listed as the user layout: %v", row)
		}
	}
}

// --product completes the user's own installations, as `morzer ls` with no flag
// lists them.
func TestProductCompletesTheUsersOwnInstallations(t *testing.T) {
	r := initUser(t)
	r.Run("__complete", "--product", "").ExitCode(0).OutputContains("demo\t", ":4")
}

// A directory in the user's tree that cannot be read is listed with what to do
// about it, and that is not to re-run as root, which lists the system's
// installations instead.
func TestAnUnreadableUserInstallationIsNotSentToRoot(t *testing.T) {
	r := initUser(t)
	sealed := r.Path("data", "morzer", "etc", "sandbox")
	if err := os.MkdirAll(sealed, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o750) })

	rows, _ := r.Run("--json", "ls").ExitCode(0).Field("data").([]any)
	for _, row := range rows {
		if fields, _ := row.(map[string]any); fields["product"] == "sandbox" {
			problem, _ := fields["problem"].(string)
			if !strings.Contains(problem, "check that you can read its directory") || strings.Contains(problem, "root") {
				t.Errorf("an unreadable user installation is told: %q", problem)
			}
			return
		}
	}
	t.Fatalf("the unreadable directory was not listed: %v", rows)
}

// A user tree whose only installation cannot be read is still the user's: the
// command lists that row rather than managing the system's installations.
func TestAUserTreeHoldingOnlyAnUnreadableInstallationIsTheUsers(t *testing.T) {
	r := clitest.NewUser(t)
	sealed := r.Path("data", "morzer", "etc", "sandbox")
	if err := os.MkdirAll(sealed, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o750) })

	r.Run("--json", "ls").ExitCode(0).
		FieldEquals("data.0.product", "sandbox").
		FieldEquals("data.0.layout", "user")
}

