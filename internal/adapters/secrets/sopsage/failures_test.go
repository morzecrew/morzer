package sopsage_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
)

// What sops does when it fails is the thing an operator meets at the worst
// moment: a machine that will not start because its secrets cannot be read.
// "cannot decrypt" is true and useless; whether the identity is missing, wrong
// or simply not a recipient sends them to three different places.
//
// The store takes an exec.Runner, so a scripted one reaches every branch --
// no need for a sops that has been made to misbehave.
//
// The decrypted form is JSON, not YAML: sops is invoked with
// `--output-type json` so the document that crosses the pipe has one
// unambiguous parse. The fixtures below are written that way for the same
// reason the adapter asks for it.

func scriptedStore(t *testing.T, encrypted string) (*sopsage.Store, *exec.Scripted, string) {
	t.Helper()

	dir := t.TempDir()
	file := filepath.Join(dir, "secrets.sops.yaml")
	if encrypted != "" {
		if err := os.WriteFile(file, []byte(encrypted), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	identity := filepath.Join(dir, "age", "identity")
	if _, err := sopsage.GenerateIdentity(identity); err != nil {
		t.Fatal(err)
	}

	runner := exec.NewScripted()
	return sopsage.New(runner, file, identity,
		sopsage.WithClock(func() time.Time {
			return time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
		}),
	), runner, dir
}

// encrypted is a document with just enough SOPS metadata to be read as one.
const encrypted = `values:
    db_password: ENC[AES256_GCM,data:abc,iv:def,tag:ghi,type:str]
sops:
    age:
        - recipient: age1lggyhqrw2nlhcxprm67z43rta597azn8gknawjehu9d9dl0jq3yqqvfafg
          enc: |
            -----BEGIN AGE ENCRYPTED FILE-----
    lastmodified: "2026-08-04T00:00:00Z"
`

func TestDecryptionFailuresAreClassifiedByRemedy(t *testing.T) {
	cases := map[string]struct {
		stderr string
		want   string
		hint   string
	}{
		"this machine's key is not a recipient": {
			"sops: no identity matched any of the recipients", "not a recipient", "recovery key",
		},
		"the data key cannot be opened": {
			"could not decrypt data key with any of the master keys", "not a recipient", "recovery key",
		},
		"the identity file is gone": {
			"open /etc/demo/age/identity: no such file or directory", "is missing", "backup",
		},
		"something else entirely": {
			"sops: unexpected internal state", "cannot decrypt", "doctor",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store, runner, _ := scriptedStore(t, encrypted)
			runner.OnExit("decrypt", 1, tc.stderr)

			_, err := store.Load(context.Background())
			if err == nil {
				t.Fatal("a failed decryption was reported as an empty secret set, " +
					"which would make the next apply render nothing and start anyway")
			}

			de := domain.AsError(err)
			if de.Code != domain.CodeSecrets {
				t.Errorf("code = %v, want the secrets code", de.Code)
			}
			if !strings.Contains(de.Message, tc.want) {
				t.Errorf("message %q does not say %q, so the operator cannot tell "+
					"which of three problems they have", de.Message, tc.want)
			}
			if !strings.Contains(de.Hint, tc.hint) {
				t.Errorf("hint %q does not point at %q", de.Hint, tc.hint)
			}
		})
	}
}

// TestATransportFailureIsNotADecryptionFailure. A sops that could not be
// started at all is a different problem from one that ran and refused.
func TestATransportFailureIsNotADecryptionFailure(t *testing.T) {
	store, runner, _ := scriptedStore(t, encrypted)
	runner.OnError("decrypt", os.ErrPermission)

	_, err := store.Load(context.Background())
	if err == nil {
		t.Fatal("a sops that never ran was reported as a successful read")
	}
	if !strings.Contains(domain.AsError(err).Message, "cannot decrypt") {
		t.Errorf("message: %q", domain.AsError(err).Message)
	}
}

// TestAnAbsentFileIsAnEmptyStore, not an error: `init` writes the first secret
// into something that does not exist yet.
func TestAnAbsentFileIsAnEmptyStore(t *testing.T) {
	store, runner, _ := scriptedStore(t, "")

	set, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("a machine with no secret state failed to load: %v", err)
	}
	if set.Len() != 0 {
		t.Errorf("secrets appeared from nothing: %d", set.Len())
	}
	if len(runner.Calls()) != 0 {
		t.Errorf("sops was run against a file that does not exist:\n%s",
			runner.CommandLines())
	}
}

func TestOutputThatIsNotADocument(t *testing.T) {
	store, runner, _ := scriptedStore(t, encrypted)
	runner.OnOutput("decrypt", "this is not a document at all")

	_, err := store.Load(context.Background())
	if err == nil {
		t.Fatal("output that is not a document was decoded into a secret set")
	}
}

