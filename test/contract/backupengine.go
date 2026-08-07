package contract

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// BackupEngineHarness is one implementation under test.
//
// The fake mirrors hookbackup's on-disk layout by hand -- a manifest file, one
// entry per component, a checksum for each -- and nothing checked that the copy
// still matched the original. A fake that drifts here is worse than no fake:
// every operation test that asserts "a corrupt backup is refused" passes
// because the fake refuses it, and says nothing about the engine that will be
// asked to do it for real.
type BackupEngineHarness struct {
	Engine ports.BackupEngine

	// Dir is where a backup lives on disk. An implementation that keeps no
	// directory returns "" and the on-disk cases are skipped.
	Dir func(ref ports.BackupRef) string

	// Encrypts declares that component bytes are unreadable without a key.
	// Declaring it falsely is what this suite catches.
	//
	// What it checks is the port's own vocabulary: a component record says
	// how it is stored, and a record claiming ports.EncryptionAge has to be
	// age. It deliberately says nothing about file naming, which is one
	// engine's layout rather than anything the port promises.
	Encrypts bool
}

// BackupEngineFactory builds a harness for one test.
//
// The engine must use a clock that advances between backups: ordering is by
// timestamp, and an implementation cannot be held to "newest first" if two
// backups claim the same instant.
type BackupEngineFactory func(t *testing.T) BackupEngineHarness

