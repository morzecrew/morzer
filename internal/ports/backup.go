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
	SHA256    string    `json:"sha256"`
}

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
