package domain

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// An attestation is what the manager knows about an operation, in a form that
// leaves the machine.
//
// The manager already verifies a signature, pins a digest, refuses an
// unverified image and journals every step. All of that evidence then stays on
// the machine in a format nothing outside this project reads. An operator asked
// to demonstrate that production runs what the vendor signed reconstructs it by
// hand, and the manager -- which knows the answer exactly -- contributes a
// screenshot.
//
// The format is an in-toto Statement, with morzer's own predicate type. Not
// SLSA Provenance: that describes how an artifact was *built*, this describes
// how one was *deployed*, and borrowing the build predicate would produce a
// document that validates against a well-known schema while asserting something
// the schema does not mean -- wrong in a way that reads as right.

const (
	// StatementType is in-toto's Statement v1 type URI.
	StatementType = "https://in-toto.io/Statement/v1"

	// PredicateType versions morzer's deployment predicate by URI, which is
	// how a consumer knows which fields to expect.
	PredicateType = "https://morzecrew.github.io/morzer/attestation/v1"

	// AttestationBound is what a signature over this document proves, and
	// -- as important -- what it does not.
	//
	// **A field in every document, not a line in the documentation.** RFC
	// 0013 exists because `release verify` printed "bundle is valid" for a
	// bundle that could not render; the same failure here is an attestation
	// read as a guarantee about the world. An artifact that travels has to
	// carry the bound on its own claim, because the reader who most needs it
	// is the one who found the file without the docs.
	AttestationBound = "This attestation proves that a process holding this installation's " +
		"signing key asserted these facts. It does not prove the facts, it does not prove " +
		"the machine was uncompromised when it signed, and it does not identify the operator."
)

// Statement is the in-toto envelope.
type Statement struct {
	Type          string    `json:"_type"`
	Subject       []Subject `json:"subject"`
	PredicateType string    `json:"predicateType"`
	Predicate     Predicate `json:"predicate"`
}

// Subject is what the statement is about: the release this operation moved to.
type Subject struct {
	Name string `json:"name"`

	// Digest is the release's content digest, keyed by algorithm the way
	// in-toto expects. Empty for an operation that had no release -- which
	// is a real case, not a defect: `config` changes an installation
	// without moving it between releases.
	Digest map[string]string `json:"digest,omitempty"`
}

// Predicate is what the manager did.
type Predicate struct {
	// Bound is AttestationBound, in every document. See the constant.
	Bound string `json:"bound"`

	Operation    AttestedOperation    `json:"operation"`
	Installation AttestedInstallation `json:"installation"`
	Release      AttestedRelease      `json:"release"`
	Verification AttestedVerification `json:"verification"`
	Images       []AttestedImage      `json:"images,omitempty"`
	Config       AttestedConfig       `json:"config"`
	Steps        []AttestedStep       `json:"steps,omitempty"`
}

type AttestedOperation struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Started Time   `json:"started"`
	Ended   Time   `json:"ended,omitempty"`

	// Outcome is the operation's status: success, failed or compensated.
	//
	// Failures are attested too, and that is the honesty test of the whole
	// feature rather than a nicety: a successful update is the least
	// interesting event to an auditor, the failed one that rolled back is
	// what they ask about, and a system that attests only its successes
	// attests nothing.
	Outcome string `json:"outcome"`
}

type AttestedInstallation struct {
	ID             string `json:"id"`
	Product        string `json:"product"`
	Mode           Mode   `json:"mode,omitempty"`
	ManagerVersion string `json:"manager_version"`

	// SigningKey is the public key that verifies this document, carried so
	// a reader holding only the file can check it against what the
	// installation says about itself.
	//
	// May be empty: a machine that has never minted a key emits an
	// unsigned statement rather than none, and a consumer written against
	// the happy machine is the risk RFC 0028 §5.6 names.
	SigningKey string `json:"signing_key,omitempty"`

	// PreviousKeys lets a verifier recognise a predecessor's signature
	// across a rebuild as *provenance* -- never as plain validity. See
	// RetiredKey: retired_at is history, not a check.
	PreviousKeys []RetiredKey `json:"previous_keys,omitempty"`
}

type AttestedRelease struct {
	FromVersion   string `json:"from_version,omitempty"`
	ToVersion     string `json:"to_version,omitempty"`
	SourceScheme  string `json:"source_scheme,omitempty"`
	ContentDigest string `json:"content_digest,omitempty"`
}

