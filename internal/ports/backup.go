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

	// ComponentVolumes is the contents of the project's named volumes,
	// read by the manager rather than produced by a hook.
	//
	// Distinct from ComponentFiles, which is whatever the hook chose to
	// write: the two have different consistency stories, and a restore has
	// to tell them apart. A `files` artifact is a file the hook produced
	// deliberately and can put back; a `volumes` tarball is a copy of
	// storage the manager took, and putting it back means writing into a
	// volume that nothing may have open.
	ComponentVolumes Component = "volumes"
)

// AllComponents is the default scope of a full backup.
//
// Volumes are in it. A release whose hook dumps its database and forgets the
// uploads volume is the common case rather than the exceptional one, and a
// backup that quietly omits the uploads is one that passes verification and
// does not work.
var AllComponents = []Component{
	ComponentDatabase, ComponentFiles, ComponentConfig, ComponentSecrets,
	ComponentManifest, ComponentVolumes,
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

	// Uncaptured names the project storage this backup did not include and
	// why. Empty when everything in scope was captured.
	Uncaptured []UncapturedVolume `json:"uncaptured,omitempty"`
}

// VolumeRecords returns the volume components of a backup, in manifest order.
//
// A record with no volume metadata is skipped rather than reported, because
// this is the accessor that only ever *reads*: `backup inspect` and the summary
// line after a backup. A listing that is one volume short is a poorer answer
// than it should be; a listing that refuses to render is no answer at all.
//
// Anything that acts on the result calls CheckVolumeRecords first, so the
// narrowing here is never what a restore proceeds on.
func (m BackupManifest) VolumeRecords() []ComponentRecord {
	var out []ComponentRecord
	for _, c := range m.Components {
		if c.Component == ComponentVolumes && c.Volume != nil {
			out = append(out, c)
		}
	}
	return out
}

// CheckVolumeRecords refuses a manifest whose volume components do not say
// which volume they hold.
//
// The metadata is everything a restore needs: the volume to write into and the
// services that must be out of the way first. Without it there is nothing to
// restore *to* -- and because VolumeRecords narrows silently, such a manifest
// restored the database, skipped the volume entirely, and reported success. An
// operator would learn about it from the application, not from the manager.
//
// It lives beside VolumeRecords rather than in the engine because it is the
// same invariant seen from the writing side, and keeping the pair together is
// what makes the narrowing above safe to read.
func (m BackupManifest) CheckVolumeRecords() error {
	for _, c := range m.Components {
		if c.Component != ComponentVolumes || c.Volume != nil {
			continue
		}
		return domain.BackupError(domain.ErrValidation,
			"backup %s records %q as a volume but does not say which volume it holds",
			m.ID, c.Path).
			WithHint("this manifest is damaged and the volume in it cannot be put " +
				"back; restore the rest with `--component database,config,secrets` " +
				"and take a fresh backup")
	}
	return nil
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

	// Volume describes the volume this component holds, set only on
	// ComponentVolumes records.
	Volume *VolumeRecord `json:"volume,omitempty"`
}

// Consistency is what a volume copy is worth.
//
// It is recorded per volume rather than per backup because one backup holds
// both kinds: an uploads volume the vendor declared safe to read live, and a
// queue spool the manager stopped a service to read.
type Consistency string

const (
	// ConsistencyCold means nothing was writing to the volume: every
	// service that mounts it was stopped for the duration of the copy.
	// This is what an undeclared volume gets.
	ConsistencyCold Consistency = "cold"

	// ConsistencyHot means the volume was read while its services ran, so
	// the copy is crash-consistent -- byte-for-byte what a power cut would
	// have left, not what a clean shutdown would have.
	//
	// It is only ever set because the release manifest declared it. The
	// manager does not decide that a volume is safe to read live; the
	// vendor claims it, and this field records the claim so an incident
	// review can see who made it.
	ConsistencyHot Consistency = "hot"
)

// VolumeRecord is what a restore needs in order to be safe, and what an
// operator needs in order to know what they have.
type VolumeRecord struct {
	// Volume is the Compose volume name, as the project's configuration
	// spells it.
	Volume string `json:"volume"`

	// Actual is the volume's real name in the container runtime, normally
	// the project name and the volume name joined. It is what a restore
	// writes into.
	Actual string `json:"actual,omitempty"`

	// Services are the services that mount it, from the resolved
	// configuration.
	//
	// Not decoration. It is what lets a restore refuse to write into a
	// volume a container has open, and what lets an operator see from the
	// manifest alone which services were stopped to take this copy.
	Services []string `json:"services,omitempty"`

	Consistency Consistency `json:"consistency"`
}

// UncapturedVolume is storage the backup deliberately did not include.
//
// A backup that silently omits a volume is the failure this whole component
// exists to prevent, so what was left out is recorded beside what was taken
// in. An operator reading a manifest after an incident can see that the
// uploads volume was excluded by the vendor, or that a bind mount was never a
// candidate, rather than inferring it from an absence.
type UncapturedVolume struct {
	// Volume is the volume name, or the host path for a bind mount.
	Volume string `json:"volume"`

	// Kind is "volume" or "bind".
	Kind string `json:"kind"`

	// Services are the services that mount it.
	Services []string `json:"services,omitempty"`

	// Reason is why it was not captured, in words an operator can act on.
	Reason string `json:"reason"`
}

const (
	// VolumeKindNamed is a named volume the runtime manages.
	VolumeKindNamed = "volume"

	// VolumeKindBind is a host path mounted into a container. Never
	// captured: it is an arbitrary path that may be enormous, may be
	// shared, and may be outside anything the manager manages.
	VolumeKindBind = "bind"

	// VolumeKindAnonymous is a volume a service mounts without naming.
	// Never captured, and unlike a bind mount no declaration could change
	// that: the runtime renames it whenever the container is recreated, so
	// a restore would have nowhere to put the contents back.
	VolumeKindAnonymous = "anonymous"
)

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
