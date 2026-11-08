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
	"encoding/binary"
	"errors"
	"fmt"
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
	_, err := v.VerifySigned(ctx, bundle, expect)
	return err
}

var _ ports.SignatureReporter = (*Verifier)(nil)

// VerifySigned is Verify, naming the key the signature verified against, or ""
// when it had no keys to check against or the bundle carried no signature it
// needed.
func (v *Verifier) VerifySigned(ctx context.Context, bundle ports.BundlePath, expect ports.Expectation) (string, error) {
	keys, err := parseKeys(expect.PublicKeys)
	if err != nil {
		return "", err
	}

	if len(keys) == 0 {
		if expect.Required {
			// Reachable only through a hand-edited installation:
			// Installation.Validate refuses this combination at load,
			// precisely so it does not become a runtime surprise.
			return "", domain.ValidationError(nil,
				"installation policy requires a signature but configures no signing keys").
				WithHint("trust one with `morzer release trust add <key>`; " +
					"installation.yaml is a report, and editing it changes nothing")
		}
		// No keys means no opinion. The checksum verifier still runs.
		return "", nil
	}

	sigPath, sumsPath, err := locate(string(bundle), expect.SignaturePath)
	if err != nil {
		return "", err
	}
	if sigPath == "" {
		if expect.Required && expect.Authoring {
			sign := fmt.Sprintf("sign it with `minisign -Sm %s`", sumsPath)
			// SHA256SUMS is optional, and minisign cannot sign a file that
			// is not there.
			if _, statErr := os.Stat(sumsPath); statErr != nil {
				sign = fmt.Sprintf("write %s with `morzer release build %s`, then %s",
					ports.SumsFileName, string(bundle), sign)
			}
			return "", domain.ValidationError(nil,
				"--signing-key was given, but this bundle is not signed: it has no %s",
				ports.SignatureFileName).
				WithHint("%s, or leave out --signing-key to check the checksums only", sign)
		}
		if expect.Required {
			return "", domain.ValidationError(nil,
				"installation policy requires a signature, but this bundle has none").
				WithHint("the bundle should contain %s alongside %s. "+
					"Obtain a signed bundle, or clear require_signature if you "+
					"accept checksum-only verification.",
					ports.SignatureFileName, ports.SumsFileName)
		}
		// Signing keys configured but nothing signed: a warning shape,
		// not a failure, because require_signature is the control that
		// decides whether a signature is mandatory.
		return "", nil
	}

	signer, err := signedBy(keys, sigPath, sumsPath)
	if err != nil || signer != "" {
		return signer, err
	}

	// Deliberately does not say which key failed or how. A signature that
	// does not verify is a signature that does not verify; the operator's
	// next step is the same whichever key it was meant for.
	return "", domain.ValidationError(domain.ErrValidation,
		"the bundle's signature does not verify against any configured signing key").
		WithHint("this bundle was not signed by a key this installation trusts. " +
			"Do not install it; check where it came from, and check " +
			"policy.signing_keys names the vendor's current key.")
}

// signedBy returns the id of the key, among keys, that the signature at sigPath
// verifies against over sumsPath, or "" when it verifies against none of them.
func signedBy(keys []gominisign.PublicKey, sigPath, sumsPath string) (string, error) {
	signature, err := gominisign.NewSignatureFromFile(sigPath)
	if err != nil {
		return "", domain.ValidationError(err, "cannot read the signature at %s", sigPath).
			WithHint("the file should be minisign output; check it downloaded completely")
	}

	signed, err := os.ReadFile(sumsPath)
	if err != nil {
		return "", domain.ValidationError(err,
			"the bundle has a signature but no %s to verify against", ports.SumsFileName).
			WithHint("the signature covers %s; without it there is nothing to check",
				ports.SumsFileName)
	}

	for _, key := range keys {
		ok, verifyErr := key.Verify(signed, signature)
		if ok && verifyErr == nil {
			return keyID(key), nil
		}
	}
	return "", nil
}

var _ ports.ShippedKeyReader = (*Verifier)(nil)

