package engine

import (
	"context"
	"errors"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/logging"
	"github.com/morzecrew/morzer/internal/ports"
)

// Operation is a named sequence of steps.
type Operation struct {
	ID   string
	Type domain.OperationType

	// Description is the human title, e.g. "update 1.1.0 -> 1.2.0".
	Description string

	Steps []Step

	// From and To are recorded in the journal for version transitions.
	From domain.Version
	To   domain.Version

	// Flags records consequential operator choices, e.g. skip-backup.
	Flags map[string]string
}

// Options configures one run.
type Options struct {
	DryRun bool

	// Resume continues an interrupted operation from its first incomplete
	// step. The prior record supplies which steps already completed.
	Resume bool

	// Prior is the journal record being resumed. Ignored unless Resume.
	Prior *domain.OperationRecord

	// ManagerVersion and InstallationID are stamped into the journal.
	ManagerVersion string
	InstallationID string

	// Timeout bounds the whole operation. Zero means unbounded, in which
	// case only signals stop it.
	Timeout time.Duration
}

// Engine runs operations.
//
// It owns ordering, journaling, and compensation. It knows nothing about
// Docker, SOPS, or any tool: steps close over ports, and the engine only calls
// the four functions on a Step.
type Engine struct {
	state ports.StateStore
	bus   *events.Bus
}

func New(state ports.StateStore, bus *events.Bus) *Engine {
	if bus == nil {
		bus = events.NewBus()
	}
	return &Engine{state: state, bus: bus}
}

// Bus exposes the event bus so presenters can subscribe.
func (e *Engine) Bus() *events.Bus { return e.bus }

// Result is the outcome of a run.
type Result struct {
	Record domain.OperationRecord
	State  *State

	// Err is the failure, already classified. It is returned separately as
	// well; the field exists so a caller holding only a Result can inspect
	// it.
	Err error
}

// Run executes an operation.
//
// The contract, in order of precedence:
//
//  1. Every state transition is journaled before and after execution, so a
//     crash mid-step is always recoverable to a known position.
//  2. A failed step's policy decides what happens next.
//  3. Compensation runs newest-first over completed compensable steps.
//  4. A non-compensable step that fails after mutating moves the operation to
//     requires-manual-intervention, which keeps surfacing in status and doctor
//     until an operator clears it.
func (e *Engine) Run(ctx context.Context, op Operation, opts Options) (Result, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	ctx = logging.WithOperation(ctx, op.ID, op.Type)
	log := logging.FromContext(ctx)

	st := newState(op.ID, op.Type, opts.DryRun, e.bus)
	st.Resumed = opts.Resume

	rec := domain.OperationRecord{
		SchemaVersion:  domain.OperationSchemaVersion,
		ID:             op.ID,
		Type:           op.Type,
		Status:         domain.StatusRunning,
		From:           op.From,
		To:             op.To,
		StartedAt:      domain.NewTime(time.Now()),
		ManagerVersion: opts.ManagerVersion,
		InstallationID: opts.InstallationID,
		DryRun:         opts.DryRun,
		Flags:          op.Flags,
		Steps:          make([]domain.StepRecord, len(op.Steps)),
	}
	for i, s := range op.Steps {
		rec.Steps[i] = domain.StepRecord{ID: s.ID, Status: domain.StepPending, Idempotent: s.Idempotent}
	}

	// A dry run plans and prints; it must not touch the journal, because a
	// planning run is not something that happened to the installation.
	if opts.DryRun {
		return e.plan(ctx, op, st, rec)
	}

	startAt := 0
	if opts.Resume {
		var err error
		startAt, err = e.resumePoint(op, opts, &rec)
		if err != nil {
			return Result{Record: rec, State: st, Err: err}, err
		}
		st.Info("resuming %s from step %d of %d (%s)", op.Type, startAt+1, len(op.Steps), op.Steps[startAt].ID)
	}

	e.journal(ctx, rec)
	e.bus.Publish(events.OperationStarted(op.ID, op.Type, op.Description,
		stepDescriptions(op.Steps), false))

	completed := make([]int, 0, len(op.Steps))
	var failure error
	var failedIdx = -1

	for i := 0; i < len(op.Steps); i++ {
		step := op.Steps[i]

		// On resume, completed non-idempotent work keeps its journaled
		// credit -- the refusal rules in resumePoint exist so it is
		// never repeated. Completed *idempotent* steps re-run instead:
		// their effects are declared safe to repeat, and re-running is
		// what rebuilds the in-memory step state later steps consume,
		// which the journal does not carry across processes.
		if i < startAt && !step.Idempotent {
			continue
		}

		// Check cancellation between steps as well as inside them: a
		// signal arriving while a step was running should not start the
		// next one.
		if err := ctx.Err(); err != nil {
			failure = interruptedErr(err, step.ID)
			failedIdx = i
			rec.Steps[i].Status = domain.StepInterrupted
			break
		}

		status, stepErr := e.runStep(ctx, step, st, i, len(op.Steps), &rec)
		rec.Steps[i].Status = status
		e.journal(ctx, rec)

		if stepErr == nil {
			if status == domain.StepSucceeded || status == domain.StepSkipped {
				completed = append(completed, i)
			}
			continue
		}

		if step.OnFailure == Continue {
			log.Warn("step failed but the operation continues",
				"step_id", step.ID, "error", stepErr.Error())
			st.Warn("step %q failed (continuing): %v", step.ID, domain.AsError(stepErr).Message)
			continue
		}

		failure = stepErr
		failedIdx = i
		break
	}

	if failure == nil {
		rec.Status = domain.StatusSucceeded
		rec.FinishedAt = domain.NewTime(time.Now())
		e.journal(ctx, rec)
		e.bus.Publish(events.OperationFinished(op.ID, op.Type, rec.Status, rec.Duration(), nil))
		return Result{Record: rec, State: st}, nil
	}

	return e.handleFailure(ctx, op, opts, st, &rec, completed, failedIdx, failure)
}

