// Package state persists the manager's own knowledge: the installation, the
// current and previous release pointers, and the operation journal.
//
// Every file carries a schema_version and every write is atomic. The journal
// is append-only JSONL: a running operation writes a record at start and at
// each transition, and the last record for an ID wins. That shape survives a
// crash mid-write -- a truncated final line is discarded on read, and the
// previous complete record still describes a known position.
package state

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// maxJournalLine bounds a single record. A line longer than this is corrupt
// rather than large: records hold step summaries, not payloads.
const maxJournalLine = 1 << 20

// Store is the filesystem implementation of ports.StateStore.
type Store struct {
	paths domain.Paths
}

func New(paths domain.Paths) *Store {
	return &Store{paths: paths}
}

var _ ports.StateStore = (*Store)(nil)

// installationEnvelope wraps the installation with the schema version, kept
// separate from the domain type so domain stays free of persistence concerns.
type installationEnvelope struct {
	SchemaVersion int                 `json:"schema_version"`
	Installation  domain.Installation `json:"installation"`
}

func (s *Store) InstallationExists(ctx context.Context) (bool, error) {
	return atomicfs.Exists(s.paths.InstallationState())
}

func (s *Store) LoadInstallation(ctx context.Context) (domain.Installation, error) {
	path := s.paths.InstallationState()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.Installation{}, domain.InstallationError(domain.ErrInstallation,
				"no installation state at %s", path).
				WithHint("run `morzer init` to create an installation")
		}
		return domain.Installation{}, domain.InstallationError(err, "cannot read %s", path)
	}

	var env installationEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return domain.Installation{}, domain.InstallationError(err,
			"installation state at %s is not valid JSON", path).
			WithHint("restore it from a backup, or re-run `morzer init` against a clean directory")
	}

	inst := env.Installation
	if inst.SchemaVersion == 0 {
		inst.SchemaVersion = env.SchemaVersion
	}

	migrated, err := migrateInstallation(inst)
	if err != nil {
		return domain.Installation{}, err
	}
	if err := migrated.Validate(); err != nil {
		return domain.Installation{}, domain.InstallationError(err,
			"installation state at %s is invalid", path)
	}
	return migrated, nil
}

func (s *Store) SaveInstallation(ctx context.Context, i domain.Installation) error {
	if i.SchemaVersion == 0 {
		i.SchemaVersion = domain.InstallationSchemaVersion
	}
	if err := i.Validate(); err != nil {
		return err
	}
	env := installationEnvelope{SchemaVersion: i.SchemaVersion, Installation: i}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return domain.Internal(err, "cannot serialise installation state")
	}
	return atomicfs.WriteFile(s.paths.InstallationState(), append(data, '\n'), 0o640)
}

// migrateInstallation runs forward-only state migrations.
//
// The spec's rule is that a new manager must work with an old installation,
// and an old manager must refuse a new one clearly. The second half is handled
// by Installation.Validate, which rejects a schema version from the future.
func migrateInstallation(i domain.Installation) (domain.Installation, error) {
	for i.SchemaVersion < domain.InstallationSchemaVersion {
		switch i.SchemaVersion {
		// case 1: migrate 1 -> 2 here when the shape changes.
		default:
			return i, domain.InstallationError(nil,
				"no migration path from installation schema %d to %d",
				i.SchemaVersion, domain.InstallationSchemaVersion)
		}
	}
	return i, nil
}

func (s *Store) CurrentRelease(ctx context.Context) (domain.ReleaseRecord, error) {
	return s.readRelease(s.paths.CurrentReleaseFile())
}

func (s *Store) PreviousRelease(ctx context.Context) (domain.ReleaseRecord, error) {
	return s.readRelease(s.paths.PreviousReleaseFile())
}

func (s *Store) readRelease(path string) (domain.ReleaseRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No release installed yet is a normal state, not an
			// error: `status` on a fresh install must still work.
			return domain.ReleaseRecord{}, nil
		}
		return domain.ReleaseRecord{}, domain.InstallationError(err, "cannot read %s", path)
	}
	var rec domain.ReleaseRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return domain.ReleaseRecord{}, domain.InstallationError(err,
			"release record at %s is not valid JSON", path)
	}
	return rec, nil
}

// SetCurrentRelease promotes r to current, demoting the existing current to
// previous.
//
// Ordering matters: previous is written first. A crash between the two writes
// then leaves previous duplicating current, which is harmless and
// self-correcting. Writing current first would leave a window where the
// release being replaced is recorded nowhere, and rollback would have nothing
// to return to.
func (s *Store) SetCurrentRelease(ctx context.Context, r domain.ReleaseRecord) error {
	if r.SchemaVersion == 0 {
		r.SchemaVersion = domain.InstallationSchemaVersion
	}

	current, err := s.CurrentRelease(ctx)
	if err != nil {
		return err
	}

	// Re-applying the same release must not clobber the previous pointer:
	// after `apply` runs twice, rollback should still reach the release
	// before this one, not this one.
	if !current.IsZero() && !current.Version.Equal(r.Version) {
		if err := s.writeRelease(s.paths.PreviousReleaseFile(), current); err != nil {
			return err
		}
	}
	return s.writeRelease(s.paths.CurrentReleaseFile(), r)
}

