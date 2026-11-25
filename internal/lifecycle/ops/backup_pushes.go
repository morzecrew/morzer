package ops

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/infra/logging"
	"github.com/morzecrew/morzer/internal/ports"
)

// backupPushes reads the push record (RFC 0061 §5.2). A missing file is an
// empty record: nothing has been pushed since the record existed.
func (d *Deps) backupPushes() (domain.BackupPushes, error) {
	data, err := os.ReadFile(d.Paths.BackupPushesFile())
	if errors.Is(err, fs.ErrNotExist) {
		return domain.BackupPushes{}, nil
	}
	if err != nil {
		return domain.BackupPushes{}, domain.InstallationError(err,
			"cannot read %s", d.Paths.BackupPushesFile())
	}
	var record domain.BackupPushes
	if err := json.Unmarshal(data, &record); err != nil {
		return domain.BackupPushes{}, domain.InstallationError(err,
			"the backup push record at %s is not valid JSON", d.Paths.BackupPushesFile())
	}
	return record, nil
}

// notePushes records that each of the targets in took holds the backup whole,
// and that the one in lost, where a push of it failed and its copy was
// removed, may not.
//
// Best effort. The copies are on the targets whatever happens here, and a
// record that is not written keeps its earlier entries. The fleet row then
// shows an older `pushed_at`, which reads as behind, or none where a
// configured target has no entry: the loud side. The exception is the entry
// for a target in lost, which stays and still names the copy that was
// removed (RFC 0061 decision 24). So a failure is logged rather than failing
// a push that succeeded. An unreadable record is replaced by this push's
// entries: it can then only lack other targets' entries, which also leaves
// the row without `pushed_at`.
func (d *Deps) notePushes(ctx context.Context, ref ports.BackupRef, took []ports.TargetRef, lost ...ports.TargetRef) {
	if len(took) == 0 && len(lost) == 0 {
		return
	}
	log := logging.FromContext(ctx)

	record, err := d.backupPushes()
	changed := false
	if err != nil {
		log.Warn("replacing an unreadable backup push record", "error", domain.AsError(err).Message)
		record, changed = domain.BackupPushes{}, true
	}
	for _, target := range took {
		if record.Took(domain.BackupPushKey(target.Canonical()), ref.ID, ref.At) {
			changed = true
		}
	}
	for _, target := range lost {
		if record.Lost(domain.BackupPushKey(target.Canonical()), ref.ID) {
			changed = true
		}
	}
	if !changed {
		return
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err == nil {
		err = atomicfs.WriteFile(d.Paths.BackupPushesFile(), append(data, '\n'), 0o640)
	}
	if err != nil {
		log.Warn("cannot write the backup push record", "backup", ref.ID, "error", err)
	}
}