// runStep executes one step through its full lifecycle.
func (e *Engine) runStep(ctx context.Context, step Step, st *State, idx, total int, rec *domain.OperationRecord) (domain.StepStatus, error) {
	stepCtx := logging.WithStep(ctx, step.ID)
	log := logging.FromContext(stepCtx)

	if step.Timeout > 0 {
		var cancel context.CancelFunc
		stepCtx, cancel = context.WithTimeout(stepCtx, step.Timeout)
		defer cancel()
	}

	st.stepID = step.ID
	started := time.Now()

	rec.Steps[idx].Status = domain.StepRunning
	// Journal the transition *before* the work, so a crash inside Execute
	// leaves a record saying which step was in flight.
	e.journal(ctx, *rec)
	e.bus.Publish(events.StepStarted(st.OpID, step.ID, step.Description, idx, total))

	finish := func(status domain.StepStatus, err error) (domain.StepStatus, error) {
		d := time.Since(started)
		rec.Steps[idx].DurationMS = d.Milliseconds()
		var domErr *domain.Error
		if err != nil {
			domErr = domain.AsError(err).WithOp(st.OpID, step.ID)
			rec.Steps[idx].Error = domErr.Message
		}
		e.bus.Publish(events.StepFinished(st.OpID, step.ID, status, d, domErr))
		return status, err
	}

	// Check first: a satisfied postcondition means the work is already
	// done, which is what makes apply idempotent.
	if step.Check != nil {
		done, err := step.Check(stepCtx, st)
		if err != nil {
			log.Debug("step check failed", "error", err)
			return finish(domain.StepFailed, e.classify(ctx, err, step))
		}
		if done {
			rec.Steps[idx].Message = "already satisfied"
			log.Debug("step skipped: postcondition already holds")
			return finish(domain.StepSkipped, nil)
		}
	}

	if step.Execute != nil {
		if err := step.Execute(stepCtx, st); err != nil {
			log.Debug("step execute failed", "error", err)
			return finish(domain.StepFailed, e.classify(ctx, err, step))
		}
	}

	// Verify is separate from Execute because a tool exiting zero is not
	// the same claim as the system being in the desired state.
	if step.Verify != nil {
		if err := step.Verify(stepCtx, st); err != nil {
			log.Debug("step verify failed", "error", err)
			return finish(domain.StepFailed, e.classify(ctx, err, step))
		}
	}

	return finish(domain.StepSucceeded, nil)
}

