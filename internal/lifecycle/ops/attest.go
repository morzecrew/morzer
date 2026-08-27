package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
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
	// A record with no id is an operation that never ran: the deployment
	// lock was held by somebody else, or the unfinished-operation gate
	// refused it before the engine started. There is nothing to attest.
	//
	// Every caller emits *before* returning its error, deliberately, so
	// that a failure is attested as well as a success -- and that is the
	// path that reaches here with a zero record. Without this guard a
	// refusal filed a statement naming no operation, no kind and no
	// outcome, at the path `<attestations>/.json`, overwritten by the next
	// one; `attest verify` then read it back as a statement, because it is
	// one. A guard must not journal its own refusals.
	if rec.ID == "" {
		return
	}

	// No early return on a missing bus. Writing the record is the job;
	// warning about a failure to write it is a courtesy, and making the
	// first conditional on the second would mean a build wired without a
	// bus silently filed nothing.
	if key := d.signingKeyForDocument(ctx, in.Installation, "attesting"); key != "" {
		in.Installation.Signing.PublicKey = key
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

	// Signed if this machine can, and pushed either way.
	//
	// **Either way** is the deliberate part, and it is why this is a
	// sequence rather than a chain of early returns -- which is what it was,
	// and which meant an unsigned statement never left the machine. A
	// build with no signer, or a machine that has never minted a key,
	// produces exactly the record that most needs to survive its disk, and
	// keeping it local because it lacked a signature would withhold
	// evidence from the installations with the least of it.
	sign(ctx, d, rec, in, path, body)

	// Off the machine last, after whatever was going to be written locally
	// is written: a push before that would copy bytes this machine does not
	// have, and a record that dies with the disk is the gap RFC 0025 §1 is
	// about.
	pushStatement(ctx, d, in.Installation, path)
}

// signingKeyForDocument resolves the key that will actually sign, so a document
// about to be built can name the key that signed it.
//
// Two failures this closes, and both were found in the attestation path before
// a second consumer existed. A machine that reached schema 6 by migration has
// no key and no operation had ever minted one, so it would have emitted
// unsigned documents forever -- RFC 0028 §5.6 says the minting step runs for
// `init` *and* for any operation that needs to sign, and only the first half
// was wired. And when the key on disk disagreed with the key in state, the
// document named state's key while the .minisig came from the other: a document
// attributable to nobody, which is precisely the case `doctor` exists to
// report.
//
// Returns empty when this machine cannot sign, which is a state and not a
// fault: the caller publishes the document unsigned rather than withholding it,
// because the installations with the least evidence are the ones that would be
// silenced.
//
// The verb names what the caller is doing, so the warning reads as a sentence
// on whichever command produced it.
func (d *Deps) signingKeyForDocument(
	ctx context.Context, inst domain.Installation, verb string,
) string {
	if d.Signer == nil {
		return ""
	}

	key, err := d.Signer.EnsureKey(ctx)
	switch {
	case err != nil:
		d.warnf("cannot resolve this machine's signing key: %s",
			domain.AsError(err).Message)
		return ""
	case inst.Signing.HasKey() && inst.Signing.PublicKey != key.Line:
		// State is not silently overwritten here -- that is
		// `init --repair`'s job, and doctor names it. What is refused is
		// emitting a document that lies about which key signed it.
		d.warnf("the signing key on disk is not the one installation state records; "+
			"%s under the key that signs (%s)", verb, key.KeyID)
	}
	return key.Line
}

// sign writes the detached signature beside a statement, or says why it could
// not. Never fails the emission; the statement is already on disk.
func sign(
	ctx context.Context, d *Deps, rec domain.OperationRecord,
	in domain.AttestationInputs, path string, body []byte,
) {
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

	if err := atomicfs.WriteFile(path+minisigExt, sig.Encoded, 0o644); err != nil {
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
// from is the version this operation moved *away* from, and is zero for an
// operation that moved nothing. `apply` converges an installation onto the
// release already recorded as current, so it passes zero: writing
// from_version == to_version there would describe an update that did not
// happen.
func attestationInputs(
	inst domain.Installation,
	rel domain.Release,
	record domain.ReleaseRecord,
	from domain.Version,
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
		in.Images = attestedImages(rel)
	}
	if !from.IsZero() {
		in.Release.FromVersion = from.String()
	}
	// The scheme the *installed* release came from -- oci, https, file --
	// without the rest of the ref, which can name a private registry and a
	// path inside it.
	in.Release.SourceScheme = schemeOf(record.SourceRef)

	// What the manager checked before it acted, which is the part an
	// auditor is actually asking about -- and the part where it is easiest
	// to write a field that reads as a check and is not one.
	//
	// SignatureVerified is **left unset**, which the document renders as an
	// absent field rather than a false one.
	//
	// The tempting shortcut is `SignatureVerified: policy.RequireSignature`,
	// on the reasoning that an operation which completed under that policy
	// must have verified a signature. The reasoning is even sound. It is
	// still the document asserting a check from a *setting* rather than
	// reporting one that happened, which is RFC 0013's defect -- `release
	// verify` printing "bundle is valid" for a bundle that could not render
	// -- reproduced in an artifact that travels to an auditor.
	//
	// And `apply` genuinely cannot establish it: signatures are checked when
	// a release is *staged*, and apply runs against one already on disk. So
	// the honest output for this operation is silence on the question, which
	// is what the absent field means.
	// DigestMatched is left unset for the same reason SignatureVerified is,
	// and it was the same mistake: `rel.Digest != ""` says the release
	// *has* a digest, not that anything compared against it. The comparison
	// happens when a bundle is fetched and staged, and this operation
	// fetched nothing.
	in.Verification = domain.AttestedVerification{
		SignatureRequired: inst.Policy.RequireSignature,
		DigestPinned:      rel.Digest != "",
	}
	return in
}

// attestedImages records what the release said should run, by repository and
// digest.
//
// From the manifest rather than from the runtime, deliberately: the statement
// says what the manager *deployed*, and asking the daemon what is running would
// make the document agree with reality by construction -- which is precisely
// the comparison `--against-live` exists to perform. A statement that recorded
// the running state could never disagree with it, and RFC 0025 decision 8 makes
// the design conditional on that disagreement being possible.
func attestedImages(rel domain.Release) []domain.AttestedImage {
	names := slices.Sorted(maps.Keys(rel.Manifest.Images))

	out := make([]domain.AttestedImage, 0, len(names))
	for _, name := range names {
		spec := rel.Manifest.Images[name]
		digest, _ := spec.Digest()
		out = append(out, domain.AttestedImage{
			Ref:    domain.RepositoryOf(spec.Ref),
			Digest: digest,
			Origin: string(spec.Source()),
		})
	}
	return out
}

// schemeOf pulls the scheme out of a source ref -- "oci", "https", "file" --
// without the rest, which can name a private registry and a path.
func schemeOf(ref string) string {
	if i := strings.Index(ref, "://"); i > 0 {
		return ref[:i]
	}
	return ""
}
