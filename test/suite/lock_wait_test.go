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
			h := newHarness(t)
			inst := h.install()
			h.setHookEnv()
			applyBaseline(t, h)
			ctx := context.Background()

			_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
			require.NoError(t, err)
			_, err = ops.Backup(ctx, h.Deps, ops.BackupOptions{Reason: "manual"})
			require.NoError(t, err)

			moved := false
			h.Deps.Locker = &lockerThatRaces{Locker: h.Locker, at: func() {
				if moved {
					return
				}
				moved = true
				inner := *h.Deps
				inner.Locker = h.Locker
				_, err := ops.Rollback(ctx, &inner, ops.RollbackOptions{})
				require.NoError(t, err)
			}}

			err = run(ctx, h, inst)
			require.True(t, moved, "%s never took the deployment lock", name)
			require.Error(t, err, "%s ran a plan made for 1.3.0 with 1.2.0 installed", name)
			assert.Contains(t, domain.AsError(err).Message,
				"changed from 1.3.0 to 1.2.0 while waiting for the deployment lock")

			current, err := h.Deps.State.CurrentRelease(ctx)
			require.NoError(t, err)
			assert.Equal(t, "1.2.0", current.Version.String())
		})
	}
}
