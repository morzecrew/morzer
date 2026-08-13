package domain

import (
	"strings"
	"time"
)

// A fleet row is what one installation says about itself, published at a stable
// key so somebody with twelve machines can read twelve rows (RFC 0026).
//
// It is deliberately a *row* and not a record. An attestation says what an
// operation did and is signed evidence for an auditor; a support bundle is
// everything about one machine for a vendor reading a ticket. This is neither:
// it is the line an operator wants on a screen -- what version, is it up, when
// did it last do anything -- and every field it does not carry is a field that
// cannot leak from twelve machines into one shared bucket.
//
// What that rules out, in the same allowlist spirit RFC 0015 established and
// 0024 applied one level up: no parameter values, no hostnames, no logs, no
// configuration content. The drift indicator is a *count* rather than a diff
// for exactly this reason -- "three targets differ" is the signal, and the
// three files are on the machine for somebody who is allowed to look.

const (
	// FleetSchemaVersion versions this payload.
	//
	// In the document rather than inferred from its shape. A reader that
	// meets a version it does not know reports a row carrying that problem
	// (RFC 0026 decision 4), and it cannot say that about a document which
	// does not state its version -- it would have to guess from the fields
	// present, which is indistinguishable from a truncated write.
	FleetSchemaVersion = 1

	// FleetPrefix is the key namespace rows live under on a target.
	//
	// Beside `attestations` and beside the backups, and invisible to both:
	// `backup list` reports a directory only when it holds a backup.json,
	// and retention removes only ids that listing produced.
	FleetPrefix = "fleet"

	// FleetFileName is the row itself, at the end of the key.
	FleetFileName = "status.json"
)

// FleetBound is what a row proves, and -- the part that matters -- what it does
// not.
//
// A field in every document, for the reason RFC 0025 made AttestationBound one:
// the reader who most needs the bound is the one who found the file without the
// documentation. Here it carries a specific warning that no other artifact in
// this project needs.
//
// **The key named in a row does not authenticate the row.** Rows from many
// machines share one bucket, and every one of those machines holds a valid
// signing key of its own. A machine that overwrites its neighbour's row
// rewrites the payload, the embedded public key and the signature together, and
// the result verifies perfectly against itself. So a verifier anchored in the
// row authenticates nothing at all against the one attacker this design has --
// which is why the anchor is a roster the reader maintains, and why the
// document says so out loud rather than leaving it to whoever wrote the reader.
const FleetBound = "This row is what one installation said about itself when it published. " +
	"The signing key named here is part of that claim, so a signature checked against it " +
	"proves only that these bytes and this key were published together -- not who published " +
	"them. Anchor verification in a roster you maintain, or treat the row as unauthenticated."

// FleetRow is the published payload.
type FleetRow struct {
	Schema int    `json:"schema"`
	Bound  string `json:"bound"`

	Product        string `json:"product"`
	InstallationID string `json:"installation_id"`

	// Mode is empty on a production machine, and present on a sandbox --
	// which is the field somebody scanning twelve rows for "why is that one
	// weird" reads first.
	Mode Mode `json:"mode,omitempty"`

	// Version is the release currently installed, empty when none is.
	Version string `json:"version,omitempty"`

	ManagerVersion string `json:"manager_version,omitempty"`

	Health FleetHealth `json:"health"`
	Drift  FleetDrift  `json:"drift"`

	// LastOperation is what this installation last did, nil when it has
	// never finished one.
	LastOperation *FleetOperation `json:"last_operation,omitempty"`

	// SigningKey is the public half, carried so an operator reading one
	// machine's row can compare a fingerprint against what
	// `morzer installation describe` printed on the machine itself.
	//
	// Never the trust anchor. See FleetBound.
	SigningKey string `json:"signing_key,omitempty"`

	// PublishedAt is when this row was written, and it is what stops an
	// older row from silently becoming current.
	//
	// The key is stable and the write replaces in place, so two publishers
	// racing -- a timer and an operator's `fleet publish`, or a slow
	// machine and a fast one -- would otherwise leave whichever finished
	// last in place regardless of which observed the newer state.
	PublishedAt Time `json:"published_at"`
}

