package tty_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ui/plain"
	"github.com/morzecrew/morzer/internal/ui/theme"
	"github.com/morzecrew/morzer/internal/ui/tty"
)

// at is a fixed clock. Every event carries an explicit time so a rendered view
// is a function of its input and nothing else.
var at = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

func ev(e events.Event, offset time.Duration) events.Event {
	e.At = at.Add(offset)
	return e
}

// successfulUpdate is the event stream of an update that works: one step
// skipped because its postcondition already held, one that runs with progress
// and subprocess output, and a warning that belongs to no step.
func successfulUpdate() []events.Event {
	return []events.Event{
		ev(events.Event{
			Kind: events.KindOperationStarted, OpID: "01J8ZP", OpType: domain.OpTypeUpdate,
			Description: "update 1.2.0 → 1.3.0", Level: events.LevelInfo,
			StepCount: 2, Steps: []string{"render configuration", "pull images"},
		}, 0),

		ev(events.Event{
			Kind: events.KindStepStarted, OpID: "01J8ZP", StepID: "render-config",
			Description: "render configuration", StepIndex: 0, StepCount: 2, Progress: -1,
		}, time.Second),
		ev(events.Event{
			Kind: events.KindStepFinished, OpID: "01J8ZP", StepID: "render-config",
			Status: string(domain.StepSkipped), Duration: 12 * time.Millisecond,
		}, 2*time.Second),

		ev(events.Event{
			Kind: events.KindStepStarted, OpID: "01J8ZP", StepID: "pull-images",
			Description: "pull images", StepIndex: 1, StepCount: 2, Progress: -1,
		}, 3*time.Second),
		ev(events.Event{
			Kind: events.KindStepProgress, OpID: "01J8ZP", StepID: "pull-images",
			Progress: 0.5, Detail: "pulling layer 7 of 11",
		}, 4*time.Second),
		ev(events.Event{
			Kind: events.KindStepOutput, OpID: "01J8ZP", StepID: "pull-images",
			Message: "sha256:9f3a extracting",
		}, 5*time.Second),
		ev(events.Event{
			Kind: events.KindMessage, Level: events.LevelWarn,
			Message: "the registry served an unpinned tag",
		}, 6*time.Second),
		ev(events.Event{
			Kind: events.KindStepFinished, OpID: "01J8ZP", StepID: "pull-images",
			Status: string(domain.StepSucceeded), Duration: 4 * time.Second,
		}, 7*time.Second),

		ev(events.Event{
			Kind: events.KindOperationFinished, OpID: "01J8ZP", OpType: domain.OpTypeUpdate,
			Status: string(domain.StatusSucceeded), Duration: 7 * time.Second,
		}, 7*time.Second),
	}
}

// failedUpdate is the stream of an update whose second step fails and is rolled
// back. The rolled-back step and the failed one are different states, and an
// operator has to be able to tell them apart.
func failedUpdate() []events.Event {
	failure := domain.HealthError(nil, "api did not become healthy within 2m").
		WithHint("check `docker compose logs api`")
	return []events.Event{
		ev(events.Event{
			Kind: events.KindOperationStarted, OpID: "01J8ZQ", OpType: domain.OpTypeUpdate,
			Description: "update 1.2.0 → 1.3.0",
			StepCount:   2, Steps: []string{"start services", "wait for health"},
		}, 0),
		ev(events.Event{
			Kind: events.KindStepStarted, OpID: "01J8ZQ", StepID: "start-services",
			Description: "start services", StepIndex: 0, StepCount: 2, Progress: -1,
		}, time.Second),
		ev(events.Event{
			Kind: events.KindStepFinished, OpID: "01J8ZQ", StepID: "start-services",
			Status: string(domain.StepCompensated), Duration: 900 * time.Millisecond,
		}, 2*time.Second),
		ev(events.Event{
			Kind: events.KindStepStarted, OpID: "01J8ZQ", StepID: "wait-healthy",
			Description: "wait for health", StepIndex: 1, StepCount: 2, Progress: -1,
		}, 3*time.Second),
		ev(events.Event{
			Kind: events.KindStepFinished, OpID: "01J8ZQ", StepID: "wait-healthy",
			Status: string(domain.StepFailed), Duration: 2 * time.Minute, Err: failure,
		}, 4*time.Second),
		ev(events.Event{
			Kind: events.KindOperationFinished, OpID: "01J8ZQ", OpType: domain.OpTypeUpdate,
			Status: string(domain.StatusCompensated), Duration: 2 * time.Minute, Err: failure,
		}, 5*time.Second),
	}
}

