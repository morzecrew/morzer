package ops

import (
	"context"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// FetchIntoStore places a verified bundle in the release store without making
// it current.
//
// One implementation for both callers. `release fetch` is an operator typing a
// reference; channel staging is a poll acting on one it resolved itself, and the
// two must land identical bundles under identical rules -- the signature policy
// above all. Two copies of this would drift in the direction they always drift:
// the automated path grows lenient, because that is the one somebody is fighting
// at three in the morning.
//
// The bundle is verified against *this machine's* policy rather than a default.
// A fetch that accepted an unsigned bundle would leave one in the store for
// `update --to` to install later, which is the same compromise arriving by a
// longer route.
func FetchIntoStore(
	ctx context.Context, d *Deps, ref ports.Ref, resolved ports.ResolvedRelease,
) (rel domain.Release, alreadyPresent bool, err error) {
	dest := d.Paths.ReleaseDir(resolved.Version.String())

	// The policy is read before anything is downloaded *or* accepted from
	// the store, and absence is the only policy-free case -- asked about on
	// its own terms rather than inferred from a load that failed. A state
	// file that exists and cannot be parsed says nothing about whether
	// signatures are required, and treating "cannot tell" as "not required"
	// is how a machine with a pinned key ends up admitting a bundle on its
	// content digest alone, which any registry that served those bytes can
	// satisfy.
	expect := ports.Expectation{Digest: resolved.Digest}
	exists, err := d.State.InstallationExists(ctx)
	if err != nil {
		return domain.Release{}, false, err
	}
	if exists {
		inst, err := d.State.LoadInstallation(ctx)
		if err != nil {
			return domain.Release{}, false, err
		}
		expect.Required = inst.Policy.RequireSignature
		expect.PublicKeys = inst.Policy.SigningKeys
	}

	// A version already present with a different digest is a conflict, not
	// something to overwrite: two different bundles claiming one version is
	// exactly what content-addressed identity exists to catch.
	if existing, loadErr := release.Load(dest); loadErr == nil {
		if !atomicfs.SameDigest(existing.Digest, resolved.Digest) {
			return domain.Release{}, false, domain.ValidationError(domain.ErrDigestMismatch,
				"release %s is already installed with a different digest", resolved.Version).
				WithHint("installed %s, incoming %s — these are different bundles "+
					"claiming the same version. A vendor iterating on a release "+
					"should publish a prerelease (1.4.1-dev.7.gabc1234) rather "+
					"than republishing one",
					shortDigest(existing.Digest), shortDigest(resolved.Digest))
		}
		// Checked against *today's* policy, not against whatever was in
		// force when it arrived. A bundle fetched before an installation
		// existed, or before `require_signature` was turned on, is
		// otherwise reported as present and usable by a machine that
		// would now refuse it -- which is how a poll comes to stage a
		// release, `status` to offer it, and `update --check` to print
		// its notes for something the update itself aborts on.
		//
		// Not removed on failure, unlike a bundle this call fetched. The
		// store entry predates the policy that now rejects it; deleting
		// an operator's release because a rule tightened is a decision
		// they get to make.
		if err := d.Verifier.Verify(ctx, ports.BundlePath(dest), expect); err != nil {
			return domain.Release{}, false, err
		}
		return existing, true, nil
	}

	if _, err := d.Source.Fetch(ctx, ref, dest); err != nil {
		return domain.Release{}, false, err
	}

	if err := d.Verifier.Verify(ctx, ports.BundlePath(dest), expect); err != nil {
		// Removed rather than left for someone to notice. A bundle that
		// failed verification sitting in the store is one `update --to`
		// away from being installed, and the operator who runs that
		// command is not the one who saw this error.
		_ = atomicfs.RemoveAll(dest)
		return domain.Release{}, false, err
	}

	rel, err = release.Load(dest)
	if err != nil {
		_ = atomicfs.RemoveAll(dest)
		return domain.Release{}, false, err
	}
	return rel, false, nil
}
