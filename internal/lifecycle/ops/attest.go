package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

// Attestation emission.
//
// The statement is written as two files beside each other: the document, and a
// detached minisign signature over it.
//
//	<var>/attestations/<operation-id>.json
//	<var>/attestations/<operation-id>.json.minisig
//
// Detached rather than an envelope with the signature inside, because that is
// what makes `minisign -Vm <file> -P <key>` work on the document unmodified --
// the command this project already teaches an operator to run against a
// release. An embedded signature would need the verifier to know how to strip
// it back out before checking, which is a bespoke step for the same gesture.
//
// **A failed emission never fails the operation.** RFC 0009 made a failed
// backup push fail the backup, because reporting success for data that is only
// on the doomed machine is exactly what it existed to prevent. This inverts
// that deliberately: a backup that did not leave is a data-loss risk that has
// already materialised, while an attestation that was not written is a gap in a
// record whose subject -- the deployment -- is fine. Failing an `update`
// because a record could not be filed would be the notification anti-pattern
// RFC 0015 spent a section avoiding.

// emitAttestation writes a signed statement for a finished operation.
//
// Errors are reported as warnings on the bus and swallowed, per the asymmetry
// above. The one thing it must never do is claim to have written a statement it
// did not.
func emitAttestation(ctx context.Context, d *Deps, rec domain.OperationRecord, in domain.AttestationInputs) {
	if d.Bus == nil && d.Signer == nil {
		return
	}

	stmt := domain.Attest(rec, in)

	body, err := json.MarshalIndent(stmt, "", "  ")
	if err != nil {
		d.warnf("cannot serialise the attestation for %s: %s", rec.ID, err)
		return
	}
	body = append(body, '\n')

	dir := d.Paths.AttestationsDir()
	if err := atomicfs.MkdirAll(dir, 0o755); err != nil {
		d.warnf("cannot create %s: %s", dir, domain.AsError(err).Message)
		return
	}

	path := filepath.Join(dir, rec.ID+".json")
	// 0644: an audit record with no secret in it by construction, and one
	// an auditor cannot read is one they will not read.
	if err := atomicfs.WriteFile(path, body, 0o644); err != nil {
		d.warnf("cannot write the attestation to %s: %s", path, domain.AsError(err).Message)
		return
	}

	if d.Signer == nil {
		// An unsigned statement is still a record, and this build signs
		// nothing. Said out loud rather than left for a reader to infer
		// from a missing file.
		d.warnf("wrote %s unsigned: this build has no signer", path)
		return
	}

	sig, err := d.Signer.Sign(ctx, body, fmt.Sprintf(
		"morzer %s %s %s", in.Installation.Product, rec.Type, rec.ID))
	if err != nil {
		// The commonest cause is a machine that has never minted a key,
		// which is not a fault -- so the statement stays, unsigned, and
		// the operator is told which of the two it is.
		d.warnf("wrote %s unsigned: %s", path, domain.AsError(err).Message)
		return
	}

	if err := atomicfs.WriteFile(path+".minisig", sig.Encoded, 0o644); err != nil {
		d.warnf("cannot write the attestation signature for %s: %s",
			rec.ID, domain.AsError(err).Message)
	}
}

// warnf publishes a warning without failing anything.
func (d *Deps) warnf(format string, args ...any) {
	if d.Bus == nil {
		return
	}
	d.Bus.Publish(events.Message(events.LevelWarn, format, args...))
}

// attestationInputs assembles what the statement needs that the operation
// record does not carry.
//
// Separated from emitAttestation so the assembly is testable without a
// filesystem, and so the one place that decides what a statement says about
// verification is a single function rather than each caller's idea of it.
func attestationInputs(
	inst domain.Installation,
	rel domain.Release,
	previous domain.ReleaseRecord,
	rendered []byte,
) domain.AttestationInputs {
	in := domain.AttestationInputs{
		Installation:   inst,
		RenderedConfig: rendered,
		SubjectName:    inst.Product,
	}

	if rel.Name() != "" {
		in.SubjectName = rel.String()
		in.Release = domain.AttestedRelease{
			ToVersion:     rel.Version().String(),
			ContentDigest: rel.Digest,
		}
	}
	if !previous.Version.IsZero() {
		in.Release.FromVersion = previous.Version.String()
		in.Release.SourceScheme = schemeOf(previous.SourceRef)
	}

	// What the manager checked before it acted, which is the part an
	// auditor is actually asking about -- and the part where it is easiest
	// to write a field that reads as a check and is not one.
	//
	// SignatureVerified is **left false unless the caller establishes it**.
	// The tempting shortcut is `SignatureVerified: policy.RequireSignature`,
	// on the reasoning that an operation which completed under that policy
	// must have verified a signature. The reasoning is even sound. It is
	// still the document asserting a check from a *setting* rather than
	// reporting one that happened, which is RFC 0013's defect -- `release
	// verify` printing "bundle is valid" for a bundle that could not render
	// -- reproduced in an artifact that travels to an auditor.
	//
	// So this function reports only what the release record carries, and
	// SetSignatureVerified is how a caller that watched the verification say
	// so. A machine with require_signature off tells the truth by leaving it
	// false; that is not "unverified", it is "this document does not claim
	// it", which is the honest reading of a field nobody populated.
	in.Verification = domain.AttestedVerification{
		SignatureRequired: inst.Policy.RequireSignature,
		DigestPinned:      rel.Digest != "",
		DigestMatched:     rel.Digest != "",
	}
	return in
}

// SetSignatureVerified records that this operation actually verified a
// signature, and against which key.
//
// A separate call rather than a parameter, so that populating it is a
// deliberate act by code that watched the check happen. Anything that has not
// watched one leaves the field alone and the document claims nothing.
func SetSignatureVerified(in *domain.AttestationInputs, keyID string) {
	in.Verification.SignatureVerified = true
	in.Verification.KeyID = keyID
}

// schemeOf pulls the scheme out of a source ref -- "oci", "https", "file" --
// without the rest, which can name a private registry and a path.
func schemeOf(ref string) string {
	if i := strings.Index(ref, "://"); i > 0 {
		return ref[:i]
	}
	return ""
}
