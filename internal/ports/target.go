package ports

import (
	"context"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
)

// BackupTarget is somewhere a backup can be kept that is not this machine.
//
// The port exists because every backup this manager takes is otherwise on the
// disk that will fail: `hookbackup` writes under BackupsDir(), retention prunes
// there, doctor checks freshness there, and restore reads from there. A target
// is the copy that survives the machine.
//
// One adapter per scheme, selected by a registry, exactly like ReleaseSource --
// so adding a transport is an adapter and one line of wiring rather than a
// branch in the lifecycle layer.
//
// Invariants every adapter is held to by the contract suite:
//   - the manifest is written last, so a half-finished push is invisible to
//     List rather than looking like a backup somebody could restore;
//   - List reads only backup.json, which is the one plaintext file in a
//     backup, so enumerating a target costs no decryption and works from a
//     machine that has lost its key;
//   - Push is idempotent: pushing the same backup twice leaves one copy;
//   - nothing is ever encrypted here. A backup arrives encrypted to the
//     deployment's own age recipients, and a target that encrypted again would
//     be a second answer to "who can read this".
type BackupTarget interface {
	// Schemes are the URL schemes this target handles: "file", "ssh", "s3".
	Schemes() []string

	// Push copies a local backup directory to the target and returns the
	// reference that will find it again.
	Push(ctx context.Context, target TargetRef, localDir, id string) (RemoteRef, error)

	// List enumerates what is there, reading each backup.json without
	// transferring anything else.
	List(ctx context.Context, target TargetRef) ([]BackupManifest, error)

	// Fetch copies one backup down into destDir, which the caller owns.
	Fetch(ctx context.Context, ref RemoteRef, destDir string) error

	// Verify reads a backup back off the target and checks its checksums,
	// writing nothing.
	//
	// A full transfer, which is the honest cost of the claim: a backup
	// nobody has read back is a hope, and that does not stop being true
	// because the backup is in a bucket. It needs no key -- the checksums
	// are of the stored bytes.
	Verify(ctx context.Context, ref RemoteRef) error

	// Remove deletes one backup. Retention calls it; nothing else does.
	Remove(ctx context.Context, ref RemoteRef) error
}

// TargetRef is a normalized backup target reference: where, plus what it takes
// to get in.
//
// It carries credentials rather than naming them because the adapter must not
// know that a secret store exists -- resolving the name is the lifecycle
// layer's job, and it is the one place that can also read them from a flag or
// from the environment when the secret store is on the machine that died.
type TargetRef struct {
	// Scheme selects the adapter.
	Scheme string

	// Host is the remote host, empty for file://.
	Host string

	// Path is the directory or key prefix backups live under. For s3:// the
	// first segment is the bucket.
	Path string

	// User is the login name for transports that have one.
	User string

	// URL is the reference as the operator wrote it, for messages. Never
	// carries a credential: TargetURL refuses one.
	URL string

	// Credentials authenticate to the target. Empty for file://, which is
	// why file:// is the transport a recovery can always reach.
	Credentials TargetCredentials
}

func (r TargetRef) String() string {
	if r.URL != "" {
		return r.URL
	}
	return r.Scheme + "://" + r.Host + r.Path
}

// Bucket returns the first path segment, which is the bucket for object
// stores, and the prefix under it.
func (r TargetRef) Bucket() (bucket, prefix string) {
	trimmed := strings.Trim(r.Path, "/")
	if trimmed == "" {
		return "", ""
	}
	bucket, prefix, _ = strings.Cut(trimmed, "/")
	return bucket, prefix
}

// TargetCredentials is everything any transport needs to authenticate.
//
// One struct rather than a per-scheme type because it is stored as one secret
// document and resolved in one place. An adapter reads the fields it
// understands and refuses when a field it requires is empty, which is a better
// failure than a type assertion at the registry boundary.
type TargetCredentials struct {
	// AccessKeyID and SecretAccessKey authenticate to an object store.
	AccessKeyID     string `yaml:"access_key_id" json:"access_key_id,omitempty"`
	SecretAccessKey string `yaml:"secret_access_key" json:"secret_access_key,omitempty"`
	SessionToken    string `yaml:"session_token" json:"session_token,omitempty"`

	// Region is the object store's region. Defaults per adapter.
	Region string `yaml:"region" json:"region,omitempty"`

	// Endpoint overrides the object store's host, which is what makes one
	// s3 adapter answer for MinIO, R2, B2 and GCS interoperability mode.
	Endpoint string `yaml:"endpoint" json:"endpoint,omitempty"`

	// PrivateKey is an OpenSSH private key, in PEM form.
	PrivateKey string `yaml:"private_key" json:"private_key,omitempty"`

	// Passphrase decrypts PrivateKey when it is encrypted.
	Passphrase string `yaml:"passphrase" json:"passphrase,omitempty"`

	// KnownHosts pins the target's host key, in the format of an OpenSSH
	// known_hosts file.
	//
	// Required for ssh://, with no flag to disable it. A target that accepts
	// any host key is a target that can be replaced by whoever is on the
	// path -- and the backup would then be pushed to a machine of their
	// choosing, which is the only way a backup encrypted to this
	// deployment's recipients can still hurt someone.
	KnownHosts string `yaml:"known_hosts" json:"known_hosts,omitempty"`
}

// IsZero reports whether nothing was supplied.
func (c TargetCredentials) IsZero() bool { return c == TargetCredentials{} }

// Redactions lists the values a log must never print.
func (c TargetCredentials) Redactions() []string {
	var out []string
	for _, v := range []string{c.SecretAccessKey, c.SessionToken, c.PrivateKey, c.Passphrase} {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// RemoteRef identifies one backup on one target.
//
// It carries the whole TargetRef rather than a URL because Fetch and Remove
// need the credentials as much as Push does, and threading them separately is
// how a retention pass ends up authenticating differently from the push that
// created what it is pruning.
type RemoteRef struct {
	Target TargetRef
	ID     string
}

// TargetURL parses a backup target URL into the reference an adapter takes.
//
// The grammar is domain's, so the check that refuses a URL at `backup target
// add` is the same one that resolves it at push time. Credentials are attached
// afterwards, by whoever could read them.
func TargetURL(raw string) (TargetRef, error) {
	u, err := domain.ParseBackupTarget(raw)
	if err != nil {
		return TargetRef{}, err
	}
	return TargetRef{
		Scheme: u.Scheme,
		Host:   u.Host,
		Path:   u.Path,
		User:   u.User,
		URL:    u.Raw,
	}, nil
}

// WithCredentials returns the reference with credentials attached.
func (r TargetRef) WithCredentials(c TargetCredentials) TargetRef {
	r.Credentials = c
	return r
}
