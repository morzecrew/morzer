package domain

import (
	"fmt"
	"sort"
	"strings"
)

// Verifying a statement asks three separate questions, and the value of the
// command is that it answers them separately.
//
//  1. Did this installation's key sign these bytes?
//  2. Do the statements form an unbroken chain?
//  3. Does the deployment running right now match the newest one?
//
// The first two can be answered from the files alone. The third is the one that
// can fail for a reason nobody planted -- an image swapped by hand, a container
// started outside the manager -- and RFC 0025 decision 8 makes the whole design
// conditional on it being able to.

// SignatureOutcome is what checking a statement's signature established.
//
// Three values rather than a boolean, and the middle one is the point. Folding
// "signed by a key this installation has retired" into "valid" would mean a
// rotation after a suspected compromise still accepts whatever the old key
// signs -- which is the one case rotation exists for (RFC 0028 decision 10).
type SignatureOutcome string

const (
	// SignedByCurrentKey: the signature verifies against the key this
	// installation signs with now.
	SignedByCurrentKey SignatureOutcome = "signed-by-current-key"

	// SignedByPredecessor: it verifies against a key this installation
	// used to sign with.
	//
	// **Provenance, not validity.** It establishes that the bytes came from
	// an earlier incarnation of this installation. It cannot establish that
	// the signature was made while that key was in service, because the
	// only date available comes from the artifact and the artifact is what
	// a forger writes. RFC 0028 decision 11 states the consequence rather
	// than pretending to a defence: rotation protects the future, and
	// nothing here protects the past.
	SignedByPredecessor SignatureOutcome = "signed-by-predecessor"

	// Unverifiable: no key this installation knows about produced it.
	Unverifiable SignatureOutcome = "unverifiable"

	// Unsigned: there is no signature to check.
	//
	// Distinct from Unverifiable because the remedies differ entirely. An
	// unsigned statement was emitted by a machine that had no key; an
	// unverifiable one was signed by something this installation cannot
	// account for, and is the finding.
	Unsigned SignatureOutcome = "unsigned"
)

// SignatureResult is the outcome, and which recorded key produced it. The key
// is empty for Unverifiable and Unsigned.
type SignatureResult struct {
	Outcome SignatureOutcome `json:"outcome"`
	Key     string           `json:"key,omitempty"`

	// RetiredAt and Reason are carried for a predecessor match, because an
	// operator reading their own timeline needs to know when the machine
	// stopped signing with it -- and, per RFC 0028 decision 11, this is the
	// only thing that timestamp is for.
	RetiredAt Time             `json:"retired_at,omitempty"`
	Reason    RetirementReason `json:"reason,omitempty"`
}

// VerifySignature decides the outcome from a checker that can test one key at a
// time.
//
// The checker is passed in rather than imported, which is what keeps this
// decidable without a crypto library in the domain layer -- and lets the order
// of the checks be tested directly. Order matters: the current key is tried
// first, so a key that is somehow both current and retired reports the
// stronger, more useful outcome.
func VerifySignature(
	signing Signing,
	hasSignature bool,
	verify func(publicKey string) bool,
) SignatureResult {
	if !hasSignature {
		return SignatureResult{Outcome: Unsigned}
	}

	if signing.HasKey() && verify(signing.PublicKey) {
		return SignatureResult{Outcome: SignedByCurrentKey, Key: signing.PublicKey}
	}

	for _, prev := range signing.PreviousKeys {
		if prev.Key == "" {
			continue
		}
		if verify(prev.Key) {
			return SignatureResult{
				Outcome:   SignedByPredecessor,
				Key:       prev.Key,
				RetiredAt: prev.RetiredAt,
				Reason:    prev.Reason,
			}
		}
	}

	return SignatureResult{Outcome: Unverifiable}
}

// ChainBreak is one place the recorded history does not join up.
type ChainBreak struct {
	// After and Before name the two statements, by operation id.
	After  string `json:"after"`
	Before string `json:"before"`

	Detail string `json:"detail"`
}

