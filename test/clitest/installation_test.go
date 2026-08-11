package clitest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// Export and import are the two halves of rebuilding a machine that is gone.
//
// The refusal that shapes both: an export only the exporting machine can read
// is not a recovery plan. So the fixture here is the arrangement an operator
// is supposed to have -- an offline key, generated and added as a recipient --
// and the round trip is run through the commands rather than around them.

// withRecoveryKey adds an offline recipient and returns the private half.
func withRecoveryKey(t *testing.T, r *clitest.Runner) string {
	t.Helper()

	identity := filepath.Join(t.TempDir(), "recovery.key")
	gen := r.Run("secret", "recipients", "generate-recovery-key", identity).ExitCode(0)

	pub := extractAgeKey(t, gen.Stdout+gen.Stderr)
	if pub == "" {
		t.Fatalf("no public key was printed:\n%s\n%s", gen.Stdout, gen.Stderr)
	}
	r.Run("secret", "recipients", "add", pub, "--kind", "recovery").ExitCode(0)
	return identity
}

func TestInstallationExportCarriesNoPlaintext(t *testing.T) {
	r := clitest.NewInstalled(t).SetSecret("db_password", dbPassword)
	withRecoveryKey(t, r)

	path := r.Path("demo.export")
	out := r.Run("installation", "export", path).ExitCode(0)
	out.NoOutputContains(dbPassword)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the export was not written: %v", err)
	}
	if strings.Contains(string(data), dbPassword) {
		t.Fatal("the export carries a decrypted secret, so anyone who reads the " +
			"file has the credential rather than needing a key")
	}
	// It has to carry the encrypted state, or an import produces an
	// installation with no secrets at all.
	if !strings.Contains(string(data), "sops") && !strings.Contains(string(data), "age") {
		t.Errorf("the export does not carry the encrypted state:\n%s", firstBytes(data))
	}
}

// TestARecoveredMachineKeepsTheOriginalIdentity is the whole point of the
// export. Without it, every backup the original took belongs to somebody else.
func TestARecoveredMachineKeepsTheOriginalIdentity(t *testing.T) {
	source := clitest.NewInstalled(t).SetSecret("db_password", dbPassword)
	identity := withRecoveryKey(t, source)

	path := filepath.Join(t.TempDir(), "demo.export")
	source.Run("installation", "export", path).ExitCode(0)
	originalID := installationID(t, source)

	// A machine that has never been initialised: import is what creates it.
	rebuilt := clitest.New(t)

	out := rebuilt.Run("installation", "import", path, "--identity", identity)
	out.ExitCode(0)

	assertIDMatches(t, rebuilt, originalID)

	// A recovery is the moment an operator is least able to reconstruct a
	// sequence from documentation, so the sequence is printed where they
	// already are -- including the warning that two live hosts sharing an
	// installation id will confuse every backup either of them takes.
	out.SaysAll(
		"was assumed from",
		"Decommission that machine",
		"morzer update",
		"morzer restore --force --confirm "+originalID,
		"morzer doctor",
	)
}

func TestInstallationImportOfSomethingThatIsNotAnExport(t *testing.T) {
	r := clitest.New(t)
	identity := filepath.Join(t.TempDir(), "recovery.key")
	r.Run("secret", "recipients", "generate-recovery-key", identity).ExitCode(0)

	junk := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(junk, []byte("this is not an export\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r.Run("installation", "import", junk, "--identity", identity).Failed()
	r.Run("installation", "import", filepath.Join(t.TempDir(), "gone"),
		"--identity", identity).Failed()
}

// TestImportRefusesOverALiveInstallation. Replacing a running machine's
// identity would orphan every backup it holds.
func TestImportRefusesOverALiveInstallation(t *testing.T) {
	source := clitest.NewInstalled(t)
	identity := withRecoveryKey(t, source)

	path := filepath.Join(t.TempDir(), "demo.export")
	source.Run("installation", "export", path).ExitCode(0)

	source.Run("installation", "import", path, "--identity", identity).Failed()
}

func TestExportRefusesWhenNothingElseCouldReadIt(t *testing.T) {
	// Created with --no-recovery-recipient, so the machine's own key is the
	// only one. An export nothing else can open is not a recovery plan.
	r := clitest.NewInstalled(t)

	out := r.Run("installation", "export", r.Path("demo.export"))
	out.Failed().OutputContains("only recipient is the exporting machine")
}

func installationID(t *testing.T, r *clitest.Runner) string {
	t.Helper()
	out := r.Run("status", "--json").ExitCode(0)
	id, ok := out.Field("data.installation_id").(string)
	if !ok || id == "" {
		t.Fatalf("the status envelope carries no installation id:\n%s", out.Stdout)
	}
	return id
}

func assertIDMatches(t *testing.T, r *clitest.Runner, want string) {
	t.Helper()
	if got := installationID(t, r); got != want {
		t.Errorf("the rebuilt machine has id %s, want %s -- every backup taken by "+
			"the original would be refused as belonging to another installation",
			got, want)
	}
}

func firstBytes(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "…"
	}
	return string(b)
}
