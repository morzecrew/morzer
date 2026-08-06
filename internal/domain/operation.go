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
func (s OperationStatus) Terminal() bool {
	return s != StatusRunning
}

// NeedsAttention reports whether the status should keep surfacing in `status`
// and `doctor` until an operator clears it explicitly.
func (s OperationStatus) NeedsAttention() bool {
	return s == StatusManualIntervention
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
func (r OperationRecord) Duration() time.Duration {
	if r.StartedAt.IsZero() {
		return 0
	}
	end := r.FinishedAt.Time
	if end.IsZero() {
		end = time.Now().UTC()
	}
	return end.Sub(r.StartedAt.Time)
}

// FirstIncompleteStep returns the index of the step --resume should continue
// from, and whether resuming is possible at all.
//
// Resume is permitted only when every step before the resume point is
// idempotent: replaying a non-idempotent step would apply its effect twice,
// which is precisely the situation the operator is trying to escape.
func (r OperationRecord) FirstIncompleteStep() (idx int, resumable bool) {
	for i, s := range r.Steps {
		switch s.Status {
		case StepSucceeded, StepSkipped:
			if !s.Idempotent {
				// A completed non-idempotent step is fine -- we will
				// not re-run it. Keep scanning.
				continue
			}
		case StepPending, StepRunning, StepFailed, StepInterrupted:
			return i, true
		case StepCompensated:
			// Compensation already undid the work; resuming from a
			// compensated step would race its own cleanup.
			return i, false
		default:
			// A status this build does not recognise: a journal
			// written by a newer manager, or a record that only
			// partially unmarshalled. Refusing is the safe reading --
			// skipping it would treat unknown work as complete.
			return i, false
		}
	}
	return len(r.Steps), false
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