// classify turns a context cancellation into an interruption rather than a
// step failure. A tool killed by our own SIGTERM must not be reported as a
// broken tool.
//
// Only the *parent* context decides that: the operator's signal or the
// operation's --timeout. A step exceeding its own per-step budget while the
// operation is live is an ordinary step failure -- treating it as an
// interruption would skip compensation, which for a rollback whose
// start-services step timed out means a moved release pointer nothing rolls
// back.
func (e *Engine) classify(parent context.Context, err error, step Step) error {
	if err == nil {
		return nil
	}
	isCtx := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	if !isCtx {
		return err
	}
	if parent.Err() != nil {
		return interruptedErr(parent.Err(), step.ID)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.Internal(err, "step %q timed out", step.ID).
			WithHint("the step's failure policy applies; investigate why it is slow")
	}
	return err
}

func interruptedErr(cause error, stepID string) error {
	if errors.Is(cause, context.DeadlineExceeded) {
		return domain.Interrupted("timed out during step %q", stepID).
			WithHint("raise --timeout, or investigate why the step is slow")
	}
	return domain.Interrupted("cancelled during step %q", stepID)
}

// handleFailure runs compensation and settles the final status.
func (e *Engine) handleFailure(
	ctx context.Context,
	op Operation,
	opts Options,
	st *State,
	rec *domain.OperationRecord,
	completed []int,
	failedIdx int,
	failure error,
) (Result, error) {
	log := logging.FromContext(ctx)
	rec.Error = domain.AsError(failure).WithOp(op.ID, stepIDAt(op, failedIdx))

	interrupted := errors.Is(failure, domain.ErrInterrupted)
	policy := Abort
	if failedIdx >= 0 && failedIdx < len(op.Steps) {
		policy = op.Steps[failedIdx].OnFailure
	}

	// A step that declared itself unrepairable is the case that needs a
	// human. Detect it before compensating, because compensation of
	// *earlier* steps does not repair this one.
	failedStepMutatedIrreversibly := failedIdx >= 0 &&
		failedIdx < len(op.Steps) &&
		op.Steps[failedIdx].RequiresInterventionOnFailure &&
		rec.Steps[failedIdx].Status == domain.StepFailed &&
		policy == Compensate

	// The failing step compensates first, when it can.
	//
	// A step that failed in Verify ran its Execute to completion, and a
	// step that failed in Execute may have got halfway. Either way it may
	// have mutated, and its Compensate function is the author's statement
	// that it knows how to undo that. Skipping it because the step is not
	// "completed" would leave exactly the partial state compensation
	// exists to clean up.
	toCompensate := completed
	if failedIdx >= 0 && failedIdx < len(op.Steps) &&
		op.Steps[failedIdx].Compensate != nil &&
		rec.Steps[failedIdx].Status == domain.StepFailed {
		toCompensate = append(append([]int(nil), completed...), failedIdx)
	}

	switch {
	case interrupted:
		rec.Status = domain.StatusInterrupted
	case policy == Compensate:
		compensated := e.compensate(ctx, op, st, rec, toCompensate)
		switch {
		case failedStepMutatedIrreversibly:
			rec.Status = domain.StatusManualIntervention
			rec.Error = domain.ManualIntervention(failure,
				"step %q failed after making changes it cannot undo", op.Steps[failedIdx].ID).
				WithOp(op.ID, op.Steps[failedIdx].ID).
				WithHint("run `morzer doctor` for the current state, then repair manually. " +
					"The operation stays flagged until you clear it with `morzer status --clear-intervention`.")
		case compensated:
			rec.Status = domain.StatusCompensated
			rec.Error = domain.Compensated(failure,
				"%s failed at step %q; earlier changes were rolled back", op.Type, stepIDAt(op, failedIdx)).
				WithOp(op.ID, stepIDAt(op, failedIdx))
		default:
			rec.Status = domain.StatusManualIntervention
			rec.Error = domain.ManualIntervention(failure,
				"%s failed at step %q and rollback did not complete", op.Type, stepIDAt(op, failedIdx)).
				WithOp(op.ID, stepIDAt(op, failedIdx)).
				WithHint("run `morzer doctor` to see what state the system is in")
		}
	default:
		rec.Status = domain.StatusFailed
	}

	rec.FinishedAt = domain.NewTime(time.Now())
	e.journal(ctx, *rec)
	e.bus.Publish(events.OperationFinished(op.ID, op.Type, rec.Status, rec.Duration(), rec.Error))

	log.Error("operation failed", "status", string(rec.Status), "error", rec.Error.Error())

	// The returned error is the classified one, so main's exit-code
	// mapping sees compensated (11) or manual-intervention (12) rather
	// than the underlying cause.
	err := error(rec.Error)
	return Result{Record: *rec, State: st, Err: err}, err
}

