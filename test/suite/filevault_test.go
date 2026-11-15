package suite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
)

// The FileVault gate (RFC 0055 decision 5): on a Mac, decrypted secrets land
// on a disk, and they render only while FileVault encrypts it or the
// installation set secrets.allow_unencrypted_disk.

// withFileVault makes h a machine whose disk is checked, as a Mac's is, with
// FileVault as *on says, and gives the store the gate root.go gives it.
func withFileVault(h *harness, on *bool) {
	h.Deps.DiskEncrypted = func(context.Context) (bool, error) { return *on, nil }
	h.Secrets.RenderGate = h.Deps.RefuseUnencryptedDisk
}

// Every render refuses while FileVault is off, until the setting allows it,
// which is a journalled downgrade; doctor fails either way.
func TestFileVaultOffRefusesEveryRenderUntilAllowed(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	off := false
	withFileVault(h, &off)

	_, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FileVault is off, and decrypted secrets would be written to an unencrypted disk")
	assert.Contains(t, domain.AsError(err).Hint, "secrets.allow_unencrypted_disk=true")
	assert.Empty(t, h.Runtime.UpOptions)
	assert.NoFileExists(t, filepath.Join(h.Paths.SecretsRenderDir(), "db_password"))
	check := doctorCheck(t, h, "secrets.disk-encryption")
	assert.Equal(t, events.CheckFail, check.Status)
	assert.Contains(t, check.Message, "secrets do not render until it is on")

	_, err = setSetting(t, h, false, map[string]string{"secrets.allow_unencrypted_disk": "true"})
	require.NoError(t, err)
	records := configRecords(t, h)
	require.NotEmpty(t, records, "allowing an unencrypted disk was not journalled")
	assert.Equal(t, "true", records[len(records)-1].Flags["secrets.allow_unencrypted_disk"])

	_, err = ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(h.Paths.SecretsRenderDir(), "db_password"))
	check = doctorCheck(t, h, "secrets.disk-encryption")
	assert.Equal(t, events.CheckFail, check.Status, "doctor stopped failing because the setting allows it")
	assert.Contains(t, check.Message, "secrets.allow_unencrypted_disk lets secrets render on it")
	assert.Contains(t, check.Remedy, "treat the age identity and every secret as exposed")

	on := true
	withFileVault(h, &on)
	assert.Equal(t, events.CheckOK, doctorCheck(t, h, "secrets.disk-encryption").Status)
	// Where the test's own directory is memory-backed, the check passes
	// before any wording applies.
	if !preflight.IsEphemeralFilesystem(preflight.FilesystemType(h.Paths.SecretsRenderDir())) {
		ephemeral := doctorCheck(t, h, "secrets.ephemeral-storage")
		assert.NotContains(t, ephemeral.Remedy, "mount a tmpfs", "a Mac was told to mount a tmpfs")
		assert.Contains(t, ephemeral.Remedy, "no memory-backed filesystem")
	}
}

// init refuses before it writes anything, so no identity is created on an
// unencrypted disk; --allow-unencrypted-disk records the setting.
func TestInitRefusesAnUnencryptedDiskBeforeItWritesAnything(t *testing.T) {
	ctx := context.Background()
	fresh := func(identity bool) *harness {
		h := newHarness(t)
		h.setHookEnv()
		if identity {
			realIdentity(t, h)
		}
		off := false
		withFileVault(h, &off)
		return h
	}
	opts := ops.InitOptions{Product: "demo", NoRecoveryKey: true, GenerateSecrets: true}

	refused := fresh(false)
	_, err := ops.Init(ctx, refused.Deps, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FileVault is off")
	assert.Equal(t, domain.ExitSecrets, domain.ExitCode(err))
	assert.NoFileExists(t, refused.Paths.AgeIdentityFile())
	exists, err := refused.Deps.State.InstallationExists(ctx)
	require.NoError(t, err)
	assert.False(t, exists)

	allowed := fresh(true)
	opts.AllowUnencryptedDisk = true
	result, err := ops.Init(ctx, allowed.Deps, opts)
	require.NoError(t, err)
	assert.True(t, installation(t, allowed).Policy.AllowUnencryptedDisk)
	assert.Equal(t, "true", result.Record.Flags["allow_unencrypted_disk"])
}

// A secret change that stores before it renders asks first, so a refused one
// leaves the stored value as it was: config apply here, and the secret
// commands through the same function.
func TestASecretChangeIsRefusedBeforeItStores(t *testing.T) {
	h := runningHarness(t)
	off := false
	withFileVault(h, &off)
	before, err := h.Secrets.Load(context.Background())
	require.NoError(t, err)
	was, _ := before.Get("smtp_password")

	_, err = configApply(t, h, "secrets:\n  smtp_password: a-value-on-an-unencrypted-disk\n", ops.Options{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "FileVault is off")
	after, err := h.Secrets.Load(context.Background())
	require.NoError(t, err)
	now, _ := after.Get("smtp_password")
	assert.Equal(t, was.Reveal(), now.Reveal(), "a refused change stored its value")
	assert.Empty(t, configRecords(t, h), "a refused change started an operation")
	require.ErrorContains(t, h.Deps.RefuseUnencryptedDisk(context.Background()), "FileVault is off")
}