// RunBackupEngineSuite runs every BackupEngine contract test.
func RunBackupEngineSuite(t *testing.T, newEngine BackupEngineFactory) {
	t.Helper()
	ctx := context.Background()

	create := func(t *testing.T, h BackupEngineHarness, reason string) ports.BackupRef {
		t.Helper()
		ref, err := h.Engine.Create(ctx, ports.Scope{
			Components: []ports.Component{ports.ComponentDatabase, ports.ComponentFiles},
			Reason:     reason,
		}, map[string]string{"taken-by": "the-contract-suite"})
		require.NoError(t, err)
		require.NotEmpty(t, ref.ID, "a backup with no id cannot be named to a restore")
		return ref
	}

	t.Run("a created backup is listed and inspectable", func(t *testing.T) {
		h := newEngine(t)
		ref := create(t, h, "manual")

		refs, err := h.Engine.List(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, refs, "a backup that was taken does not appear in the list")

		var found bool
		for _, r := range refs {
			if r.ID == ref.ID {
				found = true
				assert.False(t, r.At.IsZero(), "a backup with no timestamp cannot be aged out")
			}
		}
		assert.True(t, found, "backup %s is missing from the list", ref.ID)

		manifest, err := h.Engine.Inspect(ctx, ref)
		require.NoError(t, err)
		assert.Equal(t, ref.ID, manifest.ID)
	})

	// Self-describing is the first invariant in the port's doc comment, and
	// the one everything else rests on: without checksums, Verify is a
	// function that reports success without comparing anything.
	t.Run("the manifest describes the backup", func(t *testing.T) {
		h := newEngine(t)
		ref := create(t, h, "pre-update")

		manifest, err := h.Engine.Inspect(ctx, ref)
		require.NoError(t, err)

		assert.Equal(t, "pre-update", manifest.Reason,
			"the reason decides retention, so it has to survive into the manifest")
		assert.False(t, manifest.CreatedAt.IsZero())
		require.NotEmpty(t, manifest.Components, "a backup of nothing is not a backup")

		for _, c := range manifest.Components {
			assert.NotEmpty(t, c.Path, "a component with no path cannot be restored")
			assert.NotEmpty(t, c.SHA256,
				"component %q carries no checksum, so verification would compare nothing",
				c.Component)
			assert.Positive(t, c.Size, "component %q records no size", c.Component)
		}
	})

	t.Run("verification passes on a backup nobody touched", func(t *testing.T) {
		h := newEngine(t)
		ref := create(t, h, "manual")

		require.NoError(t, h.Engine.Verify(ctx, ref))
	})

	t.Run("verification catches a corrupted component", func(t *testing.T) {
		h := newEngine(t)
		ref := create(t, h, "manual")
		dir := dirOf(t, h, ref)

		manifest, err := h.Engine.Inspect(ctx, ref)
		require.NoError(t, err)
		require.NotEmpty(t, manifest.Components)

		// One byte, in the middle of the stored bytes. Restoring this
		// over a working system is the worst outcome available.
		component := filepath.Join(dir, filepath.FromSlash(manifest.Components[0].Path))
		data, err := os.ReadFile(component)
		require.NoError(t, err)
		require.NotEmpty(t, data)
		data[len(data)/2] ^= 0xff
		require.NoError(t, os.WriteFile(component, data, 0o600))

		err = h.Engine.Verify(ctx, ref)
		require.Error(t, err, "a corrupted backup verified successfully")
		assert.Equal(t, domain.ExitBackup, domain.ExitCode(err))
	})

	t.Run("verification catches a component that is gone", func(t *testing.T) {
		h := newEngine(t)
		ref := create(t, h, "manual")
		dir := dirOf(t, h, ref)

		manifest, err := h.Engine.Inspect(ctx, ref)
		require.NoError(t, err)
		require.NotEmpty(t, manifest.Components)

		require.NoError(t, os.Remove(
			filepath.Join(dir, filepath.FromSlash(manifest.Components[0].Path))))

		require.Error(t, h.Engine.Verify(ctx, ref),
			"a backup missing a component verified successfully")
	})

	t.Run("an unknown backup is not found rather than empty", func(t *testing.T) {
		h := newEngine(t)

		unknown := ports.BackupRef{ID: "backup-that-never-existed"}

		_, err := h.Engine.Inspect(ctx, unknown)
		require.Error(t, err, "inspecting a backup that does not exist returned a manifest")
		assert.ErrorIs(t, err, domain.ErrNotFound)

		require.Error(t, h.Engine.Verify(ctx, unknown),
			"verifying a backup that does not exist reported success, which is the "+
				"one answer that must never be given about a backup")
		require.Error(t, h.Engine.Restore(ctx, unknown, ports.RestoreOptions{}),
			"restoring a backup that does not exist reported success")
	})

	t.Run("the list is newest first", func(t *testing.T) {
		h := newEngine(t)
		first := create(t, h, "manual")
		second := create(t, h, "manual")

		refs, err := h.Engine.List(ctx)
		require.NoError(t, err)
		require.Len(t, refs, 2)
		assert.Equal(t, second.ID, refs[0].ID,
			"the list is not newest-first, so `restore` without an id would pick the wrong backup")
		assert.Equal(t, first.ID, refs[1].ID)
	})

	t.Run("pruning keeps the most recent whatever the policy says", func(t *testing.T) {
		h := newEngine(t)
		older := create(t, h, "manual")
		newest := create(t, h, "manual")

		// Keep: 0 is a policy that would delete everything. The port
		// promises the most recent survives it, because an operator who
		// misconfigures retention must not lose their last backup.
		removed, err := h.Engine.Prune(ctx, ports.RetentionPolicy{Keep: 0})
		require.NoError(t, err)

		ids := idsOf(removed)
		assert.NotContains(t, ids, newest.ID, "the most recent backup was pruned")

		refs, err := h.Engine.List(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, refs, "pruning removed every backup")
		assert.Equal(t, newest.ID, refs[0].ID)

		// Unconditionally, both halves. Asserting these only when
		// something was reported removed would let a pruner that
		// removes nothing and reports nothing satisfy the whole case --
		// which is precisely the regression retention has.
		assert.Contains(t, ids, older.ID,
			"the older backup was not pruned under a policy keeping one")
		_, err = h.Engine.Inspect(ctx, ports.BackupRef{ID: older.ID})
		require.Error(t, err,
			"backup %s was reported pruned and is still there", older.ID)
		assert.Len(t, refs, 1, "the list still holds a backup the policy pruned")
	})

	t.Run("a reason the policy exempts is not pruned", func(t *testing.T) {
		h := newEngine(t)
		exempt := create(t, h, "pre-update")
		create(t, h, "manual")
		create(t, h, "manual")

		removed, err := h.Engine.Prune(ctx, ports.RetentionPolicy{
			Keep: 1, KeepReasons: []string{"pre-update"},
		})
		require.NoError(t, err)
		assert.NotContains(t, idsOf(removed), exempt.ID,
			"the pre-update backup was pruned, and it is the one an operator "+
				"reaches for when the update they just ran went wrong")

		_, err = h.Engine.Inspect(ctx, ports.BackupRef{ID: exempt.ID})
		require.NoError(t, err, "the exempt backup is gone from disk")
	})

	// "Nothing in a backup is readable without a key except its manifest" is
	// the invariant that lets a backup leave the machine. An implementation
	// that does not claim it says so, rather than being quietly excused.
	t.Run("the layout on disk is what the manifest says it is", func(t *testing.T) {
		h := newEngine(t)
		ref := create(t, h, "manual")
		dir := dirOf(t, h, ref)

		info, err := os.Stat(dir)
		require.NoError(t, err)
		assert.Zero(t, info.Mode().Perm()&0o077,
			"the backup directory is %v: it holds the database and the encrypted "+
				"secret file, and is the most sensitive directory the manager creates",
			info.Mode().Perm())

		raw, err := os.ReadFile(filepath.Join(dir, ports.BackupManifestFileName))
		require.NoError(t, err, "the manifest is the one file a third party must be able to read")

		var onDisk ports.BackupManifest
		require.NoError(t, json.Unmarshal(raw, &onDisk),
			"the manifest on disk is not the JSON the port documents")
		assert.Equal(t, ref.ID, onDisk.ID)

		for _, c := range onDisk.Components {
			path := filepath.Join(dir, filepath.FromSlash(c.Path))
			stat, err := os.Stat(path)
			require.NoError(t, err, "the manifest names %s, which is not there", c.Path)
			assert.Equal(t, c.Size, stat.Size(),
				"%s is %d bytes and the manifest says %d", c.Path, stat.Size(), c.Size)

			if !h.Encrypts {
				continue
			}
			assert.NotEmpty(t, c.Encryption,
				"%s records no encryption, so a restore cannot tell whether to "+
					"decrypt it", c.Path)
			if c.Encryption == ports.EncryptionAge {
				head, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.True(t, strings.HasPrefix(string(head), "age-encryption.org/"),
					"%s claims to be age-encrypted and does not begin with the "+
						"age header", c.Path)
			}
		}
	})
}

func dirOf(t *testing.T, h BackupEngineHarness, ref ports.BackupRef) string {
	t.Helper()
	if h.Dir == nil {
		t.Skip("this implementation keeps no backup directory")
	}
	dir := h.Dir(ref)
	if dir == "" {
		t.Skip("this implementation keeps no backup directory")
	}
	return dir
}

func idsOf(refs []ports.BackupRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.ID)
	}
	return out
}
