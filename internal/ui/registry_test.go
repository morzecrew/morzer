package ui_test

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// The registry's two refusals, which only run when the mistake they catch is
// present — so without a test they are dead code that looks like a guarantee.

type firstReport struct{ Field string }
type emptyReport struct{ Field string }

// TestTwoViewsForOneReportIsRefused.
//
// Last-writer-wins would leave two views for one report and no way to tell
// which one draws: the file you are reading may not be the one running. It
// panics at process start, in a test as much as in the binary, which is the
// earliest moment the mistake exists.
func TestTwoViewsForOneReportIsRefused(t *testing.T) {
	ui.Register(ui.View[firstReport]{
		Plain: func(w io.Writer, r firstReport) { _, _ = io.WriteString(w, r.Field) },
	})

	require.PanicsWithValue(t,
		"ui: two views registered for ui_test.firstReport",
		func() {
			ui.Register(ui.View[firstReport]{
				Rich: func(w io.Writer, _ *theme.Theme, r firstReport) {},
			})
		},
		"the second registration was accepted")
}

// TestAViewThatRendersInNoModeIsRefused.
//
// It passes every other check this package makes — it is registered, Render
// finds it, and no error comes back — and prints nothing. A command that exits
// 0 with an empty terminal is indistinguishable from a report with no rows,
// which is the worst failure a renderer has.
func TestAViewThatRendersInNoModeIsRefused(t *testing.T) {
	require.PanicsWithValue(t,
		"ui: the view for ui_test.emptyReport renders in no mode",
		func() { ui.Register(ui.View[emptyReport]{}) },
		"a view with neither rendering was accepted")
}
