package suite

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// updatedHarness brings a harness to a converged 1.3.0 deployment with 1.2.0 as
// previous, which is the only state rollback is meaningful from.
func updatedHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	_, err := ops.Update(context.Background(), h.Deps,
		ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.NoError(t, err)
	return h
}

// setSchema puts the database at a schema version.
//
// It writes both the recorded value and the marker file the demo bundle's
// migrate hook reads, because those two disagreeing is not a state the system
// can be in: the hook is the authority on what the database holds, and a test
// that set only the record would be asserting against a fiction.
func setSchema(t *testing.T, h *harness, schema int) {
	t.Helper()
	ctx := context.Background()

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	current.SchemaAtInstall = schema
	require.NoError(t, h.Deps.State.SetCurrentRelease(ctx, current))

	require.NoError(t, os.WriteFile(
		filepath.Join(h.Paths.DataDir(), ".schema"),
		[]byte(strconv.Itoa(schema)+"\n"), 0o644))
}

func TestRollbackReturnsToThePreviousRelease(t *testing.T) {
	h := updatedHarness(t)
	ctx := context.Background()

	// Schema 12 is inside 1.2.0's range (10-12), so the return is safe.
	setSchema(t, h, 12)

	result, err := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)
	assert.Equal(t, domain.OpTypeRollback, result.Record.Type)
	assert.Equal(t, "1.3.0", result.Record.From.String())
	assert.Equal(t, "1.2.0", result.Record.To.String())

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", current.Version.String())

	link, err := os.Readlink(h.Paths.CurrentLink())
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(h.Paths.ReleasesDir(), "1.2.0"), link)
}

func TestRollbackRefusesWhenTheSchemaHasMovedOn(t *testing.T) {
	h := updatedHarness(t)
	ctx := context.Background()

	// 1.3.0 migrated the database to 14. 1.2.0 reads at most 12, so
	// swapping the containers back would leave the old code reading a
	// schema it does not understand.
	setSchema(t, h, 14)

	h.Runtime.Calls = nil

	result, rollbackErr := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
	require.Error(t, rollbackErr)
	assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(rollbackErr))

	// Nothing ran, and nothing moved.
	assert.Empty(t, h.Runtime.Calls, "a refused rollback must not touch the runtime")
	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String())

	// The three answers reach the caller even on the refusal path: the
	// assessment is the point of the command.
	report, ok := result.Data.(ops.RollbackReport)
	require.True(t, ok, "the assessment must be returned even when refused")
	assert.True(t, report.Assessment.ContainersReversible,
		"the containers themselves could still be swapped; the schema is what blocks")
	assert.False(t, report.Assessment.SchemaCompatible)
	assert.True(t, report.Assessment.RestoreRequired)

	// And the refusal names the alternative rather than leaving the operator
	// to find it.
	assert.Contains(t, domain.AsError(rollbackErr).Hint, "restore")
}

func TestRollbackRefusesAnIrreversibleRelease(t *testing.T) {
	h := updatedHarness(t)
	ctx := context.Background()
	setSchema(t, h, 12)

	// Rewrite the installed release's manifest to declare its migrations
	// irreversible, which is what a release does when it cannot be undone.
	manifestPath := filepath.Join(h.Paths.ReleasesDir(), "1.3.0", "manifest.yaml")
	data, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifestPath,
		[]byte(replaceOnce(string(data), "rollback_safe: true", "rollback_safe: false")), 0o644))

	_, err = ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
	require.Error(t, err)
	assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err))
	assert.Contains(t, domain.AsError(err).Message, "irreversible")

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String())
}

func TestRollbackForceDoesNotOverrideARefusal(t *testing.T) {
	h := updatedHarness(t)
	setSchema(t, h, 14)

	// --force authorises destructive actions, not incorrect ones. Rolling
	// back onto a schema the old code cannot read is not a thing an operator
	// can consent their way into.
	_, err := ops.Rollback(context.Background(), h.Deps, ops.RollbackOptions{
		Options: ops.Options{Force: true, Yes: true},
	})
	require.Error(t, err)
	assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err))
}

func TestRollbackNamesTheBackupToRestoreFrom(t *testing.T) {
	h := updatedHarness(t)
	setSchema(t, h, 14)

	// The update took a pre-update backup. A refusal should name it, so the
	// operator does not have to go looking for the alternative.
	result, err := ops.Rollback(context.Background(), h.Deps, ops.RollbackOptions{})
	require.Error(t, err)

	report, ok := result.Data.(ops.RollbackReport)
	require.True(t, ok, "the assessment must be returned even when refused")
	require.NotEmpty(t, report.SuggestedBackup)
	assert.Contains(t, domain.AsError(err).Hint, report.SuggestedBackup)
}

func TestRollbackWithNothingToRollBackTo(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	// Only one release has ever been installed.
	_, err := ops.Rollback(context.Background(), h.Deps, ops.RollbackOptions{})
	require.Error(t, err)
	assert.Equal(t, domain.ExitInstallation, domain.ExitCode(err))
	assert.Contains(t, domain.AsError(err).Message, "no previous release")
	assert.Contains(t, domain.AsError(err).Hint, "restore from a backup")
}

