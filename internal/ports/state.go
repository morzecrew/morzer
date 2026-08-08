package ports

import (
	"context"

	"github.com/morzecrew/morzer/internal/domain"
)

// StateStore persists everything the manager knows between invocations: the
// installation, which release is current, and the operation journal.
//
// Every write is atomic (temp, fsync, rename, fsync of the directory). A
// crash must never leave a half-written state file, because the next command
// would then refuse to run against state it cannot parse.
type StateStore interface {
	LoadInstallation(ctx context.Context) (domain.Installation, error)
	SaveInstallation(ctx context.Context, i domain.Installation) error

	// InstallationExists reports whether state has been initialised. `init`
	// checks it to refuse overwriting; every other command checks it to
	// produce a useful error instead of a parse failure.
	InstallationExists(ctx context.Context) (bool, error)

	CurrentRelease(ctx context.Context) (domain.ReleaseRecord, error)
	PreviousRelease(ctx context.Context) (domain.ReleaseRecord, error)

	// SetCurrentRelease promotes r to current and demotes the existing
	// current to previous, in that order and atomically enough that a
	// crash leaves one of the two consistent states, never a third.
	SetCurrentRelease(ctx context.Context, r domain.ReleaseRecord) error

	// AppendOperation writes one journal record. Records are appended, not
	// updated: a running operation is written once at start and once at
	// each transition, and the last record for an ID wins.
	AppendOperation(ctx context.Context, rec domain.OperationRecord) error

	// Operations reads the journal newest-first.
	Operations(ctx context.Context, filter Filter) ([]domain.OperationRecord, error)

	// LastOperation returns the most recent record, or false when the
	// journal is empty.
	LastOperation(ctx context.Context) (domain.OperationRecord, bool, error)

	// UnfinishedOperations returns records left non-terminal, which is what
	// --resume acts on and what doctor reports.
	UnfinishedOperations(ctx context.Context) ([]domain.OperationRecord, error)
}

// Filter selects journal records.
type Filter struct {
	// Type limits to one operation type; empty means all.
	Type domain.OperationType

	// Status limits to one status; empty means all.
	Status domain.OperationStatus

	// ID selects a single operation.
	ID string

	// Limit caps the number of records returned. Zero means no cap.
	Limit int
}

// Locker provides the deployment lock. Two concurrent mutating operations on
// one installation is the race the whole design assumes away, so acquiring
// this is the first step of every mutating command.
type Locker interface {
	// Acquire takes the named lock. The returned function releases it and
	// must be called even on the error paths of the caller -- defer it.
	//
	// A held lock returns an error wrapping domain.ErrLocked carrying the
	// current owner, so the operator learns which operation is in the way
	// rather than "resource busy".
	Acquire(ctx context.Context, name string, opts LockOptions) (release func() error, err error)

	// Owner reports who holds the lock without attempting to take it --
	// what `status` and `doctor` use.
	Owner(ctx context.Context, name string) (LockOwner, bool, error)
}

type LockOptions struct {
	// Wait blocks until the lock is free instead of failing immediately.
	Wait bool

	// Owner is recorded in the lock file so a blocked operator can see
	// what holds it.
	Owner LockOwner
}

// LockOwner is the metadata written into the lock file.
type LockOwner struct {
	PID         int         `json:"pid"`
	OperationID string      `json:"operation_id"`
	Type        string      `json:"type"`
	StartedAt   domain.Time `json:"started_at"`
	Host        string      `json:"host,omitempty"`

	// PIDStart distinguishes the process that took the lock from whatever
	// now happens to wear its PID.
	//
	// A holder killed with SIGKILL releases its flock and leaves this record
	// behind; the kernel is then free to hand that PID to something else, and
	// a liveness probe answers "still running" about a process that has
	// nothing to do with this deployment. The kernel's own start time for the
	// PID settles it: recycled PIDs get a new one.
	//
	// Zero on a record written before this field existed, and on any platform
	// that cannot report it -- both fall back to the PID alone, which is the
	// behaviour that came before.
	PIDStart uint64 `json:"pid_start,omitempty"`
}
