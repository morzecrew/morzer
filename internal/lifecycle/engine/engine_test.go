package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ports"
)

// journalStore is a minimal in-memory StateStore. The engine only appends, so
// the rest of the interface is unimplemented on purpose: a fuller fake would
// invite tests that assert on behaviour the engine does not have.
type journalStore struct {
	records []domain.OperationRecord
	failOn  int // append call number that fails; 0 disables
	calls   int
}

func (s *journalStore) AppendOperation(ctx context.Context, rec domain.OperationRecord) error {
	s.calls++
	if s.failOn > 0 && s.calls == s.failOn {
		return errors.New("simulated journal write failure")
	}
	s.records = append(s.records, rec)
	return nil
}

func (s *journalStore) last() domain.OperationRecord {
	if len(s.records) == 0 {
		return domain.OperationRecord{}
	}
	return s.records[len(s.records)-1]
}

func (s *journalStore) LoadInstallation(context.Context) (domain.Installation, error) {
	return domain.Installation{}, nil
}
func (s *journalStore) SaveInstallation(context.Context, domain.Installation) error { return nil }
func (s *journalStore) InstallationExists(context.Context) (bool, error)            { return true, nil }
func (s *journalStore) CurrentRelease(context.Context) (domain.ReleaseRecord, error) {
	return domain.ReleaseRecord{}, nil
}
func (s *journalStore) PreviousRelease(context.Context) (domain.ReleaseRecord, error) {
	return domain.ReleaseRecord{}, nil
}
func (s *journalStore) SetCurrentRelease(context.Context, domain.ReleaseRecord) error { return nil }
func (s *journalStore) Operations(context.Context, ports.Filter) ([]domain.OperationRecord, error) {
	return s.records, nil
}
func (s *journalStore) LastOperation(context.Context) (domain.OperationRecord, bool, error) {
	return s.last(), len(s.records) > 0, nil
}
func (s *journalStore) UnfinishedOperations(context.Context) ([]domain.OperationRecord, error) {
	return nil, nil
}

// tracker records what each step did, so tests assert on ordering rather than
// on a single boolean.
type tracker struct {
	executed    []string
	compensated []string
	checked     []string
}

func newEngine() (*Engine, *journalStore, *events.Collector) {
	store := &journalStore{}
	bus := events.NewStrictBus()
	collector := events.NewCollector()
	bus.Subscribe(collector)
	return New(store, bus), store, collector
}

// step builds a step that records its calls.
func step(tr *tracker, id string, fail bool, compensable bool) Step {
	s := Step{
		ID:          id,
		Description: "step " + id,
		Idempotent:  true,
		OnFailure:   Compensate,
		Execute: func(ctx context.Context, st *State) error {
			tr.executed = append(tr.executed, id)
			if fail {
				return domain.RuntimeError(nil, "step %s failed on purpose", id)
			}
			return nil
		},
	}
	if compensable {
		s.Compensate = func(ctx context.Context, st *State) error {
			tr.compensated = append(tr.compensated, id)
			return nil
		}
	}
	return s
}

func operation(steps ...Step) Operation {
	return Operation{
		ID: "op_test", Type: domain.OpTypeApply, Description: "test operation", Steps: steps,
	}
}

func TestRunSucceedsAndJournalsEveryTransition(t *testing.T) {
	eng, store, collector := newEngine()
	tr := &tracker{}

	op := operation(
		step(tr, "one", false, true),
		step(tr, "two", false, true),
	)

	result, err := eng.Run(context.Background(), op, Options{ManagerVersion: "1.0.0"})
	require.NoError(t, err)

	assert.Equal(t, []string{"one", "two"}, tr.executed)
	assert.Empty(t, tr.compensated, "nothing failed, so nothing must be rolled back")
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)

	// The journal must hold a record from before the work as well as
	// after: a crash mid-step has to be recoverable to a known position.
	assert.GreaterOrEqual(t, len(store.records), len(op.Steps)+2,
		"every transition is journaled before and after execution")

	require.Len(t, collector.OfKind(events.KindOperationStarted), 1)
	require.Len(t, collector.OfKind(events.KindStepFinished), 2)
	require.Len(t, collector.OfKind(events.KindOperationFinished), 1)
}

