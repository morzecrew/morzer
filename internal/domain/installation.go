package domain

import (
	"fmt"
	"strings"
	"time"
)

// InstallationSchemaVersion is the second of the three versioned contracts:
// what the manager can migrate. Bumped only when the on-disk shape changes in
// a way a previous manager would misread.
// Bumped to 2 when `settings` became `parameters`. encoding/json ignores
// unknown fields, so without the bump an older manager would read a newer
// state file, see no `settings`, and silently run the whole deployment on
// default parameters -- a wrong port rather than a refusal.
//
// Bumped to 3 when `backup.targets` arrived, for exactly the same reason and a
// worse consequence: an older manager reads the state, sees no targets, takes a
// backup, reports success, and leaves it on the machine the operator configured
// a target to survive. A refusal naming the manager version is the only honest
// answer, because the failure is otherwise invisible until the disaster.
//
// Bumped to 4 when `notify.targets` arrived, which is the same shape once more:
// an older manager sees no targets, runs an operation, reports success, and the
// operator who configured a way to be told is never told. Schema 4 names this
// shape and no other -- a second field set under one version would let a
// manager implementing only one of them rewrite the state and silently drop
// the other's fields.
const InstallationSchemaVersion = 4

// Installation is the machine-specific state of one deployment. It is the
// only place operator intent is recorded; everything else is derived from it
// plus the release.
type Installation struct {
	SchemaVersion int `yaml:"schema_version" json:"schema_version"`

	// ID identifies this deployment across backups and restores. It is
	// generated once at init and never changes -- restore confirms against
	// it, so a typo here would defeat that check.
	ID string `yaml:"id" json:"id"`

	Product   string `yaml:"product" json:"product"`
	CreatedAt Time   `yaml:"created_at" json:"created_at"`

	// Profile selects the deployment topology from runtime.profiles.
	Profile string `yaml:"profile" json:"profile,omitempty"`

	// Domains are the public names the product is served under. The first
	// is canonical and becomes the public URL in `status`.
	Domains []string `yaml:"domains" json:"domains,omitempty"`

	Providers Providers `yaml:"providers" json:"providers"`
	Policy    Policy    `yaml:"policy" json:"policy"`

	// Parameters are the operator's choices among what the release
	// declares, stored as written and validated against the declaration on
	// load. Storing the raw text means a release that changes a
	// parameter's type surfaces a validation error rather than silently
	// reinterpreting the value.
	//
	// This replaces the `settings` free-form map, which reached the
	// template context but had no writer, no schema, no documentation and
	// no test -- it was never settable, so nothing can depend on it.
	Parameters map[string]string `yaml:"parameters" json:"parameters,omitempty"`

	// Notify is where this deployment reports outcomes.
	//
	// In the installation for the same reason Backup is: a vendor cannot
	// know whether their customer runs Slack, a webhook receiver or
	// nothing, and a manifest that named one would be a vendor deciding
	// where an operator's alerts go.
	Notify NotifyConfig `yaml:"notify" json:"notify,omitempty"`

	// Backup is where this deployment's backups go besides this disk.
	//
	// In the installation rather than the release manifest, and deliberately:
	// a vendor cannot know whether their customer has a second VM, a NAS or a
	// bucket, and a manifest that named one would be a vendor deciding where
	// an operator's data is kept.
	Backup BackupConfig `yaml:"backup" json:"backup,omitempty"`
}

// NotifyConfig is where operation outcomes are reported off the machine.
type NotifyConfig struct {
	// Targets each receive every forwarded event. A failure to deliver
	// never changes an operation's outcome -- see ports.Notifier.
	Targets []NotifyTargetConfig `yaml:"targets" json:"targets,omitempty"`
}

// HasTargets reports whether anything is configured to be told.
func (n NotifyConfig) HasTargets() bool { return len(n.Targets) > 0 }

// NotifyTargetConfig is one endpoint that receives events.
//
// The shape mirrors BackupTargetConfig, with one addition it needs and backup
// targets do not: a target's endpoint may *be* its credential. A Slack or Teams
// incoming-webhook URL is a bearer token spelled as a path, so putting it in
// URL would leak it into this file, into `doctor` output, and into an
// installation export beside a recovery key.
type NotifyTargetConfig struct {
	// Name identifies the target in diagnostics. Required when URLSecret is
	// used, because there is then nothing else safe to print.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// URL is the endpoint, when the endpoint carries no credential.
	// Exclusive with URLSecret.
	URL string `yaml:"url,omitempty" json:"url,omitempty"`

	// URLSecret names a secret holding the whole endpoint URL, for
	// services that spell the credential as a path. Exclusive with URL.
	URLSecret string `yaml:"url_secret,omitempty" json:"url_secret,omitempty"`

	// Credentials names a secret holding a credential document, whose
	// header value is sent with the request. A name rather than the value,
	// for the reason BackupTargetConfig.Credentials records.
	Credentials string `yaml:"credentials,omitempty" json:"credentials,omitempty"`

	// MinLevel is the lowest check severity this target receives, one of
	// "warn" or "error". Empty means "error".
	//
	// Per target and defaulting to the quiet end because warnings fire on
	// every `doctor` run until they are fixed, and deduplication is
	// deliberately out of scope. An operator who wants rotation reminders
	// asks for them on the target that should carry them.
	MinLevel string `yaml:"min_level,omitempty" json:"min_level,omitempty"`
}

