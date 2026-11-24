package suite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// The row's backup block (RFC 0061 P1), read off a publish rather than off the
// code that builds it: what an authority sees is the question.

// withBackupClock gives the fake backup engine a clock a second apart per
// backup, so two backups taken in one test are ordered as real ones are.
func (h *harness) withBackupClock(t *testing.T) {
	t.Helper()
	h.Backup.Root = filepath.Join(h.Root, "var", "backups")
	require.NoError(t, os.MkdirAll(h.Backup.Root, 0o700))
	next := time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC)
	h.Backup.Now = func() time.Time {
		next = next.Add(time.Hour)
		return next
	}
}

func publishedBackup(t *testing.T, h *harness) domain.FleetBackup {
	t.Helper()
	result, err := ops.FleetPublish(context.Background(), h.Deps, ops.FleetPublishOptions{TargetOptions: ops.TargetOptions{Options: ops.Options{DryRun: true}}})
	require.NoError(t, err)
	report, ok := result.Data.(ops.FleetPublishReport)
	require.True(t, ok)
	require.NotNil(t, report.Row.Backup, "a schema 4 row was published without its backup block")
	return *report.Row.Backup
}

func takeBackup(t *testing.T, h *harness, push bool) ports.BackupRef {
	t.Helper()
	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: push,
	})
	require.NoError(t, err)
	ref, ok := result.Data.(ports.BackupRef)
	require.True(t, ok)
	return ref
}

func pushRecord(t *testing.T, h *harness) domain.BackupPushes {
	t.Helper()
	data, err := os.ReadFile(h.Paths.BackupPushesFile())
	if os.IsNotExist(err) {
		return domain.BackupPushes{}
	}
	require.NoError(t, err)
	var record domain.BackupPushes
	require.NoError(t, json.Unmarshal(data, &record))
	return record
}

func fileKey(dir string) string { return domain.BackupPushKey("file://" + dir) }

// A backup pushed to every target is the newest backup on every target, and
// the row says so with pushed_at equal to last_at.
func TestTheRowSaysTheNewestBackupLeftTheMachine(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)
	h.withBackupClock(t)

	before := publishedBackup(t, h)
	assert.Equal(t, 1, before.Targets)
	assert.Nil(t, before.LastAt)
	assert.Nil(t, before.PushedAt, "a machine that never pushed published a pushed_at")

	ref := takeBackup(t, h, true)

	block := publishedBackup(t, h)
	require.NotNil(t, block.LastAt)
	require.NotNil(t, block.PushedAt, "a backup on every target left the row without pushed_at")
	assert.True(t, block.LastAt.Equal(ref.At.Time))
	assert.True(t, block.PushedAt.Equal(ref.At.Time))
	assert.Equal(t, ref.ID, pushRecord(t, h).Targets[fileKey(offsite)].ID)
}

// A backup kept on the machine is not off it: last_at moves, pushed_at does not.
func TestABackupNotPushedLeavesTheRowBehind(t *testing.T) {
	h := fleetHarness(t)
	h.withFleetTarget(t)
	h.withBackupClock(t)

	first := takeBackup(t, h, true)
	second := takeBackup(t, h, false)

	block := publishedBackup(t, h)
	require.NotNil(t, block.LastAt)
	require.NotNil(t, block.PushedAt)
	assert.True(t, block.LastAt.Equal(second.At.Time))
	assert.True(t, block.PushedAt.Equal(first.At.Time),
		"a backup that never left the machine moved pushed_at")
}

// `backup push` sets the entry too, and an older retry after a newer push
// leaves it where the newer one put it (decision 14).
func TestBackupPushRecordsItAndAnOlderRetryDoesNotMoveItBack(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)
	h.withBackupClock(t)

	older := takeBackup(t, h, false)
	newer := takeBackup(t, h, false)

	_, err := ops.Push(context.Background(), h.Deps, ops.PushOptions{BackupID: newer.ID})
	require.NoError(t, err)
	assert.Equal(t, newer.ID, pushRecord(t, h).Targets[fileKey(offsite)].ID,
		"backup push did not record the target that took the backup")

	_, err = ops.Push(context.Background(), h.Deps, ops.PushOptions{BackupID: older.ID})
	require.NoError(t, err)
	assert.Equal(t, newer.ID, pushRecord(t, h).Targets[fileKey(offsite)].ID,
		"an older retry moved the entry back")

	block := publishedBackup(t, h)
	require.NotNil(t, block.PushedAt)
	assert.True(t, block.PushedAt.Equal(newer.At.Time))
}