// TestEncryptionFailuresAreReported. A save that silently did nothing would
// leave the operator believing a credential was rotated.
func TestEncryptionFailuresAreReported(t *testing.T) {
	store, runner, dir := scriptedStore(t, encrypted)

	// Decrypt succeeds, encrypt does not.
	runner.OnOutput("decrypt", `{"values":{"db_password":"old"}}`)
	runner.OnExit("encrypt", 1, "sops: failed to encrypt: no keys")

	err := store.Set(context.Background(), "db_password", domain.NewSecret("new"))
	if err == nil {
		t.Fatal("a failed encryption reported success, so the operator believes a " +
			"credential was changed when it was not")
	}

	// And the file was not replaced by anything half-written.
	data, readErr := os.ReadFile(filepath.Join(dir, "secrets.sops.yaml"))
	if readErr != nil {
		t.Fatalf("the existing state was removed by a failed write: %v", readErr)
	}
	if !strings.Contains(string(data), "ENC[") {
		t.Errorf("the encrypted state was replaced by plaintext:\n%s", data)
	}
}

// TestTheSOPSBinaryCanBeOverridden covers the option every deployment that
// installs sops somewhere unusual needs.
func TestTheSOPSBinaryCanBeOverridden(t *testing.T) {
	dir := t.TempDir()
	identity := filepath.Join(dir, "identity")
	if _, err := sopsage.GenerateIdentity(identity); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "secrets.sops.yaml")
	if err := os.WriteFile(file, []byte(encrypted), 0o600); err != nil {
		t.Fatal(err)
	}

	runner := exec.NewScripted()
	runner.OnOutput("decrypt", `{"values":{}}`)

	store := sopsage.New(runner, file, identity,
		sopsage.WithSOPSBinary("/opt/tools/sops"))

	if _, err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !runner.Ran("/opt/tools/sops") {
		t.Errorf("the configured binary was not used:\n%s", runner.CommandLines())
	}
}

// TestRenderingRemovesWhatNoDeclarationBacks. A stale file in the render
// directory is a credential the product can still read after the release
// stopped declaring it.
func TestRenderingRemovesWhatNoDeclarationBacks(t *testing.T) {
	store, runner, dir := scriptedStore(t, encrypted)
	runner.OnOutput("decrypt", `{"values":{"db_password":"a-value"}}`)

	target := filepath.Join(dir, "run", "secrets")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(target, "removed_last_release")
	if err := os.WriteFile(stale, []byte("an old credential"), 0o400); err != nil {
		t.Fatal(err)
	}

	schema := domain.SecretSchema{Secrets: []domain.SecretDeclaration{
		{Name: "db_password", Required: true},
	}}

	rendered, err := store.Render(context.Background(), target, schema)
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered) != 1 {
		t.Fatalf("rendered %d files, want 1: %+v", len(rendered), rendered)
	}
	if rendered[0].Mode != 0o400 {
		t.Errorf("a rendered secret is mode %04o, want 0400", rendered[0].Mode)
	}

	if _, err := os.Stat(stale); err == nil {
		t.Error("a secret the release no longer declares was left in the render " +
			"directory, where the product can still read it")
	}
}

func TestRenderingIntoSomewhereItCannotWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	store, runner, dir := scriptedStore(t, encrypted)
	runner.OnOutput("decrypt", `{"values":{"db_password":"a-value"}}`)

	parent := filepath.Join(dir, "readonly")
	if err := os.MkdirAll(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	_, err := store.Render(context.Background(), filepath.Join(parent, "secrets"),
		domain.SecretSchema{Secrets: []domain.SecretDeclaration{{Name: "db_password"}}})
	if err == nil {
		t.Fatal("secrets were reported rendered into a directory that could not be created")
	}
}

// TestRenderRefusesWhenARequiredSecretIsMissing. Rendering half the secrets
// and starting anyway is how a product comes up unable to reach its database.
func TestRenderRefusesWhenARequiredSecretIsMissing(t *testing.T) {
	store, runner, dir := scriptedStore(t, encrypted)
	runner.OnOutput("decrypt", `{"values":{"db_password":"a-value"}}`)

	schema := domain.SecretSchema{Secrets: []domain.SecretDeclaration{
		{Name: "db_password", Required: true},
		{Name: "session_key", Required: true},
	}}

	_, err := store.Render(context.Background(), filepath.Join(dir, "run"), schema)
	if err == nil {
		t.Fatal("a render with a required secret missing reported success")
	}
	if !strings.Contains(err.Error(), "session_key") {
		t.Errorf("the refusal does not name what is missing: %v", err)
	}
}