// Endpoint reports how this target's URL is obtained, and validates that
// exactly one of the two forms is used.
func (t NotifyTargetConfig) Endpoint() (secretName string, direct string, err error) {
	hasURL := strings.TrimSpace(t.URL) != ""
	hasSecret := strings.TrimSpace(t.URLSecret) != ""

	switch {
	case hasURL && hasSecret:
		return "", "", ValidationError(nil,
			"a notify target sets both url and url_secret").
			WithHint("use url_secret when the URL itself is a credential, url otherwise")
	case hasSecret:
		return t.URLSecret, "", nil
	case hasURL:
		return "", t.URL, nil
	default:
		return "", "", ValidationError(nil,
			"a notify target sets neither url nor url_secret")
	}
}

// Label is what diagnostics may print about this target.
//
// Never the URL when it came from a secret: the whole reason that form exists
// is that the URL is the credential.
func (t NotifyTargetConfig) Label() string {
	if t.Name != "" {
		return t.Name
	}
	if strings.TrimSpace(t.URLSecret) != "" {
		return "(url from secret " + t.URLSecret + ")"
	}
	return t.URL
}

// BackupConfig is the operator's backup arrangement beyond the local disk.
type BackupConfig struct {
	// Targets are pushed to, in order, after every backup is verified.
	//
	// Several are allowed and each is pushed to. An operator who configured
	// two targets wants two copies, so one of them failing fails the backup
	// -- see the push step, which is where that is enforced.
	Targets []BackupTargetConfig `yaml:"targets" json:"targets,omitempty"`
}

// BackupTargetConfig is one place backups are kept.
type BackupTargetConfig struct {
	// URL is where, as file://, ssh:// or s3://.
	URL string `yaml:"url" json:"url"`

	// Credentials names a secret holding the credential document for this
	// target. Empty for file://, which needs none.
	//
	// A name rather than the values: this file is a report an operator reads
	// and `morzer doctor` prints, and a bucket key in it would be a bucket
	// key in every support ticket. The values live in the encrypted secret
	// state, which is also what carries them into an export -- and therefore
	// to the rebuilt machine that has to reach the target to recover.
	Credentials string `yaml:"credentials,omitempty" json:"credentials,omitempty"`
}

// HasTargets reports whether any off-machine copy is configured.
func (b BackupConfig) HasTargets() bool { return len(b.Targets) > 0 }

// Policy is operator-set behaviour that overrides release defaults. These are
// decisions about *this machine*, so they live with the installation rather
// than the release.
type Policy struct {
	// RequireSignature refuses any bundle that is not signed. Checksum
	// verification always happens; this raises the bar.
	RequireSignature bool `yaml:"require_signature" json:"require_signature"`

	// SigningKeys are the minisign public keys a bundle's signature must
	// verify against.
	//
	// They live with the installation rather than with the release for the
	// obvious reason: a bundle naming the key that may sign it would be a
	// bundle authorising itself. This is the operator saying whose releases
	// this machine will install.
	SigningKeys []string `yaml:"signing_keys" json:"signing_keys,omitempty"`

	// RetainReleases and RetainBackups override manifest retention when
	// non-zero. An operator with a small disk should not have to edit a
	// vendor's manifest.
	RetainReleases int `yaml:"retain_releases" json:"retain_releases,omitempty"`
	RetainBackups  int `yaml:"retain_backups" json:"retain_backups,omitempty"`

	// SkipBackupBeforeUpdate turns off the pre-update backup for every
	// update on this installation.
	//
	// Named for the unsafe direction on purpose. It was BackupBeforeUpdate,
	// where the zero value -- a field absent from a hand-edited file, a
	// record written before the field existed -- meant "do not back up",
	// and the one place a missing bool decides something is the one place
	// it must not decide that. Now absence means the backup is taken.
	//
	// Recorded in the journal when it applies, so an incident review can
	// see the choice was made rather than defaulted into.
	SkipBackupBeforeUpdate bool `yaml:"skip_backup_before_update" json:"skip_backup_before_update,omitempty"`

	// StaleBackupAfter is when `doctor` starts warning that the last
	// backup is too old.
	StaleBackupAfter Duration `yaml:"stale_backup_after" json:"stale_backup_after,omitempty"`
}