// FleetHealth is what the runtime said, or that it did not answer.
type FleetHealth struct {
	// Services and Running are pointers, and it is the same lesson `ls`
	// learned about its unit count: a count has no way to spell "I could
	// not look", and zero is a real answer -- a deployment with nothing
	// running is exactly the machine somebody is scanning for. Reporting
	// 0 of 0 for a daemon that refused the connection would make the most
	// alarming row in a fleet indistinguishable from the most boring one.
	Services *int `json:"services"`
	Running  *int `json:"running"`

	// Attention is how many operations are flagged
	// requires-manual-intervention. Read from state files, so it is
	// answerable on a machine whose runtime is down -- which is the machine
	// most likely to have one.
	Attention int `json:"attention"`

	// Problem is why the runtime could not be asked, when it could not.
	// **This is a feature of the row, not a defect in it**: RFC 0026
	// decision 8 turns on `ls` needing no Docker call, precisely so that a
	// publisher can report the case where the runtime is what is broken.
	Problem string `json:"problem,omitempty"`
}

// FleetDrift says whether the files on disk still match what the release
// renders.
type FleetDrift struct {
	// Targets is how many configuration targets differ, nil when the
	// comparison could not be made -- no release installed, a template that
	// will not render, a parameter that will not resolve.
	//
	// A count and never the diff. The number is the signal an operator acts
	// on; the content is configuration, and configuration in a shared bucket
	// is the thing this payload exists not to be.
	Targets *int `json:"targets"`

	// Problem is why no comparison was made.
	Problem string `json:"problem,omitempty"`
}

// FleetOperation is the last thing this installation did.
type FleetOperation struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Outcome string `json:"outcome"`
	At      Time   `json:"at"`
}

// FleetRowInputs is everything a row is built from.
//
// A struct rather than a parameter list, for the reason AttestationInputs is
// one: these are almost all strings, and a mis-ordered argument would compile.
type FleetRowInputs struct {
	Installation   Installation
	Version        string
	ManagerVersion string
	Health         FleetHealth
	Drift          FleetDrift
	LastOperation  *FleetOperation
	PublishedAt    Time
}

// NewFleetRow builds the payload.
//
// Pure, like Attest: it takes values and returns one, so the shape of the
// document is testable without a machine, a key or a target -- and the signing,
// which needs all three, sits outside it.
//
// Every free-text field is bounded on the way in. The last operation's kind and
// outcome are the manager's own words today, and "today" is the whole argument
// against trusting that: this document goes to a bucket several machines write
// to, and a reader meets it in a terminal or a web view.
func NewFleetRow(in FleetRowInputs) FleetRow {
	row := FleetRow{
		Schema:         FleetSchemaVersion,
		Bound:          FleetBound,
		Product:        in.Installation.Product,
		InstallationID: in.Installation.ID,
		Mode:           in.Installation.Mode,
		Version:        boundedText(in.Version),
		ManagerVersion: boundedText(in.ManagerVersion),
		SigningKey:     in.Installation.Signing.PublicKey,
		PublishedAt:    in.PublishedAt,
	}

	row.Health = FleetHealth{
		Services:  copyCount(in.Health.Services),
		Running:   copyCount(in.Health.Running),
		Attention: in.Health.Attention,
		Problem:   boundedText(in.Health.Problem),
	}
	row.Drift = FleetDrift{
		Targets: copyCount(in.Drift.Targets),
		Problem: boundedText(in.Drift.Problem),
	}

	if op := in.LastOperation; op != nil {
		row.LastOperation = &FleetOperation{
			ID:      boundedText(op.ID),
			Kind:    boundedText(op.Kind),
			Outcome: boundedText(op.Outcome),
			At:      op.At,
		}
	}
	return row
}

// copyCount detaches a count so the row cannot share a pointer with whatever
// computed it.
//
// The same promise copyRetiredKeys makes about the slice: these constructors
// are value-to-value, and a caller mutating its own struct afterwards must not
// reach into the document that was built from it.
func copyCount(n *int) *int {
	if n == nil {
		return nil
	}
	out := *n
	return &out
}

// FleetKey is where this installation's row lives on a target.
//
// Guarded rather than formatted, and the guard is not paranoia about this
// manager's own values. The product and the id both come out of
// installation.yaml, which is a file on the operator's disk -- and the result
// is a key on a bucket several machines write to, so a product named `../..`
// would be one installation choosing what another's row is called.
//
// The same guard runs on the way back: `fleet ls` parses keys it read out of
// somebody else's listing, and a key it cannot parse is a row carrying that
// problem rather than a directory it walks into.
func FleetKey(product, installationID string) (string, error) {
	if err := ValidateProductName(product); err != nil {
		return "", ValidationError(err, "%q is not a product name", product)
	}
	if err := validateFleetID(installationID); err != nil {
		return "", err
	}
	return FleetPrefix + "/" + product + "/" + installationID + "/" + FleetFileName, nil
}

