package ports

import (
	"context"
	"encoding/json"
	"net/url"
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

// String is what an operator wrote, for messages. Canonical is for comparison.
func (r TargetRef) String() string {
	if r.URL != "" {
		return r.URL
	}
	return r.Canonical()
}

// Canonical is the target's identity, independent of how it was spelled.
func (r TargetRef) Canonical() string {
	out := r.Scheme + "://"
	if r.User != "" {
		out += r.User + "@"
	}
	return out + r.Host + r.Path
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

// String names what is present and prints none of it.
//
// These are live credentials in ordinary string fields, so anything that
// formats a ref -- a %+v in a log line while diagnosing a failed push, a debug
// dump of step state -- printed the private key along with it. fmt calls this
// for nested fields too, so redacting here covers the containing TargetRef
// without every call site having to remember.
//
// GoString covers %#v for the same reason: it is the other verb a developer
// reaches for when something is not working.
func (c TargetCredentials) String() string {
	parts := make([]string, 0, 6)
	if c.Region != "" {
		parts = append(parts, "region="+c.Region)
	}
	if c.Endpoint != "" {
		// Sanitised, not printed: an endpoint is a URL, and a URL can
		// carry userinfo -- https://key:secret@minio.example is a
		// credential wearing a hostname.
		parts = append(parts, "endpoint="+withoutUserinfo(c.Endpoint))
	}
	// Names only. Which credentials are configured is exactly what a
	// diagnosis needs; their values never are.
	for _, named := range []struct {
		name  string
		value string
	}{
		{"access_key_id", c.AccessKeyID},
		{"secret_access_key", c.SecretAccessKey},
		{"session_token", c.SessionToken},
		{"private_key", c.PrivateKey},
		{"passphrase", c.Passphrase},
		{"known_hosts", c.KnownHosts},
	} {
		if strings.TrimSpace(named.value) != "" {
			parts = append(parts, named.name+"=<set>")
		}
	}
	if len(parts) == 0 {
		return "TargetCredentials{}"
	}
	return "TargetCredentials{" + strings.Join(parts, " ") + "}"
}

func (c TargetCredentials) GoString() string { return c.String() }

// withoutUserinfo keeps the part of an endpoint that helps a diagnosis and
// drops the part that authenticates.
//
// When url.Parse finds an authority, its verdict is the whole answer: it read
// the userinfo if there was any, and a nil User means there was none.
//
// The rest is the bare form the S3 adapter also accepts, which has no authority
// for url.Parse to find and reaches here two ways. "192.168.1.10:9000" and
// "[::1]:9000" fail outright ("first path segment in URL cannot contain
// colon"); "user:password@host" parses as the scheme "user" with an opaque
// tail. Both are re-read behind an explicit "//", which is the authority they
// were meant to be.
//
// What that retry must never do is hand back a string it did not understand. A
// full URL that failed to parse -- an invalid port, a space in the host -- reads
// behind "//" as the host "https:" with the credential sitting in the path, so
// User comes back nil and the endpoint would be printed whole. Any "@" left
// unaccounted for after the retry is therefore reduced to "<set>": userinfo is
// the only thing that puts one in an endpoint, and one this function cannot
// place is one it cannot promise to have redacted.
func withoutUserinfo(endpoint string) string {
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Host != "" {
		if parsed.User == nil {
			return endpoint
		}
		parsed.User = nil
		return parsed.String()
	}

	prefix := ""
	if !strings.HasPrefix(endpoint, "//") {
		prefix = "//"
	}
	retried, err := url.Parse(prefix + endpoint)
	if err != nil {
		// Unparseable either way: say it is set and nothing else,
		// because whatever is in there cannot be reasoned about.
		return "<set>"
	}
	if retried.User != nil {
		retried.User = nil
		return strings.TrimPrefix(retried.String(), prefix)
	}
	if strings.Contains(endpoint, "@") {
		return "<set>"
	}
	return endpoint
}

// MarshalJSON redacts, because String does not reach encoding/json.
//
// The fields carry json tags -- they are read from a secret document -- so any
// marshal of a ref carries the private key with it. The shape is preserved:
// which credentials are configured stays visible, because that is what a
// `--json` consumer or a captured envelope legitimately reports, and every
// value that could authenticate is replaced.
func (c TargetCredentials) MarshalJSON() ([]byte, error) {
	type redacted struct {
		AccessKeyID     string `json:"access_key_id,omitempty"`
		SecretAccessKey string `json:"secret_access_key,omitempty"`
		SessionToken    string `json:"session_token,omitempty"`
		Region          string `json:"region,omitempty"`
		Endpoint        string `json:"endpoint,omitempty"`
		PrivateKey      string `json:"private_key,omitempty"`
		Passphrase      string `json:"passphrase,omitempty"`
		KnownHosts      string `json:"known_hosts,omitempty"`
	}
	const hidden = "[redacted]"
	set := func(v string) string {
		if strings.TrimSpace(v) == "" {
			return ""
		}
		return hidden
	}
	return json.Marshal(redacted{
		AccessKeyID:     set(c.AccessKeyID),
		SecretAccessKey: set(c.SecretAccessKey),
		SessionToken:    set(c.SessionToken),
		// Not secrets, and useful: they say which endpoint a failed push
		// was talking to -- once the userinfo a URL can carry is gone.
		Region:     c.Region,
		Endpoint:   withoutUserinfo(c.Endpoint),
		PrivateKey: set(c.PrivateKey),
		Passphrase: set(c.Passphrase),
		// A known_hosts line is a public key and a hostname, and neither
		// authenticates anybody to anything -- but it is credential
		// material an operator configured, and its shape is enough.
		KnownHosts: set(c.KnownHosts),
	})
}

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
