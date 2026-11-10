package domain

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// SyncConfig is how this installation follows desired-state documents an
// authority publishes for it (RFC 0057), and what it last did with one.
//
// Set locally and root-only, never by a document (decision 4): a document
// that could change the authority, the target or the switch would let a
// compromised authority hand the machine to somebody else.
type SyncConfig struct {
	// Target is where the documents are, as a backup target URL.
	Target string `yaml:"target,omitempty" json:"target,omitempty"`

	// Credentials names the secret holding Target's credential document, as
	// a backup target's does.
	Credentials string `yaml:"credentials,omitempty" json:"credentials,omitempty"`

	// Authority is the minisign public key a document must be signed with.
	Authority string `yaml:"authority,omitempty" json:"authority,omitempty"`

	// Recipient is the authority's age recipient, which this machine's
	// published facts are encrypted to (RFC 0057 §5.10).
	Recipient string `yaml:"recipient,omitempty" json:"recipient,omitempty"`

	// Interval is how often the timer fetches, as a Go duration. Empty is
	// DefaultSyncInterval.
	Interval string `yaml:"interval,omitempty" json:"interval,omitempty"`

	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// Applied and Refused are the watermark: the last document applied and
	// the last refused. Each carries the key id that signed it, so a
	// re-pinned authority starts a watermark of its own (decision 27).
	Applied *SyncDocument `yaml:"applied,omitempty" json:"applied,omitempty"`
	Refused *SyncDocument `yaml:"refused,omitempty" json:"refused,omitempty"`

	// LastFetch is the last cycle's outcome, and LastReached when the
	// target last answered, which `doctor` compares with Interval.
	LastFetch   *SyncFetch `yaml:"last_fetch,omitempty" json:"last_fetch,omitempty"`
	LastReached Time       `yaml:"last_reached,omitempty" json:"last_reached,omitempty"`
}

// DefaultSyncInterval is how often a machine fetches when it is not told.
const DefaultSyncInterval = 15 * time.Minute

// SyncInterval is the configured interval, or the default.
func (c SyncConfig) SyncInterval() time.Duration {
	if d, err := time.ParseDuration(c.Interval); err == nil && d > 0 {
		return d
	}
	return DefaultSyncInterval
}

// IsZero reports whether nothing about sync is configured or recorded.
func (c SyncConfig) IsZero() bool {
	return c.Target == "" && c.Credentials == "" && c.Authority == "" && c.Recipient == "" &&
		c.Interval == "" && !c.Enabled && c.Applied == nil && c.Refused == nil && c.LastFetch == nil &&
		c.LastReached.IsZero()
}

// SyncDocument is one document's serial and what became of it.
type SyncDocument struct {
	Serial         int64  `yaml:"serial" json:"serial"`
	AuthorityKeyID string `yaml:"authority_key_id" json:"authority_key_id"`
	At             Time   `yaml:"at" json:"at"`

	// ExpiresAt is the document's own expiry, for an applied one, so that
	// `doctor` can say the authority has gone quiet.
	ExpiresAt Time `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`

	// Step and Class say where and why a refused one stopped. Never the
	// error's text, which can quote a parameter value (decision 23).
	Step  string `yaml:"step,omitempty" json:"step,omitempty"`
	Class string `yaml:"class,omitempty" json:"class,omitempty"`
}

// SyncFetch is what one cycle found at the target.
type SyncFetch struct {
	At     Time        `yaml:"at" json:"at"`
	Result SyncOutcome `yaml:"result" json:"result"`
}

// SyncOutcome is a cycle's result, as §5.9 names them.
type SyncOutcome string

const (
	SyncOK            SyncOutcome = "ok"
	SyncAbsent        SyncOutcome = "absent"
	SyncUnreachable   SyncOutcome = "unreachable"
	SyncBadSignature  SyncOutcome = "bad-signature"
	SyncUndecryptable SyncOutcome = "undecryptable"
	SyncExpired       SyncOutcome = "expired"
	SyncReplay        SyncOutcome = "replay"
	SyncUnmanageable  SyncOutcome = "unmanageable"
	SyncInvalid       SyncOutcome = "invalid"
	SyncFailed        SyncOutcome = "failed"
	SyncCurrent       SyncOutcome = "current"
)

// ManageableSettings are the only settings a document may touch, whatever a
// release declares (decision 5, decision 21). Trust, update, sync, policy,
// recipients and targets are never among them.
var ManageableSettings = []string{"installation.domains", "installation.profile"}

// IsManageableSetting reports whether a release may list name in managed.
func IsManageableSetting(name string) bool { return slices.Contains(ManageableSettings, name) }

// DesiredStateKind is a document's kind, which the envelope states.
const DesiredStateKind = "desired-state"

// DesiredStatePrefix is where an installation's documents are, under a
// target's root: one pair per serial (decision 26).
func DesiredStatePrefix(product, installationID string) string {
	return "state/" + product + "/" + installationID + "/"
}

// DesiredStateKeys are the two objects of the document with serial.
func DesiredStateKeys(product, installationID string, serial int64) (doc, sig string) {
	doc = DesiredStatePrefix(product, installationID) + strconv.FormatInt(serial, 10) + ".age"
	return doc, doc + ".minisig"
}

// DesiredStateComment is the signature's trusted comment, which names the
// document in the clear so a machine can drop one meant for another before
// it decrypts anything (RFC 0057 §5.3).
func DesiredStateComment(product, installationID string, serial int64) string {
	return "morzer desired-state product=" + product + " installation=" + installationID +
		" serial=" + strconv.FormatInt(serial, 10)
}

// ParseDesiredStateComment reads DesiredStateComment back. It is read only
// from a trusted comment that verified, so the text is the authority's.
func ParseDesiredStateComment(comment string) (product, installationID string, serial int64, ok bool) {
	fields := strings.Fields(strings.TrimSpace(comment))
	if len(fields) != 5 || fields[0] != "morzer" || fields[1] != "desired-state" {
		return "", "", 0, false
	}
	values := map[string]string{}
	for _, f := range fields[2:] {
		k, v, found := strings.Cut(f, "=")
		if !found {
			return "", "", 0, false
		}
		values[k] = v
	}
	n, err := parseSerial(values["serial"])
	if err != nil || values["product"] == "" || values["installation"] == "" {
		return "", "", 0, false
	}
	return values["product"], values["installation"], n, true
}

// parseSerial reads a serial: digits only, so `+5` and `05x` are refused rather
// than read as five.
func parseSerial(s string) (int64, error) {
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return 0, ValidationError(nil, "serial %q is not a whole number", s)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, ValidationError(err, "serial %q is out of range", s)
	}
	return n, nil
}

// ParseDesiredStateKey reads the serial out of a document's object key, or
// reports that the key is not one.
func ParseDesiredStateKey(prefix, key string) (serial int64, sig bool, ok bool) {
	name, found := strings.CutPrefix(key, prefix)
	if !found || strings.Contains(name, "/") {
		return 0, false, false
	}
	if base, isSig := strings.CutSuffix(name, ".age.minisig"); isSig {
		n, err := parseSerial(base)
		return n, true, err == nil
	}
	if base, isDoc := strings.CutSuffix(name, ".age"); isDoc {
		n, err := parseSerial(base)
		return n, false, err == nil
	}
	return 0, false, false
}
