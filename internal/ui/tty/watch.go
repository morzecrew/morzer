package tty

import (
	"context"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// WatchOptions configure a live view of one repeatedly-read report.
//
// Generic over the report because there are two of them and they differ in
// nothing but what is read and how it is drawn: `status --watch` and
// `stats --watch` share the timer, the in-flight guard, the keys, the footer
// and the alt-screen. A second copy of all that would be where the two started
// disagreeing about what `r` does.
type WatchOptions[T any] struct {
	Output io.Writer
	// Input is the keyboard, or nil when stdin is not a terminal: see
	// Options.Input. Without one there is no `q`, and ctrl-C -- which works
	// precisely because no raw mode was entered -- is how the view ends.
	Input io.Reader
	Theme *theme.Theme

	// Interval is how often Refresh is called.
	Interval time.Duration

	// Refresh reads the current reading. It is called on a timer and on
	// demand, and is expected to be cheap: it inspects the runtime and the
	// state file, and takes no lock.
	Refresh func(context.Context) (T, error)

	// Body draws one reading into a document sized to the terminal.
	Body func(*ui.Doc, T) *ui.Doc

	// Subject names what could not be read, for the error line: "status",
	// "statistics".
	Subject string

	// StopAfterFailures ends the watch with the error after this many
	// consecutive failed reads. Zero means never, which is right for a view
	// somebody is staring at while a machine comes back up -- and wrong for
	// one sampling a daemon that has gone for good.
	StopAfterFailures int
}

// Watch runs a live view until the operator quits.
//
// The only alt-screen view in the program, and the reason is the inverse of why
// operations do not use one: a watch has no output worth keeping. It replaces
// its own frame every few seconds, so leaving fifty copies of the same table in
// the scrollback would bury whatever the operator was looking at before.
// Restoring the screen on exit is the correct behaviour precisely because there
// is nothing to preserve.
//
// It observes and never acts: no key restarts a service or clears an
// intervention. Those are operations, they take the lock, they journal, and
// they are commands. A dashboard that could mutate the deployment would be a
// participant, which is the one thing the UI layer is not.
func Watch[T any](ctx context.Context, opts WatchOptions[T]) error {
	model := newWatchModel(ctx, opts)
	program := tea.NewProgram(model,
		tea.WithOutput(opts.Output),
		tea.WithInput(opts.Input),
		tea.WithAltScreen(),
		// Unlike an operation's view, this one is bound to the context:
		// there is no work in flight that a torn-down display could
		// orphan, so Ctrl-C should simply end it.
		tea.WithContext(ctx),
	)

	final, err := program.Run()
	if err != nil && ctx.Err() != nil {
		// Interrupted. The context's cancellation is the real story and
		// the caller already knows it; a "program was killed" on top of
		// it would only obscure the exit code.
		return nil
	}
	if err != nil {
		return err
	}
	// A watch that gave up carries the reason out. The display ended
	// normally, so the program has nothing to report and the model does.
	if m, ok := final.(*watchModel[T]); ok {
		return m.fatal
	}
	return nil
}

func newWatchModel[T any](ctx context.Context, opts WatchOptions[T]) *watchModel[T] {
	interval := opts.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &watchModel[T]{
		theme:    opts.Theme,
		interval: interval,
		refresh:  opts.Refresh,
		body:     opts.Body,
		subject:  opts.Subject,
		giveUpAt: opts.StopAfterFailures,
		ctx:      ctx,
	}
}

type watchModel[T any] struct {
	theme    *theme.Theme
	interval time.Duration
	refresh  func(context.Context) (T, error)
	body     func(*ui.Doc, T) *ui.Doc
	subject  string
	ctx      context.Context

	reading *T
	err     error
	at      time.Time
	width   int
	height  int
	pending bool

	// failures counts consecutive failed reads, and fatal is the one that
	// ended the watch.
	failures int
	giveUpAt int
	fatal    error
}

// Fatal is why the watch stopped, or nil when the operator stopped it.
//
// Exported on the model rather than only returned by Watch so a test can drive
// the program without a terminal and still see the decision -- which is the
// half of `--watch` that decides an exit code.
func (m *watchModel[T]) Fatal() error { return m.fatal }

// readingMsg is the outcome of one refresh.
type readingMsg[T any] struct {
	reading T
	err     error
	at      time.Time
}

type refreshMsg time.Time

func (m *watchModel[T]) Init() tea.Cmd {
	// Claimed here as well as on every tick. The first fetch is a fetch: a
	// read that outlives the interval would otherwise meet a timer that
	// sees no one in flight and starts a second one against a runtime that
	// still owes an answer to the first -- which is the pile-up the guard
	// exists to stop, in the one window where the daemon is slowest.
	m.pending = true
	return tea.Batch(m.fetch(), m.schedule())
}

func (m *watchModel[T]) schedule() tea.Cmd {
	return tea.Tick(m.interval, func(t time.Time) tea.Msg { return refreshMsg(t) })
}

// fetch reads off the update loop. Bubble Tea runs commands in their own
// goroutine, so a slow docker inspect does not freeze the view.
func (m *watchModel[T]) fetch() tea.Cmd {
	return func() tea.Msg {
		v, err := m.refresh(m.ctx)
		return readingMsg[T]{reading: v, err: err, at: time.Now()}
	}
}

func (m *watchModel[T]) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "r":
			if m.pending {
				return m, nil
			}
			m.pending = true
			return m, m.fetch()
		}
		return m, nil

	case refreshMsg:
		if m.pending {
			// A refresh is still in flight -- the runtime is slow or
			// wedged. Queueing another would pile up calls against
			// the thing that is already struggling.
			return m, m.schedule()
		}
		m.pending = true
		return m, tea.Batch(m.fetch(), m.schedule())

	case readingMsg[T]:
		m.pending = false
		m.at = msg.at
		if msg.err != nil {
			// The last good reading stays on screen. A transient
			// failure to reach the runtime is itself worth showing,
			// but blanking the table over it would throw away the
			// information the operator is watching for.
			m.err = msg.err
			m.failures++
			if m.giveUpAt > 0 && m.failures >= m.giveUpAt {
				// Twice in a row is a daemon that has gone
				// rather than one that hiccuped, and a watch
				// that stayed up redrawing an error would exit
				// 0 whenever the operator eventually pressed q.
				m.fatal = msg.err
				return m, tea.Quit
			}
			return m, nil
		}
		m.err, m.failures = nil, 0
		v := msg.reading
		m.reading = &v
		return m, nil
	}
	return m, nil
}

func (m *watchModel[T]) View() string {
	t := m.theme
	subject := m.subject
	if subject == "" {
		subject = "the deployment"
	}

	if m.reading == nil && m.err == nil {
		return "\n  " + t.Dim("reading "+subject+"…") + "\n"
	}

	var b strings.Builder
	b.WriteString("\n")
	if m.reading != nil {
		b.WriteString(m.body(ui.NewDoc(t, ui.FixedScreen(m.width)), *m.reading).String())
	}

	if m.err != nil {
		b.WriteString("\n  " + t.Fail(t.Symbols.Fail+" could not read "+subject+": "+
			truncate(m.err.Error(), max(m.width-6, 20))) + "\n")
	}

	b.WriteString("\n  " + strings.Join([]string{
		t.Dim("updated " + m.at.Format("15:04:05")),
		t.Dim("every " + shortDuration(m.interval)),
		t.Dim("r to refresh, q to quit"),
	}, t.Dim("  ·  ")) + "\n")

	return b.String()
}
