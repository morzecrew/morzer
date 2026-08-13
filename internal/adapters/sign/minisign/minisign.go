package minisign

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	gominisign "github.com/jedisct1/go-minisign"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// Name identifies this signer in journal records and doctor output.
const Name = "minisign"

// keyMode is the secret key file's permissions: readable by its owner and
// nobody else, and not writable even by the owner. The same mode
// `stepCreateIdentity` gives the age identity, for the same reason.
const keyMode fs.FileMode = 0o400

// Signer holds this installation's signing identity.
type Signer struct {
	keyPath string

	// product names the machine in the key file's untrusted comment, which
	// is what somebody sees when they open the file or run `minisign -V`
	// against something it signed.
	product string
}

func New(keyPath, product string) *Signer {
	return &Signer{keyPath: keyPath, product: product}
}

var _ ports.Signer = (*Signer)(nil)

// EnsureKey mints the key if there is none, and returns the public half.
//
// Idempotent by reading first: a second call on a machine that already has a
// key returns that key rather than replacing it. Re-minting would silently
// invalidate every signature the machine had already made, which is the one
// failure mode of this method that produces no error and no symptom until
// somebody tries to verify an old artifact.
func (s *Signer) EnsureKey(ctx context.Context) (ports.PublicSigningKey, error) {
	key, err := s.PublicKey(ctx)
	switch {
	case err == nil:
		return key, nil
	case !errors.Is(err, domain.ErrNoSigningKey):
		// A key that exists and cannot be read is not a machine to
		// mint over. Overwriting here would destroy the identity that
		// signed everything this installation has already emitted, in
		// response to a transient read error.
		return ports.PublicSigningKey{}, err
	}

	pair, err := generate(s.comment())
	if err != nil {
		return ports.PublicSigningKey{}, err
	}

	if err := atomicfs.MkdirAll(filepath.Dir(s.keyPath), 0o700); err != nil {
		return ports.PublicSigningKey{}, err
	}
	if err := atomicfs.WriteFile(s.keyPath, pair.secretFile, keyMode); err != nil {
		return ports.PublicSigningKey{}, domain.SecretsError(err,
			"cannot write the signing key to %s", s.keyPath)
	}

	return ports.PublicSigningKey{Line: pair.publicLine, KeyID: pair.keyID}, nil
}

// PublicKey reads the key on disk without minting one.
func (s *Signer) PublicKey(ctx context.Context) (ports.PublicSigningKey, error) {
	sk, err := s.load()
	if err != nil {
		return ports.PublicSigningKey{}, err
	}
	defer sk.Wipe()
	return ports.PublicSigningKey{Line: publicLineFor(sk), KeyID: keyIDFor(sk)}, nil
}

// Sign produces a detached signature over data, in prehashed mode.
func (s *Signer) Sign(ctx context.Context, data []byte, trustedComment string) (ports.Signature, error) {
	sk, err := s.load()
	if err != nil {
		return ports.Signature{}, err
	}
	defer sk.Wipe()

	// A trusted comment travels inside the signature and minisign prints it
	// on a successful verification, so it is the one place an artifact can
	// say what it is to somebody holding only the .minisig. Newlines and
	// unprintable bytes would make a file minisign cannot read back, so
	// they are flattened rather than passed through and refused deeper.
	comment := sanitiseComment(trustedComment)

	sig, err := sk.Sign(data, gominisign.SignOptions{
		UntrustedComment: fmt.Sprintf("signature from %s", s.comment()),
		TrustedComment:   comment,
		// Prehashed ("ED"). The recommended mode, and the one that does
		// not need the message twice.
		Hashed: true,
	})
	if err != nil {
		return ports.Signature{}, domain.SecretsError(err, "cannot sign with the installation key")
	}

	return ports.Signature{Encoded: sig.Encode(), KeyID: keyIDFor(sk)}, nil
}

// load reads and parses the secret key, translating "no file" into the
// sentinel that says this machine has never minted one.
func (s *Signer) load() (gominisign.PrivateKey, error) {
	sk, err := gominisign.NewPrivateKeyFromFile(s.keyPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return gominisign.PrivateKey{}, domain.NoSigningKey(nil,
				"no signing key at %s", s.keyPath).
				WithHint("`morzer init` mints one; an installation migrated to " +
					"schema 6 acquires one the first time it signs")
		}
		return gominisign.PrivateKey{}, domain.SecretsError(err,
			"cannot read the signing key at %s", s.keyPath)
	}
	if sk.IsEncrypted() {
		// This manager writes unencrypted keys (decision 3), so an
		// encrypted one was put here by hand. Refusing beats prompting
		// for a passphrase in a systemd timer that has no terminal.
		return gominisign.PrivateKey{}, domain.SecretsError(nil,
			"the signing key at %s is passphrase-encrypted", s.keyPath).
			WithHint("this manager signs unattended and writes unencrypted keys; " +
				"decrypt it or move it aside and let `init` mint one")
	}
	return sk, nil
}

func (s *Signer) comment() string {
	if strings.TrimSpace(s.product) == "" {
		return "morzer installation signing key"
	}
	return fmt.Sprintf("morzer %s installation signing key", s.product)
}

// sanitiseComment makes any string safe as a minisign trusted comment: one
// line, printable ASCII.
//
// Replacing rather than refusing, because the caller is the manager describing
// its own operation and a refusal here would fail a lifecycle operation over a
// character in a product name.
func sanitiseComment(in string) string {
	var b strings.Builder
	for _, r := range in {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r >= 0x7f:
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "morzer"
	}
	return out
}