func (s *Store) writeRelease(path string, r domain.ReleaseRecord) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return domain.Internal(err, "cannot serialise release record")
	}
	return atomicfs.WriteFile(path, append(data, '\n'), 0o640)
}

// AppendOperation writes one journal record.
//
// The append is a plain O_APPEND write rather than an atomic replace: JSONL
// exists precisely so that adding a record does not require rewriting the
// file, and O_APPEND writes under the pipe buffer size are atomic with respect
// to other appenders.
func (s *Store) AppendOperation(ctx context.Context, rec domain.OperationRecord) error {
	if rec.SchemaVersion == 0 {
		rec.SchemaVersion = domain.OperationSchemaVersion
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return domain.Internal(err, "cannot serialise operation record")
	}
	if len(data) > maxJournalLine {
		// Truncate the step list rather than dropping the record: a
		// journal entry that says "this happened, details elided" is
		// far more useful than a missing one.
		rec.Steps = nil
		rec.Flags = map[string]string{"truncated": "step details exceeded the journal line limit"}
		data, err = json.Marshal(rec)
		if err != nil {
			return domain.Internal(err, "cannot serialise operation record")
		}
	}

	if err := atomicfs.MkdirAll(s.paths.ManagerDir(), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(s.paths.JournalFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return domain.Internal(err, "cannot open the operation journal")
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return domain.Internal(err, "cannot append to the operation journal")
	}
	// The journal is the source of truth for --resume after a crash, so it
	// is worth an fsync on every record.
	if err := f.Sync(); err != nil {
		return domain.Internal(err, "cannot flush the operation journal")
	}
	return nil
}

// Operations reads the journal newest-first, collapsing records so each
// operation ID appears once with its latest state.
func (s *Store) Operations(ctx context.Context, filter ports.Filter) ([]domain.OperationRecord, error) {
	records, err := s.readJournal()
	if err != nil {
		return nil, err
	}

	latest := make(map[string]domain.OperationRecord, len(records))
	order := make([]string, 0, len(records))
	for _, rec := range records {
		if _, seen := latest[rec.ID]; !seen {
			order = append(order, rec.ID)
		}
		latest[rec.ID] = rec
	}

	out := make([]domain.OperationRecord, 0, len(order))
	for _, id := range order {
		rec := latest[id]
		if filter.ID != "" && rec.ID != filter.ID {
			continue
		}
		if filter.Type != "" && rec.Type != filter.Type {
			continue
		}
		if filter.Status != "" && rec.Status != filter.Status {
			continue
		}
		out = append(out, rec)
	}

	// Newest first, by start time then ID. ULIDs are already
	// lexicographically time-ordered, which makes the tiebreak meaningful
	// for records written within the same second.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt.Time) {
			return out[i].ID > out[j].ID
		}
		return out[i].StartedAt.After(out[j].StartedAt.Time)
	})

	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (s *Store) LastOperation(ctx context.Context) (domain.OperationRecord, bool, error) {
	recs, err := s.Operations(ctx, ports.Filter{Limit: 1})
	if err != nil || len(recs) == 0 {
		return domain.OperationRecord{}, false, err
	}
	return recs[0], true, nil
}

// UnfinishedOperations returns records left non-terminal or needing an
// operator's attention. These are what --resume acts on and what doctor and
// status keep surfacing until cleared.
func (s *Store) UnfinishedOperations(ctx context.Context) ([]domain.OperationRecord, error) {
	all, err := s.Operations(ctx, ports.Filter{})
	if err != nil {
		return nil, err
	}
	var out []domain.OperationRecord
	for _, rec := range all {
		if !rec.Status.Terminal() || rec.Status.NeedsAttention() {
			out = append(out, rec)
		}
	}
	return out, nil
}

// readJournal parses the JSONL file, tolerating a corrupt final line.
//
// A crash during the last append leaves a partial line. Discarding it is
// correct: the operation it described is, by definition, one that did not
// finish, and the record before it still tells the resume logic where things
// stood.
func (s *Store) readJournal() ([]domain.OperationRecord, error) {
	f, err := os.Open(s.paths.JournalFile())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, domain.InstallationError(err, "cannot read the operation journal")
	}
	defer func() { _ = f.Close() }()

	var records []domain.OperationRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxJournalLine)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec domain.OperationRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// Skip rather than fail: one unreadable record must not
			// make `status` and `doctor` unusable, and those are
			// exactly the commands an operator reaches for when the
			// journal got corrupted.
			continue
		}
		records = append(records, rec)
	}
	if err := sc.Err(); err != nil {
		return records, nil
	}
	return records, nil
}
