package clitest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// `morzer installation describe` driven against a real installation (RFC 0027
// P1). What the domain tests pin is that the document carries what an operator
// chose; what these pin is that the command produces it from a live machine,
// puts it where it was asked to, and never puts a YAML document on stdout
// beside a JSON envelope.

func TestDescribeWritesTheInstallationAsADocument(t *testing.T) {
	r := clitest.NewInstalled(t)

	out := r.Run("installation", "describe").ExitCode(0)

	// The header is the first thing a reader meets, and it is what stops
	// somebody editing the file and expecting an effect.
	for _, want := range []string{
		"morzer installation describe",
		"editing this file changes nothing",
		"api_version: selfhost/v1alpha1",
		"kind: installation-document",
		"product: demo",
	} {
		if !strings.Contains(out.Stdout, want) {
			t.Errorf("the document does not carry %q:\n%s", want, out.Stdout)
		}
	}
}

func TestDescribeWritesTheFileItWasAskedFor(t *testing.T) {
	r := clitest.NewInstalled(t)
	path := filepath.Join(t.TempDir(), "nested", "morzer.yaml")

	out := r.Run("installation", "describe", "--output", path).ExitCode(0)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("nothing was written: %v", err)
	}
	if !strings.Contains(string(body), "kind: installation-document") {
		t.Errorf("the file is not the document:\n%s", body)
	}

	// stdout stays empty: a caller redirecting it gets the file's contents
	// only when they asked for stdout.
	if out.Stdout != "" {
		t.Errorf("stdout carries output for a run that named a file:\n%q", out.Stdout)
	}
	out.StderrContains(path)
}

// TestDescribeReplacingAFileNarrowsItsPermissions.
//
// `os.WriteFile`'s mode applies only when it creates the file. Writing over an
// existing world-readable `morzer.yaml` therefore left it world-readable while
// the code read as though it had set 0600 -- and the second write is the
// common case, since the whole point of the file is to be regenerated and
// diffed.
//
// It names an installation, its domains and its targets. None of that is a
// secret and none of it is anybody else's business either.
func TestDescribeReplacingAFileNarrowsItsPermissions(t *testing.T) {
	r := clitest.NewInstalled(t)
	path := filepath.Join(t.TempDir(), "morzer.yaml")

	if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r.Run("installation", "describe", "--output", path).ExitCode(0)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("the replaced document is mode %04o, want 0600", got)
	}
}

func TestDescribeUnderJSONEmitsOneObject(t *testing.T) {
	// The contract break wave 11 found in `completion install --print-path`:
	// a raw document on stdout *and* an envelope. `--json` promises one JSON
	// object, so the document is the envelope's data.
	r := clitest.NewInstalled(t)

	out := r.Run("--json", "installation", "describe").ExitCode(0)
	out.FieldEquals("ok", true)
	out.FieldEquals("data.kind", "installation-document")
	out.FieldEquals("data.product", "demo")

	if strings.Contains(out.Stdout, "api_version: ") {
		t.Errorf("a YAML document reached stdout under --json:\n%s", out.Stdout)
	}
}

func TestDescribeCarriesNoSecretValue(t *testing.T) {
	// The claim the whole document rests on, asserted against a machine
	// holding a real secret rather than against a struct.
	r := clitest.NewInstalled(t)

	const value = "correct-horse-battery-staple"
	r.RunWithInput(value, "secret", "set", "db_password").ExitCode(0)

	out := r.Run("installation", "describe").ExitCode(0)

	if strings.Contains(out.Stdout, value) {
		t.Fatalf("the secret's value is in the document:\n%s", out.Stdout)
	}
	if !strings.Contains(out.Stdout, "db_password") {
		t.Errorf("the secret's name is not in the document:\n%s", out.Stdout)
	}
}

func TestDescribeRefusesToWriteThroughASymlink(t *testing.T) {
	// The path is usually inside a repository, and a symlink there points
	// somewhere the operator did not name -- which for a file describing a
	// production installation is worth refusing over.
	r := clitest.NewInstalled(t)

	dir := t.TempDir()
	elsewhere := filepath.Join(dir, "somewhere-else.yaml")
	if err := os.WriteFile(elsewhere, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "morzer.yaml")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}

	r.Run("installation", "describe", "--output", link).Failed().
		StderrContains("symlink")

	body, err := os.ReadFile(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "original\n" {
		t.Error("it wrote through the symlink")
	}
}

func TestDescribeWorksBeforeAnyReleaseIsInstalled(t *testing.T) {
	// An installation between `init` and the first `apply` still has an
	// answer to "what is this machine", and a description that refused
	// until a release existed would be useless in the case where somebody
	// most wants to write down what they have.
	r := clitest.New(t)
	// --install-units=false because this runs as an unprivileged user
	// with no systemd to write into, as every other init test does.
	r.Run("init", "--product", "demo", "--no-recovery-recipient",
		"--install-units=false").ExitCode(0)

	out := r.Run("installation", "describe").ExitCode(0)
	out.OutputContains("product: demo")
}

// TestDescribeRefusesRatherThanRecordingWhatItCouldNotRead.
//
// The failure this closes is quiet and permanent. The document is written to be
// committed, so it outlives the run: a file recording no release because the
// pointer would not parse is a false record that somebody reviews next quarter
// and believes.
//
// The state layer already tells absence from corruption — an absent pointer is
// a normal fresh install, an unreadable one is an error — and the first version
// of this command collapsed both into an empty document.
func TestDescribeRefusesRatherThanRecordingWhatItCouldNotRead(t *testing.T) {
	r := clitest.NewInstalled(t)

	// The pointer exists and is not readable as a release record.
	pointer := filepath.Join(r.Root, "var", "lib", "demo", "manager", "current-release.json")
	if _, err := os.Stat(pointer); err != nil {
		t.Fatalf("the fixture has no release pointer, so this proves nothing: %v", err)
	}
	if err := os.WriteFile(pointer, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := r.Run("installation", "describe").Failed()

	if strings.Contains(out.Stdout, "kind: installation-document") {
		t.Errorf("it produced a document from state it could not read:\n%s", out.Stdout)
	}
}

// TestDescribeRefusesWhenTheSecretStoreWillNotAnswer.
//
// The other half of the same rule, and the half a mutation caught: making the
// secret listing swallow its error survived the whole suite, because the test
// that covers secrets checks what the document says rather than what happens
// when the store cannot be read.
//
// A committed file recording `secrets:` as absent, on an installation that has
// them, is the same false record as the release case — arguably worse, since
// the list is what somebody rebuilding the machine would work from.
func TestDescribeRefusesWhenTheSecretStoreWillNotAnswer(t *testing.T) {
	r := clitest.NewInstalled(t)

	state := filepath.Join(r.Root, "etc", "demo", "secrets.sops.yaml")
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("the fixture has no secret state, so this proves nothing: %v", err)
	}
	if err := os.WriteFile(state, []byte("not: [sops\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := r.Run("installation", "describe").Failed()
	if strings.Contains(out.Stdout, "kind: installation-document") {
		t.Errorf("it produced a document from a store it could not read:\n%s", out.Stdout)
	}
}
