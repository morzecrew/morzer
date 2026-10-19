package suite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// TestAnOperationThatWaitedForTheLockRefusesAMovedRelease.
//
// Each of these reads the current release before taking the deployment lock,
// and plans against it: the version it journals as "from", the compatibility
// gate, the release whose backup and restore operations it runs, the
// PREVIOUS_VERSION its hooks are told. With --wait, the operation it queued
// behind can move that release. An update queued behind another update
// journalled `from 1.2.0` with 1.3.0 installed, and an apply queued behind a
// rollback converged the release that had just been rolled off.
//
// Driven by a locker that rolls 1.3.0 back to 1.2.0 at the moment each one
// starts waiting, so every plan was made for a release that is gone by the time
// it would run.
func TestAnOperationThatWaitedForTheLockRefusesAMovedRelease(t *testing.T) {
	for name, run := range map[string]func(context.Context, *harness, domain.Installation) error{
		"apply": func(ctx context.Context, h *harness, _ domain.Installation) error {
			_, err := ops.Apply(ctx, h.Deps, ops.Options{})
			return err
		},
		"update": func(ctx context.Context, h *harness, _ domain.Installation) error {
			_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
			return err
		},
		"rollback": func(ctx context.Context, h *harness, _ domain.Installation) error {
			_, err := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
			return err
		},
		"backup": func(ctx context.Context, h *harness, _ domain.Installation) error {
			_, err := ops.Backup(ctx, h.Deps, ops.BackupOptions{Reason: "manual"})
			return err
		},
		"restore": func(ctx context.Context, h *harness, inst domain.Installation) error {
			_, err := ops.Restore(ctx, h.Deps, ops.RestoreOptions{
				Options:                 ops.Options{Force: true},
				ConfirmedInstallationID: inst.ID,
			})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, inst := atOneThreeZero(t)
			ctx := context.Background()

			moved := whileWaiting(h, func() {
				inner := *h.Deps
				inner.Locker = h.Locker
				_, err := ops.Rollback(ctx, &inner, ops.RollbackOptions{})
				require.NoError(t, err)
			})

			err := run(ctx, h, inst)
			require.True(t, *moved, "%s never took the deployment lock", name)
			require.Error(t, err, "%s ran a plan made for 1.3.0 with 1.2.0 installed", name)
			assert.Contains(t, domain.AsError(err).Message,
				"changed from 1.3.0 to 1.2.0 while waiting for the deployment lock")

			current, err := h.Deps.State.CurrentRelease(ctx)
			require.NoError(t, err)
			assert.Equal(t, "1.2.0", current.Version.String())
		})
	}
}

// TestAWaitingRollbackRefusesWhatMovedAroundTheRelease.
//
// The installed release's root and digest are not all a rollback plans from.
// It assesses the schema the release recorded, which an apply of the same
// release that migrated further records anew. And a rollback with no --to
// targets the previous pointer, which an update and a rollback back while it
// waited move without moving the installed release.
func TestAWaitingRollbackRefusesWhatMovedAroundTheRelease(t *testing.T) {
	for name, tc := range map[string]struct {
		move func(t *testing.T, h *harness, current domain.ReleaseRecord)
		says string
	}{
		"schema": {
			move: func(t *testing.T, h *harness, current domain.ReleaseRecord) {
				current.SchemaAtInstall = 14
				require.NoError(t, h.Deps.State.SetCurrentRelease(context.Background(), current))
			},
			says: "schema recorded for the installed release 1.3.0 changed from 12 to 14",
		},
		"previous release": {
			move: func(t *testing.T, h *harness, current domain.ReleaseRecord) {
				newer := current
				newer.Version = domain.MustParseVersion("1.4.0")
				newer.Root = t.TempDir()
				newer.Digest = "sha256:" + "14" + current.Digest[len("sha256:14"):]
				require.NoError(t, h.Deps.State.SetCurrentRelease(context.Background(), newer))
				require.NoError(t, h.Deps.State.SetCurrentRelease(context.Background(), current))
			},
			says: "previous release changed from 1.2.0 to 1.4.0",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := atOneThreeZero(t)
			ctx := context.Background()
			current, err := h.Deps.State.CurrentRelease(ctx)
			require.NoError(t, err)
			require.Equal(t, 12, current.SchemaAtInstall)

			moved := whileWaiting(h, func() { tc.move(t, h, current) })

			_, err = ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
			require.True(t, *moved, "rollback never took the deployment lock")
			require.Error(t, err, "rollback ran a plan for what was there before it waited")
			assert.Contains(t, domain.AsError(err).Message, tc.says)
		})
	}
}

// atOneThreeZero converges 1.2.0, updates it to 1.3.0 and takes a backup, so
// there is a previous release to roll back to and a backup to restore.
func atOneThreeZero(t *testing.T) (*harness, domain.Installation) {
	t.Helper()
	h := newHarness(t)
	inst := h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
	require.NoError(t, err)
	_, err = ops.Backup(ctx, h.Deps, ops.BackupOptions{Reason: "manual"})
	require.NoError(t, err)
	return h, inst
}

// whileWaiting runs move once, as the next operation starts waiting for the
// deployment lock, and reports whether it ran.
func whileWaiting(h *harness, move func()) *bool {
	moved := false
	h.Deps.Locker = &lockerThatRaces{Locker: h.Locker, at: func() {
		if moved {
			return
		}
		moved = true
		move()
	}}
	return &moved
}