// AttestedVerification is what the manager checked before it acted, which is
// the part an auditor is actually asking about.
type AttestedVerification struct {
	SignatureRequired bool `json:"signature_required"`

	// SignatureVerified is a **tri-state**, and the third state is the
	// reason it is a pointer.
	//
	// Absent means this document makes no claim: the operation did not
	// verify a signature, because verification happens when a release is
	// staged and an `apply` runs against one already on disk. Present and
	// false would mean "checked, and it did not verify" -- a much stronger
	// statement, and one an auditor would act on.
	//
	// Emitting a plain `false` for "did not check" is the RFC 0013 defect
	// the whole bound field exists to avoid: a field that reads as a
	// finding when it is an absence. So the zero value writes nothing, and
	// only a caller that watched a verification fills it in.
	SignatureVerified *bool  `json:"signature_verified,omitempty"`
	KeyID             string `json:"key_id,omitempty"`

	// DigestPinned says the release carries a content digest. That is a
	// fact about the record and is always knowable.
	DigestPinned bool `json:"digest_pinned"`

	// DigestMatched is a tri-state for the same reason SignatureVerified
	// is, and it was originally written as the same mistake: set true
	// merely because a digest existed. Having a digest is not comparing
	// against one. The comparison happens when a bundle is fetched and
	// staged; an operation that did not fetch anything has nothing to
	// report, and says nothing rather than claiming a match it never made.
	DigestMatched *bool `json:"digest_matched,omitempty"`
}

type AttestedImage struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest,omitempty"`

	// Origin is "registry" or "bundle".
	Origin string `json:"origin,omitempty"`
}

// AttestedConfig carries parameter *names* and a digest of the rendered
// values, never the values.
//
// Values are refused rather than redacted: parameters include ports and
// hostnames, and an attestation is an artifact deliberately readable by
// somebody outside the organisation. The digest is what makes drift detectable
// without publishing what drifted.
type AttestedConfig struct {
	ParameterNames []string `json:"parameter_names,omitempty"`

	// RenderedDigest is salted per installation, so it detects change on
	// **one machine over time** -- the audit question -- and is not
	// comparable across machines. That comparability is given up on
	// purpose: the input is a handful of ports and booleans, and an
	// unsalted digest over that space is brute-forceable by anybody holding
	// the document.
	RenderedDigest string `json:"rendered_digest,omitempty"`
}

type AttestedStep struct {
	ID     string `json:"id"`
	Status string `json:"status"`

	// DurationMS rather than start and end times, because that is what
	// StepRecord persists. A duration plus the operation's own start places
	// a step in time well enough for an audit, and widening a persisted
	// record to feed a new artifact would be the tail wagging the dog
	// (RFC 0025 decision 9).
	DurationMS int64  `json:"duration_ms"`
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
}

// AttestationInputs is everything outside the operation record that a
// statement needs.
//
// A struct rather than eight parameters, so that adding a field is a compile
// error at the call site rather than a silently mis-ordered argument -- these
// are almost all strings and booleans.
type AttestationInputs struct {
	Installation Installation
	Release      AttestedRelease
	SubjectName  string
	Verification AttestedVerification
	Images       []AttestedImage

	// RenderedConfig is the bytes the digest is taken over. Never stored,
	// never echoed.
	RenderedConfig []byte
}

// Attest builds the statement for a finished operation.
//
// Pure: it reads a record and some inputs and returns a value. That is what
// lets the shape of the document be tested without a machine, a key or a
// filesystem -- and the signature, which is the part that needs all three, sits
// outside it.
func Attest(rec OperationRecord, in AttestationInputs) Statement {
	subject := Subject{Name: in.SubjectName}
	if in.Release.ContentDigest != "" {
		subject.Digest = map[string]string{"sha256": strings.TrimPrefix(in.Release.ContentDigest, "sha256:")}
	}

	steps := make([]AttestedStep, 0, len(rec.Steps))
	for _, s := range rec.Steps {
		steps = append(steps, AttestedStep{
			ID:         boundedText(s.ID),
			Status:     string(s.Status),
			DurationMS: s.DurationMS,
			Message:    boundedText(s.Message),
			Error:      boundedText(s.Error),
		})
	}

	return Statement{
		Type:          StatementType,
		Subject:       []Subject{subject},
		PredicateType: PredicateType,
		Predicate: Predicate{
			Bound: AttestationBound,
			Operation: AttestedOperation{
				ID:      rec.ID,
				Kind:    string(rec.Type),
				Started: rec.StartedAt,
				Ended:   rec.FinishedAt,
				Outcome: string(rec.Status),
			},
			Installation: AttestedInstallation{
				ID:             in.Installation.ID,
				Product:        in.Installation.Product,
				Mode:           in.Installation.Mode,
				ManagerVersion: rec.ManagerVersion,
				SigningKey:     in.Installation.Signing.PublicKey,
				PreviousKeys:   copyRetiredKeys(in.Installation.Signing.PreviousKeys),
			},
			Release:      in.Release,
			Verification: in.Verification,
			Images:       append([]AttestedImage(nil), in.Images...),
			Config: AttestedConfig{
				ParameterNames: parameterNames(in.Installation.Parameters),
				RenderedDigest: SaltedConfigDigest(in.Installation.AttestationSalt, in.RenderedConfig),
			},
			Steps: steps,
		},
	}
}

