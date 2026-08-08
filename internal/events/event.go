// Package events defines the typed events the engine emits and the bus that
// carries them to presenters.
//
// The spec's package sketch places this under internal/lifecycle. It lives one
// level up instead, because ports.Notifier takes an Event: if the type lived
// inside lifecycle, the ports package would have to import the layer that
// consumes it, and the dependency arrows would stop pointing downward. The
// package still imports nothing but domain and stdlib, so nothing is lost.
package events

import (
	"time"

	"github.com/morzecrew/morzer/internal/domain"
)

// Kind identifies an event. Values are stable: JSONL event streams are a
// monitoring surface.
type Kind string

const (
	KindOperationStarted  Kind = "operation.started"
	KindOperationFinished Kind = "operation.finished"

	KindStepStarted  Kind = "step.started"
	KindStepProgress Kind = "step.progress"
	KindStepFinished Kind = "step.finished"

	// KindStepOutput carries a line of subprocess output. The live view
	// tails it; the log keeps all of it.
	KindStepOutput Kind = "step.output"

	// KindPlan is emitted once by a dry run, carrying the whole intended
	// step list before anything executes.
	KindPlan Kind = "plan"

	// KindMessage is engine-level narration that is not tied to a step.
	KindMessage Kind = "message"

	// KindCheck is one diagnostic result from doctor.
	KindCheck Kind = "check"
)

// AllKinds is every event kind, so a consumer that must classify all of them
// can be checked against the list rather than against a copy of it.
//
// The notification allowlist is the case this exists for: a Kind added here and
// not classified there would otherwise be silently not forwarded, which is
// indistinguishable from a deliberate decision not to forward it.
var AllKinds = []Kind{
	KindOperationStarted,
	KindOperationFinished,
	KindStepStarted,
	KindStepProgress,
	KindStepFinished,
	KindStepOutput,
	KindPlan,
	KindMessage,
	KindCheck,
}

// Level classifies a message for presenters that style by severity.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Event is one thing that happened. It is a single struct rather than an
// interface hierarchy: presenters switch on Kind, and a flat struct
// serialises to JSONL without a discriminator dance.
//
// Events carry no secrets. The engine redacts before publishing, so a
// presenter can never be the thing that leaks one.
type Event struct {
	Kind Kind      `json:"kind"`
	At   time.Time `json:"at"`

	OpID   string               `json:"operation_id,omitempty"`
	OpType domain.OperationType `json:"operation_type,omitempty"`

	StepID    string `json:"step_id,omitempty"`
	StepIndex int    `json:"step_index,omitempty"`
	StepCount int    `json:"step_count,omitempty"`

	// Description is the human label for the step or operation.
	Description string `json:"description,omitempty"`

	// Steps are the descriptions of every step, in order, set only on
	// operation.started.
	//
	// It is here because the live view draws the whole list from the first
	// event and dims what has not run yet -- a presenter never asks the
	// engine for anything, so a view that needs more data means the event
	// carries more data. Plain mode names each step as it starts instead,
	// so nothing is visible only in rich; the difference is when.
	Steps []string `json:"steps,omitempty"`

	Level   Level  `json:"level,omitempty"`
	Message string `json:"message,omitempty"`

	// Status is the step or operation outcome on a *.finished event.
	Status string `json:"status,omitempty"`

	// Duration is how long the step or operation took.
	Duration time.Duration `json:"duration_ms,omitempty"`

	// Progress is fractional completion in [0,1] for a step that can
	// report it. Negative means "unknown", which is different from zero.
	Progress float64 `json:"progress,omitempty"`

	// Detail is the current sub-activity, e.g. "pulling layer 7/11".
	Detail string `json:"detail,omitempty"`

	// Plan is the full step list, set only on KindPlan.
	Plan []PlanStep `json:"plan,omitempty"`

	// Check is a doctor result, set only on KindCheck.
	Check *CheckResult `json:"check,omitempty"`

	// Err is the failure, set on finished events that failed.
	Err *domain.Error `json:"error,omitempty"`

	// DryRun marks events from a planning run so a presenter can label
	// them as intentions rather than facts.
	DryRun bool `json:"dry_run,omitempty"`
}

