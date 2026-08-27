package ui_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/ui"
)

// TestTheScreenComesFromTheDestination.
//
// `morzer release list | grep` is a pipe on stdout and, usually, a terminal on
// stderr. A screen probed from the process rather than from the stream being
// written to answers with the width of something nobody is reading, and a table
// then drops columns to fit it — hiding data in the one place where nothing was
// ever going to be truncated.
func TestTheScreenComesFromTheDestination(t *testing.T) {
	t.Setenv("COLUMNS", "")

	// Every file in this process now looks like a 137-column terminal. The
	// question is whether that reaches a report written somewhere else.
	ui.StubTerminalSize(t, 137)

	got := ui.ScreenFor(&bytes.Buffer{})
	require.Falsef(t, got.Known,
		"a report written to a pipe was sized from a terminal elsewhere: %+v", got)
	require.Positive(t, got.Width, "an unknown screen still needs a measure")

	// And a destination that *is* a terminal gets its width.
	require.Equal(t, ui.Screen{Width: 137, Known: true}, ui.ScreenFor(os.Stdout))
}

// TestAnExportedCOLUMNSWinsOverTheDestination.
//
// An operator who exports it means it for everything, and it is what lets the
// rendering tests pin every width without a pty.
func TestAnExportedCOLUMNSWinsOverTheDestination(t *testing.T) {
	t.Setenv("COLUMNS", "137")

	got := ui.ScreenFor(&bytes.Buffer{})
	require.True(t, got.Known)
	require.Equal(t, 137, got.Width)

	// Malformed values are ignored rather than parsed to something absurd.
	for _, bad := range []string{"", "0", "-1", "+80", "80x24", "abc", "10001"} {
		t.Setenv("COLUMNS", bad)
		require.Falsef(t, ui.ScreenFor(&bytes.Buffer{}).Known,
			"COLUMNS=%q was taken as a width", bad)
	}
}

// TestAScreenNobodyHasSizedYetIsNotATinyScreen.
//
// The live view is told its size by the terminal program, which sends it after
// the first frame. Taken literally, that zero makes a document too narrow to
// wrap anything and a table too narrow for any column — a first frame that
// looks like a bug in the report rather than in the sizing.
func TestAScreenNobodyHasSizedYetIsNotATinyScreen(t *testing.T) {
	t.Setenv("COLUMNS", "")

	for _, width := range []int{0, -1, 3} {
		got := ui.FixedScreen(width)
		require.Falsef(t, got.Known, "a width of %d was treated as measured", width)
		require.GreaterOrEqualf(t, got.Width, 20,
			"a width of %d produced a measure nothing can be drawn in", width)
	}

	require.Equal(t, ui.Screen{Width: 60, Known: true}, ui.FixedScreen(60))
}
