package domain

import (
	"strings"
	"time"
)

// InstallationSchemaVersion is the second of the three versioned contracts:
// what the manager can migrate. Bumped only when the on-disk shape changes in
// a way a previous manager would misread.
const InstallationSchemaVersion = 1

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

	Providers Providers      `yaml:"providers" json:"providers"`
	Policy    Policy         `yaml:"policy" json:"policy"`
	Settings  map[string]any `yaml:"settings" json:"settings,omitempty"`
}

// Policy is operator-set behaviour that overrides release defaults. These are
// decisions about *this machine*, so they live with the installation rather
// than the release.
type Policy struct {
	// RequireSignature refuses any bundle that is not signed. Checksum
	// verification always happens; this raises the bar.
	RequireSignature bool `yaml:"require_signature" json:"require_signature"`

	// RetainReleases and RetainBackups override manifest retention when
	// non-zero. An operator with a small disk should not have to edit a
	// vendor's manifest.
	RetainReleases int `yaml:"retain_releases" json:"retain_releases,omitempty"`
	RetainBackups  int `yaml:"retain_backups" json:"retain_backups,omitempty"`

	// BackupBeforeUpdate is on by default; disabling it is recorded in the
	// journal so an incident review can see the choice was made.
	BackupBeforeUpdate bool `yaml:"backup_before_update" json:"backup_before_update"`

	// StaleBackupAfter is when `doctor` starts warning that the last
	// backup is too old.
	StaleBackupAfter Duration `yaml:"stale_backup_after" json:"stale_backup_after,omitempty"`
}

// DefaultPolicy is what `init` writes. Safe defaults, explicitly opted out of
// rather than silently absent.
func DefaultPolicy() Policy {
	return Policy{
		RequireSignature:   false,
		BackupBeforeUpdate: true,
		StaleBackupAfter:   Duration(48 * time.Hour),
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
