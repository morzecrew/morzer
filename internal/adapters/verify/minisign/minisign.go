// Package minisign implements ports.Verifier for detached minisign signatures.
//
// It answers "did the holder of this key publish this bundle", which is a
// different question from the one the checksum verifier answers and is why they
// compose rather than replace one another. It answers neither "is this bundle
// safe to run" nor "is this vendor trustworthy": a signed bundle still ships
// hooks that execute as root, and signing proves provenance, not intent.
//
// minisign rather than cosign, deliberately. Cosign as a library pulls in a
// transparency-log client, an OIDC flow and a large fraction of the sigstore
// ecosystem, all of which assume infrastructure a self-hosted vendor may not
// have. minisign is an Ed25519 signature over a file and a public key an
// operator can paste into a config. If keyless signing is ever wanted, it
// arrives as a third Verifier behind this same port.
//
// Verification only. The manager never signs -- see RFC 0004 decision 8: the
// signing key belongs in a vendor's release pipeline, and building signing into
// the manager would invite that key onto a deployment host.
package minisign

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	gominisign "github.com/jedisct1/go-minisign"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Name identifies this verifier in journal records and doctor output.
const Name = "minisign"

type Verifier struct{}

func New() *Verifier { return &Verifier{} }

var _ ports.Verifier = (*Verifier)(nil)

func (v *Verifier) Name() string { return Name }

// Verify checks the bundle's detached signature against the configured keys.
//
// The shape of the check is deliberate. The signature covers the bundle's
// SHA256SUMS file; the checksum verifier ties that file to the contents. Two
// small steps, each independently checkable with standard tools, rather than
// one bespoke signature over a tree digest only this program can compute.
func (v *Verifier) Verify(ctx context.Context, bundle ports.BundlePath, expect ports.Expectation) error {
	keys, err := parseKeys(expect.PublicKeys)
	if err != nil {
		return err
	}

	if len(keys) == 0 {
		if expect.Required {
			// Reachable only through a hand-edited installation:
			// Installation.Validate refuses this combination at load,
			// precisely so it does not become a runtime surprise.
			return domain.ValidationError(nil,
				"installation policy requires a signature but configures no signing keys").
				WithHint("add policy.signing_keys to installation.yaml, " +
					"or clear require_signature")
		}
		// No keys means no opinion. The checksum verifier still runs.
		return nil
	}

	sigPath, sumsPath, err := locate(string(bundle), expect.SignaturePath)
	if err != nil {
		return err
	}
	if sigPath == "" {
		if expect.Required {
			return domain.ValidationError(nil,
				"installation policy requires a signature, but this bundle has none").
				WithHint("the bundle should contain %s alongside %s. "+
					"Obtain a signed bundle, or clear require_signature if you "+
					"accept checksum-only verification.",
					ports.SignatureFileName, ports.SumsFileName)
		}
		// Signing keys configured but nothing signed: a warning shape,
		// not a failure, because require_signature is the control that
		// decides whether a signature is mandatory.
		return nil
	}

	signature, err := gominisign.NewSignatureFromFile(sigPath)
	if err != nil {
		return domain.ValidationError(err, "cannot read the signature at %s", sigPath).
			WithHint("the file should be minisign output; check it downloaded completely")
	}

	signed, err := os.ReadFile(sumsPath)
	if err != nil {
		return domain.ValidationError(err,
			"the bundle has a signature but no %s to verify against", ports.SumsFileName).
			WithHint("the signature covers %s; without it there is nothing to check",
				ports.SumsFileName)
	}

	for _, key := range keys {
		ok, verifyErr := key.Verify(signed, signature)
		if ok && verifyErr == nil {
			return nil
		}
	}

	// Deliberately does not say which key failed or how. A signature that
	// does not verify is a signature that does not verify; the operator's
	// next step is the same whichever key it was meant for.
	return domain.ValidationError(domain.ErrValidation,
		"the bundle's signature does not verify against any configured signing key").
		WithHint("this bundle was not signed by a key this installation trusts. " +
			"Do not install it; check where it came from, and check " +
			"policy.signing_keys names the vendor's current key.")
}

// parseKeys turns configured strings into keys, reporting a bad one by
// position.
//
// A malformed key is a configuration error rather than a verification failure:
// the difference matters, because one means "fix installation.yaml" and the
// other means "do not install this bundle".
func parseKeys(raw []string) ([]gominisign.PublicKey, error) {
	out := make([]gominisign.PublicKey, 0, len(raw))
	for i, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		key, err := gominisign.NewPublicKey(s)
		if err != nil {
			return nil, domain.ValidationError(err,
				"policy.signing_keys[%d] is not a valid minisign public key", i).
				WithHint("a minisign public key is a single base64 line, " +
					"as printed by `minisign -G`")
		}
		out = append(out, key)
	}
	return out, nil
}

// locate finds the signature and the file it covers.
//
// An explicit SignaturePath wins, so a caller that fetched a detached signature
// alongside a download can name it. Otherwise the bundle's own files are used,
// which is the case for anything that arrived as a directory or an archive.
func locate(bundle, explicit string) (sigPath, sumsPath string, err error) {
	sumsPath = filepath.Join(bundle, ports.SumsFileName)

	if explicit != "" {
		if _, statErr := os.Stat(explicit); statErr != nil {
			return "", "", domain.ValidationError(statErr,
				"the signature at %s cannot be read", explicit)
		}
		return explicit, sumsPath, nil
	}

	candidate := filepath.Join(bundle, ports.SignatureFileName)
	if _, statErr := os.Stat(candidate); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return "", sumsPath, nil
		}
		return "", "", domain.ValidationError(statErr,
			"cannot read %s", candidate)
	}
	return candidate, sumsPath, nil
}
