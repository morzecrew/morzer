package tty

import (
	"fmt"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/ui"
)

// View draws the step list.
//
// Completed steps collapse to one line with a duration; the active step expands
// with a spinner, a progress bar when the step can report one, and a short tail
// of subprocess output; pending steps are dimmed. The shape is a list that only
// ever grows downward, which is what keeps it readable in scrollback after the
// operation is over.
func (m *Model) View() string {
	if m.opID == "" {
		return "" // nothing has happened yet
	}

	var b strings.Builder
	b.WriteString("\n  " + m.header() + "\n\n")

	for i := range m.steps {
		b.WriteString(m.renderStep(i))
	}

	for _, msg := range m.messages {
		b.WriteString("\n  " + msg + "\n")
	}
	if tail := m.renderOutput(); tail != "" {
		b.WriteString("\n" + tail)
	}

	b.WriteString("\n  " + m.footer() + "\n")
	return b.String()
}

func (m *Model) header() string {
	label := m.description
	if label == "" {
		label = string(m.opType)
	}
	if m.dryRun {
		label += m.theme.Dim("  (dry run — nothing will change)")
	}
	return m.theme.Bold("morzer " + label)
}

// renderStep draws one line, plus the active step's extras.
func (m *Model) renderStep(i int) string {
	s := m.steps[i]

	symbol, style := m.marker(s.state)

	// A pending step is dimmed whole, description included: the list is
	// there to show what is coming, not to compete with what is running.
	label := s.description
	if label == "" {
		label = "…" // the engine has not named this one yet
	}
	if s.state == statePending {
		label = m.theme.Dim(label)
	}
	line := "  " + style(symbol) + " " + label

	switch {
	case s.state == stateActive:
		// The active line carries the live detail: a bar when the step
		// can report a fraction, the current sub-activity otherwise.
		if s.progress >= 0 {
			line += "  " + m.progress.ViewAs(s.progress) +
				fmt.Sprintf(" %3.0f%%", s.progress*100)
		}
		if s.detail != "" {
			line += "  " + m.theme.Detail(truncate(s.detail, m.detailWidth()))
		}

	case s.state == stateFailed && s.detail != "":
		line += "  " + m.theme.Fail(s.detail)

	case s.state == stateSkipped:
		line += "  " + m.theme.Dim("already satisfied")

	case s.duration > 0:
		line += "  " + m.theme.Dim(shortDuration(s.duration))
	}

	return line + "\n"
}

// marker maps a state to its symbol and style.
//
// The symbol is the signal and the style reinforces it; with colour off, every
// state is still distinct.
func (m *Model) marker(state stepState) (string, func(string) string) {
	sym := m.theme.Symbols
	switch state {
	case stateActive:
		if m.cancelling {
			return sym.Warn, m.theme.Warn
		}
		return m.spinner.View(), m.theme.Active
	case stateDone:
		return sym.OK, m.theme.OK
	case stateSkipped:
		return sym.Skipped, m.theme.Dim
	case stateFailed:
		return sym.Fail, m.theme.Fail
	case stateCompensated:
		return sym.Compensated, m.theme.Warn
	default:
		return sym.Pending, m.theme.Dim
	}
}

// renderOutput draws the tail of the active step's subprocess output.
func (m *Model) renderOutput() string {
	lines := m.output.all()
	if len(lines) == 0 || m.finished {
		return ""
	}

	var b strings.Builder
	for _, line := range lines {
		// Truncated, never wrapped. A wrapped 200-column docker line
		// destroys the step list's alignment, and the full text is in
		// the log either way.
		b.WriteString("    " + m.theme.Dim(truncate(line, m.width-6)) + "\n")
	}
	return b.String()
}

func (m *Model) footer() string {
	parts := []string{m.theme.Dim(m.opID)}

	if !m.started.IsZero() {
		parts = append(parts, m.theme.Dim(shortDuration(m.elapsed())))
	}

	switch {
	case m.cancelling && !m.finished:
		parts = append(parts, m.theme.Warn("cancelling — waiting for child processes (ctrl-c again to force quit)"))
	case m.finished:
		parts = append(parts, m.outcome())
	default:
		parts = append(parts, m.theme.Dim("ctrl-c to cancel"))
	}

	return strings.Join(parts, m.theme.Dim("  ·  "))
}

// outcome is the one-word verdict in the footer.
//
// The three failure statuses are kept distinct, because they mean different
// things to whoever is reading: the system is where it started, the partial
// work was undone, or it could not be and a human has to look.
func (m *Model) outcome() string {
	switch m.status {
	case "succeeded":
		return m.theme.OK("done")
	case "compensated":
		return m.theme.Warn("rolled back")
	case "requires-manual-intervention":
		return m.theme.Fail("needs a human")
	case "":
		return ""
	default:
		return m.theme.Fail(m.status)
	}
}

func (m *Model) elapsed() time.Duration {
	if m.duration > 0 {
		return m.duration
	}
	if m.now.IsZero() {
		return 0
	}
	return m.now.Sub(m.started)
}

// detailWidth is what is left on the active line after the symbol, the
// description and the bar.
func (m *Model) detailWidth() int {
	return max(m.width/3, 12)
}

// truncate shortens to a display width, with an ellipsis when it cuts.
//
// A width of one or less is left alone rather than replaced by the ellipsis
// itself: the callers derive it by subtracting a margin from the terminal, so
// the degenerate value means the screen is too narrow to say anything, not that
// this string in particular needs cutting.
func truncate(s string, width int) string {
	if width <= 1 {
		return s
	}
	return ui.Truncate(s, width, "…")
}

// shortDuration renders an elapsed time the way somebody watching reads one.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
