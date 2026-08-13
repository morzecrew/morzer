package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
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
	// No early return on a missing bus. Writing the record is the job;
	// warning about a failure to write it is a courtesy, and making the
	// first conditional on the second would mean a build wired without a
	// bus silently filed nothing.
	// The key that will actually sign, resolved before the document is
	// built so the document names it.
	//
	// Two failures this closes. A machine that reached schema 6 by
	// migration has no key and no operation had ever minted one, so it
	// would have emitted unsigned statements forever -- RFC 0028 §5.6 says
	// the minting step runs for `init` *and* for any operation that needs
	// to sign, and only the first half was wired. And when the key on disk
	// disagreed with the key in state, the document named state's key while
	// the .minisig came from the other: a statement attributable to nobody,
	// which is precisely the case `doctor` exists to report.
	if d.Signer != nil {
		key, err := d.Signer.EnsureKey(ctx)
		switch {
		case err != nil:
			d.warnf("cannot resolve this machine's signing key: %s",
				domain.AsError(err).Message)
		case in.Installation.Signing.HasKey() && in.Installation.Signing.PublicKey != key.Line:
			// State is not silently overwritten here -- that is
			// `init --repair`'s job, and doctor names it. What is
			// refused is emitting a document that lies about which
			// key signed it.
			d.warnf("the signing key on disk is not the one installation state records; "+
				"attesting under the key that signs (%s)", key.KeyID)
			in.Installation.Signing.PublicKey = key.Line
		default:
			in.Installation.Signing.PublicKey = key.Line
		}
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
	names := make([]string, 0, len(rel.Manifest.Images))
	for name := range rel.Manifest.Images {
		names = append(names, name)
	}
	sort.Strings(names)

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
