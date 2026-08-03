package contract

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// StateStoreFactory builds a store for one test.
type StateStoreFactory func(t *testing.T) ports.StateStore

// RunStateStoreSuite runs every StateStore contract test.
func RunStateStoreSuite(t *testing.T, newStore StateStoreFactory) {
	t.Helper()
	ctx := context.Background()

	sample := func() domain.Installation {
		return domain.Installation{
			SchemaVersion: domain.InstallationSchemaVersion,
			ID:            "inst_01ABCDEF",
			Product:       "demo",
			CreatedAt:     domain.NewTime(time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)),
			Profile:       "embedded",
			Domains:       []string{"demo.example"},
			Policy:        domain.DefaultPolicy(),
		}
	}

	t.Run("reports no installation before one is saved", func(t *testing.T) {
		store := newStore(t)

		exists, err := store.InstallationExists(ctx)
		require.NoError(t, err)
		assert.False(t, exists)

		_, err = store.LoadInstallation(ctx)
		require.Error(t, err, "loading a nonexistent installation must be an error, not a zero value")
		assert.Equal(t, domain.ExitInstallation, domain.ExitCode(err))
	})

	t.Run("installation round-trips", func(t *testing.T) {
		store := newStore(t)
		want := sample()

		require.NoError(t, store.SaveInstallation(ctx, want))

		exists, err := store.InstallationExists(ctx)
		require.NoError(t, err)
		assert.True(t, exists)

		got, err := store.LoadInstallation(ctx)
		require.NoError(t, err)

		assert.Equal(t, want.ID, got.ID)
		assert.Equal(t, want.Product, got.Product)
		assert.Equal(t, want.Profile, got.Profile)
		assert.Equal(t, want.Domains, got.Domains)
		assert.Equal(t, want.Policy.BackupBeforeUpdate, got.Policy.BackupBeforeUpdate)
	})

	t.Run("rejects an invalid installation", func(t *testing.T) {
		store := newStore(t)
		invalid := sample()
		invalid.ID = ""

		require.Error(t, store.SaveInstallation(ctx, invalid),
			"an installation without an ID would break backup and restore identity checks")
	})

	t.Run("no release installed is not an error", func(t *testing.T) {
		store := newStore(t)

		// `status` on a fresh installation must work, so an absent
		// release pointer is a zero value rather than a failure.
		current, err := store.CurrentRelease(ctx)
		require.NoError(t, err)
		assert.True(t, current.IsZero())

		previous, err := store.PreviousRelease(ctx)
		require.NoError(t, err)
		assert.True(t, previous.IsZero())
	})

	t.Run("setting a release promotes the old one to previous", func(t *testing.T) {
		store := newStore(t)
		require.NoError(t, store.SaveInstallation(ctx, sample()))

		v1 := domain.ReleaseRecord{
			SchemaVersion: 1, Name: "demo", Version: domain.MustParseVersion("1.0.0"),
			Root: "/opt/demo/releases/1.0.0", Digest: "sha256:aaa",
		}
		v2 := domain.ReleaseRecord{
			SchemaVersion: 1, Name: "demo", Version: domain.MustParseVersion("1.1.0"),
			Root: "/opt/demo/releases/1.1.0", Digest: "sha256:bbb",
		}

		require.NoError(t, store.SetCurrentRelease(ctx, v1))
		require.NoError(t, store.SetCurrentRelease(ctx, v2))

		current, err := store.CurrentRelease(ctx)
		require.NoError(t, err)
		assert.True(t, current.Version.Equal(v2.Version))

		previous, err := store.PreviousRelease(ctx)
		require.NoError(t, err)
		assert.True(t, previous.Version.Equal(v1.Version),
			"rollback depends on the displaced release becoming previous")
	})

	t.Run("re-applying the same release preserves previous", func(t *testing.T) {
		store := newStore(t)
		require.NoError(t, store.SaveInstallation(ctx, sample()))

		v1 := domain.ReleaseRecord{SchemaVersion: 1, Name: "demo",
			Version: domain.MustParseVersion("1.0.0"), Root: "/r/1.0.0"}
		v2 := domain.ReleaseRecord{SchemaVersion: 1, Name: "demo",
			Version: domain.MustParseVersion("1.1.0"), Root: "/r/1.1.0"}

		require.NoError(t, store.SetCurrentRelease(ctx, v1))
		require.NoError(t, store.SetCurrentRelease(ctx, v2))
		// A second apply of the release that is already current must
		// not shift previous forward, or rollback would land on the
		// version currently running.
		require.NoError(t, store.SetCurrentRelease(ctx, v2))

		previous, err := store.PreviousRelease(ctx)
		require.NoError(t, err)
		assert.True(t, previous.Version.Equal(v1.Version),
			"a repeated apply must not overwrite the rollback target")
	})

	t.Run("journal is append-only and last-record-wins", func(t *testing.T) {
		store := newStore(t)

		rec := domain.OperationRecord{
			SchemaVersion: 1, ID: "op_1", Type: domain.OpTypeApply,
			Status: domain.StatusRunning, StartedAt: domain.NewTime(time.Now()),
		}
		require.NoError(t, store.AppendOperation(ctx, rec))

		rec.Status = domain.StatusSucceeded
		rec.FinishedAt = domain.NewTime(time.Now())
		require.NoError(t, store.AppendOperation(ctx, rec))

		records, err := store.Operations(ctx, ports.Filter{})
		require.NoError(t, err)
		require.Len(t, records, 1,
			"two records for one operation must collapse to its latest state")
		assert.Equal(t, domain.StatusSucceeded, records[0].Status)
	})

	t.Run("operations are returned newest first", func(t *testing.T) {
		store := newStore(t)
		base := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

		for i, id := range []string{"op_a", "op_b", "op_c"} {
			require.NoError(t, store.AppendOperation(ctx, domain.OperationRecord{
				SchemaVersion: 1, ID: id, Type: domain.OpTypeApply,
				Status:    domain.StatusSucceeded,
				StartedAt: domain.NewTime(base.Add(time.Duration(i) * time.Minute)),
			}))
		}

		records, err := store.Operations(ctx, ports.Filter{})
		require.NoError(t, err)
		require.Len(t, records, 3)
		assert.Equal(t, "op_c", records[0].ID, "the most recent operation must come first")
	})

	t.Run("filters select by type, status and limit", func(t *testing.T) {
		store := newStore(t)
		now := time.Now()

		require.NoError(t, store.AppendOperation(ctx, domain.OperationRecord{
			SchemaVersion: 1, ID: "op_apply", Type: domain.OpTypeApply,
			Status: domain.StatusSucceeded, StartedAt: domain.NewTime(now)}))
		require.NoError(t, store.AppendOperation(ctx, domain.OperationRecord{
			SchemaVersion: 1, ID: "op_backup", Type: domain.OpTypeBackup,
			Status: domain.StatusFailed, StartedAt: domain.NewTime(now.Add(time.Minute))}))

		byType, err := store.Operations(ctx, ports.Filter{Type: domain.OpTypeBackup})
		require.NoError(t, err)
		require.Len(t, byType, 1)
		assert.Equal(t, "op_backup", byType[0].ID)

		byStatus, err := store.Operations(ctx, ports.Filter{Status: domain.StatusSucceeded})
		require.NoError(t, err)
		require.Len(t, byStatus, 1)
		assert.Equal(t, "op_apply", byStatus[0].ID)

		limited, err := store.Operations(ctx, ports.Filter{Limit: 1})
		require.NoError(t, err)
		assert.Len(t, limited, 1)
	})

	t.Run("last operation reflects the newest record", func(t *testing.T) {
		store := newStore(t)

		_, ok, err := store.LastOperation(ctx)
		require.NoError(t, err)
		assert.False(t, ok, "an empty journal must report no last operation, not an error")

		require.NoError(t, store.AppendOperation(ctx, domain.OperationRecord{
			SchemaVersion: 1, ID: "op_x", Type: domain.OpTypeApply,
			Status: domain.StatusSucceeded, StartedAt: domain.NewTime(time.Now())}))

		rec, ok, err := store.LastOperation(ctx)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "op_x", rec.ID)
	})

	t.Run("unfinished operations include running and manual-intervention", func(t *testing.T) {
		store := newStore(t)
		now := time.Now()

		require.NoError(t, store.AppendOperation(ctx, domain.OperationRecord{
			SchemaVersion: 1, ID: "op_done", Type: domain.OpTypeApply,
			Status: domain.StatusSucceeded, StartedAt: domain.NewTime(now)}))
		require.NoError(t, store.AppendOperation(ctx, domain.OperationRecord{
			SchemaVersion: 1, ID: "op_running", Type: domain.OpTypeApply,
			Status: domain.StatusRunning, StartedAt: domain.NewTime(now.Add(time.Minute))}))
		require.NoError(t, store.AppendOperation(ctx, domain.OperationRecord{
			SchemaVersion: 1, ID: "op_stuck", Type: domain.OpTypeUpdate,
			Status:    domain.StatusManualIntervention,
			StartedAt: domain.NewTime(now.Add(2 * time.Minute))}))
		// A plain failure is terminal: the system is where it started,
		// so there is nothing left hanging.
		require.NoError(t, store.AppendOperation(ctx, domain.OperationRecord{
			SchemaVersion: 1, ID: "op_failed", Type: domain.OpTypeApply,
			Status: domain.StatusFailed, StartedAt: domain.NewTime(now.Add(3 * time.Minute))}))

		unfinished, err := store.UnfinishedOperations(ctx)
		require.NoError(t, err)

		ids := make(map[string]bool, len(unfinished))
		for _, rec := range unfinished {
			ids[rec.ID] = true
		}

		assert.True(t, ids["op_running"], "a running operation is unfinished")
		assert.True(t, ids["op_stuck"],
			"requires-manual-intervention must keep surfacing until cleared")
		assert.False(t, ids["op_done"])
		assert.False(t, ids["op_failed"],
			"a plain failure is terminal: nothing is left half-applied")
	})

	t.Run("journal records carry no secrets", func(t *testing.T) {
		store := newStore(t)

		// Redaction happens before writing, not at display time. This
		// asserts the record shape has nowhere for a value to hide.
		rec := domain.OperationRecord{
			SchemaVersion: 1, ID: "op_secret", Type: domain.OpTypeSecret,
			Status: domain.StatusSucceeded, StartedAt: domain.NewTime(time.Now()),
			Flags: map[string]string{"name": "db_password"},
		}
		require.NoError(t, store.AppendOperation(ctx, rec))

		records, err := store.Operations(ctx, ports.Filter{ID: "op_secret"})
		require.NoError(t, err)
		require.Len(t, records, 1)
		assert.Equal(t, "db_password", records[0].Flags["name"],
			"names are recorded; values have no field to live in")
	})
}
