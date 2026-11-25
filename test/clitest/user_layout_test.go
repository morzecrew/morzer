package clitest_test

import (
	"os"
	"path/filepath"
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
