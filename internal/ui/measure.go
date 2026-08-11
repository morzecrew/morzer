package ui

import (
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// MaxContentWidth caps the measure.
//
// The measure and the viewport are different numbers, and conflating them is
// the defect this constant exists to fix. The *viewport* is the terminal,
// whatever it is. The *measure* governs two things and only two: how far
// wrapped text runs before it breaks, and how far apart two pieces of content
// on one line may be pushed. A table whose columns genuinely need 130
// characters may use them -- packed left, ending where its content ends. What
// nothing may do is justify to the viewport, which is what the first doctor
// table did: `description = width - width/3 - 12` put a check and the sentence
// explaining it 207 spaces apart on a 380-column screen.
//
// 100 rather than 80 because the tables here carry identifiers and versions
// that 80 genuinely cannot hold, and because RenderNotes already holds the
// 80-column line for prose. Typographic practice puts a comfortable measure at
// 45-75 characters; glamour, already a dependency, defaults to 80. A hundred is
// the widest that is still one measure rather than a screen.
const MaxContentWidth = 100

// Gutter is the space between two columns, everywhere.
//
// One value, so "how far apart are these" is never a per-view decision.
const Gutter = 2

// Screen is what a document is drawn onto.
type Screen struct {
	Width int

	// Known is false when nothing reported a width: stdout is a pipe, a
	// file, or a CI log capture. It matters for exactly one decision --
	// whether a table may drop a column to fit -- and the answer there is
	// no. There is no screen to be too narrow, the fallback width is a
	// guess, and degrading on a guess loses a column the operator asked
	// for in a context where nothing was ever going to be truncated.
	Known bool
}

// ScreenFor reports what a document written to w is drawn onto.
//
// The destination decides, not the process. A report goes to stdout and a
// callout to stderr, and `morzer release list | grep` is a terminal on one and
// a pipe on the other: probing whichever stream happens to be a terminal would
// answer with stderr's width for output nobody is looking at, and a table would
// then drop columns to fit a screen the reader is not reading from.
//
// COLUMNS still wins. An operator who exports it means it for everything, and
// it is what lets the golden tests pin every width without a pty.
func ScreenFor(w any) Screen {
	if v, ok := os.LookupEnv("COLUMNS"); ok {
		if n := atoiSafe(v); n > minContentWidth {
			return Screen{Width: n, Known: true}
		}
	}
	if f, ok := w.(*os.File); ok {
		if n, ok := terminalSize(f); ok && n > minContentWidth {
			return Screen{Width: n, Known: true}
		}
	}
	return Screen{Width: fallbackWidth}
}

// terminalSize is how wide a file is, when it is a terminal.
//
// A variable so a test can answer for any file: whether a screen is taken from
// the stream being written to or from whichever of the process's own streams
// happens to be a terminal is the whole rule here, and a test process has no
// terminal to tell the two apart with.
var terminalSize = func(f *os.File) (int, bool) {
	n, _, err := term.GetSize(int(f.Fd()))
	return n, err == nil
}

// CurrentScreen is the screen this process's diagnostics are drawn onto.
//
// Stderr, because that is where narration goes. A report asks ScreenFor with
// the stream it is being written to.
func CurrentScreen() Screen { return ScreenFor(os.Stderr) }

// FixedScreen is a screen of a stated width, for a caller that already knows it
// -- the live view is told its size by the terminal program.
//
// A width nobody has supplied yet is unknown rather than tiny: Bubble Tea sends
// the size after the first frame, and a zero taken literally makes that frame a
// document too narrow for any column of any table.
func FixedScreen(width int) Screen {
	if width < minContentWidth {
		return Screen{Width: fallbackWidth}
	}
	return Screen{Width: width, Known: true}
}

// Width is the visible width of a string.
//
// It answers the two questions that make alignment hard: ANSI sequences take no
// columns, and an East Asian wide rune takes two. Every alignment decision here
// goes through it, so a styled cell and its unstyled equivalent pad identically.
func Width(s string) int { return ansi.StringWidth(s) }

// Truncate shortens a string to width, marking that it did.
//
// ANSI-aware, which a hand-rolled version is not: a truncation that counted
// escape bytes as columns would cut a styled cell short and leave its reset
// sequence behind, colouring the rest of the row.
func Truncate(s string, width int, tail string) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, tail)
}

// Wrap breaks text so no line exceeds width.
//
// Words longer than the measure are broken rather than allowed to overhang -- a
// container ID, a digest or a URL is the common case, and a line running past
// the measure is what the measure exists to prevent. Styling survives the break:
// the wrapper re-opens the active sequence on each line, which is the whole
// reason this is not four lines of strings.Fields.
func Wrap(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	return strings.Split(ansi.Wrap(s, width, ""), "\n")
}

// pad right-pads to width, measuring visible columns.
func pad(s string, width int) string {
	if n := width - Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// padLeft left-pads to width, for the columns that align right.
func padLeft(s string, width int) string {
	if n := width - Width(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}
