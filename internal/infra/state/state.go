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
	"path/filepath"
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
	if err := s.checkModeUnchanged(ctx, i); err != nil {
		return err
	}
	env := installationEnvelope{SchemaVersion: i.SchemaVersion, Installation: i}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return domain.Internal(err, "cannot serialise installation state")
	}
	return atomicfs.WriteFile(s.paths.InstallationState(), append(data, '\n'), 0o640)
}

// checkModeUnchanged enforces that `mode` is fixed when an installation is
// created.
//
// Here rather than in a command, because the claim is about *every* surface: not
// `config set`, not `import`, not any command that writes installation state.
// One chokepoint is also the only version of this rule that a command added
// later cannot forget to honour.
//
// Both directions are refused, which took a correction to get right. The first
// draft allowed dev → production on the reasoning that only the reverse was
// dangerous. It is not: production → dev puts real data under relaxed rules
// immediately, and dev → production presents untrusted history as trustworthy --
// you find out at rollback time that `previous` was pruned and no pre-update
// backup was ever taken. The second is quieter and lands when it costs most.
//
// A machine with no installation yet is *creating* one, which is when the choice
// is made. That is why `init --mode dev` and `import --mode dev` work and
// nothing else does.
func (s *Store) checkModeUnchanged(ctx context.Context, next domain.Installation) error {
	current, err := s.LoadInstallation(ctx)
	if err != nil {
		// No installation, or one this manager cannot read. Neither is a
		// mode change: the first is a creation, and the second fails on
		// its own terms at the next read rather than being reported here
		// as something about modes.
		return nil //nolint:nilerr // absence is creation, not a transition
	}
	if current.Mode == next.Mode {
		return nil
	}

	refusal := domain.ValidationError(nil,
		"mode is fixed when an installation is created: this one is %s",
		current.Mode.Describe())
	if next.Mode == domain.ModeDev {
		return refusal.WithHint("a production machine cannot be demoted to a sandbox; " +
			"its data would immediately be under relaxed rules")
	}
	return refusal.WithHint("a sandbox cannot be promoted to production -- its history is " +
		"not trustworthy, and you would find that out at rollback time. " +
		"Promotion is backup, fresh `init`, restore")
}

