package tty

import (
	"context"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// WatchOptions configure the live status view.
type WatchOptions struct {
	Output io.Writer
	// Input is the keyboard, or nil when stdin is not a terminal: see
	// Options.Input. Without one there is no `q`, and ctrl-C -- which works
	// precisely because no raw mode was entered -- is how the view ends.
	Input io.Reader
	Theme *theme.Theme

	// Interval is how often Refresh is called.
	Interval time.Duration

	// Refresh reads the current status. It is called on a timer and on
	// demand, and is expected to be cheap: it inspects the runtime and the
	// state file, and takes no lock.
	Refresh func(context.Context) (ops.Status, error)
}

// Watch runs the status view until the operator quits.
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
func Watch(ctx context.Context, opts WatchOptions) error {
	program := tea.NewProgram(NewWatchModel(ctx, opts),
		tea.WithOutput(opts.Output),
		tea.WithInput(opts.Input),
		tea.WithAltScreen(),
		// Unlike an operation's view, this one is bound to the context:
		// there is no work in flight that a torn-down display could
		// orphan, so Ctrl-C should simply end it.
		tea.WithContext(ctx),
	)

	_, err := program.Run()
	if err != nil && ctx.Err() != nil {
		// Interrupted. The context's cancellation is the real story and
		// the caller already knows it; a "program was killed" on top of
		// it would only obscure the exit code.
		return nil
	}
	return err
}

// NewWatchModel builds the watch view.
//
// Exported so tests can drive it without a terminal; production code calls
// Watch, which owns the program and the alt-screen.
func NewWatchModel(ctx context.Context, opts WatchOptions) tea.Model {
	interval := opts.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &watchModel{
		theme:    opts.Theme,
		interval: interval,
		refresh:  opts.Refresh,
		ctx:      ctx,
	}
}

type watchModel struct {
	theme    *theme.Theme
	interval time.Duration
	refresh  func(context.Context) (ops.Status, error)
	ctx      context.Context

	status  *ops.Status
	err     error
	at      time.Time
	width   int
	height  int
	pending bool
}

// statusMsg is the outcome of one refresh.
type statusMsg struct {
	status ops.Status
	err    error
	at     time.Time
}

type refreshMsg time.Time

func (m *watchModel) Init() tea.Cmd {
	return tea.Batch(m.fetch(), m.schedule())
}

func (m *watchModel) schedule() tea.Cmd {
	return tea.Tick(m.interval, func(t time.Time) tea.Msg { return refreshMsg(t) })
}

// fetch reads the status off the update loop. Bubble Tea runs commands in their
// own goroutine, so a slow docker inspect does not freeze the view.
func (m *watchModel) fetch() tea.Cmd {
	return func() tea.Msg {
		s, err := m.refresh(m.ctx)
		return statusMsg{status: s, err: err, at: time.Now()}
	}
}

func (m *watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

	case statusMsg:
		m.pending = false
		m.at = msg.at
		if msg.err != nil {
			// The last good status stays on screen. A transient
			// failure to reach the runtime is itself worth showing,
			// but blanking the table over it would throw away the
			// information the operator is watching for.
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		s := msg.status
		m.status = &s
		return m, nil
	}
	return m, nil
}

func (m *watchModel) View() string {
	t := m.theme

	if m.status == nil && m.err == nil {
		return "\n  " + t.Dim("reading status…") + "\n"
	}

	var b strings.Builder
	b.WriteString("\n")
	if m.status != nil {
		b.WriteString(statusBody(t, *m.status))
	}

	if m.err != nil {
		b.WriteString("\n  " + t.Fail(t.Symbols.Fail+" could not read status: "+
			truncate(m.err.Error(), max(m.width-6, 20))) + "\n")
	}

	b.WriteString("\n  " + strings.Join([]string{
		t.Dim("updated " + m.at.Format("15:04:05")),
		t.Dim("every " + shortDuration(m.interval)),
		t.Dim("r to refresh, q to quit"),
	}, t.Dim("  ·  ")) + "\n")

	return b.String()
}
