package ports

import "context"

// Signer is this machine's own signing identity: it produces detached
// signatures over bytes the machine is about to hand to somebody else.
//
// Deliberately not the same port as Verifier. Verifier answers "did the holder
// of the vendor's release key publish this bundle", and RFC 0004 decision 8
// keeps that key off deployment hosts. This one answers "did this installation
// say this", with a key that belongs on the machine precisely because it speaks
// only for that machine. Merging them would be an interface whose two halves
// have opposite threat models.
//
// What a signature by this key proves, carried by every consumer verbatim
// (RFC 0028 §5.5):
//
//	This signature proves that a process holding this installation's signing
//	key produced these bytes. It does not prove the bytes are true, it does
//	not prove the machine was uncompromised when it signed, and it does not
//	identify the operator.
type Signer interface {
	// EnsureKey mints this installation's signing key if it does not have
	// one, and returns the public half either way.
	//
	// Idempotent, and the idempotence is load-bearing: `init` calls it, and
	// so does every operation that is about to sign on a machine that
	// reached schema 6 by migration and has never signed. A second call
	// must return the same key rather than mint a new one -- a signer that
	// re-minted would break every signature it had already made.
	EnsureKey(ctx context.Context) (PublicSigningKey, error)

	// PublicKey returns the current key without minting one.
	//
	// Separate from EnsureKey because the read-only surfaces -- `status`,
	// `doctor`, the export -- must be able to ask what this machine's key
	// is without a side effect that generates cryptographic material.
	// Returns ErrNoSigningKey when the machine has none, which is a normal
	// state and not a failure.
	PublicKey(ctx context.Context) (PublicSigningKey, error)

	// Sign returns a detached signature over data.
	//
	// The signature is minisign's, in prehashed mode, so that verifying it
	// is `minisign -Vm <file> -P <key>` -- a command this project already
	// teaches an operator to run against a release. A second signature
	// format would be a second thing to learn for the same gesture.
	//
	// trustedComment is signed along with the signature and is visible to
	// `minisign -V`, which prints it on success. It must be a single
	// printable line.
	Sign(ctx context.Context, data []byte, trustedComment string) (Signature, error)
}

// SigningKeyRotator replaces this installation's signing key (RFC 0028 P2).
//
// Optional and type-asserted, like the runtime's capabilities: a build that
// signs nothing has nothing to rotate. Three calls rather than one, because the
// key file and the public key installation state records cannot change in one
// write, and the order between them decides what a crash leaves behind. The
// successor is staged, the caller records it as current with the old key as a
// predecessor, and only then is it moved into place -- so the old public key is
// in state whatever happens to the process.
type SigningKeyRotator interface {
	// StageKey mints the successor beside the current key and returns its
	// public half. A successor already staged, by a rotation that was
	// interrupted, is returned rather than replaced.
	StageKey(ctx context.Context) (PublicSigningKey, error)

	// ActivateStagedKey moves the staged key over the current one, which is
	// then gone. With nothing staged it does nothing, so a resumed rotation
	// can call it again.
	ActivateStagedKey(ctx context.Context) error

	// DiscardStagedKey removes a staged key that was never activated.
	DiscardStagedKey(ctx context.Context) error
}

// PublicSigningKey is the public half of a machine's signing key.
type PublicSigningKey struct {
	// Line is the key as minisign's -P flag accepts it, and as it is
	// recorded in installation state.
	Line string

	// KeyID identifies the key in messages, hex, as minisign prints it in
	// a public key file's comment.
	KeyID string
}

// Signature is a detached minisign signature.
type Signature struct {
	// Encoded is the full .minisig file content, ready to write beside the
	// signed bytes or embed in an artifact.
	Encoded []byte

	// KeyID identifies the key that made it.
	KeyID string
}

// SignatureChecker verifies a detached signature against a public key.
//
// Separate from Signer because verification needs no private key and no
// machine: `attest verify` runs against a directory of files, and requiring the
// signer would mean a statement could only be checked on a host that can also
// produce new ones. That is the wrong shape for an audit tool -- the auditor is
// not the machine.
type SignatureChecker interface {
	// Check reports whether signature was made over data by publicKey.
	//
	// A boolean rather than an error, because the caller is asking a
	// question with three answers and "no" is one of them: `attest verify`
	// tries the current key, then each retired key, and a failure to verify
	// is data rather than a fault. A malformed key or signature is also
	// false -- there is no outcome it could otherwise produce that the
	// caller would treat differently.
	Check(data []byte, signature []byte, publicKey string) bool
}
