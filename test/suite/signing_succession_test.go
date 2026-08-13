package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// Succession across a real rebuild, through the real export and import.
//
// The domain tests pin the transform, which is a different claim: they prove
// SucceedSigning does the right thing to a value somebody hands it. This proves
// the value actually reaches it -- that the public key rides out in an export,
// that the private key does not, and that the machine on the other side records
// a predecessor instead of claiming a key it cannot use.
//
// That last one is the failure worth the setup. A rebuilt machine that kept the
// dead machine's public key in state would sign with a key it just minted while
// publishing a key it does not hold, which is the disagreement `doctor` refuses
// -- manufactured by the import path itself.

func TestARebuiltMachineRecordsItsPredecessorAndNotItsKey(t *testing.T) {
	ctx := context.Background()

	requireSOPS(t)
	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)

	// A machine that has signed: the key exists, state records it, and it
	// has a salt whose whole purpose is to survive this.
	inst, err := origin.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	originalKey := inst.Signing.PublicKey
	require.NotEmpty(t, originalKey, "init did not record a signing key")
	require.NotEmpty(t, inst.AttestationSalt, "init did not mint an attestation salt")

	exportPath := filepath.Join(t.TempDir(), "demo.export.yaml")
	_, err = ops.Export(ctx, origin.Deps, ops.ExportOptions{Path: exportPath})
	require.NoError(t, err)

	raw, err := os.ReadFile(exportPath)
	require.NoError(t, err)

	// The public half travels, because a verifier following a chain across
	// the rebuild has to find it somewhere.
	assert.Contains(t, string(raw), originalKey,
		"the export does not carry the public signing key, so a predecessor cannot be recorded")

	// The private half does not, and this is the assertion that matters
	// most: an export goes to a bucket, and a signing key inside one signs
	// as the machine for whoever finds it.
	secret, err := os.ReadFile(origin.Paths.SigningKeyFile())
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(secret)), "\n") {
		if strings.HasPrefix(line, "untrusted comment:") {
			continue
		}
		require.NotEmpty(t, line)
		assert.NotContains(t, string(raw), line,
			"the export carries the private signing key")
	}

	require.NoError(t, os.RemoveAll(origin.Root))

	rebuilt := newMachine(t, t.TempDir())
	export, err := ops.LoadExport(exportPath)
	require.NoError(t, err)
	_, err = ops.Import(ctx, rebuilt.Deps, ops.ImportOptions{
		SourcePath:   exportPath,
		Export:       export,
		IdentityFile: recoveryPath,
	})
	require.NoError(t, err)

	got, err := rebuilt.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)

	require.Len(t, got.Signing.PreviousKeys, 1,
		"the rebuilt machine did not record the predecessor it replaced")
	assert.Equal(t, originalKey, got.Signing.PreviousKeys[0].Key)
	assert.Equal(t, domain.RetiredByRebuild, got.Signing.PreviousKeys[0].Reason)
	assert.False(t, got.Signing.PreviousKeys[0].RetiredAt.IsZero(),
		"a predecessor with no retirement time tells an operator nothing about their own timeline")

	assert.NotEqual(t, originalKey, got.Signing.PublicKey,
		"the rebuilt machine claims a key whose private half died with the old one")

	// The salt is carried, not re-minted. A fresh one breaks the
	// configuration-digest chain on exactly the machine whose history most
	// needs to line up.
	assert.Equal(t, inst.AttestationSalt, got.AttestationSalt,
		"the rebuild re-minted the attestation salt and broke its own chain")
}

// The key file's permissions are the security property, so they are asserted
// rather than assumed -- the same way `stepCreateIdentity` asserts the age
// identity's.
func TestTheSigningKeyAndItsDirectoryAreNotReadableByAnybodyElse(t *testing.T) {
	requireSOPS(t)
	_, recoveryPub := generateRecoveryKey(t)
	m := initOriginMachine(t, context.Background(), recoveryPub)

	info, err := os.Stat(m.Paths.SigningKeyFile())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o400), info.Mode().Perm(),
		"the signing key is readable by more than its owner")

	dir, err := os.Stat(m.Paths.SigningDir())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dir.Mode().Perm(),
		"the directory holding the signing key is not 0700")
}