// DefaultPolicy is what `init` writes. Safe defaults, explicitly opted out of
// rather than silently absent.
func DefaultPolicy() Policy {
	return Policy{
		RequireSignature: false,
		StaleBackupAfter: Duration(48 * time.Hour),
	}
}

// PublicURL is the canonical address of the product, or empty when no domain
// was configured.
func (i Installation) PublicURL() string {
	if len(i.Domains) == 0 {
		return ""
	}
	d := i.Domains[0]
	if strings.Contains(d, "://") {
		return d
	}
	return "https://" + d
}

// RetentionReleases resolves the effective release retention: installation
// policy wins over the manifest, per the precedence rules.
func (i Installation) RetentionReleases(m Manifest) int {
	if i.Policy.RetainReleases > 0 {
		return i.Policy.RetainReleases
	}
	if m.Retention.Releases > 0 {
		return m.Retention.Releases
	}
	return DefaultRetentionReleases
}

// RetentionBackups resolves the effective backup retention.
func (i Installation) RetentionBackups(m Manifest) int {
	if i.Policy.RetainBackups > 0 {
		return i.Policy.RetainBackups
	}
	if m.Retention.Backups > 0 {
		return m.Retention.Backups
	}
	return DefaultRetentionBackups
}

// Validate checks the installation is internally consistent. Called after
// loading, so a hand-edited installation.yaml fails loudly at the start of an
// operation rather than halfway through it.
func (i Installation) Validate() error {
	var v validationErrors

	if i.SchemaVersion == 0 {
		v.add("schema_version", "is required")
	} else if i.SchemaVersion > InstallationSchemaVersion {
		return IncompatibleError(nil,
			"installation was written by a newer manager (schema %d, this manager reads %d)",
			i.SchemaVersion, InstallationSchemaVersion).
			WithHint("upgrade the manager; state migrations only run forward")
	}
	if i.ID == "" {
		v.add("id", "is required")
	}
	if i.Product == "" {
		v.add("product", "is required")
	} else if err := ValidateProductName(i.Product); err != nil {
		v.add("product", "%s", AsError(err).Message)
	}
	if i.Policy.RetainReleases < 0 {
		v.add("policy.retain_releases", "must not be negative")
	}
	if i.Policy.RetainBackups < 0 {
		v.add("policy.retain_backups", "must not be negative")
	}

	// An unsatisfiable policy is refused where it is written rather than
	// where it would be enforced. Requiring a signature with no key to check
	// it against would fail every operation with a message about bundles,
	// when the problem is one line of installation.yaml.
	if i.Policy.RequireSignature && len(i.Policy.SigningKeys) == 0 {
		v.add("policy.require_signature",
			"is set but policy.signing_keys is empty, so no bundle could ever satisfy it")
	}
	for n, key := range i.Policy.SigningKeys {
		if strings.TrimSpace(key) == "" {
			v.add(fmt.Sprintf("policy.signing_keys[%d]", n), "is empty")
		}
	}

	// A target is checked where it is written, not where it is used. The
	// alternative is a typo that surfaces during the nightly backup weeks
	// later, in the one operation whose whole purpose is to still work.
	seen := make(map[string]bool, len(i.Backup.Targets))
	for n, t := range i.Backup.Targets {
		field := fmt.Sprintf("backup.targets[%d]", n)

		parsed, err := ParseBackupTarget(t.URL)
		if err != nil {
			v.add(field+".url", "%s", AsError(err).Message)
			continue
		}
		if seen[parsed.Canonical()] {
			// Two identical targets would be pushed to twice and
			// pruned twice, and the second pass would report
			// removing what the first already removed.
			v.add(field+".url", "is listed twice")
		}
		seen[parsed.Canonical()] = true
	}

	return v.err()
}

// Time wraps time.Time to guarantee RFC3339 UTC in every serialised form.
// Mixed timezones in a journal make incident timelines needlessly hard to
// read, and the journal is the artifact an operator reaches for first.
type Time struct {
	time.Time
}

func NewTime(t time.Time) Time { return Time{t.UTC().Truncate(time.Second)} }

func (t Time) MarshalText() ([]byte, error) {
	if t.IsZero() {
		return []byte(""), nil
	}
	return []byte(t.UTC().Format(time.RFC3339)), nil
}

func (t *Time) UnmarshalText(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" {
		*t = Time{}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ValidationError(err, "invalid timestamp %q", s).
			WithHint("timestamps are RFC3339, e.g. 2026-08-03T00:00:00Z")
	}
	*t = Time{parsed.UTC()}
	return nil
}
