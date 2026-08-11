package views

import (
	"fmt"
	"io"
	"strings"

	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

func init() {
	ui.Register(ui.View[ops.DoctorReport]{
		Rich:  func(w io.Writer, t *theme.Theme, r ops.DoctorReport) { emit(w, DoctorDoc(doc(w, t), r)) },
		Plain: func(w io.Writer, r ops.DoctorReport) { emit(w, DoctorDoc(plainDoc(w), r)) },
	})
}

// Verbose is a report that expands every group.
//
// A distinct type rather than a flag on DoctorReport, because the flag would
// have to be carried through the lifecycle layer, serialised into `--json`, and
// then ignored there -- a presentation choice recorded in a machine contract.
// The command wraps the report it already has; the registry dispatches on the
// wrapper.
type Verbose struct{ ops.DoctorReport }

func init() {
	ui.Register(ui.View[Verbose]{
		Rich: func(w io.Writer, t *theme.Theme, v Verbose) {
			emit(w, doctorDoc(doc(w, t), v.DoctorReport, true))
		},
		Plain: func(w io.Writer, v Verbose) {
			emit(w, doctorDoc(plainDoc(w), v.DoctorReport, true))
		},
	})
}

// DoctorDoc draws the diagnostic report, collapsed.
func DoctorDoc(d *ui.Doc, report ops.DoctorReport) *ui.Doc {
	return doctorDoc(d, report, false)
}

// doctorDoc draws the report, showing what is wrong first.
//
// Twenty-nine checks with twenty-one of them passing is a report that buries its
// own finding. A group with nothing to report is one line saying how many checks
// it ran; a group with something shows only the checks that have something, in
// full. `--verbose` expands everything, which is what a support engineer asking
// for output wants.
//
// Group order is first-seen -- what ui.GroupChecks already produces -- so a
// category appears where its first check ran and its checks stay adjacent. That
// is the contract; the order of any particular run is not.
func doctorDoc(d *ui.Doc, report ops.DoctorReport, verbose bool) *ui.Doc {
	t := d.Theme()

	groups := ui.GroupChecks(report.Results)
	label := 0
	for _, g := range groups {
		if n := ui.Width(g.Category); n > label {
			label = n
		}
	}

	for _, group := range groups {
		interesting := make([]events.CheckResult, 0, len(group.Results))
		for _, res := range group.Results {
			if verbose || res.Status != events.CheckOK {
				interesting = append(interesting, res)
			}
		}

		if len(interesting) == 0 {
			// The collapsed form: the category, a tick, and how many
			// checks stand behind it. Enough that an operator can
			// see the area was examined without reading it.
			d.FieldsPadded(2, label, []ui.Field{{
				Label: group.Category,
				Value: t.OK(t.Symbols.OK),
				Note:  checkCount(len(group.Results)),
			}})
			continue
		}

		rows := make([]ui.CheckRow, 0, len(interesting))
		for _, res := range interesting {
			rows = append(rows, ui.CheckRow{
				State:       checkState(res.Status),
				Description: res.Description,
				Message:     res.Message,
			})
		}
		d.Text(2, "%s", group.Category)
		d.Checks(4, rows)
	}

	// The remedies are the best thing this command prints, and they go
	// above the summary so the last thing on screen is the count.
	if remedies := ui.Remedies(report.Results); len(remedies) > 0 {
		d.Heading("what to do")
		for _, res := range remedies {
			d.Text(4, "%s", t.Highlight(res.Description))
			d.Text(6, "%s", res.Remedy)
		}
	}

	d.Blank()
	d.Text(2, "%s", doctorSummary(t, report))

	// Only when something failed: a support link on a clean run is one
	// nobody reads on the run that mattered.
	if report.SupportURL != "" {
		d.Blank()
		d.Fields(2, []ui.Field{{Label: "support", Value: report.SupportURL}})
	}
	return d
}

// checkCount is the collapsed group's "and this is how many".
func checkCount(n int) string {
	if n == 1 {
		return "1 check"
	}
	return fmt.Sprintf("%d checks", n)
}

func checkState(status events.CheckStatus) ui.CheckState {
	switch status {
	case events.CheckWarn:
		return ui.CheckWarned
	case events.CheckFail:
		return ui.CheckFailed
	default:
		return ui.CheckPassed
	}
}

// doctorSummary is the counted footer, with each count styled only when it is
// not zero: "0 failed" in red reads as a failure at a glance.
func doctorSummary(t *theme.Theme, report ops.DoctorReport) string {
	ok := fmt.Sprintf("%d ok", report.Summary.OK)
	if report.Summary.OK > 0 {
		ok = t.OK(ok)
	}
	warn := fmt.Sprintf("%d warning", report.Summary.Warn)
	if report.Summary.Warn > 0 {
		warn = t.Warn(warn)
	}
	fail := fmt.Sprintf("%d failed", report.Summary.Fail)
	if report.Summary.Fail > 0 {
		fail = t.Fail(fail)
	}
	return strings.Join([]string{ok, warn, fail}, ", ")
}