// ShippedKey reads the key a bundle ships and checks the bundle's signature
// against it.
//
// A bundle that ships a key declares that it is signed, so one with no
// signature is refused rather than read as unsigned: either nobody signed it,
// or the signature was removed on the way.
func (v *Verifier) ShippedKey(bundle ports.BundlePath) (ports.ShippedKey, error) {
	line, key, err := readKeyFile(filepath.Join(string(bundle), ports.PublicKeyFileName))
	if err != nil || line == "" {
		return ports.ShippedKey{}, err
	}
	shipped := ports.ShippedKey{Key: line, ID: keyID(key)}

	sigPath, sumsPath, err := locate(string(bundle), "")
	if err != nil {
		return ports.ShippedKey{}, err
	}
	if sigPath == "" {
		return ports.ShippedKey{}, domain.ValidationError(nil,
			"the bundle ships its key, %s in %s, but is not signed: it has no %s",
			shipped.ID, ports.PublicKeyFileName, ports.SignatureFileName).
			WithHint("a bundle that ships its key declares that it is signed: " +
				"either it was never signed, or its signature was removed")
	}

	signer, err := signedBy([]gominisign.PublicKey{key}, sigPath, sumsPath)
	if err != nil {
		return ports.ShippedKey{}, err
	}
	if signer == "" {
		return ports.ShippedKey{}, domain.ValidationError(domain.ErrValidation,
			"the bundle's signature does not verify against the key it ships, %s in %s",
			shipped.ID, ports.PublicKeyFileName).
			WithHint("%s must be the public half of the key that signed %s: "+
				"either it is stale, or someone else signed the bundle",
				ports.PublicKeyFileName, ports.SumsFileName)
	}
	return shipped, nil
}

// maxKeyFileSize bounds the read of a key file a bundle ships, which is not
// trusted yet. `minisign -G` writes about a hundred bytes.
const maxKeyFileSize = 4 << 10

// readKeyFile reads a public key file, returning "" and no error when there is
// none.
//
// It takes the file `minisign -G` writes and nothing looser, an `untrusted
// comment:` line and then the key, which is also what `minisign -Vm -p` reads.
// The key line comes back as written, for a caller that records it.
func readKeyFile(path string) (string, gominisign.PublicKey, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", gominisign.PublicKey{}, nil
	}
	if err != nil {
		return "", gominisign.PublicKey{}, domain.ValidationError(err,
			"cannot read %s", ports.PublicKeyFileName)
	}
	if !info.Mode().IsRegular() || info.Size() > maxKeyFileSize {
		return "", gominisign.PublicKey{}, invalidKeyFile(nil)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", gominisign.PublicKey{}, domain.ValidationError(err,
			"cannot read %s", ports.PublicKeyFileName)
	}

	lines := strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "untrusted comment:") {
		return "", gominisign.PublicKey{}, invalidKeyFile(nil)
	}
	line := strings.TrimSpace(lines[1])
	key, err := gominisign.NewPublicKey(line)
	if err != nil {
		return "", gominisign.PublicKey{}, invalidKeyFile(err)
	}
	return line, key, nil
}

func invalidKeyFile(cause error) error {
	return domain.ValidationError(cause,
		"the bundle's %s is not a minisign public key file", ports.PublicKeyFileName).
		WithHint("it should be the file `minisign -G` writes: " +
			"an `untrusted comment:` line, then the key")
}

var _ ports.KeyChecker = (*Verifier)(nil)

// CheckKey parses one key with the parser verification uses, so a key accepted
// here is one Verify can use.
func (v *Verifier) CheckKey(key string) error {
	if _, err := gominisign.NewPublicKey(strings.TrimSpace(key)); err != nil {
		return domain.ValidationError(err, "%q is not a valid minisign public key", key).
			WithHint("a minisign public key is a single base64 line, " +
				"as printed by `minisign -G`")
	}
	return nil
}

// keyID renders a key id the way minisign prints it: the eight id bytes read as
// a little-endian integer, in uppercase hex. The signer adapter prints its own
// keys the same way, so the two can be matched by eye.
func keyID(key gominisign.PublicKey) string {
	return fmt.Sprintf("%016X", binary.LittleEndian.Uint64(key.KeyId[:]))
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