// run drives a model through a stream without a terminal and returns the final
// frame. Enough for the content assertions; teatest covers the wiring.
func run(stream []events.Event) (*tty.Model, string) {
	m := tty.New(theme.New(false, false), nil)
	var model tea.Model = m
	for _, e := range stream {
		model, _ = model.Update(tty.EventMsg(e))
	}
	return m, model.View()
}

func TestTheStepListShowsEveryOutcome(t *testing.T) {
	_, view := run(successfulUpdate())

	for _, want := range []string{
		"update 1.2.0 → 1.3.0", // the operation
		"render configuration", // the skipped step
		"already satisfied",    // and why it was skipped
		"pull images",          // the step that ran
		"01J8ZP",               // the operation id, for the journal
		"done",                 // the outcome
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the final view does not mention %q:\n%s", want, view)
		}
	}
}

// TestTheElapsedClockUsesTheEnginesOwnTotal catches what the first real-terminal
// run showed: an operation that finished between two half-second ticks reported
// 0ms, because the clock only advanced on the tick.
func TestTheElapsedClockUsesTheEnginesOwnTotal(t *testing.T) {
	_, view := run(successfulUpdate())

	if !strings.Contains(view, "7.0s") {
		t.Errorf("the footer does not carry the operation's duration:\n%s", view)
	}
}

func TestAFailureNamesTheStepAndTheReason(t *testing.T) {
	_, view := run(failedUpdate())

	for _, want := range []string{
		"wait for health",
		"api did not become healthy within 2m",
		"rolled back",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the final view does not mention %q:\n%s", want, view)
		}
	}

	// The compensated step and the failed step must not render identically:
	// "this was undone" and "this broke" are different facts.
	sym := theme.ASCIISymbols
	if !strings.Contains(view, sym.Fail) || !strings.Contains(view, sym.Compensated) {
		t.Errorf("failed and compensated steps are not distinguishable:\n%s", view)
	}
}

// TestThePendingStepsAreNamedFromTheFirstEvent pins decision 1: the view draws
// what is still to come, and gets it from the event rather than by asking the
// engine.
func TestThePendingStepsAreNamedFromTheFirstEvent(t *testing.T) {
	// Only the first event. Nothing has run.
	_, view := run(successfulUpdate()[:1])

	for _, want := range []string{"render configuration", "pull images"} {
		if !strings.Contains(view, want) {
			t.Errorf("the view does not name the pending step %q:\n%s", want, view)
		}
	}
	// Counted as line prefixes: the marker is a single character and
	// counting it anywhere in the frame would also count the dots in
	// "1.2.0".
	pending := 0
	for _, line := range strings.Split(view, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), theme.ASCIISymbols.Pending+" ") {
			pending++
		}
	}
	if pending != 2 {
		t.Errorf("expected 2 pending markers, got %d:\n%s", pending, view)
	}
}

