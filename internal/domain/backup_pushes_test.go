package domain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// The push record says what has left the machine (RFC 0061 §5.2). Each test is
// a sequence §6 names, because what goes wrong is an order of events: a target
// set that changes between pushes, or a retry that arrives after a newer push.

var (
	targetA = domain.BackupPushKey("s3://bucket.example/a")
	targetB = domain.BackupPushKey("s3://bucket.example/b")
	targetC = domain.BackupPushKey("s3://bucket.example/c")
)

func pushedAt(t *testing.T, p domain.BackupPushes, configured ...string) string {
	t.Helper()
	at, ok := p.PushedAt(configured)
	if !ok {
		return ""
	}
	return at.UTC().Format("2006-01-02T15:04:05Z")
}

// The key is a digest, so the record holds no second copy of a URL that can
// carry credentials, and one URL always names one target.
func TestAPushKeyIsADigestOfTheURL(t *testing.T) {
	key := domain.BackupPushKey("sftp://backup@host.example/srv")
	assert.True(t, strings.HasPrefix(key, "sha256:"))
	assert.NotContains(t, key, "host.example")
	assert.Equal(t, key, domain.BackupPushKey("sftp://backup@host.example/srv"))
	assert.NotEqual(t, key, domain.BackupPushKey("sftp://backup@host.example/srv2"))
}

func TestPushedAtIsTheOldestEntryAmongTheConfiguredTargets(t *testing.T) {
	var p domain.BackupPushes
	p.Took(targetA, "b2", at("2026-11-23T03:00:00Z"))
	p.Took(targetB, "b1", at("2026-11-22T03:00:00Z"))

	assert.Equal(t, "2026-11-22T03:00:00Z", pushedAt(t, p, targetA, targetB),
		"every configured target holds a backup at least as new as the oldest entry")
	assert.Empty(t, pushedAt(t, p), "no target configured is nothing left the machine")
}

// A target added, or put in place of another, has taken nothing yet.
func TestAReplacedTargetLeavesNoPushedAt(t *testing.T) {
	var p domain.BackupPushes
	p.Took(targetA, "b1", at("2026-11-23T03:00:00Z"))

	assert.Empty(t, pushedAt(t, p, targetB),
		"a target that replaced another read as holding the copy it never took")

	p.Took(targetB, "b2", at("2026-11-24T03:00:00Z"))
	assert.Equal(t, "2026-11-24T03:00:00Z", pushedAt(t, p, targetB))
}

func TestARemovedTargetIsIgnored(t *testing.T) {
	var p domain.BackupPushes
	p.Took(targetA, "b2", at("2026-11-23T03:00:00Z"))
	p.Took(targetB, "b1", at("2026-11-20T03:00:00Z"))

	assert.Equal(t, "2026-11-23T03:00:00Z", pushedAt(t, p, targetA),
		"a target no longer configured held pushed_at back")
}

// An older retry leaves the entry where a newer push put it (decision 14).
func TestAnOlderRetryDoesNotMoveAnEntryBack(t *testing.T) {
	var p domain.BackupPushes
	require.True(t, p.Took(targetA, "b2", at("2026-11-23T03:00:00Z")))

	assert.False(t, p.Took(targetA, "b1", at("2026-11-22T03:00:00Z")),
		"pushing an older backup moved the entry back")
	assert.False(t, p.Took(targetA, "b2", at("2026-11-23T03:00:00Z")),
		"pushing the same backup again changed the record")
	assert.Equal(t, "b2", p.Targets[targetA].ID)
}

// A failed push removes only the copy of the backup it was pushing, so only
// an entry naming that backup goes: a retry of an older one that fails leaves
// the newer entry, and the other targets' entries, as they were.
func TestAFailedPushForgetsOnlyTheBackupItRemoved(t *testing.T) {
	var p domain.BackupPushes
	p.Took(targetA, "b2", at("2026-11-23T03:00:00Z"))
	p.Took(targetB, "b2", at("2026-11-23T03:00:00Z"))

	assert.False(t, p.Lost(targetA, "b1"), "a failed push of b1 forgot b2")
	assert.False(t, p.Lost(targetC, "b2"), "a target with no entry changed the record")
	assert.True(t, p.Lost(targetA, "b2"))
	assert.NotContains(t, p.Targets, targetA)
	assert.Equal(t, "b2", p.Targets[targetB].ID, "a failure at one target forgot another's entry")
	assert.Empty(t, pushedAt(t, p, targetA, targetB))
}

