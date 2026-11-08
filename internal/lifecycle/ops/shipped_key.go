package ops

import (
	"context"
	"os"
	"path/filepath"
	"slices"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
)

// A release may ship its vendor's public key as minisign.pub. An `init` that
// creates an installation, given no key, trusts it once the release's signature
// verifies against it, and requires a signature from then on (RFC 0052). After
// that the key a release ships is never trusted again: `update` verifies
// against the installation's policy, and only names a shipped key it refuses.

// keyTrustedKey is the key a first-use `init` trusted, carried from the step
// that staged the release to the step that records it in the policy.
const keyTrustedKey = "trusted-shipped-key"

// trustedKeySource is what the journal and `--json` say a trusted key came
// from, where a key given with --signing-key says nothing.
const trustedKeySource = "release"

// releaseTrust is what `init`'s stage step checks the release against.
type releaseTrust struct {
	keys     []string
	required bool

	// implied marks a signature required only because a key was given,
	// which a release that is not signed meets as a refusal naming the
	// opt-out (decision 5).
	implied bool

	// repair marks a repair of an installation that exists, whose policy
	// the stage step reads under the lock and verifies against.
	repair bool

	// shipped marks a first use: the key the release ships is trusted, if
	// it ships one (RFC 0052 §5.2).
	shipped bool

	// plan is what a plan of the stage step says about a shipped key, and
	// empty where the step would trust none.
	plan string
}

// initTrust works out what the stage step verifies against. policy is the one
// a repair found, and nil for an `init` that creates the installation.
func initTrust(opts InitOptions, policy *domain.Policy) releaseTrust {
	switch {
	case policy != nil:
		// A repair is not a first use (decision 15). It checks what the
		// installation trusts, and a key it is given beside that, which
		// it does not record.
		return releaseTrust{
			keys:     append(slices.Clone(policy.SigningKeys), opts.SigningKeys...),
			required: policy.RequireSignature || opts.RequireSignature,
		}
	case len(opts.SigningKeys) > 0:
		// The given key wins over a shipped one (decision 4), and a
		// trusted key means a required signature unless the operator
		// said otherwise (decision 5).
		return releaseTrust{
			keys:     opts.SigningKeys,
			required: !opts.SignatureNotRequired,
			implied:  !opts.SignatureNotRequired && !opts.RequireSignature,
		}
	default:
		return releaseTrust{required: opts.RequireSignature, shipped: !opts.SignatureNotRequired}
	}
}

// shippedKey reads the key a bundle ships and checks the bundle's signature
// against it. None when the bundle ships none, or the verifier cannot read one.
func (d *Deps) shippedKey(bundle string) (ports.ShippedKey, error) {
	reader, ok := d.Verifier.(ports.ShippedKeyReader)
	if !ok {
		return ports.ShippedKey{}, nil
	}
	return reader.ShippedKey(ports.BundlePath(bundle))
}

// LocalShippedKey reads the key a bundle on this machine ships, for the wizard
// and the plan, neither of which stages it. A directory is read where it is,
// and an archive is extracted to a temporary directory, because the key is not
// its first entry. A remote reference is not fetched, and local is false.
func (d *Deps) LocalShippedKey(ctx context.Context, releasePath string) (key ports.ShippedKey, local bool, err error) {
	ref, err := ports.ParseRef(releasePath)
	if err != nil || !ref.IsLocal() {
		return ports.ShippedKey{}, false, err
	}
	if info, statErr := os.Stat(ref.Location); statErr == nil && info.IsDir() {
		key, err := d.shippedKey(ref.Location)
		return key, true, err
	}

	dir, err := os.MkdirTemp("", "morzer-key-")
	if err != nil {
		return ports.ShippedKey{}, true, domain.Internal(err,
			"cannot create a temporary directory to read the bundle")
	}
	defer func() { _ = os.RemoveAll(dir) }()
	bundle, err := d.Source.Fetch(ctx, ref, dir)
	if err != nil {
		return ports.ShippedKey{}, true, err
	}
	key, err = d.shippedKey(bundle.String())
	return key, true, err
}

