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

// TestTheMeasureHasAFloor.
//
// Doc is a public type and Screen is a plain struct, so a zero can arrive from
// any caller that has not been told a width. A measure of zero wraps every line
// to itself and leaves no room for a single column.
func TestTheMeasureHasAFloor(t *testing.T) {
	// Known, because that is the case with teeth: an unknown screen never
	// drops or shrinks a column, so a zero there costs only the wrapping.
	// A zero that claims to be measured reaches the table, where every cell
	// is truncated to it and the row comes out empty.
	for _, width := range []int{0, -5, 1} {
		d := ui.NewPlainDoc(ui.Screen{Width: width, Known: true})
		// Without a header, the column starts at zero and takes its
		// width from the cell — capped by the measure, which is the
		// number under test. A header would seed the width and hide it.
		d.Table(0, ui.Table{
			Columns:  []ui.Column{{Essential: true}},
			Rows:     [][]string{{"1.2.0"}},
			NoHeader: true,
		})

		require.Containsf(t, d.String(), "1.2.0",
			"a table on a %d-column screen lost its only cell:\n%q", width, d.String())
	}
}