// A plan writes nothing, the push record included.
func TestADryRunPushRecordsNothing(t *testing.T) {
	h := fleetHarness(t)
	h.withFleetTarget(t)
	h.withBackupClock(t)
	takeBackup(t, h, false)

	_, err := ops.Push(context.Background(), h.Deps, ops.PushOptions{
		Options: ops.Options{DryRun: true},
	})
	require.NoError(t, err)
	assert.NoFileExists(t, h.Paths.BackupPushesFile())
}

// A push that fails at one target keeps the whole copies on the others, so
// their entries are set; the failing target has none, so the row has no
// pushed_at until it is fixed or removed.
func TestAPartlyFailedPushRecordsTheTargetsThatTookIt(t *testing.T) {
	h := fleetHarness(t)
	inst, good := h.withFleetTarget(t)
	h.withBackupClock(t)

	blocked := filepath.Join(h.Root, "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o600))
	bad := filepath.Join(blocked, "backups")
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + good}, {URL: "file://" + bad}}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: true,
	})
	require.Error(t, err)

	record := pushRecord(t, h)
	assert.Contains(t, record.Targets, fileKey(good),
		"the target that took the backup whole has no entry")
	assert.NotContains(t, record.Targets, fileKey(bad))
	assert.Nil(t, publishedBackup(t, h).PushedAt,
		"a target that holds nothing was read as holding the backup")

	// The failing target removed: the one that remains holds the backup.
	inst.Backup.Targets = inst.Backup.Targets[:1]
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
	assert.NotNil(t, publishedBackup(t, h).PushedAt)
}

// A failed push removes its copy from the target it failed at, and that may be
// the copy an earlier push left there: the entry naming it goes too.
func TestAFailedRepeatPushForgetsTheCopyItRemoved(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)
	h.withBackupClock(t)
	ref := takeBackup(t, h, true)
	require.Equal(t, ref.ID, pushRecord(t, h).Targets[fileKey(offsite)].ID)

	// The target's copy gone, and the target no longer writable.
	require.NoError(t, os.RemoveAll(offsite))
	require.NoError(t, os.WriteFile(offsite, []byte("not a directory"), 0o600))
	_, err := ops.Push(context.Background(), h.Deps, ops.PushOptions{BackupID: ref.ID})
	require.Error(t, err)

	assert.NotContains(t, pushRecord(t, h).Targets, fileKey(offsite),
		"the record names a copy the failed push removed")
	assert.Nil(t, publishedBackup(t, h).PushedAt)
}

// A target put in place of another holds nothing until a push reaches it.
func TestAReplacedTargetLeavesTheRowWithoutPushedAt(t *testing.T) {
	h := fleetHarness(t)
	inst, _ := h.withFleetTarget(t)
	h.withBackupClock(t)
	takeBackup(t, h, true)
	require.NotNil(t, publishedBackup(t, h).PushedAt)

	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + filepath.Join(t.TempDir(), "new")}}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
	assert.Nil(t, publishedBackup(t, h).PushedAt,
		"a replaced target read as holding the copy the old one took")
}

// A record that cannot be read is a problem on the row, never "nothing left".
func TestAnUnreadablePushRecordIsAProblemOnTheRow(t *testing.T) {
	h := fleetHarness(t)
	h.withFleetTarget(t)
	h.withBackupClock(t)
	takeBackup(t, h, true)

	require.NoError(t, os.WriteFile(h.Paths.BackupPushesFile(), []byte("{not json"), 0o640))
	block := publishedBackup(t, h)
	assert.Nil(t, block.PushedAt)
	assert.Contains(t, block.Problem, "push record")

	// The next push replaces it.
	takeBackup(t, h, true)
	block = publishedBackup(t, h)
	assert.NotNil(t, block.PushedAt)
	assert.Empty(t, block.Problem)
}

// `backup.scheduled` off reads as declared, not as backups that stopped.
func TestBackupsAtTheStorageLayerAreDeclaredOnTheRow(t *testing.T) {
	h := fleetHarness(t)
	inst, _ := h.withFleetTarget(t)
	inst.Policy.SkipScheduledBackups = true
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	block := publishedBackup(t, h)
	require.NotNil(t, block.Scheduled)
	assert.False(t, *block.Scheduled)
}