func TestCheckSkipsSatisfiedSteps(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	skipped := step(tr, "already-done", false, true)
	skipped.Check = func(ctx context.Context, st *State) (bool, error) {
		tr.checked = append(tr.checked, "already-done")
		return true, nil
	}

	result, err := eng.Run(context.Background(),
		operation(skipped, step(tr, "runs", false, true)), Options{})
	require.NoError(t, err)

	assert.Equal(t, []string{"already-done"}, tr.checked)
	assert.Equal(t, []string{"runs"}, tr.executed,
		"a satisfied postcondition means Execute must not run")
	assert.Equal(t, domain.StepSkipped, result.Record.Steps[0].Status)
}

func TestVerifyFailureFailsTheStep(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	// A tool exiting zero is not the same claim as the system being in the
	// desired state, which is why Verify exists separately from Execute.
	s := step(tr, "lies", false, true)
	s.Verify = func(ctx context.Context, st *State) error {
		return domain.RuntimeError(nil, "the change did not take effect")
	}

	_, err := eng.Run(context.Background(), operation(s), Options{})
	require.Error(t, err)
	assert.Contains(t, tr.executed, "lies")
	assert.Contains(t, tr.compensated, "lies")
}

func TestCompensationRunsInReverseOrder(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	op := operation(
		step(tr, "first", false, true),
		step(tr, "second", false, true),
		step(tr, "third", true, true), // fails
	)

	result, err := eng.Run(context.Background(), op, Options{})
	require.Error(t, err)

	assert.Equal(t, []string{"first", "second", "third"}, tr.executed)
	// Newest first, starting with the step that failed: it may have
	// mutated before failing, and undoing "first" before "second" would
	// undo a precondition the later step still depends on.
	assert.Equal(t, []string{"third", "second", "first"}, tr.compensated,
		"compensation runs newest-first, including the step that failed")

	assert.Equal(t, domain.StatusCompensated, result.Record.Status)
	assert.Equal(t, domain.ExitCompensated, domain.ExitCode(err),
		"a compensated failure exits 11, not with the underlying cause's code")
}

func TestSkippedStepsAreNotCompensated(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	skipped := step(tr, "skipped", false, true)
	skipped.Check = func(ctx context.Context, st *State) (bool, error) { return true, nil }

	op := operation(skipped, step(tr, "ran", false, true), step(tr, "failed", true, true))

	_, err := eng.Run(context.Background(), op, Options{})
	require.Error(t, err)

	assert.NotContains(t, tr.compensated, "skipped",
		"a step that never ran has nothing to undo, and undoing it would revert state it did not create")
	assert.Contains(t, tr.compensated, "ran")
}

func TestNonCompensableFailureRequiresManualIntervention(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	// A step that mutated and cannot undo it is exactly the case that
	// needs a human, and must not be reported as a clean rollback. The
	// step declares this itself rather than the engine inferring it from a
	// nil Compensate -- see the field's documentation.
	irreversible := step(tr, "migration", true, false)
	irreversible.RequiresInterventionOnFailure = true

	op := operation(step(tr, "before", false, true), irreversible)

	result, err := eng.Run(context.Background(), op, Options{})
	require.Error(t, err)

	assert.Equal(t, domain.StatusManualIntervention, result.Record.Status)
	assert.Equal(t, domain.ExitManualIntervention, domain.ExitCode(err))
	assert.True(t, result.Record.Status.NeedsAttention(),
		"the status must keep surfacing in status and doctor until cleared")
	assert.Contains(t, tr.compensated, "before",
		"earlier compensable steps still roll back")
}

