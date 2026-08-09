package tty

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"

	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// RenderDoctor draws the diagnostic report as a table.
//
// Not a Bubble Tea program: doctor produces a report and stops, so there is
// nothing to animate and nothing to own the terminal for. This is a styled
// print, which is also why it works when piped into a pager.
//
// The grouping comes from ui.GroupChecks, the same function plain uses. Two
// implementations of the same table is how the two renderers start disagreeing
// about what the system found.
func RenderDoctor(w io.Writer, t *theme.Theme, report ops.DoctorReport, width int) {
	f := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }

	for i, group := range ui.GroupChecks(report.Results) {
		if i > 0 {
			f("")
		}
		f("%s", t.Bold(group.Category))
		f("%s", doctorTable(t, group.Results, width))
	}

	if remedies := ui.Remedies(report.Results); len(remedies) > 0 {
		f("")
		f("%s", t.Bold("what to do"))
		for _, res := range remedies {
			f("  %s", t.Highlight(res.Description))
			f("    %s", res.Remedy)
		}
	}

	f("")
	f("%s", summary(t, report))

	// Only when something failed: a support link on a clean run is one
	// nobody reads on the run that mattered.
	if report.SupportURL != "" {
		f("")
		f("%s %s", t.Bold("support"), report.SupportURL)
	}
}

// doctorTable renders one category.
func doctorTable(t *theme.Theme, results []events.CheckResult, width int) string {
	// The description column takes what is left after the marker and a
	// message column that gets a third of the terminal. Fixed widths would
	// make an 80-column terminal wrap and a 200-column one mostly empty.
	message := max(width/3, 24)
	description := max(width-message-12, 20)

	tbl := table.New().
		Border(lipgloss.HiddenBorder()).
		BorderTop(false).BorderBottom(false).
		BorderLeft(false).BorderRight(false).
		StyleFunc(func(row, col int) lipgloss.Style {
			switch col {
			case 0:
				return lipgloss.NewStyle().Width(4).Align(lipgloss.Right)
			case 1:
				return lipgloss.NewStyle().Width(description)
			default:
				return lipgloss.NewStyle().Width(message)
			}
		})

	for _, res := range results {
		symbol, style := checkMarker(t, res.Status)
		tbl.Row(style(symbol), res.Description, t.Dim(res.Message))
	}
	return tbl.String()
}

// checkMarker maps a diagnostic status to its symbol and style.
//
// The symbol carries the state and the colour reinforces it, so a monochrome
// terminal loses nothing -- which matters here more than anywhere, because a
// doctor report is the thing an operator pastes into a bug report.
func checkMarker(t *theme.Theme, status events.CheckStatus) (string, func(string) string) {
	switch status {
	case events.CheckWarn:
		return t.Symbols.Warn, t.Warn
	case events.CheckFail:
		return t.Symbols.Fail, t.Fail
	default:
		return t.Symbols.OK, t.OK
	}
}

// summary is the counted footer, with each count styled only when it is not
// zero: "0 failed" in red reads as a failure at a glance.
func summary(t *theme.Theme, report ops.DoctorReport) string {
	parts := []string{fmt.Sprintf("%d ok", report.Summary.OK)}
	if report.Summary.OK > 0 {
		parts[0] = t.OK(parts[0])
	}

	warn := fmt.Sprintf("%d warning", report.Summary.Warn)
	if report.Summary.Warn > 0 {
		warn = t.Warn(warn)
	}
	fail := fmt.Sprintf("%d failed", report.Summary.Fail)
	if report.Summary.Fail > 0 {
		fail = t.Fail(fail)
	}

	return strings.Join(append(parts, warn, fail), ", ")
}
