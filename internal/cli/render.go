package cli

import (
	"context"
	"os"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
	"github.com/morzecrew/morzer/internal/ui/tty"
)

// rich reports whether this run draws the styled views.
//
// Quiet is excluded because quiet means errors only, and the plain presenter is
// required because it is what the styled path falls back to.
func (a *App) rich() bool {
	return a.Mode == ui.ModeRich && !a.Flags.quiet && a.plain != nil
}

// terminalInput is the file the live views read keys from.
//
// The injected stream when it is one -- an embedder that supplied its own pty
// must have its keys read from there, not from a stdin nobody is typing at --
// and the process's own otherwise, which is what Bubble Tea needs to open a
// terminal at all.
func (a *App) terminalInput() *os.File {
	if f, ok := a.Stream.In.(*os.File); ok {
		return f
	}
	return os.Stdin
}

// theme resolves the styling for this run.
func (a *App) theme() *theme.Theme {
	return theme.New(
		ui.UseColor(a.Mode, a.Flags.noColor, os.LookupEnv),
		theme.Unicode(os.LookupEnv),
	)
}

// runOperation runs one operation under whatever renderer the mode calls for,
// reports its result, and returns the operation's own error.
//
// Every step-based command goes through here, which is what keeps the choice
// of renderer in one place rather than in each command.
func (a *App) runOperation(ctx context.Context, fn func(context.Context) (ops.Result, error)) error {
	var (
		result ops.Result
		err    error
	)
	work := func() { result, err = fn(ctx) }

	switch {
	case !a.rich():
		work()
	case a.Flags.dryRun:
		// A plan is computed and then printed. There is nothing to
		// animate, so it gets a styled print rather than a program.
		a.runPlan(work)
	default:
		a.runLive(work)
	}

	a.finish(result)
	return err
}

// runLive drives the live view for the duration of one operation.
//
// Failure of the display is never failure of the operation. If the program
// cannot start, or dies mid-run, the plain presenter is unmuted and narrates
// the rest; the operation neither knows nor cares, because nothing here is on
// the path that produces its result.
func (a *App) runLive(work func()) {
	// Muted rather than unsubscribed: the plain presenter narrates
	// everything outside an operation, and it is also the fallback here.
	a.plain.Mute()

	tty.Run(tty.Options{
		Output: a.Stream.Err,
		Input:  a.terminalInput(),
		Theme:  a.theme(),
		// The view's ^C arrives as a keystroke -- raw mode suppressed
		// the signal main listens for -- so cancelling the operation's
		// context is this callback's job, exactly as the signal handler
		// would have.
		OnCancel:  a.cancelOperation,
		Subscribe: func(s events.Sink) func() { return a.bus.Subscribe(s) },
		OnDisplayFailure: func(err error) {
			if err != nil {
				// Logged, not returned. The operator loses the
				// animation, not the operation, and mapping a
				// rendering fault onto an exit code would be
				// exactly backwards.
				a.log.Warn("the live renderer failed; falling back to plain output",
					"error", err)
			}
			a.plain.Unmute()
		},
	}, work)
}

// runPlan renders a dry run's step list and configuration diffs.
func (a *App) runPlan(work func()) {
	var plan *events.Event
	unsubscribe := a.bus.SubscribeFunc(func(e events.Event) {
		if e.Kind == events.KindPlan {
			captured := e
			plan = &captured
		}
	})

	a.plain.Mute()
	work()
	unsubscribe()
	a.plain.Unmute()

	// No plan event means the run never reached the engine -- a refusal
	// during preflight, a bundle that failed verification. The error the
	// caller is about to print is the whole story, and inventing a header
	// above it would only obscure that.
	if plan != nil {
		tty.RenderPlan(a.Stream.Err, a.theme(), *plan, ui.TerminalWidth())
	}
}

// watchStatus runs the live status view.
//
// Refused outside a terminal rather than falling back to a loop of printed
// tables: a redraw needs a screen to redraw, and a --watch in a systemd unit
// or a CI job would otherwise fill a log with thousands of copies of the same
// table until someone noticed the disk.
func (a *App) watchStatus(ctx context.Context, interval time.Duration) error {
	if a.Mode == ui.ModeJSON {
		return domain.Usage("--watch and --json cannot be combined").
			WithHint("--json emits one object and exits; poll `morzer status --json` instead")
	}
	if !a.rich() {
		return domain.Usage("--watch needs a terminal").
			WithHint("run `morzer status` for a single reading, or " +
				"`watch morzer status` to poll one from a script")
	}

	// The plain presenter would otherwise write into the alternate screen
	// underneath the view.
	a.plain.Mute()
	defer a.plain.Unmute()

	return tty.Watch(ctx, tty.WatchOptions{
		Output:   a.Stream.Err,
		Input:    a.terminalInput(),
		Theme:    a.theme(),
		Interval: interval,
		Refresh: func(ctx context.Context) (ops.Status, error) {
			return ops.GetStatus(ctx, a.Deps)
		},
	})
}