func TestContinuePolicyKeepsGoing(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	optional := step(tr, "optional", true, false)
	optional.OnFailure = Continue

	op := operation(optional, step(tr, "after", false, true))

	result, err := eng.Run(context.Background(), op, Options{})
	require.NoError(t, err, "a Continue-policy failure must not fail the operation")

	assert.Equal(t, []string{"optional", "after"}, tr.executed)
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)
	assert.Equal(t, domain.StepFailed, result.Record.Steps[0].Status,
		"the failure is still recorded, it just does not stop the operation")
}

func TestAbortPolicyLeavesEverythingAlone(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	failing := step(tr, "preflight", true, false)
	failing.OnFailure = Abort

	_, err := eng.Run(context.Background(), operation(failing, step(tr, "never", false, true)), Options{})
	require.Error(t, err)

	assert.NotContains(t, tr.executed, "never")
	assert.Empty(t, tr.compensated, "Abort means there was nothing to undo in the first place")
	assert.Equal(t, domain.ExitRuntime, domain.ExitCode(err),
		"an aborted operation surfaces the underlying error's own exit code")
}

func TestDryRunMutatesNothingAndJournalsNothing(t *testing.T) {
	eng, store, collector := newEngine()
	tr := &tracker{}

	planned := step(tr, "would-run", false, true)
	planned.Check = func(ctx context.Context, st *State) (bool, error) {
		tr.checked = append(tr.checked, "would-run")
		return false, nil
	}
	satisfied := step(tr, "already-done", false, true)
	satisfied.Check = func(ctx context.Context, st *State) (bool, error) { return true, nil }

	_, err := eng.Run(context.Background(), operation(planned, satisfied), Options{DryRun: true})
	require.NoError(t, err)

	assert.Empty(t, tr.executed, "--dry-run must not execute anything")
	assert.Equal(t, []string{"would-run"}, tr.checked, "--dry-run runs every Check")
	assert.Empty(t, store.records,
		"a planning run is not something that happened to the installation")

	plans := collector.OfKind(events.KindPlan)
	require.Len(t, plans, 1)
	require.Len(t, plans[0].Plan, 2)
	assert.True(t, plans[0].Plan[0].WillRun)
	assert.False(t, plans[0].Plan[1].WillRun, "a satisfied check means the step will not run")
}

func TestCancellationIsInterruptionNotFailure(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	ctx, cancel := context.WithCancel(context.Background())

	blocking := step(tr, "long", false, true)
	blocking.Execute = func(ctx context.Context, st *State) error {
		tr.executed = append(tr.executed, "long")
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}

	result, err := eng.Run(ctx, operation(blocking, step(tr, "after", false, true)), Options{})
	require.Error(t, err)

	// A tool killed by our own SIGTERM must not be reported as a broken
	// tool: the operator would go hunting for a bug that is not there.
	assert.Equal(t, domain.StatusInterrupted, result.Record.Status)
	assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(err))
	assert.NotContains(t, tr.executed, "after")
}

func TestCompensationRunsEvenAfterCancellation(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	ctx, cancel := context.WithCancel(context.Background())

	first := step(tr, "mutating", false, true)
	failing := step(tr, "cancelled", false, true)
	failing.Execute = func(ctx context.Context, st *State) error {
		tr.executed = append(tr.executed, "cancelled")
		cancel()
		return domain.RuntimeError(nil, "failed while the context was cancelled")
	}

	_, err := eng.Run(ctx, operation(first, failing), Options{})
	require.Error(t, err)

	// This is the property that matters: compensation gets a fresh
	// context. Using the cancelled one would abort every compensator
	// immediately and guarantee the half-applied state it exists to
	// prevent.
	assert.Contains(t, tr.compensated, "mutating",
		"compensation must run on a fresh context, not the cancelled one")
}

func TestJournalFailureDoesNotFailTheOperation(t *testing.T) {
	store := &journalStore{failOn: 2}
	bus := events.NewStrictBus()
	eng := New(store, bus)
	tr := &tracker{}

	// A full disk must not turn a working deployment into a broken one.
	_, err := eng.Run(context.Background(), operation(step(tr, "work", false, true)), Options{})
	require.NoError(t, err, "a journal write failure is logged, not fatal")
	assert.Equal(t, []string{"work"}, tr.executed)
}

