package ports

import (
	"context"

	"github.com/morzecrew/morzer/internal/domain"
)

// SecretStore owns the encrypted secret state of an installation.
//
// Invariants every implementation must uphold, and which the contract suite
// in test/contract enforces:
//   - values never reach argv, logs, or the journal;
//   - the encrypted state is replaced atomically;
//   - rendered files are 0400 inside a 0700 directory;
//   - the master key never leaves the manager process.
//
// v1 is sops-age. Later: SOPS+KMS, external Vault, systemd credentials.
type SecretStore interface {
	// Load decrypts the whole secret set into memory.
	Load(ctx context.Context) (domain.SecretSet, error)

	// Set stores a value, replacing any existing one.
	Set(ctx context.Context, name string, value domain.Secret) error

	// Generate creates a value according to the declaration's generator
	// and stores it. It fails rather than overwriting an existing secret
	// unless spec.Overwrite is set -- silently regenerating a live
	// credential is the kind of accident that takes a product down.
	Generate(ctx context.Context, name string, spec GenSpec) error

	// Remove deletes a secret. Removing an absent secret is not an error.
	Remove(ctx context.Context, name string) error

	// Render writes the secret set to targetDir as individual files, one
	// per declaration, and returns what it wrote. Files not backed by a
	// declaration are removed from the target: a stale secret file left
	// behind after a schema change is a leak with no owner.
	Render(ctx context.Context, targetDir string, schema domain.SecretSchema) ([]RenderedFile, error)

	// Metadata reports per-secret facts that are safe to display: when it
	// was last changed, how long the value is, its fingerprint. Never the
	// value.
	Metadata(ctx context.Context) ([]SecretMetadata, error)

	// Recipients lists who can decrypt the state.
	Recipients(ctx context.Context) ([]Recipient, error)
	AddRecipient(ctx context.Context, r Recipient) error

	// RemoveRecipient refuses to remove the last recipient, and refuses to
	// remove the machine's own identity -- either would lock the manager
	// out of its own state.
	RemoveRecipient(ctx context.Context, r Recipient) error

	// ReencryptFor replaces the recipient set wholesale, re-encrypting the
	// state for exactly the recipients given.
	//
	// Distinct from AddRecipient and RemoveRecipient, which preserve the
	// rest of the set. Recovery deliberately replaces a machine key that no
	// longer exists: there is no host left to remove it from, and the
	// refusals those two enforce -- never drop the machine's own key --
	// would make rebuilding a machine impossible.
	//
	// An empty set is refused. It would produce state nothing can ever
	// read, which is the one outcome no flag should be able to authorise.
	ReencryptFor(ctx context.Context, recipients []Recipient) error

	// Rotate replaces a secret with a freshly generated value of the same
	// shape, returning the previous value's fingerprint for the journal.
	Rotate(ctx context.Context, name string, spec GenSpec) error

	// Initialized reports whether encrypted state exists yet.
	Initialized(ctx context.Context) (bool, error)

	// EnsureIdentity creates this machine's key if it does not exist and
	// returns its public half. Idempotent: an existing identity is returned
	// untouched, because regenerating one would make every existing
	// encrypted value unreadable.
	EnsureIdentity(ctx context.Context) (string, error)

	// IdentityPublicKey returns the machine's public key without creating
	// one. It reports an error when no identity exists, which is what
	// `doctor` needs to distinguish "missing" from "unreadable".
	IdentityPublicKey(ctx context.Context) (string, error)

	// ValidateRecipient checks that a string is a usable public key.
	//
	// Exposed on the port so an operator's typo is caught before anything is
	// created, rather than after: a malformed recipient that happened to
	// parse would encrypt the state to a key nobody holds.
	ValidateRecipient(key string) error
}

// RecoverableSecretStore is an optional capability: a store that can be
// re-opened under a decryption identity other than this machine's own.
//
// Exactly one operation needs it. Rebuilding a lost machine means reading state
// encrypted for a key this host does not have, using an offline key the
// operator holds -- and then re-encrypting for a freshly generated machine key.
// Without it, the recovery recipient `init` insists on would remain a
// safeguard with no way to use it.
//
// It is optional rather than part of SecretStore because it is not universally
// implementable: a store backed by a KMS or by systemd credentials has no
// identity file to swap, and would have to fail the method rather than
// implement it. An operation that needs the capability asserts for it and says
// so plainly when the configured provider does not offer it.
type RecoverableSecretStore interface {
	SecretStore

	// WithIdentity returns a view of the same state that decrypts with the
	// identity at path. It does not read the file: an unusable identity
	// surfaces on the first operation, with that operation's error message.
	WithIdentity(identityFile string) SecretStore

	// ExportState returns the encrypted state verbatim, without decrypting
	// it. Export has no reason to see plaintext, and a format that carried
	// it would be one an operator could leak by copying a file.
	ExportState(ctx context.Context) ([]byte, error)

	// ImportState replaces the encrypted state wholesale with bytes an
	// export carried. It validates the shape before writing: overwriting
	// state with something unreadable is not recoverable by re-running.
	ImportState(ctx context.Context, state []byte) error
}

// GenSpec parameterises generation.
type GenSpec struct {
	Generator domain.Generator

	// Overwrite permits replacing an existing value.
	Overwrite bool
}

// RenderedFile is one secret written to the render directory. It carries no
// value -- only where it landed and what shape it had.
type RenderedFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
	Size int    `json:"size"`
}

// SecretMetadata is the displayable facts about a secret.
type SecretMetadata struct {
	Name string `json:"name"`

	// Fingerprint is a truncated hash of the value. It lets an operator
	// confirm two installations hold the same secret without either of
	// them printing it.
	Fingerprint string `json:"fingerprint"`

	Length      int         `json:"length"`
	LastChanged domain.Time `json:"last_changed,omitempty"`
}

// RecipientKind distinguishes the roles a recipient plays, because they have
// different removal rules.
type RecipientKind string

const (
	// RecipientMachine is this host's own identity. Removing it locks the
	// manager out.
	RecipientMachine RecipientKind = "machine"
	// RecipientRecovery is an offline key held elsewhere -- the answer to
	// "the VM is gone".
	RecipientRecovery RecipientKind = "recovery"
	// RecipientOperator is an additional human or automation key.
	RecipientOperator RecipientKind = "operator"
)

// Recipient is a public key that can decrypt the secret state.
type Recipient struct {
	// PublicKey is the age recipient string ("age1...").
	PublicKey string        `json:"public_key"`
	Kind      RecipientKind `json:"kind"`
	Comment   string        `json:"comment,omitempty"`
}
