package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/source"
	"github.com/morzecrew/morzer/internal/adapters/source/local"
	"github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// followsAChannelToo has the rig's machine follow a channel with
// update.auto_apply on, as well as its authority. The channel's 1.3.0 is a
// release the unattended gate admits, and lets the same names be managed.
func (r *syncRig) followsAChannelToo(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	staged := filepath.Join(t.TempDir(), "bundle-1.3.0")
	copyBundle(t, filepath.Join(testBundlePath(t), "..", "bundle-1.3.0"), staged)
	retargetManifest(t, staged, r.h.Root)
	for _, edit := range [][3]string{
		{"manifest.yaml", `min_manager_version: "0.0.0"`, `min_manager_version: "0.16.0"`},
		{"manifest.yaml", "database_schema_produces: 14", "database_schema_produces: 12"},
		{"hooks/migrate", "target=14", "target=12"},
	} {
		path := filepath.Join(staged, edit[0])
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		patched := strings.Replace(string(raw), edit[1], edit[2], 1)
		require.NotEqual(t, string(raw), patched, "%s no longer says %q", edit[0], edit[1])
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, []byte(patched), info.Mode().Perm()))
	}
	path := filepath.Join(staged, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(raw,
		"\nmanaged:\n  parameters: [log_level]\n  secrets: [db_password]\n  settings: [installation.domains]\n"...), 0o644))

	archivePath := filepath.Join(t.TempDir(), "bundle.tar.zst")
	writeTarZst(t, staged, archivePath)
	archive, err := os.ReadFile(archivePath)
	require.NoError(t, err)
	srv := newFakeRegistry(t, archive, oci.MediaType, 0)
	sources, err := source.NewRegistry(local.New(), newTestSource(t, srv))
	require.NoError(t, err)
	r.h.Deps.Source = sources

	inst := installation(t, r.h)
	inst.Update.Check, inst.Update.AutoApply = true, true
	inst.Update.Channel = ociRef(srv, "demo/bundle", "stable").String()
	inst.Policy.RequireSignature = true
	inst.Policy.SigningKeys = []string{"RWQfakekey"}
	require.NoError(t, r.h.Deps.State.SaveInstallation(ctx, inst))
}

// whileLocked runs fn once, the first time the runtime is called while the
// deployment lock is held: inside another operation, where a timer's tick
// would land.
func (r *syncRig) whileLocked(fn func()) {
	r.h.Runtime.OnCall = func(string) {
		if _, held, _ := r.h.Locker.Owner(context.Background(), "deployment"); held {
			r.h.Runtime.OnCall = nil
			fn()
		}
	}
}

// The sync timer and an unattended update contend for the deployment lock
// (RFC 0057 §6). Whichever ticks while the other holds it gives way, records
// nothing and fails nothing, and both complete on their next tick.
func TestASyncAndAnUnattendedUpdateBothCompleteAcrossTicks(t *testing.T) {
	ctx := context.Background()
	r := newSyncRig(t)
	r.followsAChannelToo(t)

	// An update's tick lands inside the sync's operation.
	r.publish(t, 10, "parameters:\n  log_level: debug\n")
	var updateErr error
	ticked := false
	r.whileLocked(func() {
		ticked = true
		_, updateErr = ops.RunUnattended(ctx, r.h.Deps, ops.UnattendedOptions{})
	})
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)
	require.True(t, ticked, "the update's tick never landed inside the sync")
	assert.ErrorIs(t, updateErr, domain.ErrLocked)
	candidate, err := r.h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	assert.Empty(t, candidate.Failed, "an update that gave way was recorded as failed")

	// A sync's tick lands inside the update's install.
	r.publish(t, 20, "parameters:\n  log_level: warn\n")
	var syncErr error
	ticked = false
	r.whileLocked(func() {
		ticked = true
		_, syncErr = r.sync(t)
	})
	res, err := ops.RunUnattended(ctx, r.h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)
	require.True(t, res.Applied, "refused: %s", res.Assessment.Why())
	require.True(t, ticked, "the sync's tick never landed inside the update")
	assert.ErrorIs(t, syncErr, domain.ErrLocked)
	state := r.state(t)
	require.NotNil(t, state.Applied)
	assert.Equal(t, int64(10), state.Applied.Serial, "the sync that gave way applied anyway")
	assert.Nil(t, state.Refused, "a sync that gave way refused the document")

	// The next tick of each completes.
	assert.Equal(t, domain.SyncOK, r.syncOK(t).Result)
	assert.Equal(t, int64(20), r.state(t).Applied.Serial)
	current, err := r.h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String())
	assert.Equal(t, "warn", installation(t, r.h).Parameters["log_level"])
}