// MaxAttestedText bounds every free-text field in a document that travels --
// an attestation's steps, and a fleet row's.
//
// Generous enough for the sentence a failing step actually produces, and small
// enough that no amount of it changes what the document is.
const MaxAttestedText = 300

// boundedText makes a string safe to put in a document that travels.
//
// RFC 0025 §10 named this risk and P1 did not act on it: **a step's text is not
// all the manager's own words.** A failing hook contributes the last three
// lines of its stderr to the step's error, and a hook ships with the release --
// so vendor-controlled output reaches a signed artifact. `lastLines` bounds it
// by lines, which is not a bound at all: three lines of a script that prints a
// megabyte without a newline is a megabyte.
//
// It mattered less while statements stayed on the disk that produced them. P4
// pushes them to a bucket automatically, which is what turns an ugly local file
// into unbounded vendor bytes on somebody's object store, in a document signed
// by this installation.
//
// Two rules, both about what a *reader* of the artifact meets:
//
//   - **Control characters are dropped**, tabs and newlines included. A record
//     that travels is read in terminals, in logs and in web views; an escape
//     sequence in a signed document is a payload aimed at whoever opens it.
//   - **Truncated to MaxAttestedText bytes**, on a rune boundary, and it says
//     that it was. A silent truncation would leave an auditor reading half a
//     sentence as though it were the whole one.
//
// The journal keeps the full text. Only the copy that leaves is bounded, which
// is the right split: diagnosing a failure happens on the machine, and the
// statement exists to be read somewhere else.
func boundedText(s string) string {
	var b strings.Builder
	b.Grow(min(len(s), MaxAttestedText))

	for _, r := range s {
		// Unicode's Cc (C0 and C1) plus the format characters that
		// reorder text visually -- Cf covers the bidi overrides that
		// make a string display as something other than what it says.
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > MaxAttestedText {
			return b.String() + "… [truncated]"
		}
		b.WriteRune(r)
	}
	return b.String()
}

// BoundedText is boundedText, exported, because the rule runs in both
// directions and only one of them was wired.
//
// Everything boundedText says is about what this machine *writes*. A fleet row
// is read back off a target several machines can write to, and there the bytes
// are chosen by somebody else entirely -- so the reader needs the same
// treatment more than the writer does. See FleetRow.Bounded.
func BoundedText(s string) string { return boundedText(s) }

// CanonicalConfig encodes a set of rendered configuration files as the bytes
// the digest is taken over.
//
// Canonical, and injective. Map iteration order is random in Go, so digesting
// the concatenation directly would produce a different digest for identical
// configuration on every run.
//
// Both the target and the content are length-prefixed rather than delimited.
// Delimiters alone are not injective when a target may contain the delimiter: a
// path holding a newline could be chosen so that one target-and-content pair
// encodes identically to a different one, and two different configurations
// would share a digest.
//
// **Here rather than beside the operation that renders**, because the verifier
// re-derives this from the files on disk. Two encoders for one digest is one
// encoder too many: they would agree on the day they were written and drift
// into a drift detector that reports drift on every machine.
func CanonicalConfig(rendered map[string][]byte) []byte {
	if len(rendered) == 0 {
		return nil
	}

	targets := slices.Sorted(maps.Keys(rendered))

	var buf bytes.Buffer
	for _, target := range targets {
		fmt.Fprintf(&buf, "%d:%s%d:", len(target), target, len(rendered[target]))
		buf.Write(rendered[target])
	}
	return buf.Bytes()
}

// SaltedConfigDigest is HMAC-SHA256 of the rendered configuration under the
// installation's salt.
//
// HMAC rather than sha256(salt || data): it is the construction built for
// keyed digests, and it removes the question of whether this one is length-
// extendable from anybody's review.
//
// An empty salt returns an empty digest rather than an unsalted one. A machine
// with no salt is one that predates schema 6, and emitting an *unsalted* digest
// there would silently publish a value that can be brute-forced back to a port
// number -- the exact failure the salt exists to prevent, appearing on the
// machines least likely to be watched. Absent is honest; unsalted is not.
func SaltedConfigDigest(salt string, rendered []byte) string {
	if strings.TrimSpace(salt) == "" || len(rendered) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write(rendered)
	return "sha256:" + hex.EncodeToString(mac.Sum(nil))
}

// parameterNames returns the names an operator set, sorted, without values.
func parameterNames(params map[string]string) []string {
	if len(params) == 0 {
		return nil
	}
	names := slices.Sorted(maps.Keys(params))
	return names
}
