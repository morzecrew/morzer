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

	// Rotate replaces a secret with a freshly generated value of the same
	// shape, returning the previous value's fingerprint for the journal.
	Rotate(ctx context.Context, name string, spec GenSpec) error

	// Initialized reports whether encrypted state exists yet.
	Initialized(ctx context.Context) (bool, error)
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