// compensate runs Compensate for completed steps, newest first.
//
// It returns true only when every compensable step undid itself. A step
// without a Compensate function is not a failure of compensation -- it simply
// had nothing to undo, which is why read-only steps do not block the
// compensated status.
func (e *Engine) compensate(ctx context.Context, op Operation, st *State, rec *domain.OperationRecord, completed []int) bool {
	log := logging.FromContext(ctx)
	allOK := true

	// Compensation must run even when the operation was cancelled, so it
	// gets a fresh context with its own budget. Using the cancelled one
	// would abort every compensator immediately and guarantee the very
	// half-applied state compensation exists to prevent.
	compCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), compensationBudget)
	defer cancel()

	for i := len(completed) - 1; i >= 0; i-- {
		idx := completed[i]
		step := op.Steps[idx]
		if step.Compensate == nil {
			continue
		}
		if rec.Steps[idx].Status == domain.StepSkipped {
			// The step never ran, so there is nothing to undo, and
			// undoing it would revert state it did not create.
			continue
		}

		st.stepID = step.ID
		e.bus.Publish(events.Message(events.LevelWarn, "rolling back: %s", step.Description))

		if err := step.Compensate(compCtx, st); err != nil {
			allOK = false
			rec.Steps[idx].Error = "compensation failed: " + domain.AsError(err).Message
			log.Error("compensation failed", "step_id", step.ID, "error", err)
			e.bus.Publish(events.Message(events.LevelError,
				"rollback of %q failed: %v", step.ID, domain.AsError(err).Message))
			// Keep going: later-registered steps may still undo
			// their own work, and stopping here would strand more
			// state than continuing.
			continue
		}
		rec.Steps[idx].Status = domain.StepCompensated
	}

	return allOK
}

// compensationBudget bounds rollback. It is generous because compensation
// often means stopping containers and restoring a symlink, but it is bounded
// because a rollback that hangs forever is worse than one that gives up and
// says so.
const compensationBudget = 10 * time.Minute

// plan runs every Check and emits the intended step list without mutating.
func (e *Engine) plan(ctx context.Context, op Operation, st *State, rec domain.OperationRecord) (Result, error) {
	steps := make([]events.PlanStep, 0, len(op.Steps))

	for i, step := range op.Steps {
		st.stepID = step.ID
		ps := events.PlanStep{ID: step.ID, Description: step.Description, WillRun: true}

		if step.Check != nil {
			done, err := step.Check(ctx, st)
			switch {
			case err != nil:
				// A Check that cannot answer is reported, not
				// fatal: the point of a plan is to show as much
				// as can be known.
				ps.Reason = "cannot determine: " + domain.AsError(err).Message
			case done:
				ps.WillRun = false
				ps.Reason = "already satisfied"
			}
		}

		if step.PlanDetail != nil {
			detail, diff := step.PlanDetail(ctx, st)
			if detail != "" {
				ps.Description = step.Description + " — " + detail
			}
			ps.Diff = diff
		}

		steps = append(steps, ps)
		rec.Steps[i].Status = domain.StepPending
	}

	e.bus.Publish(events.Plan(op.ID, op.Type, steps))

	rec.Status = domain.StatusSucceeded
	rec.FinishedAt = domain.NewTime(time.Now())
	return Result{Record: rec, State: st}, nil
}

