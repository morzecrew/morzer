package domain

import (
	"crypto/sha256"
	"encoding/hex"
)

// BackupPushes is the push record (RFC 0061 §5.2): for each backup target, the
// newest backup it took whole. The fleet row's `pushed_at` is read from it.
//
// Derived, so the export does not carry it. Losing it costs a row without
// `pushed_at` until the next push, which an authority reads as nothing having
// left this machine -- the loud side to fail on.
type BackupPushes struct {
	Targets map[string]BackupPush `json:"targets"`
}

// BackupPush is the newest backup one target took whole.
type BackupPush struct {
	ID      string `json:"id"`
	TakenAt Time   `json:"taken_at"`
}

// BackupPushKey names a target in the record by a digest of its canonical URL,
// the form `backup.target-freshness` keys targets on. A digest, so the record
// holds no second copy of a URL that can carry credentials (decision 13).
func BackupPushKey(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Took records that the target named by key took the backup whole, and
// reports whether the record changed.
//
// An entry moves only to a backup taken strictly after the one it names, so a
// retry of an older backup leaves it as it was (decision 14), and pushing the
// same backup again changes nothing.
func (p *BackupPushes) Took(key, id string, takenAt Time) bool {
	if p.Targets == nil {
		p.Targets = map[string]BackupPush{}
	}
	if have, ok := p.Targets[key]; ok && !takenAt.After(have.TakenAt.Time) {
		return false
	}
	p.Targets[key] = BackupPush{ID: id, TakenAt: takenAt}
	return true
}

// PushedAt is the oldest entry among the targets configured now, so every one
// of them holds a backup taken at or after it.
//
// False when no target is configured, or when one has no entry: it was added,
// or put in place of another, and has taken nothing yet. An entry for a target
// no longer configured is ignored, so removing a target never moves the time
// back (decision 13).
func (p BackupPushes) PushedAt(configured []string) (Time, bool) {
	if len(configured) == 0 {
		return Time{}, false
	}
	var oldest Time
	for i, key := range configured {
		entry, ok := p.Targets[key]
		if !ok || entry.TakenAt.IsZero() {
			return Time{}, false
		}
		if i == 0 || entry.TakenAt.Before(oldest.Time) {
			oldest = entry.TakenAt
		}
	}
	return oldest, true
}