func TestStepTimeoutBecomesInterruption(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	slow := step(tr, "slow", false, true)
	slow.Timeout = 10 * time.Millisecond
	slow.Execute = func(ctx context.Context, st *State) error {
		tr.executed = append(tr.executed, "slow")
		<-ctx.Done()
		return ctx.Err()
	}

	result, err := eng.Run(context.Background(), operation(slow), Options{})
	require.Error(t, err)
	assert.Equal(t, domain.StatusInterrupted, result.Record.Status)
	assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(err))
}

// TestFaultInjectionAtEveryStep is the suite the spec calls the one that
// matters most: the step engine's value is entirely in what happens when
// something breaks, so every step of a representative operation is failed in
// turn and the resulting state asserted.
func TestFaultInjectionAtEveryStep(t *testing.T) {
	const stepCount = 6

	for failAt := 0; failAt < stepCount; failAt++ {
		t.Run("fails at step "+string(rune('0'+failAt)), func(t *testing.T) {
			eng, store, _ := newEngine()
			tr := &tracker{}

			steps := make([]Step, stepCount)
			for i := range steps {
				id := string(rune('a' + i))
				steps[i] = step(tr, id, i == failAt, true)
			}

			result, err := eng.Run(context.Background(), operation(steps...), Options{})
			require.Error(t, err, "the operation must fail when a step does")

			// Everything up to and including the failing step ran;
			// nothing after it did.
			assert.Len(t, tr.executed, failAt+1,
				"execution must stop at the failing step")

			// Every step that ran rolls back, newest first --
			// including the failing one, which may have mutated
			// before it failed.
			assert.Len(t, tr.compensated, failAt+1,
				"every step that ran, including the one that failed, must be compensated")
			for i := 0; i <= failAt; i++ {
				expected := string(rune('a' + failAt - i))
				assert.Equal(t, expected, tr.compensated[i],
					"compensation order must be newest-first")
			}

			assert.Equal(t, domain.StatusCompensated, result.Record.Status)
			assert.Equal(t, domain.ExitCompensated, domain.ExitCode(err))

			// The journal must describe where things stopped, which
			// is what makes the failure diagnosable at all.
			last := store.last()
			require.NotNil(t, last.Error)
			assert.Equal(t, string(rune('a'+failAt)), last.Steps[failAt].ID)
			assert.Equal(t, domain.StepCompensated, last.Steps[failAt].Status,
				"the failing step rolled itself back, so it is compensated rather than failed")
			assert.Contains(t, last.Error.Error(), string(rune('a'+failAt)),
				"the journalled error must name the step that failed")
		})
	}
}

func TestResumeContinuesFromFirstIncompleteStep(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	prior := domain.OperationRecord{
		ID: "op_test", Type: domain.OpTypeApply, Status: domain.StatusInterrupted,
		StartedAt: domain.NewTime(time.Now()),
		Steps: []domain.StepRecord{
			{ID: "a", Status: domain.StepSucceeded, Idempotent: true},
			{ID: "b", Status: domain.StepSucceeded, Idempotent: true},
			{ID: "c", Status: domain.StepInterrupted, Idempotent: true},
			{ID: "d", Status: domain.StepPending, Idempotent: true},
		},
	}

	op := operation(
		step(tr, "a", false, true), step(tr, "b", false, true),
		step(tr, "c", false, true), step(tr, "d", false, true),
	)

	result, err := eng.Run(context.Background(), op, Options{Resume: true, Prior: &prior})
	require.NoError(t, err)

	assert.Equal(t, []string{"c", "d"}, tr.executed,
		"resume must skip the steps that already completed")
	assert.Equal(t, "op_test", result.Record.ID,
		"a resumed run continues the same operation rather than starting a new one")
}

