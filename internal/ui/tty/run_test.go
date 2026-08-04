package tty_test

import (
	"bytes"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ui/theme"
	"github.com/morzecrew/morzer/internal/ui/tty"
)

// Run is the whole terminal-program lifecycle, and the reason internal/cli
// does not import Bubble Tea. What it has to get right is not the drawing --
// the model tests cover that -- but the handover: a display that cannot start
// must not take the operation with it, and work must run either way.

// pipeInput is a real *os.File that is not a terminal and never delivers a
// key, which is what a redirected run gives Bubble Tea.
//
// A pipe rather than /dev/null: bubbletea's cancel reader registers the input
// with epoll, and /dev/null cannot be registered -- the program then fails to
// start for a reason that has nothing to do with what is under test.
func pipeInput(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// Only the write end is closed. Bubble Tea's read loop is a goroutine
	// that outlives Run -- it sits in epoll on the descriptor it was given
	// and is not joined at teardown -- so closing the read end here is a
	// race on the file, which the detector duly found. Letting the read end
	// be collected leaks one descriptor per test and races with nothing.
	t.Cleanup(func() { _ = w.Close() })
	return r
}

func TestRunExecutesTheWorkAndTearsTheViewDown(t *testing.T) {
	bus := events.NewBus()
	var out bytes.Buffer

	var (
		mu        sync.Mutex
		ran       bool
		failure   error
		failureOK bool
	)

	tty.Run(tty.Options{
		Output:    &out,
		Input:     pipeInput(t),
		Theme:     theme.New(false, false),
		Subscribe: func(s events.Sink) func() { return bus.Subscribe(s) },
		OnDisplayFailure: func(err error) {
			mu.Lock()
			defer mu.Unlock()
			failure, failureOK = err, true
		},
	}, func() {
		mu.Lock()
		ran = true
		mu.Unlock()

		bus.Publish(events.OperationStarted("op-1", domain.OpTypeUpdate,
			"update demo", []string{"a", "b"}, false))
		bus.Publish(events.StepStarted("op-1", "a", "first step", 0, 2))
		bus.Publish(events.StepFinished("op-1", "a", domain.StepSucceeded, time.Millisecond, nil))
	})

	mu.Lock()
	defer mu.Unlock()

	if !ran {
		t.Fatal("the work never ran, so attaching a display can stop an operation")
	}
	// The handover is called exactly once, with nil on a clean run: the
	// caller uses it to decide whether the plain presenter takes over.
	if !failureOK {
		t.Error("the caller was never told the display finished, so it cannot " +
			"know whether to print the summary itself")
	}
	if failure != nil {
		t.Errorf("a clean run reported a display failure: %v", failure)
	}
}

// TestWorkRunsEvenWhenTheDisplayCannotStart. A rendering fault is not an
// operation fault and must never reach an exit code.
func TestWorkRunsEvenWhenTheDisplayCannotStart(t *testing.T) {
	bus := events.NewBus()

	var (
		mu  sync.Mutex
		ran bool
	)

	tty.Run(tty.Options{
		// A writer that refuses everything, which is what a closed
		// terminal looks like from inside the program.
		Output:           brokenWriter{},
		Input:            pipeInput(t),
		Theme:            theme.New(false, false),
		Subscribe:        func(s events.Sink) func() { return bus.Subscribe(s) },
		OnDisplayFailure: func(error) {},
	}, func() {
		mu.Lock()
		ran = true
		mu.Unlock()
	})

	mu.Lock()
	defer mu.Unlock()
	if !ran {
		t.Fatal("a display that could not draw stopped the operation, which turns " +
			"a cosmetic problem into an outage")
	}
}

// TestTheSubscriptionIsRemovedAfterwards. A sink left attached would keep a
// dead program's channel in the bus for the life of the process.
func TestTheSubscriptionIsRemovedAfterwards(t *testing.T) {
	bus := events.NewBus()

	var (
		mu         sync.Mutex
		subscribed int
		removed    int
	)

	tty.Run(tty.Options{
		Output: &bytes.Buffer{},
		Input:  pipeInput(t),
		Theme:  theme.New(false, false),
		Subscribe: func(s events.Sink) func() {
			mu.Lock()
			subscribed++
			mu.Unlock()
			unsub := bus.Subscribe(s)
			return func() {
				mu.Lock()
				removed++
				mu.Unlock()
				unsub()
			}
		},
		OnDisplayFailure: func(error) {},
	}, func() {})

	mu.Lock()
	defer mu.Unlock()
	if subscribed != 1 || removed != 1 {
		t.Errorf("subscribed %d times and unsubscribed %d; the bus keeps a dead "+
			"program's sink", subscribed, removed)
	}
}

// TestNoOptionsAtAllStillRunsTheWork covers the zero value, which a caller
// that only wants the work reaches by accident.
func TestNoOptionsAtAllStillRunsTheWork(t *testing.T) {
	var ran bool
	tty.Run(tty.Options{
		Output:    &bytes.Buffer{},
		Input:     pipeInput(t),
		Theme:     theme.New(false, false),
		Subscribe: func(events.Sink) func() { return func() {} },
	}, func() { ran = true })

	if !ran {
		t.Error("the work did not run with no failure handler configured")
	}
}

// brokenWriter is a terminal that has gone away.
type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("terminal closed") }
