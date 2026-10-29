package minisign

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
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

var (
	_ ports.Signer            = (*Signer)(nil)
	_ ports.SigningKeyRotator = (*Signer)(nil)
)

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

	key, created, err := s.mint(s.keyPath)
	if err != nil {
		return ports.PublicSigningKey{}, err
	}
	if !created {
		// Somebody else minted between our read and the create.
		// Theirs is the identity.
		return s.PublicKey(ctx)
	}
	return key, nil
}

// mint writes a new key to path, unless a key is already there, and reports
// whether it was the one that wrote it.
//
// O_EXCL rather than an atomic rename, and this is the one place in the
// codebase where a rename is the wrong primitive.
//
// A rename is last-writer-wins, so two EnsureKey calls that both read "no key"
// would both mint and the second would replace the first. The loser's
// signatures then verify against a key no longer on the machine, with no error
// at the time and no symptom until somebody checks an old artifact. An
// exclusive create makes the race decidable: exactly one creator wins, and the
// loser reads the winner's key rather than overwriting it.
//
// `init` holds the deployment lock, so this is defence for the port's contract
// -- any operation about to sign may call this -- rather than for a race that
// exists today.
func (s *Signer) mint(path string) (ports.PublicSigningKey, bool, error) {
	pair, err := generate(s.comment())
	if err != nil {
		return ports.PublicSigningKey{}, false, err
	}
	if err := atomicfs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ports.PublicSigningKey{}, false, err
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, keyMode)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ports.PublicSigningKey{}, false, nil
		}
		return ports.PublicSigningKey{}, false, domain.SecretsError(err,
			"cannot write the signing key to %s", path)
	}
	if _, err := f.Write(pair.secretFile); err != nil {
		_ = f.Close()
		return ports.PublicSigningKey{}, false, domain.SecretsError(err,
			"cannot write the signing key to %s", path)
	}
	if err := f.Close(); err != nil {
		return ports.PublicSigningKey{}, false, domain.SecretsError(err,
			"cannot write the signing key to %s", path)
	}
	return ports.PublicSigningKey{Line: pair.publicLine, KeyID: pair.keyID}, true, nil
}

// stagedPath is where a rotation keeps the successor until it is recorded:
// beside the key, so moving it into place is a rename within one directory.
func (s *Signer) stagedPath() string { return s.keyPath + ".next" }

// StageKey mints the key a rotation will move into place, or returns the one
// an interrupted rotation already staged.
func (s *Signer) StageKey(ctx context.Context) (ports.PublicSigningKey, error) {
	if key, err := publicKeyAt(s.stagedPath()); !errors.Is(err, domain.ErrNoSigningKey) {
		return key, err
	}
	key, created, err := s.mint(s.stagedPath())
	if err != nil || created {
		return key, err
	}
	return publicKeyAt(s.stagedPath())
}

// ActivateStagedKey moves the staged key over the current one. The old private
// key goes with the rename: its signatures verify against the public half state
// keeps, and nothing signs with it again.
func (s *Signer) ActivateStagedKey(ctx context.Context) error {
	if err := os.Rename(s.stagedPath(), s.keyPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return domain.SecretsError(err, "cannot move the new signing key into %s", s.keyPath)
	}
	atomicfs.SyncDir(filepath.Dir(s.keyPath))
	return nil
}

// DiscardStagedKey removes a staged key that was never moved into place.
func (s *Signer) DiscardStagedKey(ctx context.Context) error {
	if err := os.Remove(s.stagedPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return domain.SecretsError(err, "cannot remove the staged signing key %s", s.stagedPath())
	}
	return nil
}

// PublicKey reads the key on disk without minting one.
func (s *Signer) PublicKey(ctx context.Context) (ports.PublicSigningKey, error) {
	return publicKeyAt(s.keyPath)
}

// publicKeyAt reads the public half of the key at path.
func publicKeyAt(path string) (ports.PublicSigningKey, error) {
	sk, err := loadKey(path)
	if err != nil {
		return ports.PublicSigningKey{}, err
	}
	defer sk.Wipe()
	return ports.PublicSigningKey{Line: publicLineFor(sk), KeyID: keyIDFor(sk)}, nil
}

// Sign produces a detached signature over data, in prehashed mode.
func (s *Signer) Sign(ctx context.Context, data []byte, trustedComment string) (ports.Signature, error) {
	sk, err := loadKey(s.keyPath)
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

// loadKey reads and parses the secret key at path, translating "no file" into
// the sentinel that says this machine has never minted one.
func loadKey(path string) (gominisign.PrivateKey, error) {
	sk, err := gominisign.NewPrivateKeyFromFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return gominisign.PrivateKey{}, domain.NoSigningKey(nil,
				"no signing key at %s", path).
				WithHint("`morzer init` mints one; an installation migrated to " +
					"schema 6 acquires one the first time it signs")
		}
		return gominisign.PrivateKey{}, domain.SecretsError(err,
			"cannot read the signing key at %s", path)
	}
	if sk.IsEncrypted() {
		// This manager writes unencrypted keys (decision 3), so an
		// encrypted one was put here by hand. Refusing beats prompting
		// for a passphrase in a systemd timer that has no terminal.
		return gominisign.PrivateKey{}, domain.SecretsError(nil,
			"the signing key at %s is passphrase-encrypted", path).
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

// Checker verifies detached signatures without holding any key.
//
// Stateless, so it is constructed where it is used rather than wired through
// the whole dependency graph as a singleton.
type Checker struct{}

func NewChecker() *Checker { return &Checker{} }

var _ ports.SignatureChecker = (*Checker)(nil)

// Check reports whether signature was made over data by publicKey.
func (c *Checker) Check(data []byte, signature []byte, publicKey string) bool {
	pk, err := gominisign.NewPublicKey(strings.TrimSpace(publicKey))
	if err != nil {
		return false
	}
	sig, err := gominisign.DecodeSignature(string(signature))
	if err != nil {
		return false
	}
	ok, err := pk.Verify(data, sig)
	return err == nil && ok
}
