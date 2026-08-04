package agecrypt_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
)

// This is what stands between a backup and whoever finds the file. Everything
// below is either "the right key opens it" or "nothing else does".

func identity(t *testing.T) (path, public string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "identity")
	public, err := sopsage.GenerateIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, public
}

func TestARoundTripThroughOneKey(t *testing.T) {
	key, public := identity(t)
	const plaintext = "-- PostgreSQL database dump\nCREATE TABLE customers (id int);\n"

	var sealed bytes.Buffer
	if err := agecrypt.Encrypt(&sealed, strings.NewReader(plaintext), []string{public}); err != nil {
		t.Fatal(err)
	}

	// The ciphertext must not contain the plaintext, which is the entire
	// claim and worth asserting rather than assuming.
	if strings.Contains(sealed.String(), "CREATE TABLE") {
		t.Fatal("the encrypted form carries readable plaintext")
	}

	var opened bytes.Buffer
	if err := agecrypt.Decrypt(&opened, bytes.NewReader(sealed.Bytes()), key); err != nil {
		t.Fatal(err)
	}
	if opened.String() != plaintext {
		t.Errorf("round trip changed the contents:\n%q", opened.String())
	}
}

// TestEveryRecipientCanOpenIt is the arrangement `init` insists on: this
// machine's key plus an offline one, so losing the machine does not lose the
// data.
func TestEveryRecipientCanOpenIt(t *testing.T) {
	machine, machinePub := identity(t)
	offline, offlinePub := identity(t)
	stranger, _ := identity(t)

	var sealed bytes.Buffer
	if err := agecrypt.Encrypt(&sealed, strings.NewReader("the data"),
		[]string{machinePub, offlinePub}); err != nil {
		t.Fatal(err)
	}

	for name, key := range map[string]string{"the machine": machine, "the offline key": offline} {
		var out bytes.Buffer
		if err := agecrypt.Decrypt(&out, bytes.NewReader(sealed.Bytes()), key); err != nil {
			t.Errorf("%s could not open it: %v", name, err)
		} else if out.String() != "the data" {
			t.Errorf("%s opened it to %q", name, out.String())
		}
	}

	var out bytes.Buffer
	err := agecrypt.Decrypt(&out, bytes.NewReader(sealed.Bytes()), stranger)
	if err == nil {
		t.Fatal("a key that is not a recipient opened the backup")
	}
	if !strings.Contains(domain.AsError(err).Hint, "--identity") {
		t.Errorf("the refusal does not suggest the offline key, which is the "+
			"remedy in the case this exists for: %q", domain.AsError(err).Hint)
	}
}

// TestAlteredCiphertextIsRefusedRatherThanDecrypted. age is authenticated, so
// this is a stronger guarantee than the checksum in the backup manifest: a
// changed byte cannot produce changed plaintext, only a refusal.
func TestAlteredCiphertextIsRefusedRatherThanDecrypted(t *testing.T) {
	key, public := identity(t)

	var sealed bytes.Buffer
	if err := agecrypt.Encrypt(&sealed, strings.NewReader(strings.Repeat("data", 500)),
		[]string{public}); err != nil {
		t.Fatal(err)
	}

	altered := sealed.Bytes()
	altered[len(altered)/2] ^= 0x01

	var out bytes.Buffer
	if err := agecrypt.Decrypt(&out, bytes.NewReader(altered), key); err == nil {
		t.Fatal("a ciphertext altered by one bit decrypted successfully, so the " +
			"restore would write data nobody wrote")
	}
}

// TestTruncatedCiphertextIsRefused. An interrupted upload is the ordinary way
// this happens, and it must not restore into a half-empty database.
func TestTruncatedCiphertextIsRefused(t *testing.T) {
	key, public := identity(t)

	var sealed bytes.Buffer
	if err := agecrypt.Encrypt(&sealed, strings.NewReader(strings.Repeat("data", 500)),
		[]string{public}); err != nil {
		t.Fatal(err)
	}

	cut := sealed.Bytes()[:sealed.Len()/2]
	var out bytes.Buffer
	if err := agecrypt.Decrypt(&out, bytes.NewReader(cut), key); err == nil {
		t.Fatal("a truncated backup decrypted successfully")
	}
}

func TestEncryptingToNobodyIsRefused(t *testing.T) {
	var out bytes.Buffer

	for name, recipients := range map[string][]string{
		"an empty list": {},
		"a nil list":    nil,
	} {
		t.Run(name, func(t *testing.T) {
			err := agecrypt.Encrypt(&out, strings.NewReader("data"), recipients)
			if err == nil {
				t.Fatal("plaintext was written when encryption was asked for, which " +
					"is the failure this package exists to prevent")
			}
			if out.Len() != 0 {
				t.Errorf("%d bytes were written before the refusal", out.Len())
			}
		})
	}
}

func TestARecipientThatIsNotOneIsRefusedByName(t *testing.T) {
	_, public := identity(t)

	var out bytes.Buffer
	err := agecrypt.Encrypt(&out, strings.NewReader("data"),
		[]string{public, "age1-this-is-a-typo"})
	if err == nil {
		t.Fatal("a malformed recipient was accepted, so the backup would be " +
			"encrypted to fewer keys than the operator believes")
	}
	if !strings.Contains(domain.AsError(err).Hint, "age1") {
		t.Errorf("hint %q does not say what a valid key looks like",
			domain.AsError(err).Hint)
	}
}

func TestDecryptingWithAnIdentityThatIsNotThere(t *testing.T) {
	_, public := identity(t)

	var sealed bytes.Buffer
	if err := agecrypt.Encrypt(&sealed, strings.NewReader("data"), []string{public}); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"no path at all":           "",
		"a path that is not there": filepath.Join(t.TempDir(), "gone"),
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := agecrypt.Decrypt(&out, bytes.NewReader(sealed.Bytes()), path); err == nil {
				t.Fatal("a backup was decrypted without a key")
			}
		})
	}

	// A file that exists and is not a key.
	junk := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(junk, []byte("this is not an age key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := agecrypt.Decrypt(&out, bytes.NewReader(sealed.Bytes()), junk); err == nil {
		t.Fatal("a file that is not a key decrypted a backup")
	}
}

// TestALargeStreamIsNotHeldInMemory is a shape check rather than a memory
// measurement: the API takes readers and writers, so a caller cannot
// accidentally buffer a database dump by using it the obvious way.
func TestALargeStreamIsNotHeldInMemory(t *testing.T) {
	key, public := identity(t)

	const size = 8 << 20
	var sealed bytes.Buffer
	if err := agecrypt.Encrypt(&sealed, io.LimitReader(repeating{}, size),
		[]string{public}); err != nil {
		t.Fatal(err)
	}

	var counted countingWriter
	if err := agecrypt.Decrypt(&counted, bytes.NewReader(sealed.Bytes()), key); err != nil {
		t.Fatal(err)
	}
	if counted.n != size {
		t.Errorf("decrypted %d bytes, want %d", counted.n, size)
	}
}

type repeating struct{}

func (repeating) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte('a' + i%26)
	}
	return len(p), nil
}

type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}
