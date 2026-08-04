package domain

import (
	"net/url"
	"path"
	"strings"
)

// BackupTargetURL is a parsed backup target address.
//
// The grammar lives in domain rather than beside the port because it is a rule
// about what may be written into an installation, and Validate has to enforce it
// where the value is recorded. A target URL that only fails at push time is a
// target that fails during the nightly backup, three weeks after the typo.
type BackupTargetURL struct {
	// Scheme selects the adapter: file, ssh, s3.
	Scheme string

	// Host is the remote host. Empty for file:// and for s3://, whose host
	// component is the bucket and is folded into Path.
	Host string

	// Path is the directory or key prefix backups live under. For s3:// the
	// first segment is the bucket.
	Path string

	// User is the login name, for the transports that have one.
	User string

	// Raw is the URL as the operator wrote it.
	Raw string
}

// Canonical is the target's identity, independent of how it was spelled.
//
// There is deliberately no String method here. The one on ports.TargetRef
// returns what the operator wrote, which is right for a message and wrong for a
// comparison -- and a type carrying both is a type where the wrong one gets
// picked. This layer only ever needs the identity.
//
// `file:///mnt/a`, `file://localhost/mnt/a` and `file:///mnt/a/` are one
// target. Comparing the raw strings instead let all three into an installation,
// which meant the same directory was pushed to three times and pruned three
// times -- each pass seeing a state the other two had just changed.
func (u BackupTargetURL) Canonical() string {
	out := u.Scheme + "://"
	if u.User != "" {
		out += u.User + "@"
	}
	return out + u.Host + u.Path
}

// ParseBackupTarget parses a backup target URL.
//
// Scheme support is deliberately not checked here. Which transports a build has
// is the registry's business, and its refusal can name them ("this build
// supports: file, s3, ssh"), which a fixed list in the domain layer could not.
// What is checked is everything that would be wrong in any build.
func ParseBackupTarget(raw string) (BackupTargetURL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return BackupTargetURL{}, Usage("the backup target URL is empty").
			WithHint("targets look like file:///mnt/usb/backups, " +
				"ssh://user@host/srv/backups, or s3://bucket/prefix")
	}
	if !strings.Contains(raw, "://") {
		return BackupTargetURL{}, Usage("%q is not a backup target URL", raw).
			WithHint("a target needs a scheme: file://, ssh:// or s3://")
	}

	u, err := url.Parse(raw)
	if err != nil {
		// With a hint like every other refusal here. A parser error alone
		// tells an operator their string is wrong without telling them
		// what a right one looks like, which is the one thing they need.
		return BackupTargetURL{}, Usage("invalid backup target %q", raw).
			WithHint("targets look like file:///mnt/usb/backups, " +
				"ssh://user@host/srv/backups, or s3://bucket/prefix")
	}

	// A credential in the URL is refused rather than accepted. This string
	// is written to installation.yaml, printed by `morzer doctor` and echoed
	// in every error message about the target, so a password in it is a
	// password in the journal and in the next support ticket.
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			return BackupTargetURL{}, Usage(
				"the backup target URL carries a password").
				WithHint("the URL is stored in installation.yaml and printed by " +
					"`morzer doctor`; put the credential in a secret and name it " +
					"with --credentials instead")
		}
	}

	out := BackupTargetURL{
		Scheme: u.Scheme,
		Host:   u.Host,
		Path:   path.Clean("/" + u.Path),
		Raw:    raw,
	}
	if u.User != nil {
		out.User = u.User.Username()
	}

	switch u.Scheme {
	case "":
		return BackupTargetURL{}, Usage("%q is not a backup target URL", raw).
			WithHint("a target needs a scheme: file://, ssh:// or s3://")

	case "file":
		// file://host/path is a legal URL and means something under SMB.
		// Here it would silently write to a different place than the
		// operator read in their own configuration.
		if u.Host != "" && u.Host != "localhost" {
			return BackupTargetURL{}, Usage(
				"file:// backup targets are local paths, but %q names the host %q",
				raw, u.Host).
				WithHint("write file:///absolute/path -- three slashes")
		}
		out.Host = ""
		if out.Path == "/" {
			return BackupTargetURL{}, Usage("the file:// backup target has no path").
				WithHint("write file:///mnt/usb/backups")
		}

	case "ssh":
		if u.Host == "" {
			return BackupTargetURL{}, Usage("the ssh:// backup target names no host").
				WithHint("write ssh://user@host/srv/backups")
		}
		if out.Path == "/" {
			return BackupTargetURL{}, Usage("the ssh:// backup target has no path").
				WithHint("write ssh://user@host/srv/backups")
		}

	case "s3":
		// s3://bucket/prefix, because that is how every other tool spells
		// it. The bucket arrives as the URL's host and is folded into the
		// path so one Bucket() answers for it everywhere.
		if u.Host == "" {
			return BackupTargetURL{}, Usage("the s3:// backup target names no bucket").
				WithHint("write s3://bucket/prefix")
		}
		out.Path = strings.TrimSuffix("/"+u.Host+out.Path, "/")
		out.Host = ""

	case "http", "https":
		return BackupTargetURL{}, Usage(
			"%q is a release source, not a backup target", raw).
			WithHint("backups are pushed, and there is no standard way to push over " +
				"https; use s3:// for an object store")
	}

	return out, nil
}
