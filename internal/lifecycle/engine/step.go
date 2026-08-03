// Package engine runs operations as sequences of steps, with journaling,
// compensation, dry-run planning and resume.
//
// Steps are the single place ports are composed. Adding a stage to an
// operation -- a signature check, a notification, a pre-flight snapshot --
// means adding a step, not editing a monolithic procedure. It is also what
// makes the terminal UI cheap: the renderer draws a list of steps it was
// handed, knowing nothing about what any of them do.
package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
)

// FailurePolicy decides what the engine does when a step fails.
type FailurePolicy int

const (
	// Abort stops the operation and leaves everything as it is. Correct
	// for steps that mutate nothing: if preflight fails, there is nothing
	// to undo.
	Abort FailurePolicy = iota

	// Compensate stops and runs Compensate for every completed compensable
	// step, newest first.
	Compensate

	// Continue records the failure and proceeds. Reserved for genuinely
	// optional work -- sending a notification, pruning old releases --
	// where failing the whole operation would be worse than the gap.
	Continue
)

func (p FailurePolicy) String() string {
	switch p {
	case Abort:
		return "abort"
	case Compensate:
		return "compensate"
	case Continue:
		return "continue"
	default:
		return "unknown"
	}
}

// Step is one unit of work.
//
// The four functions separate concerns that are usually tangled: Check asks
// whether the work is already done (idempotence), Execute does it, Verify
// confirms it took effect, and Compensate undoes it. A step that implements
// only Execute is valid; one that implements Check as well is resumable.
type Step struct {
	ID          string
	Description string

	// Idempotent declares that running this step twice is equivalent to
	// running it once. --resume refuses to continue past a completed
	// non-idempotent step, so this flag is a safety assertion, not a hint.
	Idempotent bool

	// Timeout bounds this step. Zero means the operation's budget governs.
	Timeout time.Duration

	OnFailure FailurePolicy

	// Check reports whether the postcondition already holds. Returning
	// true marks the step skipped without running Execute. It must have no
	// side effects: --dry-run runs every Check.
	Check func(context.Context, *State) (done bool, err error)

	// Execute performs the work.
	Execute func(context.Context, *State) error

	// Verify confirms the work took effect. Separate from Execute because
	// a tool exiting zero is not the same claim as the system being in the
	// desired state.
	Verify func(context.Context, *State) error

	// Compensate undoes the step. Nil means the step is not compensable.
	Compensate func(context.Context, *State) error

	// RequiresInterventionOnFailure declares that a failure here can leave
	// the system in a state no automatic action can repair -- a half-run
	// migration, a partially restored database.
	//
	// It is an explicit flag rather than inferred from a nil Compensate,
	// because most steps without a compensator are simply read-only and
	// have nothing to undo. Inferring it would flag every failed health
	// check as needing a human, which trains operators to clear the flag
	// without looking -- destroying the value of the one signal that is
	// supposed to stop them.
	RequiresInterventionOnFailure bool

	// PlanDetail describes what the step would do, for --dry-run. It may
	// return a diff for configuration changes.
	PlanDetail func(context.Context, *State) (detail string, diff string)
}

// State is the operation-scoped context handed to every step.
//
// It carries values between steps -- the fetched release, the loaded secrets,
// the backup reference taken before an update -- without those needing to be
// fields on a bespoke struct per operation. Access is typed through helpers so
// a missing or mistyped value is an error at the point of use rather than a
// panic three steps later.
type State struct {
	OpID   string
	OpType domain.OperationType

	// DryRun tells a step it must not mutate. Steps generally do not need
	// to check it -- the engine skips Execute entirely -- but a Check that
	// would create a scratch directory does.
	DryRun bool

	// Resumed marks a run continuing an interrupted operation.
	Resumed bool

	mu     sync.RWMutex
	values map[string]any

	bus    *events.Bus
	stepID string
}

func newState(opID string, opType domain.OperationType, dryRun bool, bus *events.Bus) *State {
	return &State{
		OpID:   opID,
		OpType: opType,
		DryRun: dryRun,
		values: make(map[string]any),
		bus:    bus,
	}
}

// Set stores a value for later steps.
func (s *State) Set(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
}

// Get retrieves a value.
func (s *State) Get(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok
}

// Progress reports fractional completion of the current step. Values outside
// [0,1] are clamped; pass -1 through Detail instead when completion is
// unknown.
func (s *State) Progress(fraction float64, detail string) {
	if s.bus == nil {
		return
	}
	switch {
	case fraction < 0:
		fraction = -1
	case fraction > 1:
		fraction = 1
	}
	s.bus.Publish(events.StepProgress(s.OpID, s.stepID, fraction, detail))
}

// Detail reports what the step is currently doing, without a completion
// fraction.
func (s *State) Detail(format string, args ...any) {
	s.Progress(-1, fmt.Sprintf(format, args...))
}

// Output forwards a line of subprocess output to the live view.
func (s *State) Output(line string) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.StepOutput(s.OpID, s.stepID, line))
}

// Warn emits a warning that is not a failure.
func (s *State) Warn(format string, args ...any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Message(events.LevelWarn, format, args...))
}

// Info emits an informational message.
func (s *State) Info(format string, args ...any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Message(events.LevelInfo, format, args...))
}

// GetTyped is a generic accessor over State. It is a free function rather than
// a method because Go does not allow type parameters on methods.
//
// A missing or mistyped value is a bug in step wiring, so it produces an
// internal error naming the key -- which is a great deal easier to debug than
// a nil dereference in whichever step happened to read it.
func GetTyped[T any](s *State, key string) (T, error) {
	var zero T
	v, ok := s.Get(key)
	if !ok {
		return zero, domain.Internal(nil, "step state is missing %q", key)
	}
	typed, ok := v.(T)
	if !ok {
		return zero, domain.Internal(nil, "step state key %q holds %T, expected %T", key, v, zero)
	}
	return typed, nil
}

// MustGet is GetTyped for values a step knows are present because an earlier
// step in the same operation set them. It returns the zero value rather than
// panicking: a step that reads a value it did not require should degrade, not
// crash an operation midway.
func MustGet[T any](s *State, key string) T {
	v, _ := GetTyped[T](s, key)
	return v
}

// Well-known state keys. Declared as constants so a typo is a compile error in
// the packages that use them rather than a silent miss at runtime.
const (
	KeyInstallation  = "installation"
	KeyRelease       = "release"
	KeyPrevRelease   = "previous-release"
	KeySecrets       = "secrets"
	KeySecretSchema  = "secret-schema"
	KeyRuntimeConfig = "runtime-config"
	KeyBackupRef     = "backup-ref"
	KeyRenderedFiles = "rendered-files"
	KeyHealthResults = "health-results"
	KeySchemaVersion = "database-schema-version"
)