// PlanStep is one entry in a dry-run plan.
type PlanStep struct {
	ID          string `json:"id"`
	Description string `json:"description"`

	// WillRun is false when the step's Check reported its postcondition
	// already holds -- the difference between a plan and a list.
	WillRun bool `json:"will_run"`

	// Reason explains a skip.
	Reason string `json:"reason,omitempty"`

	// Diff is a unified diff of an intended configuration change, when the
	// step can produce one.
	Diff string `json:"diff,omitempty"`
}

// CheckStatus is a doctor result. The exit code reflects the worst one seen.
type CheckStatus string

const (
	CheckOK   CheckStatus = "ok"
	CheckWarn CheckStatus = "warn"
	CheckFail CheckStatus = "fail"
)

// Worse returns the more severe of two statuses, so a doctor run can fold its
// results into one exit code.
func (s CheckStatus) Worse(o CheckStatus) CheckStatus {
	rank := map[CheckStatus]int{CheckOK: 0, CheckWarn: 1, CheckFail: 2}
	if rank[o] > rank[s] {
		return o
	}
	return s
}

// CheckResult is one diagnostic. Every non-ok result carries a Remedy: a
// diagnostic that tells an operator something is wrong without telling them
// what to do about it has done half a job.
type CheckResult struct {
	ID          string      `json:"id"`
	Category    string      `json:"category"`
	Description string      `json:"description"`
	Status      CheckStatus `json:"status"`
	Message     string      `json:"message,omitempty"`
	Remedy      string      `json:"remedy,omitempty"`

	Duration time.Duration `json:"-"`
}

// Constructors. Presenters rely on At always being set, so events are built
// here rather than as struct literals at call sites.

func OperationStarted(opID string, opType domain.OperationType, desc string, steps []string, dryRun bool) Event {
	return Event{
		Kind: KindOperationStarted, At: time.Now(), OpID: opID, OpType: opType,
		Description: desc, StepCount: len(steps), Steps: steps,
		DryRun: dryRun, Level: LevelInfo,
	}
}

func OperationFinished(opID string, opType domain.OperationType, status domain.OperationStatus, d time.Duration, err *domain.Error) Event {
	lvl := LevelInfo
	if err != nil {
		lvl = LevelError
	}
	return Event{
		Kind: KindOperationFinished, At: time.Now(), OpID: opID, OpType: opType,
		Status: string(status), Duration: d, Err: err, Level: lvl,
	}
}

func StepStarted(opID, stepID, desc string, idx, count int) Event {
	return Event{
		Kind: KindStepStarted, At: time.Now(), OpID: opID, StepID: stepID,
		Description: desc, StepIndex: idx, StepCount: count, Level: LevelInfo, Progress: -1,
	}
}

func StepProgress(opID, stepID string, progress float64, detail string) Event {
	return Event{
		Kind: KindStepProgress, At: time.Now(), OpID: opID, StepID: stepID,
		Progress: progress, Detail: detail, Level: LevelDebug,
	}
}

func StepOutput(opID, stepID, line string) Event {
	return Event{
		Kind: KindStepOutput, At: time.Now(), OpID: opID, StepID: stepID,
		Message: line, Level: LevelDebug,
	}
}

func StepFinished(opID, stepID string, status domain.StepStatus, d time.Duration, err *domain.Error) Event {
	lvl := LevelInfo
	if err != nil {
		lvl = LevelError
	}
	return Event{
		Kind: KindStepFinished, At: time.Now(), OpID: opID, StepID: stepID,
		Status: string(status), Duration: d, Err: err, Level: lvl,
	}
}

func Plan(opID string, opType domain.OperationType, steps []PlanStep) Event {
	return Event{
		Kind: KindPlan, At: time.Now(), OpID: opID, OpType: opType,
		Plan: steps, DryRun: true, Level: LevelInfo,
	}
}

func Message(level Level, format string, args ...any) Event {
	return Event{Kind: KindMessage, At: time.Now(), Level: level, Message: sprintf(format, args...)}
}

func Check(r CheckResult) Event {
	lvl := LevelInfo
	switch r.Status {
	case CheckWarn:
		lvl = LevelWarn
	case CheckFail:
		lvl = LevelError
	}
	return Event{Kind: KindCheck, At: time.Now(), Check: &r, Level: lvl}
}