// VerifyChain reports where a sequence of statements fails to join up.
//
// Continuity is `from_version` matching the predecessor's `to_version`: a gap
// means a release was installed by something that filed no statement, which is
// exactly what an audit is trying to rule out.
//
// **Only version-moving operations participate.** An `apply` converges onto the
// release already installed and moves nothing, so it carries no `from_version`
// -- requiring one would report a break on every ordinary operation and train
// whoever reads this to ignore it.
//
// The input need not be sorted; statements are ordered by their operation's
// start time here, because a directory listing is alphabetical and an auditor's
// copy may have arrived in any order.
func VerifyChain(statements []Statement) []ChainBreak {
	moving := make([]Statement, 0, len(statements))
	for _, s := range statements {
		if s.Predicate.Release.FromVersion != "" || s.Predicate.Release.ToVersion != "" {
			moving = append(moving, s)
		}
	}
	sort.SliceStable(moving, func(i, j int) bool {
		return moving[i].Predicate.Operation.Started.Before(moving[j].Predicate.Operation.Started.Time)
	})

	var breaks []ChainBreak
	for i := 1; i < len(moving); i++ {
		prev, cur := moving[i-1], moving[i]

		from := cur.Predicate.Release.FromVersion
		if from == "" {
			// Converged onto what was already there. Nothing to join.
			continue
		}

		// The predecessor's *outcome* decides what the next statement
		// should have moved from. A failed operation left the release
		// where it was, so the next one moves from the same version --
		// treating a failure as a transition would report a break on
		// every rollback that worked.
		want := prev.Predicate.Release.ToVersion
		if prev.Predicate.Operation.Outcome != string(StatusSucceeded) {
			want = prev.Predicate.Release.FromVersion
			if want == "" {
				continue
			}
		}

		if from != want {
			breaks = append(breaks, ChainBreak{
				After:  prev.Predicate.Operation.ID,
				Before: cur.Predicate.Operation.ID,
				Detail: fmt.Sprintf("moves from %s, but the previous operation left %s",
					from, want),
			})
		}
	}
	return breaks
}

// LiveMismatch is one way the running deployment differs from what the newest
// statement says was deployed.
type LiveMismatch struct {
	// Kind is what disagrees: "image", "missing", "unattested".
	Kind string `json:"kind"`

	// Subject names the service or image the mismatch is about.
	Subject string `json:"subject"`

	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`

	Detail string `json:"detail"`
}

// LiveImage is what the runtime reports for one running service.
type LiveImage struct {
	Service string
	Ref     string
	Digest  string
}

// CompareToLive reports how the running deployment differs from a statement.
//
// This is the check RFC 0025 decision 8 makes the whole design conditional on:
// it must be able to fail for a reason nobody planted. Three ways it can, and
// each is something an operator does by hand at three in the morning:
//
//   - an image running at a digest the statement never mentioned;
//   - a service the statement attested that is no longer running;
//   - a service running that the statement never attested at all.
//
// A statement listing no images produces no findings rather than declaring
// everything unattested. That is the P1 machine -- images are recorded only
// where the manager inspected them -- and reporting a wall of findings for a
// document that simply did not carry the data would make the mode useless on
// exactly the installations that have been running longest.
func CompareToLive(stmt Statement, live []LiveImage) []LiveMismatch {
	// Matched on **digest**, not on the image reference.
	//
	// The reference is the wrong key and the first draft used it. The
	// manager rewrites a bundled image to a local alias -- `<repo>:morzer-
	// sha256-<hex>`, because a daemon cannot resolve a digest reference for
	// a repository it never pulled from -- so on every air-gapped
	// deployment the running reference is not the manifest's. Comparing
	// references reported every service as missing and every service as
	// unattested, on precisely the installations RFC 0011 exists for.
	//
	// The digest survives that rewriting, which is the whole point of the
	// alias carrying it. It is also the thing worth comparing: an operator
	// who swaps an image changes the bytes, and the digest is what names
	// them.
	attested := make(map[string]AttestedImage, len(stmt.Predicate.Images))
	for _, img := range stmt.Predicate.Images {
		if img.Digest == "" {
			// An image the manifest never pinned. Nothing to
			// compare, and treating it as a finding would report
			// drift on a manifest that never promised a digest.
			continue
		}
		attested[img.Digest] = img
	}
	if len(attested) == 0 {
		return nil
	}

	var out []LiveMismatch
	seen := make(map[string]bool, len(live))

	for _, l := range live {
		if l.Digest == "" {
			// Cannot tell what this is running. Not a mismatch:
			// "unpinned" and "swapped" are different situations and
			// only one of them is a finding.
			continue
		}
		seen[l.Digest] = true
		if _, ok := attested[l.Digest]; !ok {
			out = append(out, LiveMismatch{
				Kind:     "image",
				Subject:  l.Service,
				Actual:   l.Digest,
				Expected: expectedDigests(attested),
				Detail: fmt.Sprintf("%s runs %s at %s, which no attested image matches",
					l.Service, l.Ref, short(l.Digest)),
			})
		}
	}

	digests := make([]string, 0, len(attested))
	for d := range attested {
		digests = append(digests, d)
	}
	sort.Strings(digests)
	for _, d := range digests {
		if !seen[d] {
			img := attested[d]
			out = append(out, LiveMismatch{
				Kind:     "missing",
				Subject:  img.Ref,
				Expected: d,
				Detail: fmt.Sprintf("%s at %s was attested and is not running",
					img.Ref, short(d)),
			})
		}
	}
	return out
}

// expectedDigests renders the attested set for a message, so an operator
// looking at a mismatch can see what should have been there.
func expectedDigests(attested map[string]AttestedImage) string {
	out := make([]string, 0, len(attested))
	for d := range attested {
		out = append(out, short(d))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func short(digest string) string {
	const keep = 19 // "sha256:" plus twelve
	if len(digest) <= keep {
		return digest
	}
	return digest[:keep]
}