// ParseFleetKey reads a product and an installation id back out of a key.
//
// It refuses anything FleetKey would not have produced, which is what makes the
// listing safe to walk: the keys come from a prefix somebody else can write to,
// so `fleet/../../etc/passwd` and `fleet/demo/x/y/status.json` both have to be
// findings rather than fetches.
func ParseFleetKey(key string) (product, installationID string, err error) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != FleetPrefix || parts[3] != FleetFileName {
		return "", "", ValidationError(nil,
			"%q is not a fleet row's key", key).
			WithHint("rows are at %s/<product>/<installation-id>/%s",
				FleetPrefix, FleetFileName)
	}
	// Rebuilt rather than trusted: the split above accepts a product this
	// manager would never have written, and it is the same check both ways
	// so the two cannot drift.
	if _, err := FleetKey(parts[1], parts[2]); err != nil {
		return "", "", err
	}
	return parts[1], parts[2], nil
}

// maxFleetIDLen bounds an installation id used as a path component.
//
// Ids this manager writes are 30 characters ("op_" and a ULID). This is about
// the ones it reads: an id from a hand-edited installation.yaml, or from a key
// on somebody else's bucket.
const maxFleetIDLen = 128

// validateFleetID refuses an id that is not usable as one path component.
func validateFleetID(id string) error {
	refuse := func(why string) error {
		return ValidationError(nil, "%q is not an installation id: %s", id, why).
			WithHint("ids are generated by `morzer init` and look like " +
				"op_01K2Z9QW8ERT6YH3VXNBM5CDFG")
	}

	switch {
	case id == "":
		return refuse("it is empty")
	case len(id) > maxFleetIDLen:
		return refuse("it is too long")
	case strings.HasPrefix(id, "."):
		// `.` and `..` above all, but every dotted name too: a key
		// beginning with a dot is a file a directory listing hides,
		// which is the wrong property for a row whose whole job is
		// being seen.
		return refuse("it begins with a dot")
	}

	// An allowlist. A denylist here would have to anticipate every
	// character that means something to a path, a URL, a shell and an S3
	// key at once, and the four disagree.
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return refuse("it contains characters an id does not")
		}
	}
	return nil
}

// Validate checks a row read back off a target.
//
// A reader must not trust what it parsed. These are the properties the rest of
// the reader relies on rather than re-checking, and a row that fails one is
// displayed carrying that problem (decision 4) rather than dropped.
//
// The schema check is first and is the reason the version is in the payload: a
// row from a newer manager is refused *as a whole* rather than read
// field-by-field, which is the same rule LoadInstallation already applies to a
// future installation. Reading the fields this manager happens to recognise out
// of a document it does not understand would produce a row that looks complete
// and is not.
func (r FleetRow) Validate() error {
	switch {
	case r.Schema == 0:
		return ValidationError(nil, "it states no schema version").
			WithHint("this is not a fleet row, or it was written by something else")
	case r.Schema > FleetSchemaVersion:
		return ValidationError(nil,
			"it was written by a newer manager (schema %d, this manager reads %d)",
			r.Schema, FleetSchemaVersion).
			WithHint("upgrade the manager reading the fleet")
	case r.Product == "":
		return ValidationError(nil, "it names no product")
	case r.InstallationID == "":
		return ValidationError(nil, "it names no installation")
	case r.PublishedAt.IsZero():
		// Without this the row has no age, so staleness cannot be
		// computed and the reader would show it as fresh -- reporting
		// the least trustworthy row in the fleet as the most current.
		return ValidationError(nil, "it says when nothing was published")
	}
	return nil
}

// Age is how long ago this row was published, as of now.
//
// Negative when the row is stamped in the future, and deliberately not clamped:
// a row from a machine whose clock is wrong is a finding, and flattening it to
// zero would present it as having been published this instant.
func (r FleetRow) Age(now time.Time) time.Duration {
	return now.Sub(r.PublishedAt.Time)
}

// Stale reports whether this row is older than the threshold.
//
// A row exactly at the threshold is not yet stale: a publisher on an hourly
// timer read against a one-hour threshold would otherwise alternate between
// stale and fresh depending on scheduler jitter.
func (r FleetRow) Stale(now time.Time, after time.Duration) bool {
	return after > 0 && r.Age(now) > after
}
