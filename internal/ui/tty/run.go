package tty

import (
	"io"
	"os"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// Options configure one live run.
type Options struct {
	// Output is where the view is drawn. stderr, always: stdout carries the
	// command's result, and a spinner in it would break the first pipeline
	// anyone wrote.
	Output io.Writer

	// Input is the terminal the program reads keys from, or nil when there
	// is no terminal to read from -- a redirected stdin, an embedder's
	// buffer. Nil is not a degraded mode by accident: Bubble Tea subscribes
	// to input only when it has some, so nothing tries to put a pipe into
	// raw mode, and ctrl-C stays an ordinary signal because nothing
	// suppressed it. Only Ctrl-C means
	// anything.
	Input io.Reader

	// OnCancel is invoked on the first Ctrl-C. Raw mode turns ISIG off, so
	// the keystroke is the only form a ^C takes at a live view -- without
	// this callback the footer's "ctrl-c to cancel" is a lie and the
	// operation runs to completion. A second Ctrl-C force-quits.
	OnCancel func()

	Theme *theme.Theme

	// Subscribe attaches a sink to the event bus and returns the
	// unsubscribe function. Passed in rather than taking a *events.Bus so
	// this package needs no knowledge of how the bus is assembled.
	Subscribe func(events.Sink) func()

	// OnDisplayFailure is called when the program cannot start, or dies
	// mid-run. It is how the caller hands the rest of the operation back to
	// the plain presenter.
	//
	// It is not an error return, because a rendering fault is not an
	// operation fault and must never reach an exit code.
	OnDisplayFailure func(error)
}

// Run attaches the live view to the bus, runs work, and tears the view down.
//
// The program lives for the operation and no longer. Everything outside one --
// `status`, an error raised before the engine is reached -- is plain output,
// and there is no state worth keeping between operations anyway.
//
// This function is why internal/cli does not import Bubble Tea: the whole
// terminal-program lifecycle lives here, behind an interface made of the
// event bus and a closure.
func Run(opts Options, work func()) {
	program := tea.NewProgram(
		New(opts.Theme, opts.OnCancel),
		tea.WithOutput(opts.Output),
		tea.WithInput(opts.Input),
	)

	// Once, because it is called both from the goroutine below when the
	// program dies early and unconditionally once work returns.
	var once sync.Once
	handOver := func(err error) {
		once.Do(func() {
			if opts.OnDisplayFailure != nil {
				opts.OnDisplayFailure(err)
			}
		})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		final, err := program.Run()
		if err != nil {
			handOver(err)
			return
		}
		// The second Ctrl-C's force quit. Exiting here rather than in
		// the model means Run has returned -- Bubble Tea has restored
		// the terminal -- so the operator's shell comes back cooked
		// instead of raw with no echo.
		if m, ok := final.(*Model); ok && m.forceQuit {
			os.Exit(domain.ExitInterrupted)
		}
	}()

	// p.Send is a no-op once the program has exited, so a late event -- or
	// one arriving after a display failure -- cannot panic the sink.
	unsubscribe := opts.Subscribe(Subscribe(program))
	work()
	unsubscribe()

	// The model quits itself on operation.finished. This covers the paths
	// that never emit one -- a lock timeout, a preflight refusal, a bundle
	// that failed verification -- so a failure before the engine starts
	// cannot leave a program running with nothing to draw.
	program.Quit()
	<-done

	// Whatever comes next -- the result summary, the error, the next
	// command in the shell -- belongs to the plain path again.
	handOver(nil)
}