// migrateInstallation runs forward-only state migrations.
//
// The spec's rule is that a new manager must work with an old installation,
// and an old manager must refuse a new one clearly. The second half is handled
// by Installation.Validate, which rejects a schema version from the future.
func migrateInstallation(i domain.Installation) (domain.Installation, error) {
	for i.SchemaVersion < domain.InstallationSchemaVersion {
		switch i.SchemaVersion {
		case 2:
			// 2 -> 3 added backup.targets. There is nothing to
			// convert: an installation written before targets
			// existed has none, and the zero value is the correct
			// reading of that. The bump is entirely for the other
			// direction -- an older manager must refuse a state
			// whose targets it would ignore, take a backup, report
			// success, and leave it on the machine the operator
			// configured a target to survive.
			//
			// TestASchemaTwoInstallationStillLoads pins this half.
			i.SchemaVersion = 3
		case 3:
			// 3 -> 4 added notify.targets. Nothing to convert, for the
			// same reason as 2 -> 3: an installation written before
			// notification existed has no targets, and the zero value
			// reads that correctly. The bump is entirely for the other
			// direction -- an older manager must refuse a state whose
			// targets it would ignore while reporting success to an
			// operator who arranged to be told about failures.
			//
			// One line, and the loop's default returns "no migration
			// path" without it, which would fail every schema-3
			// installation on disk. TestASchemaThreeInstallationStillLoads
			// pins it.
			i.SchemaVersion = 4
		case 4:
			// 4 -> 5 added `mode`. Nothing to convert: an installation
			// written before modes existed is a production machine, and
			// the absent value reads that correctly.
			//
			// The bump is for the write path rather than the read one,
			// which is what distinguishes it from the two above. An
			// older manager reading a dev sandbox would treat it as
			// production -- stricter, and therefore safe -- but `config
			// set` rewrites the whole state, unknown fields are dropped
			// on the way through, and the sandbox would silently stop
			// being one. Refusing a state file from the future is the
			// only thing that prevents it.
			i.SchemaVersion = 5
		// case 1: there is no 1 -> 2 path. Schema 1 predates any
		// released manager, so nothing on disk is at it.
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

// UpdateCandidate reads what the channel last pointed at.
//
// A missing file is the ordinary state -- most machines follow no channel --
// and so is an unreadable one: this record is derived from a poll and rebuilt by
// the next, so refusing to report `status` because a disposable file is corrupt
// would take a diagnostic away over something that repairs itself.
func (s *Store) UpdateCandidate(ctx context.Context) (domain.UpdateCandidate, error) {
	data, err := os.ReadFile(s.paths.UpdateCandidateFile())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.UpdateCandidate{}, nil
		}
		return domain.UpdateCandidate{}, domain.InstallationError(err,
			"cannot read %s", s.paths.UpdateCandidateFile())
	}

	var rec domain.UpdateCandidate
	if err := json.Unmarshal(data, &rec); err != nil {
		return domain.UpdateCandidate{}, domain.InstallationError(err,
			"the update candidate at %s is not valid JSON",
			s.paths.UpdateCandidateFile())
	}
	return rec, nil
}

func (s *Store) SetUpdateCandidate(ctx context.Context, c domain.UpdateCandidate) error {
	if c.SchemaVersion == 0 {
		c.SchemaVersion = domain.UpdateCandidateSchemaVersion
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return domain.Internal(err, "cannot serialise the update candidate")
	}
	return atomicfs.WriteFile(s.paths.UpdateCandidateFile(), append(data, '\n'), 0o640)
}

// ClearUpdateCandidate forgets the candidate, which is what applying it means.
func (s *Store) ClearUpdateCandidate(ctx context.Context) error {
	if err := os.Remove(s.paths.UpdateCandidateFile()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return domain.InstallationError(err, "cannot remove %s",
			s.paths.UpdateCandidateFile())
	}
	return nil
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
	// is worth an fsync on every record -- and of the directory holding it
	// plus that directory's own entry in the parent: a fsynced file whose
	// entry chain was lost to the same power cut is a journal that never
	// existed. Both unconditionally: creation cannot be observed from here
	// (lock acquisition creates the manager directory before the first
	// append ever runs), and an append interrupted between fsyncs must not
	// leave later appends believing the chain is already durable.
	if err := f.Sync(); err != nil {
		return domain.Internal(err, "cannot flush the operation journal")
	}
	atomicfs.SyncDir(s.paths.ManagerDir())
	atomicfs.SyncDir(filepath.Dir(s.paths.ManagerDir()))
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

	// Read line by line rather than through a bufio.Scanner: a Scanner
	// stops at the first line longer than its buffer, which would silently
	// hide every record after one overlong or corrupt line -- including the
	// crashed operation --resume exists to find. An oversized line is
	// skipped like any other unreadable record, and reading continues.
	var (
		records []domain.OperationRecord
		r       = bufio.NewReaderSize(f, 64*1024)
		buf     []byte
		tooLong bool
	)
	for {
		chunk, isPrefix, err := r.ReadLine()
		if len(chunk) > 0 && !tooLong {
			if len(buf)+len(chunk) > maxJournalLine {
				// Discard the line but keep its memory bounded and
				// keep scanning to the next newline.
				tooLong = true
				buf = buf[:0]
			} else {
				buf = append(buf, chunk...)
			}
		}
		if err == nil && isPrefix {
			continue
		}

		if !tooLong {
			line := strings.TrimSpace(string(buf))
			if line != "" {
				var rec domain.OperationRecord
				if jsonErr := json.Unmarshal([]byte(line), &rec); jsonErr == nil {
					records = append(records, rec)
				}
				// Skip rather than fail: one unreadable record
				// must not make `status` and `doctor` unusable,
				// and those are exactly the commands an operator
				// reaches for when the journal got corrupted.
			}
		}
		buf, tooLong = buf[:0], false

		if err != nil {
			// io.EOF, or a read failure -- either way the records so
			// far are what there is, same tolerance as unreadable
			// lines.
			return records, nil
		}
	}
}
