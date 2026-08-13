package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
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
	SignatureRequired bool   `json:"signature_required"`
	SignatureVerified bool   `json:"signature_verified"`
	KeyID             string `json:"key_id,omitempty"`
	DigestPinned      bool   `json:"digest_pinned"`
	DigestMatched     bool   `json:"digest_matched"`
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
			ID:         s.ID,
			Status:     string(s.Status),
			DurationMS: s.DurationMS,
			Message:    s.Message,
			Error:      s.Error,
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
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
