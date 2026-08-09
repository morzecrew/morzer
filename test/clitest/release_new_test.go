package clitest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// The scaffold is where three conventions stop being advice. Without it they
// are documentation, and every bundle written from now on acquires them by
// hand or not at all.

// TestReleaseNewVerifiesWithNoEdits is the assertion that couples the
// generator to the verifier, in both directions: a scaffold that drifts fails
// here, and so does a verifier that grows stricter than the scaffold.
//
// Under `--render-check`, which is the strongest gate `verify` has (RFC 0013
// decision 11). It matters for this bundle specifically: the scaffolded template
// calls `secretFile` against the scaffolded schema, so the two files have to
// agree about the secret's name -- and nothing but a render would notice if a
// later edit renamed one of them.
func TestReleaseNewVerifiesWithNoEdits(t *testing.T) {
	r := clitest.New(t)
	dir := filepath.Join(t.TempDir(), "my-product")

	r.Run("release", "new", dir, "--vendor", "example").ExitCode(0)

	r.Run("release", "verify", dir, "--render-check").ExitCode(0).
		OutputContains("bundle is valid")
}

// TestReleaseNewCarriesTheConventions, asserted against the file tree rather
// than by reading the generator -- so a generator rewritten without them fails.
func TestReleaseNewCarriesTheConventions(t *testing.T) {
	r := clitest.New(t)
	dir := filepath.Join(t.TempDir(), "my-product")
	r.Run("release", "new", dir).ExitCode(0)

	manifest, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	first := strings.SplitN(string(manifest), "\n", 2)[0]
	if !strings.HasPrefix(first, "# yaml-language-server: $schema=") {
		t.Errorf("the manifest's first line is not the schema modeline: %q", first)
	}

	// Templates named like templates, so an editor stops reporting Go
	// template syntax as broken YAML.
	if _, err := os.Stat(filepath.Join(dir, "templates", "app.yaml.tmpl")); err != nil {
		t.Errorf("the scaffold's template is not named .yaml.tmpl: %v", err)
	}

	// And the secret schema out of templates/, because nothing renders it.
	if _, err := os.Stat(filepath.Join(dir, "secrets.schema.yaml")); err != nil {
		t.Errorf("the secret schema is not at the bundle root: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "templates")); err == nil {
		for _, e := range entries {
			if strings.Contains(e.Name(), "secret") {
				t.Errorf("templates/ still holds %s, which is not a template", e.Name())
			}
		}
	}

	// The release notes are declared, not merely dropped beside the
	// manifest: a stub nothing points at is the condition the declaration
	// exists to remove.
	if !strings.Contains(string(manifest), "release_notes: RELEASE.md") {
		t.Error("the scaffold writes RELEASE.md without declaring it")
	}
	if _, err := os.Stat(filepath.Join(dir, "RELEASE.md")); err != nil {
		t.Errorf("the scaffold declares release notes it does not ship: %v", err)
	}
}

// TestTheScaffoldedVersionIsAPlaceholderTheBuildRefuses couples two decisions
// that were made in different RFCs.
//
// The scaffold writes 0.0.0 so nobody hand-maintains the field the tooling
// owns; `build` refuses 0.0.0 so a forgotten --version in CI cannot ship a
// bundle that is clean at every gate and collides with the next one. Each is
// only safe because of the other, and nothing else would notice if one moved.
func TestTheScaffoldedVersionIsAPlaceholderTheBuildRefuses(t *testing.T) {
	r := clitest.New(t)
	dir := filepath.Join(t.TempDir(), "my-product")
	r.Run("release", "new", dir).ExitCode(0)

	r.Run("release", "build", dir).Failed().OutputContains("0.0.0")

	r.Run("release", "build", dir, "--version", "0.1.0").ExitCode(0).OutputContains("0.1.0")
}

// TestAFailedScaffoldLeavesNothingBehind.
//
// A half-written scaffold is worse than none: the retry meets the files the
// failed attempt created and refuses over them, so the operator has to work out
// which of the files in front of them this command wrote before they can try
// again.
//
// The failure is provoked with an unwritable `templates/`, which sorts last --
// so five files are written before the sixth fails. An earlier version of this
// test used a directory where a file belongs and proved nothing: the existence
// check saw the directory and refused *before* any write, so the rollback it
// meant to exercise never ran.
func TestAFailedScaffoldLeavesNothingBehind(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test depends on")
	}

	r := clitest.New(t)
	dir := filepath.Join(t.TempDir(), "my-product")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The bundle root stays writable; only templates/ is not, so the
	// writes ahead of it in sort order succeed.
	if err := os.Mkdir(filepath.Join(dir, "templates"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "templates"), 0o755) })

	r.Run("release", "new", dir).Failed()

	// Every file the attempt got to before the failure, gone.
	for _, name := range []string{
		"manifest.yaml", "VERSION", "RELEASE.md", "secrets.schema.yaml", "compose/compose.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err == nil {
			t.Errorf("a failed scaffold left %s behind, so a retry refuses over it", name)
		}
	}
}

// TestReleaseNewRefusesToWriteOverAnything.
//
// Scaffolding into a directory that already holds a bundle would replace a
// vendor's manifest with a skeleton, and there is no undo for that on a
// machine with no version control.
func TestReleaseNewRefusesToWriteOverAnything(t *testing.T) {
	r := clitest.New(t)
	dir := filepath.Join(t.TempDir(), "my-product")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r.Run("release", "new", dir).Failed().OutputContains("already exists")

	// And left it alone.
	data, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "mine\n" {
		t.Errorf("a refused scaffold overwrote the existing manifest: %q", data)
	}
}
