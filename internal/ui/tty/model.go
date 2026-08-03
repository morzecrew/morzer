// Package tty renders operations as a live step list.
//
// It is a bus subscriber and nothing else. `events.Sink.Handle` returns
// nothing, so a renderer structurally cannot signal back into the engine --
// "the UI observes, never participates" is enforced by the interface rather
// than by convention. Everything here follows from that:
//
//   - It never cancels. Ctrl-C is observed and drawn as a state; the root
//     context's signal handler in main owns the actual cancellation.
//   - It can panic. The bus recovers from a sink panic, logs it and drops the
//     sink, and the operation runs to completion with no display.
//   - It can fail to start. Then the plain presenter takes the whole run, and
//     the only cost is that the operator sees lines instead of motion.
//
// No alt-screen. Operation output stays in scrollback, because an operator
// whose update just failed needs to scroll back through it.
package tty

import (
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// outputTail is how many lines of subprocess output the active step shows.
//
// Enough to see what a tool is doing, few enough that a step list stays a step
// list. Everything is in the log regardless.
const outputTail = 3

// minWidth is the narrowest layout that still reads. Below it the view stops
// shrinking rather than wrapping into nonsense.
const minWidth = 40

// stepState is what a step looks like to the view.
type stepState int

const (
	statePending stepState = iota
	stateActive
	stateDone
	stateSkipped
	stateFailed
	stateCompensated
)

type stepView struct {
	id          string
	description string
	state       stepState
	duration    time.Duration
	detail      string
	progress    float64 // negative means unknown
}

// Model is the live view of one operation.
type Model struct {
	theme *theme.Theme

	opID        string
	opType      domain.OperationType
	description string
	dryRun      bool

	steps  []stepView
	active int

	// output is the tail of the active step's subprocess output.
	output *ring

	// messages are engine narration not tied to a step. Kept because plain
	// mode prints them, and rich must never show less.
	messages []string

	spinner  spinner.Model
	progress progress.Model

	started    time.Time
	now        time.Time
	duration   time.Duration // the engine's own total, once it reports one
	cancelling bool
	finished   bool
	failure    *domain.Error
	status     string

	width, height int
}

// New builds a model. It draws nothing until the first event arrives.
func New(t *theme.Theme) *Model {
	sp := spinner.New()
	sp.Spinner = spinner.Spinner{Frames: t.Spinner, FPS: time.Second / 10}

	return &Model{
		theme:    t,
		active:   -1,
		output:   newRing(outputTail),
		spinner:  sp,
		progress: progress.New(progress.WithDefaultGradient(), progress.WithoutPercentage()),
		width:    80,
		height:   24,
	}
}

// eventMsg carries a bus event into the program's update loop.
type eventMsg struct{ event events.Event }

// EventMsg wraps an event as a Bubble Tea message.
//
// Exported so tests can drive the model from an event stream directly, which
// is the same path Subscribe takes. Production code uses Subscribe.
func EventMsg(e events.Event) tea.Msg { return eventMsg{event: e} }

// tickMsg advances the elapsed clock.
type tickMsg time.Time

// Subscribe returns a sink that forwards bus events to a running program.
//
// The only coupling point between the engine's event stream and this package,
// and it points one way. `p.Send` is a no-op once the program has exited, so a
// late event from a finished operation cannot panic the sink.
func Subscribe(p *tea.Program) events.Sink {
	return events.SinkFunc(func(e events.Event) { p.Send(eventMsg{event: e}) })
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tick())
}

func tick() tea.Cmd {
	return tea.Tick(time.Second/2, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, minWidth), msg.Height
		return m, nil

	case tea.KeyMsg:
		// Ctrl-C is drawn, not acted on. The signal reaches the process
		// group through the runtime's own handler and cancels the root
		// context; all this does is stop the view claiming work is
		// still progressing while children are being torn down.
		if msg.Type == tea.KeyCtrlC {
			m.cancelling = true
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tickMsg:
		m.now = time.Time(msg)
		return m, tick()

	case eventMsg:
		return m, m.apply(msg.event)
	}
	return m, nil
}

