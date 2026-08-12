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
