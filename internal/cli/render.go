package cli

import (
	"context"
	"io"
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

// render puts a report on stdout in whatever mode this run resolved to.
//
// The one boundary. A command decides *what* to say and hands over a value;
// `internal/ui` decides how it looks. Before this existed the mode was resolved
// carefully for every invocation and then honoured by 8% of the output -- 59
// direct prints against 5 renderer dispatches -- so a contributor adding a
// command had nothing to cross: `fmt.Fprintf(app.Stream.Out, …)` compiled and
// the mode was silently ignored.
//
// JSON is not rendered. The value *is* the machine-readable contract, published
// by App.finish through the envelope, exactly as before: a view that reshaped it
// would make every presentation change a breaking one.
//
// A report with no registered view returns an internal error rather than
// printing nothing. The registry test in internal/ui makes that unreachable in
// a shipped binary; it is written out because "unreachable" and "silent on the
// operator's terminal" must never be the same code path.
func (a *App) render(report any) error {
	if a.json != nil {
		a.jsonData = report
		return nil
	}

	mode := ui.ModePlain
	if a.rich() {
		mode = ui.ModeRich
	}
	if err := ui.Render(a.Stream.Out, mode, a.theme(), report); err != nil {
		return domain.Internal(err, "cannot render this report")
	}
	return nil
}

// notice draws a callout on stderr.
//
// Narration rather than a report, which is why it goes to stderr and not
// through the registry: nothing here is the answer to a command, and `--json`
// must not gain a key for it. It uses the components anyway, because the thing
// being narrated -- where the recovery key went, what to do after an import --
// is the one an operator must not scroll past, and the rule that reports and
// narration look like the same product is what makes the callout mean anything.
//
// Suppressed by --quiet along with every other narration, and by --json, where
// a box on stderr would land in the middle of a script's log.
func (a *App) notice(c ui.Callout) {
	if a.Flags.quiet || a.Mode == ui.ModeJSON {
		return
	}

	d := ui.NewPlainDoc(ui.CurrentScreen())
	if a.rich() {
		d = ui.NewDoc(a.theme(), ui.CurrentScreen())
	}
	d.Callout(2, c)
	d.Emit(a.Stream.Err)
}

// terminalInput is what the live views read keys from, and nil when there is
// nothing to read keys from.
//
// The injected stream when it is a terminal -- an embedder that supplied its
// own pty must have its keys read from there, not from a stdin nobody is
// typing at. Nil otherwise, and that is the important half: the output mode is
// decided by stdout and stderr, so `morzer apply < /dev/null` at a terminal
// legitimately draws the live view, and handing its reader a pipe would mean
// raw-mode setup on something that cannot be put in raw mode. With no input
// Bubble Tea subscribes to none, nothing is put in raw mode, and ctrl-C stays
// the signal main already handles.
func (a *App) terminalInput() io.Reader {
	if ui.IsTerminal(a.Stream.In) {
		return a.Stream.In
	}
	return nil
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
