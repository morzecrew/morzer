package ports

import (
	"context"

	"github.com/morzecrew/morzer/internal/domain"
)

// BackupEngine coordinates backup and restore. It does not implement either:
// the actual dump and the actual file copy belong to pg_dump, restic, or a
// release's own hooks.
//
// Invariants:
//   - every backup is self-describing (release version, schema version,
//     installation ID, timestamp, component list, checksums);
//   - plaintext secrets are never included -- the encrypted SOPS file is,
//     the rendered /run files are not;
//   - nothing in a backup is readable without a key except its manifest, so a
//     backup that leaves the machine carries no credential and no data with
//     it;
//   - restore requires explicit confirmation from the CLI layer.
type BackupEngine interface {
	Create(ctx context.Context, scope Scope, labels map[string]string) (BackupRef, error)
	List(ctx context.Context) ([]BackupRef, error)
	Inspect(ctx context.Context, ref BackupRef) (BackupManifest, error)

	// Verify re-reads the backup and checks its checksums. A backup that
	// has never been verified is a hope, not a backup.
	Verify(ctx context.Context, ref BackupRef) error

	Restore(ctx context.Context, ref BackupRef, opts RestoreOptions) error

	// Prune removes backups beyond the retention policy, never the most
	// recent one regardless of policy.
	Prune(ctx context.Context, policy RetentionPolicy) ([]BackupRef, error)
}

// Component names a part of a backup. They are separable because restore
// sometimes needs only one of them.
type Component string

const (
	ComponentDatabase Component = "database"
	ComponentFiles    Component = "files"
	ComponentConfig   Component = "config"  // installation.yaml, application.yaml
	ComponentSecrets  Component = "secrets" // the encrypted SOPS file only
	ComponentManifest Component = "manifest"
)

// AllComponents is the default scope of a full backup.
var AllComponents = []Component{
	ComponentDatabase, ComponentFiles, ComponentConfig, ComponentSecrets, ComponentManifest,
}

// Scope selects what a backup covers.
type Scope struct {
	Components []Component

	// Reason records why the backup was taken -- "pre-update", "manual",
	// "scheduled". It lands in the manifest and makes retention decisions
	// explicable.
	Reason string
}

// BackupRef identifies a backup. It is opaque to the lifecycle layer: only
// the engine that produced it knows how to resolve it.
type BackupRef struct {
	ID   string      `json:"id"`
	Path string      `json:"path,omitempty"`
	At   domain.Time `json:"at"`
	Size int64       `json:"size,omitempty"`
}

func (r BackupRef) IsZero() bool { return r.ID == "" }

// BackupManifestFileName is the self-describing header inside every backup,
// wherever the backup is.
//
// It lives here rather than with the engine that writes it because it is the
// one name a backup engine and every backup target have to agree on: a target
// enumerates a remote store by reading these and nothing else, which is what
// makes listing a bucket cost no decryption and work from a machine that has
// lost its key.
const BackupManifestFileName = "backup.json"

// BackupManifest is the self-describing header every backup carries. It is
// what makes a restore onto a different VM safe: the manager can check
// compatibility before touching anything.
type BackupManifest struct {
	SchemaVersion  int               `json:"schema_version"`
	ID             string            `json:"id"`
	InstallationID string            `json:"installation_id"`
	Product        string            `json:"product"`
	ReleaseVersion domain.Version    `json:"release_version"`
	SchemaAtBackup int               `json:"schema_at_backup,omitempty"`
	CreatedAt      domain.Time       `json:"created_at"`
	Components     []ComponentRecord `json:"components"`
	Labels         map[string]string `json:"labels,omitempty"`
	ManagerVersion string            `json:"manager_version"`
	Reason         string            `json:"reason,omitempty"`
}

// ComponentRecord is one part of a backup with its checksum.
type ComponentRecord struct {
	Component Component `json:"component"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`

	// SHA256 is the digest of the file *as stored*, so `backup verify` can
	// detect rot without holding a key. Tampering is caught separately and
	// more strongly: the encryption is authenticated, so an altered
	// ciphertext fails to decrypt rather than producing altered plaintext.
	SHA256 string `json:"sha256"`

	// Encryption names how the stored file is protected, empty for a
	// component written before this was recorded.
	//
	// A field rather than an inference from the extension: a restore has to
	// know what to do with a file it did not write, and reading that off a
	// filename is how a renamed artifact becomes an unreadable backup.
	Encryption Encryption `json:"encryption,omitempty"`
}

// Encryption is how one component is stored.
type Encryption string

const (
	// EncryptionNone is a component written before backups were encrypted.
	// Restores still read them; nothing writes them any more.
	EncryptionNone Encryption = ""

	// EncryptionAge is the deployment's own age recipients: this machine's
	// identity plus whatever offline and operator keys `secret recipients`
	// has been told about.
	EncryptionAge Encryption = "age"
)

type RestoreOptions struct {
	// Components limits what is restored; empty restores everything the
	// backup contains.
	Components []Component

	// Force is required for a destructive restore and must trace back to a
	// typed confirmation of the installation ID at the CLI layer.
	Force bool

	// TargetInstallationID is checked against the backup's own ID. When
	// they differ the restore is cross-installation and needs Force --
	// restoring one deployment's data over another is almost always a
	// mistake, and occasionally exactly what disaster recovery means.
	TargetInstallationID string

	// IdentityFile decrypts the backup, defaulting to this machine's own
	// age identity.
	//
	// It exists for the case the whole recovery design is for: the machine
	// that took the backup is gone, and the key in hand is the offline one.
	// Without it an encrypted backup would be readable only by the host
	// that no longer exists.
	IdentityFile string
}

// RetentionPolicy governs pruning.
type RetentionPolicy struct {
	// Keep is the number of backups to retain. The most recent backup is
	// never pruned, whatever Keep says.
	Keep int

	// KeepReasons are reasons exempt from pruning, e.g. a pre-update
	// backup kept until the update is confirmed good.
	KeepReasons []string
}
