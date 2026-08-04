package sopsage_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/domain"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// Key handling is the part of the secret store that does not need sops, and it
// is where the irreversible mistakes live: an identity overwritten is every
// encrypted value lost, a malformed recipient is state encrypted to nobody, a
// removed machine key is a manager locked out of its own deployment. The
// contract suite covers encryption; this covers the refusals around it.

func store(t *testing.T, identity string) *sopsage.Store {
	t.Helper()
	return sopsage.New(infraexec.New(), filepath.Join(t.TempDir(), "secrets.sops.yaml"), identity)
}

func TestGenerateIdentityWritesAKeyNobodyElseCanRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "age", "identity")

	pub, err := sopsage.GenerateIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub, "age1") {
		t.Errorf("public key = %q, want an age1... recipient", pub)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// A private key that exists for even a moment at 0644 is a private key
	// that could have been read.
	if got := info.Mode().Perm(); got != 0o400 {
		t.Errorf("the identity is mode %04o, want 0400", got)
	}

	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dir.Mode().Perm(); got != 0o700 {
		t.Errorf("the identity directory is mode %04o, want 0700", got)
	}

	// The public half is derived from the file, not remembered.
	derived, err := sopsage.PublicKeyFromIdentityFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if derived != pub {
		t.Errorf("the key derived from the file (%s) is not the one generated (%s)",
			derived, pub)
	}

	// The comment is for a human reading the file, and must agree.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), pub) {
		t.Error("the file does not record its own public key, so an operator " +
			"holding a backup cannot tell which deployment it belongs to")
	}
}

func TestEveryWayAnIdentityFileCanBeUnusable(t *testing.T) {
	dir := t.TempDir()

	notAKey := filepath.Join(dir, "garbage")
	if err := os.WriteFile(notAKey, []byte("this is not an age key\n"), 0o400); err != nil {
		t.Fatal(err)
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("# just a comment\n"), 0o400); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		path string
		want string
	}{
		"no path configured at all": {"", "no age identity file"},
		"a path that is not there":  {filepath.Join(dir, "absent"), "does not exist"},
		"a file that is not a key":  {notAKey, "not valid"},
		// age's own parser refuses a file with no key lines, so this
		// never reaches the "contains no keys" branch below it. That
		// branch is defence in depth against a future parser that
		// returns an empty slice instead, and is deliberately
		// unreachable today.
		"a file with only comments": {empty, "not valid"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := sopsage.PublicKeyFromIdentityFile(tc.path)
			if err == nil {
				t.Fatalf("%s produced a public key", name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the failure does not say %q: %v", tc.want, err)
			}
		})
	}
}

func TestAMissingIdentityFileTellsTheOperatorWhereToLook(t *testing.T) {
	_, err := sopsage.PublicKeyFromIdentityFile(filepath.Join(t.TempDir(), "identity"))
	if err == nil {
		t.Fatal("a missing identity produced a key")
	}
	hint := domain.AsError(err).Hint
	if !strings.Contains(hint, "backup") {
		t.Errorf("hint %q does not mention restoring from a backup, which is "+
			"the only remedy once the key is gone", hint)
	}
}

// TestEnsureIdentityIsIdempotent. Regenerating would leave every existing
// encrypted value unreadable.
func TestEnsureIdentityIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "age", "identity")
	s := store(t, path)
	ctx := context.Background()

	first, err := s.EnsureIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EnsureIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("EnsureIdentity generated a second key (%s then %s), which "+
			"would make every existing secret unreadable", first, second)
	}

	pub, err := s.IdentityPublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pub != first {
		t.Errorf("IdentityPublicKey = %s, want %s", pub, first)
	}
}