// resumePoint determines where a resumed operation continues, and refuses when
// resuming would be unsafe.
func (e *Engine) resumePoint(op Operation, opts Options, rec *domain.OperationRecord) (int, error) {
	if opts.Prior == nil {
		return 0, domain.Usage("nothing to resume: no interrupted operation was found").
			WithHint("run the command without --resume to start a new operation")
	}
	prior := *opts.Prior

	if prior.Type != op.Type {
		return 0, domain.Usage("cannot resume a %s operation with %s", prior.Type, op.Type).
			WithHint("re-run `morzer %s --resume`", prior.Type)
	}
	if prior.Status.Terminal() && prior.Status != domain.StatusInterrupted && prior.Status != domain.StatusFailed {
		return 0, domain.Usage("operation %s finished as %s and cannot be resumed", prior.ID, prior.Status)
	}

	idx, resumable := prior.FirstIncompleteStep()
	if !resumable {
		return 0, domain.Usage("operation %s cannot be resumed safely", prior.ID).
			WithHint("its journal holds a step in a state this manager cannot " +
				"continue from; run `morzer doctor` and start a fresh operation")
	}

	// The step list being resumed must be the list that was interrupted --
	// checked in full, before the resume index is used, so a journal with
	// *more* steps than this manager plans reports list drift rather than a
	// misleading completion. Credit for completed steps is carried by
	// position: after a manager upgrade that inserted,
	// removed or reordered steps, that credit would land on the wrong steps
	// and silently skip or re-run a mutating one.
	if len(prior.Steps) != len(op.Steps) {
		return 0, domain.Usage(
			"operation %s journaled %d steps but this manager plans %d",
			prior.ID, len(prior.Steps), len(op.Steps)).
			WithHint("the step list changed since the operation was interrupted; " +
				"run `morzer doctor` and start a fresh operation")
	}
	for i := range op.Steps {
		if op.Steps[i].ID != prior.Steps[i].ID {
			return 0, domain.Usage(
				"operation %s journaled step %d as %q but this manager plans %q",
				prior.ID, i+1, prior.Steps[i].ID, op.Steps[i].ID).
				WithHint("the step list changed since the operation was interrupted; " +
					"run `morzer doctor` and start a fresh operation")
		}
	}
	// No idx bounds check: resumable implies idx < len(prior.Steps), and
	// the length equality above makes that idx < len(op.Steps); a record
	// with nothing incomplete was already refused as not resumable.

	// The step being re-run is the safety question: journaled Pending it
	// never started, but Running, Interrupted or Failed may have applied
	// part of its effect before the stop, and re-applying is only safe when
	// *both* sides declare it -- the current step for the code that will
	// run now, and the journaled record for the code that half-ran then. A
	// manager upgrade that reclassified a step as idempotent must not
	// retroactively bless the old implementation's half-applied effect.
	if prev := prior.Steps[idx]; prev.Status != domain.StepPending &&
		(!op.Steps[idx].Idempotent || !prev.Idempotent) {
		return 0, domain.Usage(
			"cannot resume: step %q was %s when the operation stopped and is not safe to repeat",
			op.Steps[idx].ID, prev.Status).
			WithHint("run `morzer doctor`, repair manually, then acknowledge the record " +
				"with `morzer status --clear-intervention` and start a fresh operation")
	}

	// Carry the prior run's step outcomes forward so the journal shows one
	// continuous operation rather than two partial ones.
	rec.ID = prior.ID
	rec.StartedAt = prior.StartedAt
	for i := 0; i < idx && i < len(rec.Steps) && i < len(prior.Steps); i++ {
		rec.Steps[i] = prior.Steps[i]
	}
	return idx, nil
}

// journal writes a record, treating a write failure as non-fatal.
//
// The alternative -- failing an operation because its journal could not be
// written -- would mean a full disk turns a working deployment into a broken
// one. The failure is logged and surfaced as a warning instead.
func (e *Engine) journal(ctx context.Context, rec domain.OperationRecord) {
	if e.state == nil {
		return
	}
	if err := e.state.AppendOperation(ctx, rec); err != nil {
		logging.FromContext(ctx).Error("cannot write journal record", "error", err)
		e.bus.Publish(events.Message(events.LevelWarn,
			"could not write the operation journal: %v", domain.AsError(err).Message))
	}
}

func stepIDAt(op Operation, idx int) string {
	if idx < 0 || idx >= len(op.Steps) {
		return ""
	}
	return op.Steps[idx].ID
}

// stepDescriptions is the step list as the live view draws it before anything
// has run.
func stepDescriptions(steps []Step) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Description
	}
	return out
}