// TestSubprocessOutputIsTailedNotAccumulated pins the bound. An image pull
// emits thousands of lines and the view is a step list, not a log.
func TestSubprocessOutputIsTailedNotAccumulated(t *testing.T) {
	stream := []events.Event{
		ev(events.Event{Kind: events.KindOperationStarted, OpID: "x", StepCount: 1}, 0),
		ev(events.Event{
			Kind: events.KindStepStarted, OpID: "x", StepID: "pull",
			Description: "pull images", StepIndex: 0, StepCount: 1, Progress: -1,
		}, time.Second),
	}
	for i := range 200 {
		stream = append(stream, ev(events.Event{
			Kind: events.KindStepOutput, OpID: "x", StepID: "pull",
			Message: "line " + string(rune('a'+i%26)) + strings.Repeat("0", i%3),
		}, 2*time.Second))
	}
	_, view := run(stream)

	if strings.Count(view, "line ") > 4 {
		t.Errorf("the view accumulated output instead of tailing it:\n%s", view)
	}
	if !strings.Contains(view, "line ") {
		t.Errorf("the view dropped subprocess output entirely:\n%s", view)
	}
}

// TestLongLinesAreTruncatedNotWrapped keeps the step list aligned. A 400-column
// docker line wrapped across a narrow terminal destroys the layout, and the
// full text is in the log either way.
func TestLongLinesAreTruncatedNotWrapped(t *testing.T) {
	stream := []events.Event{
		ev(events.Event{Kind: events.KindOperationStarted, OpID: "x", StepCount: 1}, 0),
		ev(events.Event{
			Kind: events.KindStepStarted, OpID: "x", StepID: "pull",
			Description: "pull images", StepIndex: 0, StepCount: 1, Progress: -1,
		}, time.Second),
		ev(events.Event{
			Kind: events.KindStepOutput, OpID: "x", StepID: "pull",
			Message: strings.Repeat("x", 400),
		}, 2*time.Second),
	}

	m := tty.New(theme.New(false, false), nil)
	var model tea.Model = m
	model, _ = model.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	for _, e := range stream {
		model, _ = model.Update(tty.EventMsg(e))
	}

	for _, line := range strings.Split(model.View(), "\n") {
		if len(line) > 60 {
			t.Errorf("a %d-cell line escaped a 60-column terminal: %q", len(line), line)
		}
	}
}

// TestTheProgramDrawsAndExits is the wiring test: a real Bubble Tea program,
// fed through the same Sink the bus uses, drawing to a real (simulated)
// terminal and quitting on its own when the operation finishes.
func TestTheProgramDrawsAndExits(t *testing.T) {
	tm := teatest.NewTestModel(t, tty.New(theme.New(false, false), nil),
		teatest.WithInitialTermSize(90, 30))

	for _, e := range successfulUpdate() {
		tm.Send(tty.EventMsg(e))
	}

	// No tm.Quit(): the model quits itself on operation.finished, which is
	// what lets the CLI wait for the program instead of guessing when to
	// tear it down.
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	out := new(bytes.Buffer)
	if _, err := out.ReadFrom(tm.FinalOutput(t)); err != nil {
		t.Fatalf("reading the program's output: %v", err)
	}
	if got := stripANSI(out.String()); !strings.Contains(got, "pull images") {
		t.Errorf("the program never drew the step list:\n%s", got)
	}
}

// TestCtrlCRequestsCancellationThroughTheCallback pins the repaired contract:
// raw mode turns ISIG off, so a ^C at the live view arrives as a keystroke and
// the kernel never raises the SIGINT main's handler listens for. The first ^C
// must therefore invoke the caller's OnCancel -- before this, the footer's
// "ctrl-c to cancel" was a lie and the update ran to completion -- and be
// drawn. The view still only observes the engine; the callback is the
// caller's, not a path into the bus.
func TestCtrlCRequestsCancellationThroughTheCallback(t *testing.T) {
	cancelled := 0
	m := tty.New(theme.New(false, false), func() { cancelled++ })
	var model tea.Model = m
	for _, e := range successfulUpdate()[:4] { // mid-operation
		model, _ = model.Update(tty.EventMsg(e))
	}

	model, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Error("ctrl-c produced a command; cancellation goes through the callback, not the runtime")
	}
	if cancelled != 1 {
		t.Fatalf("the first ctrl-c invoked OnCancel %d times, want exactly once", cancelled)
	}
	if !strings.Contains(model.View(), "cancelling") {
		t.Errorf("ctrl-c is not reflected in the view:\n%s", model.View())
	}
	if !strings.Contains(model.View(), "force quit") {
		t.Errorf("the footer must offer the second-ctrl-c escape hatch:\n%s", model.View())
	}
}