// TestEnsureIdentityRefusesToReplaceOneItCannotParse is the most destructive
// thing this package could do, and the one it must not.
func TestEnsureIdentityRefusesToReplaceOneItCannotParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(path, []byte("AGE-SECRET-KEY-CORRUPTED-BY-A-BAD-DISK\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store(t, path).EnsureIdentity(context.Background())
	if err == nil {
		t.Fatal("a damaged identity was silently replaced, which destroys the " +
			"only key that could read the secret state")
	}
	if !strings.Contains(domain.AsError(err).Hint, "refusing to replace") {
		t.Errorf("hint %q does not say what it refused to do",
			domain.AsError(err).Hint)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("the damaged identity was overwritten anyway")
	}
}

func TestValidateRecipient(t *testing.T) {
	good, err := sopsage.GenerateIdentity(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatal(err)
	}

	if err := sopsage.ValidateRecipient(good); err != nil {
		t.Errorf("a freshly generated recipient was refused: %v", err)
	}
	// Whitespace from a copy-paste is not a typo.
	if err := sopsage.ValidateRecipient("  " + good + "\n"); err != nil {
		t.Errorf("a recipient with surrounding whitespace was refused: %v", err)
	}

	for name, key := range map[string]string{
		"empty":               "",
		"only whitespace":     "   \n",
		"an ssh key":          "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample",
		"a truncated age key": good[:len(good)-4],
		"a private key by mistake": "AGE-SECRET-KEY-1QQPQZRFRTC0GX3S8DMXQFRSY5DXQFCP" +
			"RQPQZRFRTC0GX3S8DMXQFRSY5DXQ",
	} {
		t.Run(name, func(t *testing.T) {
			err := sopsage.ValidateRecipient(key)
			if err == nil {
				t.Fatalf("%q was accepted as a recipient; the state would be "+
					"encrypted to a key nobody holds", key)
			}
			if name != "empty" && name != "only whitespace" {
				if !strings.Contains(domain.AsError(err).Hint, "age1") {
					t.Errorf("hint %q does not say what a valid key looks like",
						domain.AsError(err).Hint)
				}
			}
		})
	}
}

// TestALongKeyIsTruncatedInMessages: an age key is 62 characters, and quoting
// it whole three times in one error makes the error unreadable.
func TestALongKeyIsTruncatedInMessages(t *testing.T) {
	long := "age1" + strings.Repeat("q", 80)

	err := sopsage.ValidateRecipient(long)
	if err == nil {
		t.Fatal("an invalid key was accepted")
	}
	// The adapter's own sentence, not the whole chain: age's parser quotes
	// the key it was given, and replacing a library's diagnostic with a
	// shortened one would lose the reason it was rejected.
	msg := domain.AsError(err).Message
	if strings.Contains(msg, long) {
		t.Errorf("the whole key was quoted into the summary: %q", msg)
	}
	if !strings.Contains(msg, "…") {
		t.Errorf("nothing marks the summary as abbreviated: %q", msg)
	}
}

func TestRecipientsOnStateThatDoesNotExistYet(t *testing.T) {
	got, err := store(t, "").Recipients(context.Background())
	if err != nil {
		t.Fatalf("asking about a machine with no secret state failed: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("recipients appeared from nothing: %+v", got)
	}
}

func TestRecipientsOnAFileThatIsNotSOPS(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secrets.sops.yaml")
	if err := os.WriteFile(file, []byte("\tthis: [is not: yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := sopsage.New(infraexec.New(), file, "").Recipients(context.Background())
	if err == nil {
		t.Fatal("a corrupt file was read as a recipient list")
	}
	if !strings.Contains(domain.AsError(err).Hint, "backup") {
		t.Errorf("hint %q offers no way out", domain.AsError(err).Hint)
	}
}

// TestRecipientsIdentifiesTheMachineKeyByComparison, not by annotation, so a
// lost or hand-edited sidecar cannot make the manager think it is safe to
// remove its own access.
func TestRecipientsIdentifiesTheMachineKeyByComparison(t *testing.T) {
	dir := t.TempDir()
	identity := filepath.Join(dir, "identity")
	machine, err := sopsage.GenerateIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	other, err := sopsage.GenerateIdentity(filepath.Join(dir, "other"))
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(dir, "secrets.sops.yaml")
	writeSOPSHeader(t, file, machine, other)

	// The sidecar deliberately lies: it calls the machine key an operator
	// key, which is what a hand-edited file looks like.
	if err := os.WriteFile(filepath.Join(dir, "secrets.recipients.yaml"),
		[]byte("recipients:\n  "+machine+":\n    kind: operator\n  "+
			other+":\n    kind: recovery\n    comment: offline key\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	got, err := sopsage.New(infraexec.New(), file, identity).Recipients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d recipients, want 2: %+v", len(got), got)
	}

	// Machine first, then recovery, then operators -- the order the report
	// prints, so the key that matters most is at the top.
	if got[0].PublicKey != machine {
		t.Errorf("the machine key is not first: %+v", got)
	}
	if got[0].Kind != ports.RecipientMachine {
		t.Errorf("the sidecar's lie was believed: kind = %q, want machine", got[0].Kind)
	}
	if got[1].Kind != ports.RecipientRecovery || got[1].Comment != "offline key" {
		t.Errorf("the annotation for the recovery key was lost: %+v", got[1])
	}
}

// TestAnUnreadableSidecarDegradesTheDisplayAndNothingElse.
func TestAnUnreadableSidecarDegradesTheDisplayAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	key, err := sopsage.GenerateIdentity(filepath.Join(dir, "identity"))
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(dir, "secrets.sops.yaml")
	writeSOPSHeader(t, file, key)
	if err := os.WriteFile(filepath.Join(dir, "secrets.recipients.yaml"),
		[]byte("\tnot: [valid yaml\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	got, err := sopsage.New(infraexec.New(), file, "").Recipients(context.Background())
	if err != nil {
		t.Fatalf("a corrupt sidecar broke the recipient list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d recipients, want 1", len(got))
	}
	// The keys themselves are the source of truth; the kind falls back.
	if got[0].Kind != ports.RecipientOperator {
		t.Errorf("kind = %q, want the operator default", got[0].Kind)
	}
}

func TestExportRefusesWhenThereIsNothingToExport(t *testing.T) {
	_, err := store(t, "").ExportState(context.Background())
	if err == nil {
		t.Fatal("a machine with no secret state exported something")
	}
	if !strings.Contains(domain.AsError(err).Hint, "morzer init") {
		t.Errorf("hint %q does not say what to do first", domain.AsError(err).Hint)
	}
}

// TestImportRefusesAnythingItCannotVouchFor. Writing something unreadable over
// the secret state is the one failure here that re-running cannot fix.
func TestImportRefusesAnythingItCannotVouchFor(t *testing.T) {
	dir := t.TempDir()
	key, err := sopsage.GenerateIdentity(filepath.Join(dir, "identity"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "secrets.sops.yaml")
	writeSOPSHeader(t, file, key)

	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	s := sopsage.New(infraexec.New(), file, "")
	ctx := context.Background()

	cases := map[string]struct {
		state []byte
		want  string
	}{
		"empty":                   {[]byte("   \n"), "empty secret state"},
		"not a SOPS document":     {[]byte("\tthis: [is not: yaml\n"), "not a SOPS document"},
		"a document with no keys": {[]byte("sops:\n  age: []\n"), "no age recipients"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := s.ImportState(ctx, tc.state)
			if err == nil {
				t.Fatalf("%s was imported over the existing state", name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q: %v", tc.want, err)
			}

			// Nothing was touched. This is the assertion that matters:
			// the check has to happen before the write.
			now, readErr := os.ReadFile(file)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(now) != string(original) {
				t.Fatal("the existing secret state was overwritten by a refused import")
			}
		})
	}
}

func TestImportWritesTheStateAtSixHundred(t *testing.T) {
	dir := t.TempDir()
	key, err := sopsage.GenerateIdentity(filepath.Join(dir, "identity"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "export.sops.yaml")
	writeSOPSHeader(t, source, key)
	exported, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "secrets.sops.yaml")
	if err := sopsage.New(infraexec.New(), target, "").
		ImportState(context.Background(), exported); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	// Encryption is not a licence to widen filesystem permissions.
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("imported state is mode %04o, want 0600", got)
	}
}

func TestReencryptForRefusesAnEmptyRecipientSet(t *testing.T) {
	err := store(t, "").ReencryptFor(context.Background(), nil)
	if err == nil {
		t.Fatal("the state was re-encrypted for nobody")
	}
	if !strings.Contains(domain.AsError(err).Hint, "undecryptable") {
		t.Errorf("hint %q does not say what would happen", domain.AsError(err).Hint)
	}
}

func TestReencryptForValidatesEveryKeyBeforeTouchingAnything(t *testing.T) {
	dir := t.TempDir()
	good, err := sopsage.GenerateIdentity(filepath.Join(dir, "identity"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "secrets.sops.yaml")
	writeSOPSHeader(t, file, good)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	err = sopsage.New(infraexec.New(), file, "").ReencryptFor(context.Background(),
		[]ports.Recipient{
			{PublicKey: good, Kind: ports.RecipientMachine},
			{PublicKey: "age1-this-is-a-typo", Kind: ports.RecipientRecovery},
		})
	if err == nil {
		t.Fatal("a malformed recipient was accepted")
	}

	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the state was rewritten before the recipient list was checked, " +
			"so a typo in one key can damage access for all of them")
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets.recipients.yaml")); err == nil {
		t.Error("annotations were written for a recipient set that was refused")
	}
}

func TestWithIdentityChangesOnlyTheKey(t *testing.T) {
	dir := t.TempDir()
	machine, err := sopsage.GenerateIdentity(filepath.Join(dir, "identity"))
	if err != nil {
		t.Fatal(err)
	}
	recovery := filepath.Join(dir, "recovery")
	recoveryPub, err := sopsage.GenerateIdentity(recovery)
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(dir, "secrets.sops.yaml")
	writeSOPSHeader(t, file, machine, recoveryPub)

	original := sopsage.New(infraexec.New(), file, filepath.Join(dir, "identity"))
	view := original.WithIdentity(recovery)

	// Same file: an export from the recovery view is the same document.
	fromOriginal, err := original.ExportState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	exporter, ok := view.(interface {
		ExportState(context.Context) ([]byte, error)
	})
	if !ok {
		t.Fatal("the recovery view lost ExportState")
	}
	fromView, err := exporter.ExportState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(fromOriginal) != string(fromView) {
		t.Error("WithIdentity changed which file is read; only the key used to " +
			"open it should differ")
	}

	// And the machine key is now identified differently, because the
	// identity in hand is a different one.
	viewed, err := view.Recipients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var machineKind string
	for _, r := range viewed {
		if r.PublicKey == recoveryPub {
			machineKind = string(r.Kind)
		}
	}
	if machineKind != string(ports.RecipientMachine) {
		t.Errorf("under the recovery identity, that key reads as %q rather than "+
			"the machine key -- which is what makes recovery able to drop a "+
			"key belonging to a host that no longer exists", machineKind)
	}
}

// writeSOPSHeader writes a document with just enough SOPS metadata to be read
// as a recipient list. Encrypting for real needs the sops binary and is what
// the contract suite is for.
func writeSOPSHeader(t *testing.T, path string, recipients ...string) {
	t.Helper()

	var b strings.Builder
	b.WriteString("data: ENC[AES256_GCM,data:abc,iv:def,tag:ghi,type:str]\n")
	b.WriteString("sops:\n    age:\n")
	for _, r := range recipients {
		b.WriteString("        - recipient: " + r + "\n")
		b.WriteString("          enc: |\n            -----BEGIN AGE ENCRYPTED FILE-----\n")
	}
	b.WriteString("    lastmodified: \"2026-08-04T00:00:00Z\"\n")

	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}