// The sequence a single record for all targets could not follow: a target
// added, an older backup pushed to every target, the added target removed.
func TestAddingATargetPushingOlderThenRemovingItKeepsTheNewer(t *testing.T) {
	var p domain.BackupPushes
	p.Took(targetA, "x", at("2026-11-23T03:00:00Z"))

	// B added; Y, older than X, pushed to both.
	p.Took(targetA, "y", at("2026-11-22T03:00:00Z"))
	p.Took(targetB, "y", at("2026-11-22T03:00:00Z"))
	assert.Equal(t, "2026-11-22T03:00:00Z", pushedAt(t, p, targetA, targetB))

	// B removed.
	assert.Equal(t, "2026-11-23T03:00:00Z", pushedAt(t, p, targetA),
		"removing the added target left pushed_at at the older backup")
}

func TestAnEntryWithNoTimeIsNoEntry(t *testing.T) {
	p := domain.BackupPushes{Targets: map[string]domain.BackupPush{targetC: {ID: "b1"}}}
	assert.Empty(t, pushedAt(t, p, targetC),
		"an entry with no time published a pushed_at nobody took")
}

// The block publishes what it read, leaves out what it could not, and never
// shares memory with what built it (RFC 0061 §5.1, decision 7).
func TestTheBackupBlockCarriesItsInputs(t *testing.T) {
	last, pushed := at("2026-11-23T03:00:12Z"), at("2026-11-22T03:00:09Z")
	recovery := true
	in := &domain.FleetBackup{
		Targets: 2, LastAt: &last, PushedAt: &pushed, RecoveryRecipient: &recovery,
	}
	row := domain.NewFleetRow(domain.FleetRowInputs{
		Installation: fleetInstallation(),
		PublishedAt:  at("2026-11-24T10:00:00Z"),
		Backup:       in,
	})

	recovery = false
	in.Targets = 9

	require.NotNil(t, row.Backup)
	assert.Equal(t, 2, row.Backup.Targets)
	require.NotNil(t, row.Backup.RecoveryRecipient)
	assert.True(t, *row.Backup.RecoveryRecipient, "the block shares memory with its input")

	body, err := json.Marshal(row)
	require.NoError(t, err)
	assert.Contains(t, string(body),
		`"backup":{"targets":2,"last_at":"2026-11-23T03:00:12Z","pushed_at":"2026-11-22T03:00:09Z","recovery_recipient":true}`)
}

func TestTheBackupBlockLeavesOutWhatItCouldNotRead(t *testing.T) {
	row := domain.NewFleetRow(domain.FleetRowInputs{
		Installation: fleetInstallation(),
		PublishedAt:  at("2026-11-24T10:00:00Z"),
		Backup: &domain.FleetBackup{
			Targets: 1, Problem: "cannot list backups: \x1b[2J" + strings.Repeat("x", 5000),
		},
	})

	body, err := json.Marshal(row.Backup)
	require.NoError(t, err)
	for _, field := range []string{"last_at", "pushed_at", "recovery_recipient", "scheduled", "expected"} {
		assert.NotContains(t, string(body), field,
			"a field that could not be read was published")
	}
	assert.NotContains(t, row.Backup.Problem, "\x1b", "an escape sequence reached the row")
	assert.LessOrEqual(t, len(row.Backup.Problem), domain.MaxAttestedText+len("… [truncated]"))

	read := domain.FleetRow{Backup: &domain.FleetBackup{Problem: "x\x1b[2J"}}.Bounded()
	assert.NotContains(t, read.Backup.Problem, "\x1b", "a row read back kept an escape in backup.problem")
}