// A model wired without a callback -- the tests' own stance -- only draws.
func TestCtrlCWithoutACallbackOnlyDraws(t *testing.T) {
	var model tea.Model = tty.New(theme.New(false, false), nil)
	for _, e := range successfulUpdate()[:4] { // mid-operation
		model, _ = model.Update(tty.EventMsg(e))
	}
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !strings.Contains(model.View(), "cancelling") {
		t.Errorf("ctrl-c is not reflected in the view:\n%s", model.View())
	}
}

// TestRichNeverShowsWhatPlainDoesNot is the parity check, and the reason it is
// mechanical rather than a habit.
//
// The rule is that the two renderers carry the same information and differ only
// in motion. So for every event the live view *reacts* to -- meaning its frame
// changed -- the plain presenter must have printed something. Anything visible
// only in rich is a gap in plain, which is what CI, systemd and every log
// actually read.
func TestRichNeverShowsWhatPlainDoesNot(t *testing.T) {
	for name, stream := range map[string][]events.Event{
		"success": successfulUpdate(),
		"failure": failedUpdate(),
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			// Verbose, because rich shows progress detail and a tail
			// of subprocess output, and plain shows those under -v.
			p := plain.New(&buf, true)

			var model tea.Model = tty.New(theme.New(false, false), nil)
			for _, e := range stream {
				before, printed := model.View(), buf.Len()

				p.Handle(e)
				model, _ = model.Update(tty.EventMsg(e))

				changed := model.View() != before
				said := buf.Len() > printed
				if changed && !said {
					t.Errorf("%s changed the live view but printed nothing "+
						"in plain mode: rich would show what a log never does",
						e.Kind)
				}
			}

			// And over the whole run: every word the final frame
			// carries must appear somewhere in the plain output.
			// The per-event check above allows the two to differ in
			// *when* they say something -- rich names the pending
			// steps up front, plain names each as it starts -- but
			// not in *whether*.
			plainText := buf.String()
			for _, word := range strings.Fields(stripANSI(model.View())) {
				if isPresentation(word) {
					continue
				}
				if !strings.Contains(plainText, word) {
					t.Errorf("the live view says %q and the plain output never does:"+
						"\n--- rich ---\n%s\n--- plain ---\n%s",
						word, model.View(), plainText)
				}
			}
		})
	}
}

// isPresentation reports whether a token is chrome rather than information:
// symbols, rules, elapsed clocks and the outcome word, which the two renderers
// are free to phrase differently. Everything else has to match.
func isPresentation(word string) bool {
	switch word {
	case "·", "—", "…", "morzer", "done", "ctrl-c", "to", "cancel",
		"needs", "a", "human", "rolled", "back", "cancelling",
		"waiting", "for", "child", "processes", "already", "satisfied":
		return true
	}
	sym := theme.ASCIISymbols
	for _, s := range []string{sym.OK, sym.Fail, sym.Warn, sym.Pending,
		sym.Skipped, sym.Compensated} {
		if word == s {
			return true
		}
	}
	// Durations and percentages: both renderers show them, rounded
	// differently and measured from different instants.
	return durationOrPercent.MatchString(word)
}

var durationOrPercent = regexp.MustCompile(`^\(?\d+(\.\d+)?(ms|s|m|h|%)`)

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func stripANSI(s string) string { return ansi.ReplaceAllString(s, "") }