func TestRollbackRestoresThePointerWhenConvergenceFails(t *testing.T) {
	h := updatedHarness(t)
	ctx := context.Background()
	setSchema(t, h, 12)

	// The previous release comes up but never becomes ready.
	h.Health.Healthy = false

	result, err := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
	require.Error(t, err)
	assert.Equal(t, domain.StatusCompensated, result.Record.Status)

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String(),
		"a failed rollback must leave the release that was running current")

	link, err := os.Readlink(h.Paths.CurrentLink())
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(h.Paths.ReleasesDir(), "1.3.0"), link)
}

// TestRollbackDoesNotResetTheRecordedSchema is the subtle one. Rolling the
// containers back does not roll the database back, so the recorded schema must
// keep describing what is actually in the database -- otherwise the next
// assessment would be made against a number that is a fiction.
func TestRollbackDoesNotResetTheRecordedSchema(t *testing.T) {
	h := updatedHarness(t)
	ctx := context.Background()
	setSchema(t, h, 12)

	_, err := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
	require.NoError(t, err)

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", current.Version.String())
	assert.Equal(t, 12, current.SchemaAtInstall,
		"the schema describes the database, not the release; rolling the containers "+
			"back does not migrate anything, so the recorded value must still be "+
			"what the migrate hook reports the database holds")
}

func TestRollbackDryRunAssessesWithoutActing(t *testing.T) {
	h := updatedHarness(t)
	ctx := context.Background()
	setSchema(t, h, 12)

	h.Runtime.Calls = nil

	result, err := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{
		Options: ops.Options{DryRun: true},
	})
	require.NoError(t, err)

	// The assessment is available without acting, which is what makes
	// `rollback --dry-run --json` a usable "can I roll back?" query.
	report, ok := result.Data.(ops.RollbackReport)
	require.True(t, ok)
	assert.True(t, report.Assessment.ContainersReversible)
	assert.True(t, report.Assessment.SchemaCompatible)
	assert.Equal(t, "1.3.0", report.From.String())
	assert.Equal(t, "1.2.0", report.To.String())

	assert.Empty(t, h.Runtime.Calls, "a plan must not touch the runtime")
	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String(), "a plan must not move the pointer")
}

func replaceOnce(s, old, new string) string {
	i := indexOf(s, old)
	if i < 0 {
		return s
	}
	return s[:i] + new + s[i+len(old):]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestRollbackToReachesOlderReleases is the gap --to exists to close.
//
// Each rollback promotes the release it displaced to previous, so a second
// rollback without --to returns to where the first started. Naming the target
// is the only way to reach a release two steps back.
func TestRollbackToReachesOlderReleases(t *testing.T) {
	h := updatedHarness(t) // 1.2.0 -> 1.3.0
	ctx := context.Background()
	setSchema(t, h, 12)

	// Without --to, rollback oscillates.
	_, err := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
	require.NoError(t, err)
	first, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	require.Equal(t, "1.2.0", first.Version.String())

	_, err = ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
	require.NoError(t, err)
	second, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", second.Version.String(),
		"a second rollback without --to returns to where the first started")

	// Landing back on 1.3.0 re-ran its migration, so the schema is at 14
	// again and 1.2.0 genuinely cannot read it. Put the database back within
	// range: --to selects *where* to go, and the assessment still governs
	// whether going there is safe.
	setSchema(t, h, 12)

	// Naming the target reaches it directly.
	result, err := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{To: "1.2.0"})
	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)
	assert.Equal(t, "true", result.Record.Flags["to_explicit"],
		"a targeted rollback skipped over releases, which the journal should show")

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", current.Version.String())
}

func TestRollbackToRefusesAForwardMove(t *testing.T) {
	h := updatedHarness(t)
	ctx := context.Background()
	setSchema(t, h, 12)

	_, err := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
	require.NoError(t, err) // now on 1.2.0, with 1.3.0 still in the store

	// Moving forward is an update: it gates on upgrade_from and takes a
	// backup, neither of which a rollback does.
	_, err = ops.Rollback(ctx, h.Deps, ops.RollbackOptions{To: "1.3.0"})
	require.Error(t, err)
	assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
	assert.Contains(t, domain.AsError(err).Hint, "update --to 1.3.0")
}

func TestRollbackToAnUninstalledVersion(t *testing.T) {
	h := updatedHarness(t)
	setSchema(t, h, 12)

	_, err := ops.Rollback(context.Background(), h.Deps, ops.RollbackOptions{To: "9.9.9"})
	require.Error(t, err)
	assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
	// The error names what is available rather than leaving the operator to
	// go looking.
	assert.Contains(t, domain.AsError(err).Hint, "1.3.0")
	assert.Contains(t, domain.AsError(err).Hint, "1.2.0")
}

func TestRollbackToStillAssesses(t *testing.T) {
	h := updatedHarness(t)
	ctx := context.Background()

	// A named target gets the same three-question assessment as the default
	// one: --to selects where to go, not whether it is safe.
	setSchema(t, h, 14)

	_, err := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{To: "1.2.0"})
	require.Error(t, err)
	assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err))
}

func TestRollbackToTheRunningReleaseIsRefused(t *testing.T) {
	h := updatedHarness(t)
	setSchema(t, h, 12)

	// Rolling back to what is already running would stop and restart the
	// product for nothing.
	_, err := ops.Rollback(context.Background(), h.Deps, ops.RollbackOptions{To: "1.3.0"})
	require.Error(t, err)
	assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
	assert.Contains(t, domain.AsError(err).Hint, "apply")
}