func TestResumeRefusesWhenAnEarlierStepIsNotIdempotent(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	prior := domain.OperationRecord{
		ID: "op_test", Type: domain.OpTypeApply, Status: domain.StatusInterrupted,
		StartedAt: domain.NewTime(time.Now()),
		Steps: []domain.StepRecord{
			{ID: "a", Status: domain.StepSucceeded, Idempotent: true},
			{ID: "b", Status: domain.StepInterrupted, Idempotent: true},
		},
	}

	// The first step is not safe to repeat, so resuming past it would
	// apply its effect twice -- which is the situation the operator is
	// trying to escape.
	notIdempotent := step(tr, "a", false, true)
	notIdempotent.Idempotent = false

	_, err := eng.Run(context.Background(),
		operation(notIdempotent, step(tr, "b", false, true)),
		Options{Resume: true, Prior: &prior})

	require.Error(t, err)
	assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
	assert.Empty(t, tr.executed, "nothing must run when resume is refused")
}

func TestResumeWithoutPriorIsAUsageError(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	_, err := eng.Run(context.Background(), operation(step(tr, "a", false, true)),
		Options{Resume: true})

	require.Error(t, err)
	assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
}

func TestStateCarriesValuesBetweenSteps(t *testing.T) {
	eng, _, _ := newEngine()

	producer := Step{ID: "produce", Idempotent: true, Execute: func(ctx context.Context, st *State) error {
		st.Set("answer", 42)
		return nil
	}}
	consumer := Step{ID: "consume", Idempotent: true, Execute: func(ctx context.Context, st *State) error {
		got, err := GetTyped[int](st, "answer")
		if err != nil {
			return err
		}
		if got != 42 {
			return domain.Internal(nil, "expected 42, got %d", got)
		}
		return nil
	}}

	_, err := eng.Run(context.Background(), operation(producer, consumer), Options{})
	require.NoError(t, err)
}

func TestGetTypedReportsMismatchesClearly(t *testing.T) {
	st := newState("op", domain.OpTypeApply, false, events.NewBus())
	st.Set("value", "a string")

	_, err := GetTyped[int](st, "value")
	require.Error(t, err, "a mistyped state key must be an error at the point of use")
	assert.Contains(t, err.Error(), "value")

	_, err = GetTyped[int](st, "absent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absent")
}

func TestPanickingSinkDoesNotStopTheOperation(t *testing.T) {
	store := &journalStore{}
	bus := events.NewBus() // recovering bus, as in production
	panicked := false
	bus.OnPanic(func(any) { panicked = true })
	bus.SubscribeFunc(func(e events.Event) {
		if e.Kind == events.KindStepStarted {
			panic("the terminal was resized into nonsense")
		}
	})

	eng := New(store, bus)
	tr := &tracker{}

	_, err := eng.Run(context.Background(), operation(step(tr, "work", false, true)), Options{})

	require.NoError(t, err,
		"a failed presenter is logged and dropped, never propagated")
	assert.True(t, panicked, "the panic must still be reported")
	assert.Equal(t, []string{"work"}, tr.executed)
}

// TestReadOnlyStepFailureDoesNotDemandAHuman guards the distinction the
// RequiresInterventionOnFailure flag exists to draw.
//
// Health checks and smoke tests have no compensator because they mutate
// nothing. Treating their failure as "manual intervention required" would flag
// every transient failure for an operator acknowledgement, which trains people
// to clear the flag without looking -- and then the one time it matters, they
// clear that one too.
func TestReadOnlyStepFailureDoesNotDemandAHuman(t *testing.T) {
	eng, _, _ := newEngine()
	tr := &tracker{}

	readOnly := step(tr, "health-check", true, false) // fails, no compensator
	require.False(t, readOnly.RequiresInterventionOnFailure)

	op := operation(step(tr, "mutating", false, true), readOnly)

	result, err := eng.Run(context.Background(), op, Options{})
	require.Error(t, err)

	assert.Equal(t, domain.StatusCompensated, result.Record.Status,
		"a read-only step's failure is a clean rollback, not a state needing repair")
	assert.Equal(t, domain.ExitCompensated, domain.ExitCode(err))
	assert.Contains(t, tr.compensated, "mutating")
}
