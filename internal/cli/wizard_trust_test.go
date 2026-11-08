package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	gominisign "github.com/jedisct1/go-minisign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/verify"
	"github.com/morzecrew/morzer/internal/adapters/verify/checksum"
	"github.com/morzecrew/morzer/internal/adapters/verify/minisign"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// keyShippingBundle copies the test bundle, ships a fresh minisign key in it
// and signs it, returning the bundle and the key's id.
func keyShippingBundle(t *testing.T) (dir, id string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "bundle")
	require.NoError(t, atomicfs.CopyTree(testdataBundle(t), dir, atomicfs.DefaultExtractLimits()))

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sk := gominisign.PrivateKey{SignatureAlgorithm: [2]byte{'E', 'd'}, ChecksumAlgorithm: [2]byte{'B', '2'}}
	_, err = rand.Read(sk.KeyId[:])
	require.NoError(t, err)
	copy(sk.SecretKey[:], priv)
	pk := sk.PublicKey()
	blob := append(append(append([]byte{}, pk.SignatureAlgorithm[:]...), pk.KeyId[:]...), pk.PublicKey[:]...)

	require.NoError(t, os.WriteFile(filepath.Join(dir, ports.PublicKeyFileName),
		[]byte("untrusted comment: minisign public key\n"+base64.StdEncoding.EncodeToString(blob)+"\n"), 0o644))
	require.NoError(t, release.WriteSums(dir))
	sig, err := sk.SignFile(filepath.Join(dir, ports.SumsFileName), gominisign.SignOptions{})
	require.NoError(t, err)
	encoded, err := sig.MarshalText()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ports.SignatureFileName), encoded, 0o644))
	return dir, fmt.Sprintf("%016X", binary.LittleEndian.Uint64(sk.KeyId[:]))
}

// The wizard asks before trusting the key a release ships, where the stage
// step would trust one, and shows a local bundle's key by its id (RFC 0052
// decision 10). The answers are the profile, the domains, the recovery choice
// and then the question.
func TestTheWizardAsksBeforeTrustingTheKeyAReleaseShips(t *testing.T) {
	ask := func(t *testing.T, answer string, opts ops.InitOptions) (ops.InitOptions, string, error) {
		t.Helper()
		app, shown := wizardApp(t, "1", "", "3", answer)
		app.Deps.Verifier = verify.NewChain(checksum.New(), minisign.New())
		got, err := runInitWizard(context.Background(), app, opts)
		return got, shown.String(), err
	}

	t.Run("yes trusts the key it showed", func(t *testing.T) {
		bundle, id := keyShippingBundle(t)
		got, shown, err := ask(t, "y", ops.InitOptions{Product: "demo", ReleasePath: bundle})
		require.NoError(t, err)

		assert.Contains(t, shown, "Trust the signing key this release ships, "+id)
		require.NotNil(t, got.ApprovedKey)
		assert.Equal(t, id, got.ApprovedKey.ID)
		assert.NotEmpty(t, got.ApprovedKey.Key)
		assert.False(t, got.SignatureNotRequired)
	})

	t.Run("no installs with checksums only, and says so in the command", func(t *testing.T) {
		bundle, _ := keyShippingBundle(t)
		got, _, err := ask(t, "n", ops.InitOptions{Product: "demo", ReleasePath: bundle})
		require.NoError(t, err)

		assert.True(t, got.SignatureNotRequired)
		assert.Nil(t, got.ApprovedKey)
		assert.Contains(t, EquivalentCommand(got), "--require-signature=false")
	})

	t.Run("no after --require-signature is refused", func(t *testing.T) {
		bundle, _ := keyShippingBundle(t)
		_, _, err := ask(t, "n", ops.InitOptions{Product: "demo", ReleasePath: bundle, RequireSignature: true})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the only key there is was turned down")
	})

	// Nothing to ask, and the stage step is still held to it: a key that
	// appears before staging was never approved.
	t.Run("a local bundle with no key is held to none", func(t *testing.T) {
		app, shown := wizardApp(t, "1", "", "3")
		app.Deps.Verifier = verify.NewChain(checksum.New(), minisign.New())
		got, err := runInitWizard(context.Background(), app, ops.InitOptions{
			Product: "demo", ReleasePath: testdataBundle(t),
		})
		require.NoError(t, err)
		assert.NotContains(t, shown.String(), "Trust the signing key")
		require.NotNil(t, got.ApprovedKey, "the wizard read a bundle with no key and held the stage step to nothing")
		assert.Empty(t, got.ApprovedKey.Key)
	})

	t.Run("not asked after --signing-key", func(t *testing.T) {
		bundle, _ := keyShippingBundle(t)
		app, shown := wizardApp(t, "1", "", "3")
		app.Deps.Verifier = verify.NewChain(checksum.New(), minisign.New())
		_, err := runInitWizard(context.Background(), app, ops.InitOptions{
			Product: "demo", ReleasePath: bundle, SigningKeys: []string{"RWQ…"},
		})
		require.NoError(t, err)
		assert.NotContains(t, shown.String(), "Trust the signing key")
	})
}