// checkFirstUseKey refuses a shipped key a first use cannot trust: one other
// than the key the wizard showed (decision 10), or none after an explicit
// --require-signature (decision 6).
func checkFirstUseKey(shipped ports.ShippedKey, opts InitOptions) error {
	if approved := opts.ApprovedKey; approved != nil && shipped.Key != approved.Key {
		const hint = "the bundle changed after it was read for the question: " +
			"check where it came from before running `morzer init` again"
		if approved.Key == "" {
			return domain.ValidationError(domain.ErrValidation,
				"the release ships a key, %s, and shipped none when it was read for the question",
				shipped.ID).WithHint(hint)
		}
		return domain.ValidationError(domain.ErrValidation,
			"the release does not ship the key you approved, %s", approved.ID).WithHint(hint)
	}
	if shipped.Key == "" && opts.RequireSignature {
		return domain.Usage(
			"--require-signature was given, but the release ships no key to check its signature against").
			WithHint("pass --signing-key with the vendor's minisign public key")
	}
	return nil
}

// noteFirstUse records the key a first use trusted, or warns that it trusted
// none (decision 12): a release that ships no key still installs, and the
// operator is told what that leaves unchecked.
func (d *Deps) noteFirstUse(st *engine.State, bundle string, version domain.Version, shipped ports.ShippedKey) {
	if shipped.Key != "" {
		st.Set(keyTrustedKey, shipped)
		st.SetFlag("trusted_key_id", shipped.ID)
		st.SetFlag("trusted_key_source", trustedKeySource)
		return
	}
	if isSigned(bundle) {
		st.Warn("release %s is signed but ships no key, so its signature was not checked: "+
			"pass --signing-key with the vendor's public key to check it", version)
		return
	}
	st.Warn("release %s is not signed, so this installation trusts no signing key "+
		"and will not check the releases after it either", version)
}

// isSigned reports whether a bundle carries a signature, checked or not.
func isSigned(bundle string) bool {
	_, err := os.Stat(filepath.Join(bundle, ports.SignatureFileName))
	return err == nil
}

// refusedForShippedKey names the key a refused release ships, when that key
// signed it and the installation does not trust it (decision 8). Its id only:
// the full key in a refusal would make trusting a hijacked one a copy-paste.
func (d *Deps) refusedForShippedKey(source domain.Release, policy domain.Policy, refusal error) error {
	if len(policy.SigningKeys) == 0 {
		return refusal
	}
	shipped, err := d.shippedKey(source.Root)
	if err != nil || shipped.Key == "" || slices.ContainsFunc(policy.SigningKeys, sameKey(shipped.Key)) {
		return refusal
	}
	return domain.ValidationError(domain.ErrValidation,
		"release %s is signed by the key it ships, %s, which this installation does not trust",
		source.Version(), shipped.ID).
		WithHint("compare %s with the key the vendor publishes. If they moved to a new key, "+
			"trust it as they publish it, with `morzer release trust add <key>`, and update again; "+
			"otherwise do not install this release", shipped.ID)
}

// noteShippedKey tells an installation that trusts no key about the one a
// release ships, and trusts nothing itself (decision 13): a timer adopting
// whatever a moved tag ships would pin an attacker's key without anyone looking.
func (d *Deps) noteShippedKey(st *engine.State, source domain.Release) {
	shipped, err := d.shippedKey(source.Root)
	if err != nil || shipped.Key == "" {
		return
	}
	st.Warn("release %s ships a signing key, %s, and this installation trusts none, "+
		"so its signature was not checked. To check every release from now on, trust "+
		"the key the vendor publishes with `morzer release trust add <key>`, then run "+
		"`morzer config set release.require_signature=true`", source.Version(), shipped.ID)
}