// apply folds one event into the model.
func (m *Model) apply(e events.Event) tea.Cmd {
	// Events carry their own timestamps, so the elapsed clock advances
	// with the work rather than only on the half-second tick. An operation
	// that finishes between two ticks would otherwise report 0ms, which is
	// how the first real-terminal run of this view looked.
	if e.At.After(m.now) {
		m.now = e.At
	}

	switch e.Kind {
	case events.KindOperationStarted:
		m.opID, m.opType = e.OpID, e.OpType
		m.description, m.dryRun = e.Description, e.DryRun
		m.started = e.At
		// The whole list is drawn from this one event, with everything
		// that has not run yet dimmed. Seeing what is still to come is
		// most of the value of watching: an operator who knows the
		// backup is step three does not wonder whether it was skipped.
		m.steps = make([]stepView, max(e.StepCount, len(e.Steps)))
		for i := range m.steps {
			m.steps[i].progress = -1
			if i < len(e.Steps) {
				m.steps[i].description = e.Steps[i]
			}
		}

	case events.KindStepStarted:
		m.active = e.StepIndex
		m.output.reset()
		if e.StepIndex >= 0 && e.StepIndex < len(m.steps) {
			m.steps[e.StepIndex] = stepView{
				id: e.StepID, description: e.Description,
				state: stateActive, progress: -1,
			}
		}

	case events.KindStepProgress:
		if s := m.step(e.StepID); s != nil {
			s.progress, s.detail = e.Progress, e.Detail
		}

	case events.KindStepOutput:
		m.output.push(e.Message)

	case events.KindStepFinished:
		if s := m.step(e.StepID); s != nil {
			s.state, s.duration = finishedState(e.Status), e.Duration
			// A failed step keeps its reason on the line. The error is
			// also printed in full after the program exits, but a step
			// list whose only failure signal is a red mark makes the
			// operator scroll to find out which one broke and why.
			s.detail = ""
			if e.Err != nil {
				s.detail = e.Err.Message
			}
		}
		m.output.reset()

	case events.KindOperationFinished:
		m.finished, m.status, m.failure = true, e.Status, e.Err
		// The engine's own measurement, not this view's arithmetic: it
		// is what the journal records and what plain mode prints, and
		// two different numbers for one operation is one too many.
		m.duration = e.Duration
		// Quitting here rather than on a timer: the operation is over,
		// and a view that lingered would be a view an operator has to
		// dismiss.
		return tea.Quit

	case events.KindMessage:
		// Plain mode prints warnings and errors, so this does too.
		// Anything less would break the rule that rich never shows less
		// than plain.
		if e.Level == events.LevelWarn || e.Level == events.LevelError {
			m.messages = append(m.messages, m.theme.Warn(
				m.theme.Symbols.Warn+" "+e.Message))
		}
	}
	return nil
}

func finishedState(status string) stepState {
	switch domain.StepStatus(status) {
	case domain.StepSucceeded:
		return stateDone
	case domain.StepSkipped:
		return stateSkipped
	case domain.StepCompensated:
		return stateCompensated
	default:
		return stateFailed
	}
}

func (m *Model) step(id string) *stepView {
	for i := range m.steps {
		if m.steps[i].id == id {
			return &m.steps[i]
		}
	}
	return nil
}

// Steps exposes the rendered step state for the parity test, which asserts
// that everything plain prints reaches the model too.
func (m *Model) Steps() []string {
	out := make([]string, 0, len(m.steps))
	for _, s := range m.steps {
		if s.id != "" {
			out = append(out, s.id)
		}
	}
	return out
}

// Failure exposes the terminal error, for the same reason.
func (m *Model) Failure() *domain.Error { return m.failure }

// Messages exposes engine narration, for the same reason.
func (m *Model) Messages() []string { return m.messages }
