package contract

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// BackupTargetHarness is one adapter under test, plus enough to see what it
// actually did.
type BackupTargetHarness struct {
	Target ports.BackupTarget

	// Ref points somewhere with no backups in it; the suite pushes its own.
	Ref ports.TargetRef

	// Keys lists the raw objects on the target.
	//
	// The suite needs this because Push and Fetch are not inverses: Fetch is
	// driven by the manifest, so a Push that uploaded something the manifest
	// does not name would be invisible to any assertion made by fetching.
	// The gap between the two is exactly where a plaintext database dump
	// would hide, so the suite looks at the target directly.
	Keys func() []string
}

// BackupTargetFactory builds a harness over an empty target.
//
// The same indirection as ReleaseSourceFactory, for the same reason: every
// adapter is handed the same backup and must answer the same questions about
// it. A transport that dropped a component, reordered the upload, or changed a
// byte would fail here rather than during a restore on a machine where the
// original no longer exists.
type BackupTargetFactory func(t *testing.T) BackupTargetHarness

// RunBackupTargetSuite runs every BackupTarget contract test.
func RunBackupTargetSuite(t *testing.T, newTarget BackupTargetFactory) {
	t.Helper()

	t.Run("declares at least one scheme", func(t *testing.T) {
		schemes := newTarget(t).Target.Schemes()
		require.NotEmpty(t, schemes,
			"a target that declares no scheme can never be selected by a registry")
		for _, s := range schemes {
			assert.NotEmpty(t, s, "an empty scheme string would match a malformed URL")
		}
	})

	t.Run("an empty target lists nothing rather than failing", func(t *testing.T) {
		h := newTarget(t)
		target, ref := h.Target, h.Ref

		manifests, err := target.List(context.Background(), ref)
		require.NoError(t, err,
			"a target with no backups yet is the state before the first push; "+
				"`backup list --remote` has to be able to answer it")
		assert.Empty(t, manifests)
	})

	t.Run("a pushed backup comes back byte for byte", func(t *testing.T) {
		h := newTarget(t)
		target, ref := h.Target, h.Ref
		ctx := context.Background()

		local := writeBackup(t, "20260101T000000Z", map[string]string{
			"database.sql.age":   "not really age, but it is bytes and they must survive",
			"secrets.sops.yaml":  "sops: encrypted",
			"nested/artifact.gz": "a hook artifact in a subdirectory",
		})

		remote, err := target.Push(ctx, ref, local, "20260101T000000Z")
		require.NoError(t, err)
		assert.Equal(t, "20260101T000000Z", remote.ID)

		back := filepath.Join(t.TempDir(), "fetched")
		require.NoError(t, target.Fetch(ctx, remote, back))

		// This is the assertion the whole port rests on. A backup that
		// came back different from what went out is not a backup, and
		// the moment anyone finds out is during a restore.
		assertSameTree(t, local, back)
	})

	t.Run("list reports the manifest of every backup, newest first", func(t *testing.T) {
		h := newTarget(t)
		target, ref := h.Target, h.Ref
		ctx := context.Background()

		for _, id := range []string{"20260101T000000Z", "20260103T000000Z", "20260102T000000Z"} {
			local := writeBackup(t, id, map[string]string{"database.sql.age": "data " + id})
			_, err := target.Push(ctx, ref, local, id)
			require.NoError(t, err)
		}

		manifests, err := target.List(ctx, ref)
		require.NoError(t, err)
		require.Len(t, manifests, 3)

		ids := []string{manifests[0].ID, manifests[1].ID, manifests[2].ID}
		assert.Equal(t, []string{"20260103T000000Z", "20260102T000000Z", "20260101T000000Z"}, ids,
			"newest first, because that is the order `backup list` prints and the "+
				"order retention walks")

		// The manifest is what makes a listing useful without a key: an
		// operator staring at a bucket has to be able to tell which
		// backup is which.
		assert.NotEmpty(t, manifests[0].Product)
		assert.False(t, manifests[0].CreatedAt.IsZero())
	})

	t.Run("pushing the same backup twice leaves one copy", func(t *testing.T) {
		h := newTarget(t)
		target, ref := h.Target, h.Ref
		ctx := context.Background()

		local := writeBackup(t, "20260101T000000Z", map[string]string{"database.sql.age": "data"})

		_, err := target.Push(ctx, ref, local, "20260101T000000Z")
		require.NoError(t, err)
		_, err = target.Push(ctx, ref, local, "20260101T000000Z")
		require.NoError(t, err, "a re-push is the documented remedy for a push that failed halfway")

		manifests, err := target.List(ctx, ref)
		require.NoError(t, err)
		assert.Len(t, manifests, 1,
			"a second copy under a second name would make retention count wrong, and "+
				"retention deciding what to delete on a wrong count is how the last "+
				"good backup goes")
	})

	t.Run("verify reads the backup back and checks it", func(t *testing.T) {
		h := newTarget(t)
		target, ref := h.Target, h.Ref
		ctx := context.Background()

		local := writeBackup(t, "20260101T000000Z", map[string]string{
			"database.sql.age":   "ciphertext",
			"nested/artifact.gz": "more ciphertext",
		})
		remote, err := target.Push(ctx, ref, local, "20260101T000000Z")
		require.NoError(t, err)

		require.NoError(t, target.Verify(ctx, remote),
			"a backup that was just pushed must verify against its own manifest")

		// Verifying something that is not there is not-found rather than a
		// pass: a scheduled verification that reports success against an
		// empty target is worse than one that does not run.
		err = target.Verify(ctx, ports.RemoteRef{Target: ref, ID: "20991231T235959Z"})
		require.Error(t, err)
		assert.True(t, errors.Is(err, domain.ErrNotFound), "got: %v", err)
	})

	t.Run("verify notices a component that is gone", func(t *testing.T) {
		h := newTarget(t)
		target, ref := h.Target, h.Ref
		ctx := context.Background()

		local := writeBackup(t, "20260101T000000Z", map[string]string{
			"database.sql.age": "ciphertext",
		})
		remote, err := target.Push(ctx, ref, local, "20260101T000000Z")
		require.NoError(t, err)

		// Pushing a second backup under the same id, with a manifest naming
		// a component that was never uploaded, is the shape a partially
		// deleted or partially rotted backup has.
		damaged := writeBackup(t, "20260101T000000Z", map[string]string{
			"database.sql.age": "ciphertext",
			"absent.age":       "this one is removed before the push",
		})
		require.NoError(t, os.Remove(filepath.Join(damaged, "absent.age")))
		// Push refuses it outright, which is the first line of defence.
		_, err = target.Push(ctx, ref, damaged, "20260101T000000Z")
		require.Error(t, err, "a backup naming a component that is not there was pushed")

		// And the good one still verifies, so the failed push did not
		// damage what was already there.
		require.NoError(t, target.Verify(ctx, remote))
	})

	t.Run("removing one backup leaves the others", func(t *testing.T) {
		h := newTarget(t)
		target, ref := h.Target, h.Ref
		ctx := context.Background()

		for _, id := range []string{"20260101T000000Z", "20260102T000000Z"} {
			local := writeBackup(t, id, map[string]string{"database.sql.age": "data " + id})
			_, err := target.Push(ctx, ref, local, id)
			require.NoError(t, err)
		}

		require.NoError(t, target.Remove(ctx,
			ports.RemoteRef{Target: ref, ID: "20260101T000000Z"}))

		manifests, err := target.List(ctx, ref)
		require.NoError(t, err)
		require.Len(t, manifests, 1,
			"retention removes one backup at a time; a Remove that took its "+
				"neighbours with it would empty a target on the first prune")
		assert.Equal(t, "20260102T000000Z", manifests[0].ID)

		// And what survived is still whole, not just still listed.
		back := filepath.Join(t.TempDir(), "fetched")
		require.NoError(t, target.Fetch(ctx,
			ports.RemoteRef{Target: ref, ID: "20260102T000000Z"}, back))
	})

	t.Run("removing a backup does not take a neighbour whose id shares its prefix", func(t *testing.T) {
		h := newTarget(t)
		target, ref := h.Target, h.Ref
		ctx := context.Background()

		// Ids this manager writes are fixed-length timestamps, so this
		// cannot arise from its own output. It can arise from a manifest
		// on the target -- which is a file this manager may not have
		// written, and which is what Remove is driven by. A listing
		// prefix is a string match, not a path one.
		for _, id := range []string{"20260101T000000Z", "20260101T000000Z-copy"} {
			local := writeBackup(t, "20260101T000000Z", map[string]string{"database.sql.age": id})
			_, err := target.Push(ctx, ref, local, id)
			require.NoError(t, err)
		}

		require.NoError(t, target.Remove(ctx,
			ports.RemoteRef{Target: ref, ID: "20260101T000000Z"}))

		back := filepath.Join(t.TempDir(), "fetched")
		require.NoError(t, target.Fetch(ctx,
			ports.RemoteRef{Target: ref, ID: "20260101T000000Z-copy"}, back),
			"removing one backup deleted a component of another whose id merely "+
				"started with the same characters")
	})

	t.Run("remove deletes a backup and is safe to repeat", func(t *testing.T) {
		h := newTarget(t)
		target, ref := h.Target, h.Ref
		ctx := context.Background()

		local := writeBackup(t, "20260101T000000Z", map[string]string{
			"database.sql.age":   "data",
			"nested/artifact.gz": "more data",
		})
		remote, err := target.Push(ctx, ref, local, "20260101T000000Z")
		require.NoError(t, err)

		require.NoError(t, target.Remove(ctx, remote))
		require.NoError(t, target.Remove(ctx, remote),
			"removal is retried after a partial failure, so removing what is already "+
				"gone must succeed rather than fail the retention pass")

		manifests, err := target.List(ctx, ref)
		require.NoError(t, err)
		assert.Empty(t, manifests)
	})

	t.Run("fetching a backup that is not there is not found", func(t *testing.T) {
		h := newTarget(t)
		target, ref := h.Target, h.Ref

		err := target.Fetch(context.Background(),
			ports.RemoteRef{Target: ref, ID: "20991231T235959Z"},
			filepath.Join(t.TempDir(), "nothing"))
		require.Error(t, err, "a fetch of nothing must be an error, not an empty success")
		assert.True(t, errors.Is(err, domain.ErrNotFound),
			"the refusal must be not-found, so a caller can tell it from an "+
				"unreachable target: one means look somewhere else, the other "+
				"means fix the network. got: %v", err)
	})

	t.Run("pushing something that is not a backup is refused", func(t *testing.T) {
		h := newTarget(t)
		target, ref := h.Target, h.Ref

		// A directory with no manifest. Pushing it would put something
		// on the target that List cannot describe and restore cannot
		// use.
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x"), 0o600))

		_, err := target.Push(context.Background(), ref, dir, "20260101T000000Z")
		require.Error(t, err)
	})

	t.Run("only what the manifest names is pushed", func(t *testing.T) {
		h := newTarget(t)
		target, ref := h.Target, h.Ref
		ctx := context.Background()

		local := writeBackup(t, "20260101T000000Z", map[string]string{"database.sql.age": "ciphertext"})

		// An interrupted restore leaves exactly this: a staging
		// directory of *decrypted* components beside the encrypted ones.
		// A push that copied everything it found would carry a plaintext
		// database dump to a second machine, which is the one thing
		// encrypting backups was for.
		staging := filepath.Join(local, ".restore-abc123")
		require.NoError(t, os.MkdirAll(staging, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(staging, "database.sql"),
			[]byte("PLAINTEXT DUMP"), 0o600))

		_, err := target.Push(ctx, ref, local, "20260101T000000Z")
		require.NoError(t, err)

		// Asserted against the target itself rather than against a fetch.
		// Fetch is manifest-driven too, so it would bring back exactly the
		// components whatever Push uploaded -- and the plaintext would sit
		// on the target unnoticed by any test that only round-trips.
		keys := h.Keys()
		require.NotEmpty(t, keys, "the harness reported nothing on the target after a push")
		for _, key := range keys {
			assert.NotContains(t, key, ".restore-",
				"a plaintext dump left by an interrupted restore reached the target: %s", key)
			assert.False(t, strings.HasSuffix(key, "database.sql"),
				"an unencrypted component reached the target: %s", key)
		}
	})
}

