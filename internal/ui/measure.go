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

// ContentWidth is what a view may draw inside.
func ContentWidth() int { return measureFor(TerminalWidth()) }

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

// CurrentScreen reports what this process is drawing onto.
func CurrentScreen() Screen {
	if v, ok := os.LookupEnv("COLUMNS"); ok {
		if n := atoiSafe(v); n > 20 {
			// An operator who exports COLUMNS means it, and it is
			// what lets the golden tests pin every width without a
			// pty.
			return Screen{Width: n, Known: true}
		}
	}
	for _, f := range []*os.File{os.Stderr, os.Stdout} {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 20 {
			return Screen{Width: w, Known: true}
		}
	}
	return Screen{Width: fallbackWidth}
}

// FixedScreen is a screen of a stated width, for a caller that already knows it
// -- the live view is told its size by the terminal program.
func FixedScreen(width int) Screen { return Screen{Width: width, Known: true} }

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
