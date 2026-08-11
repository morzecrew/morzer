package domain

import "time"

// OperationType names a mutating command. It is part of the journal contract,
// so values are stable.
type OperationType string

const (
	OpTypeInit     OperationType = "init"
	OpTypeApply    OperationType = "apply"
	OpTypeUpdate   OperationType = "update"
	OpTypeRollback OperationType = "rollback"
	OpTypeBackup   OperationType = "backup"
	OpTypeRestore  OperationType = "restore"
	OpTypeSecret   OperationType = "secret"
	OpTypeConfig   OperationType = "config"
	OpTypeRelease  OperationType = "release"

	// OpTypeExec is a command an operator ran inside a running service.
	//
	// The one journalled operation that mutates nothing on its own: what it
	// records is that a human was inside the deployment and what they asked
	// it to do, which is the fact an incident review needs and the one
	// nothing else in the journal would carry.
	OpTypeExec OperationType = "exec"

	// OpTypeImport rebuilds a machine from an installation export. It is
	// its own type rather than a flavour of init because an incident review
	// needs to see, at a glance, that this machine's identity was assumed
	// rather than created.
	OpTypeImport OperationType = "import"
)

// OperationStatus is the lifecycle of one operation.
//
// The distinction that matters: `failed` means the system is where it started,
// `compensated` means the engine successfully undid partial work, and
// `requires-manual-intervention` means it could not -- an operator must look.
// Collapsing these would hide exactly the case that needs a human.
type OperationStatus string

const (
	StatusRunning            OperationStatus = "running"
	StatusSucceeded          OperationStatus = "succeeded"
	StatusFailed             OperationStatus = "failed"
	StatusCompensated        OperationStatus = "compensated"
	StatusInterrupted        OperationStatus = "interrupted"
	StatusManualIntervention OperationStatus = "requires-manual-intervention"
)

// Terminal reports whether the status is final. A non-terminal record in the
// journal is what `--resume` looks for and what `doctor` flags.
//
// The finished states are enumerated rather than derived from "not running",
// so a status this build does not recognise -- a record written by a newer
// manager, a damaged line -- is not finished. Deriving it the other way made
// every unknown status silently complete and invisible.
func (s OperationStatus) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCompensated,
		StatusInterrupted, StatusManualIntervention:
		return true
	default:
		return false
	}
}

// NeedsAttention reports whether the status should keep surfacing in `status`
// and `doctor` until an operator clears it explicitly.
//
// Fail-safe in the same direction, and by enumerating the negative: the states
// that do *not* need a human are listed, so an unrecognised one gets looked at.
// A record this manager cannot interpret is exactly the case a human should
// see, and `--clear-intervention` can acknowledge it.
func (s OperationStatus) NeedsAttention() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCompensated,
		StatusInterrupted, StatusRunning:
		return false
	default:
		return true
	}
}

// StepStatus is the per-step outcome.
type StepStatus string

const (
	StepPending     StepStatus = "pending"
	StepRunning     StepStatus = "running"
	StepSucceeded   StepStatus = "succeeded"
	StepSkipped     StepStatus = "skipped" // Check reported the postcondition already held
	StepFailed      StepStatus = "failed"
	StepCompensated StepStatus = "compensated"
	StepInterrupted StepStatus = "interrupted"
)

// OperationSchemaVersion versions the journal record shape.
const OperationSchemaVersion = 1

// OperationRecord is one line of the append-only journal. It is the source of
// truth for --resume, status, and audit.
//
// It contains no secrets: redaction happens before writing, not at display
// time, because a journal that must be redacted on read is a journal that
// leaks the moment someone cats it.
type OperationRecord struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	Type          OperationType   `json:"type"`
	Status        OperationStatus `json:"status"`

	From Version `json:"from,omitempty"`
	To   Version `json:"to,omitempty"`

	StartedAt  Time `json:"started_at"`
	FinishedAt Time `json:"finished_at,omitempty"`

	Steps []StepRecord `json:"steps,omitempty"`

	ManagerVersion string `json:"manager_version"`
	InstallationID string `json:"installation_id,omitempty"`
	DryRun         bool   `json:"dry_run,omitempty"`

	// Flags records consequential choices the operator made, so an
	// incident review can see that e.g. --skip-backup was used.
	Flags map[string]string `json:"flags,omitempty"`

	Error *Error `json:"error,omitempty"`
}

// Duration is the wall-clock time the operation took, or time so far when it
// is still running.
//
// The clock read is the only one in this package, and it is confined to this
// wrapper so the arithmetic stays pure and testable: DurationAt is what a test
// -- or anything holding its own clock -- calls.
func (r OperationRecord) Duration() time.Duration {
	return r.DurationAt(time.Now().UTC())
}

// DurationAt is Duration as of a given instant. A record that has finished
// ignores it: the answer is a property of the record, not of when it is asked.
func (r OperationRecord) DurationAt(now time.Time) time.Duration {
	if r.StartedAt.IsZero() {
		return 0
	}
	end := r.FinishedAt.Time
	if end.IsZero() {
		end = now
	}
	return end.Sub(r.StartedAt.Time)
}

// FirstIncompleteStep returns the index of the step --resume should continue
// from, and whether resuming is possible at all.
//
// The whole list is validated, not only the prefix before the resume point: a
// status this build does not recognise, or a step compensation already
// unwound, poisons the record wherever it sits, because the journal was
// written by a run this manager cannot fully interpret. What happens to the
// steps *before* the returned index is the engine's decision, not this
// record's -- completed idempotent steps re-run to rebuild in-memory step
// state, completed non-idempotent ones keep their journaled credit.
func (r OperationRecord) FirstIncompleteStep() (idx int, resumable bool) {
	idx, resumable = len(r.Steps), false
	for i, s := range r.Steps {
		switch s.Status {
		case StepSucceeded, StepSkipped:
			// Completed; keep scanning.
		case StepPending, StepRunning, StepFailed, StepInterrupted:
			if !resumable {
				idx, resumable = i, true
			}
		case StepCompensated:
			// Compensation already undid work in this record;
			// resuming would race its own cleanup.
			return i, false
		default:
			// A status this build does not recognise: a journal
			// written by a newer manager, or a record that only
			// partially unmarshalled. Refusing is the safe reading --
			// skipping it would treat unknown work as complete.
			return i, false
		}
	}
	return idx, resumable
}

// StepRecord is the journaled state of one step.
type StepRecord struct {
	ID         string     `json:"id"`
	Status     StepStatus `json:"status"`
	DurationMS int64      `json:"duration_ms"`
	Idempotent bool       `json:"idempotent,omitempty"`
	Message    string     `json:"message,omitempty"`
	Error      string     `json:"error,omitempty"`
}