// writeBackup builds a backup directory the way hookbackup does: components,
// plus the manifest that names them.
func writeBackup(t *testing.T, id string, files map[string]string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), id)
	require.NoError(t, os.MkdirAll(dir, 0o700))

	manifest := ports.BackupManifest{
		SchemaVersion:  2,
		ID:             id,
		InstallationID: "inst_contract",
		Product:        "demo",
		ReleaseVersion: domain.MustParseVersion("1.0.0"),
		CreatedAt:      domain.NewTime(mustParseID(t, id)),
		ManagerVersion: "0.0.0",
		Reason:         "contract",
	}

	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

		manifest.Components = append(manifest.Components, ports.ComponentRecord{
			Component:  ports.ComponentDatabase,
			Path:       name,
			Size:       int64(len(content)),
			Encryption: ports.EncryptionAge,
		})
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ports.BackupManifestFileName),
		append(data, '\n'), 0o600))

	return dir
}

func mustParseID(t *testing.T, id string) time.Time {
	t.Helper()
	parsed, err := time.Parse("20060102T150405Z", id)
	require.NoError(t, err)
	return parsed
}

// assertSameTree compares two directories file by file.
//
// Only the files the manifest names are compared, because those are the only
// ones a push carries -- see the "only what the manifest names is pushed" case.
func assertSameTree(t *testing.T, want, got string) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(want, ports.BackupManifestFileName))
	require.NoError(t, err)
	var manifest ports.BackupManifest
	require.NoError(t, json.Unmarshal(data, &manifest))

	names := []string{ports.BackupManifestFileName}
	for _, c := range manifest.Components {
		names = append(names, c.Path)
	}

	for _, name := range names {
		wantBytes, err := os.ReadFile(filepath.Join(want, filepath.FromSlash(name)))
		require.NoError(t, err)
		gotBytes, err := os.ReadFile(filepath.Join(got, filepath.FromSlash(name)))
		require.NoError(t, err, "%s did not come back from the target", name)
		assert.Equal(t, string(wantBytes), string(gotBytes),
			"%s came back different from what was pushed", name)
	}
}
