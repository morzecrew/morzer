package clitest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/test/clitest"
)

// `morzer support bundle` against a real installation, encrypting for real
// (RFC 0024 P4).
//
// The Go suite drives the operation and proves the refusals; this drives the
// binary an operator runs, through `init` and a manifest a vendor would ship,
// and asserts on the characters that reach a terminal. It is also where the
// sample in the reference page comes from: a sample nobody captured from a
// running binary is a sample that drifts the first time a column moves.

// declaringSupportRecipients stages the example bundle with a vendor's
// recipient declared in its manifest, and returns the public key.
func declaringSupportRecipients(t *testing.T, r *clitest.Runner) string {
	t.Helper()

	identity := filepath.Join(t.TempDir(), "vendor-identity")
	public, err := sopsage.GenerateIdentity(identity)
	if err != nil {
		t.Fatalf("cannot mint a vendor key: %v", err)
	}

	manifest := filepath.Join(r.Bundle, "manifest.yaml")
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// Appended to the mapping the fixture already has, which is where a
	// vendor's own namespaced keys live. A second `extensions:` key would be
	// a duplicate and the manifest would not decode at all -- which is a
	// failure this fixture would report as the feature not working.
	if !strings.Contains(string(raw), "\nextensions:\n") {
		t.Fatalf("the fixture manifest has no extensions block to extend:\n%s", raw)
	}
	block := "  morzer.dev/support:\n    recipients:\n      - " + public + "\n"
	if err := os.WriteFile(manifest, append(raw, []byte(block)...), 0o644); err != nil {
		t.Fatal(err)
	}
	return public
}

// The archive an operator is told about is the encrypted one.
func TestSupportBundleEncryptsToTheDeclaredRecipient(t *testing.T) {
	r := clitest.New(t)
	public := declaringSupportRecipients(t, r)
	r.Run("init", "--release", r.Bundle, "--profile", "embedded",
		"--domain", "demo.example", "--no-recovery-recipient",
		"--install-units=false").ExitCode(0)

	dir := t.TempDir()
	out := r.Run("support", "bundle", "--dir", dir).ExitCode(0)

	// The recipients are on stdout in full, and the plaintext warning is
	// not: an operator who sees both learns nothing from either.
	out.OutputContains("encrypted to, and readable by, only these recipients", public)
	out.NoOutputContains("this archive is not encrypted")

	written, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 {
		t.Fatalf("expected one archive, found %d: %v", len(written), written)
	}
	if !strings.HasSuffix(written[0].Name(), ".tar.zst.age") {
		t.Errorf("the encrypted archive is not named as one: %s", written[0].Name())
	}

	// Reported the same way through the machine-readable contract, which is
	// what a vendor's intake automation reads.
	j := r.Run("--json", "support", "bundle", "--dir", t.TempDir()).ExitCode(0)
	j.FieldEquals("data.encrypted", true)
	if got := j.Field("data.recipients"); len(got.([]any)) != 1 || got.([]any)[0] != public {
		t.Errorf("data.recipients is %v, want [%s]", got, public)
	}
}

// The preview names them before anything is written, which is the only moment
// checking them is worth anything.
func TestSupportPreviewNamesTheRecipients(t *testing.T) {
	r := clitest.New(t)
	public := declaringSupportRecipients(t, r)
	r.Run("init", "--release", r.Bundle, "--profile", "embedded",
		"--domain", "demo.example", "--no-recovery-recipient",
		"--install-units=false").ExitCode(0)

	dir := t.TempDir()
	r.Run("support", "bundle", "--preview", "--dir", dir).
		ExitCode(0).
		OutputContains("nothing written", public)

	written, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Errorf("a preview wrote %d file(s)", len(written))
	}
}

// A declaration that cannot be used stops the command, and no archive appears.
//
// Driven through the binary because this is the refusal an operator meets, and
// its whole value is that it happens instead of a plaintext archive rather than
// alongside one.
func TestSupportBundleRefusesAnUnusableRecipientDeclaration(t *testing.T) {
	r := clitest.New(t)

	manifest := filepath.Join(r.Bundle, "manifest.yaml")
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	block := "  morzer.dev/support:\n    recipients:\n      - not-an-age-key\n"
	if err := os.WriteFile(manifest, append(raw, []byte(block)...), 0o644); err != nil {
		t.Fatal(err)
	}

	r.Run("init", "--release", r.Bundle, "--profile", "embedded",
		"--domain", "demo.example", "--no-recovery-recipient",
		"--install-units=false").ExitCode(0)

	dir := t.TempDir()
	r.Run("support", "bundle", "--dir", dir).
		Failed().
		SaysAll("not an age recipient")

	written, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Errorf("the refusal wrote %d file(s), which is the leak it was refusing", len(written))
	}
}
