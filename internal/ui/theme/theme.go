// Package theme holds the symbols and styles the terminal views draw with.
//
// Two rules shape everything here, and both come from the same place -- the
// terminals this runs on are not all the one on your desk.
//
// **Symbols carry state; colour reinforces it.** Every state is distinguishable
// with colour switched off entirely, because `NO_COLOR`, a monochrome terminal
// and a pipe into `less` are supported targets rather than degraded ones. If a
// reader can only tell "failed" from "done" by hue, the view is broken for more
// people than you think.
//
// **Nothing here is configurable.** One theme, adapting to a light or dark
// background. Configurable colour is a maintenance surface, a support burden and
// a source of unreadable combinations, and no operator has asked for it.
package theme

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Symbols are the state markers a step list draws.
//
// The ASCII set is not a lesser version: it is what a Linux virtual console, a
// terminal without a Unicode locale, and half the world's CI log viewers will
// actually render. Both sets are padded to the same display width so a column
// of them stays aligned whichever is in use.
type Symbols struct {
	OK          string
	Fail        string
	Active      string
	Pending     string
	Skipped     string
	Warn        string
	Compensated string
}

// UnicodeSymbols is the preferred set.
var UnicodeSymbols = Symbols{
	OK:          "✓",
	Fail:        "✗",
	Active:      "▸",
	Pending:     "·",
	Skipped:     "»",
	Warn:        "!",
	Compensated: "↺",
}

// ASCIISymbols is what a terminal that cannot render the above gets.
var ASCIISymbols = Symbols{
	OK:          "+",
	Fail:        "x",
	Active:      ">",
	Pending:     ".",
	Skipped:     "-",
	Warn:        "!",
	Compensated: "<",
}

// SpinnerFrames are the frames of the active-step spinner, per symbol set.
//
// The braille frames are one cell wide and animate smoothly; the ASCII ones are
// the classic four. Both are the same width as the static symbols they replace,
// so a step changing state does not shift the line.
var (
	UnicodeSpinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	ASCIISpinner   = []string{"|", "/", "-", "\\"}
)

// Theme is the resolved styling for one run.
type Theme struct {
	Symbols Symbols
	Spinner []string

	// Colour reports whether styles do anything. Kept so a caller can skip
	// work that only matters when styled.
	Colour bool

	ok        lipgloss.Style
	fail      lipgloss.Style
	warn      lipgloss.Style
	active    lipgloss.Style
	dim       lipgloss.Style
	bold      lipgloss.Style
	detail    lipgloss.Style
	added     lipgloss.Style
	removed   lipgloss.Style
	highlight lipgloss.Style
}

// New resolves a theme.
//
// `colour` comes from ui.UseColor, which has already considered NO_COLOR,
// CLICOLOR, the terminal type and whether the stream is a terminal at all.
// `unicode` comes from Unicode below.
func New(colour, unicode bool) *Theme {
	t := &Theme{Colour: colour, Symbols: ASCIISymbols, Spinner: ASCIISpinner}
	if unicode {
		t.Symbols, t.Spinner = UnicodeSymbols, UnicodeSpinner
	}

	if !colour {
		// Every style is the identity function. The views do not branch
		// on colour; they always call the style and the style decides,
		// which is what keeps a monochrome path from rotting unnoticed.
		return t
	}

	// Adaptive pairs, so the same theme reads on a light or dark
	// background without the operator configuring anything.
	t.ok = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "42"})
	t.fail = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"})
	t.warn = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "130", Dark: "214"})
	t.active = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "26", Dark: "39"})
	t.dim = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "247", Dark: "243"})
	t.bold = lipgloss.NewStyle().Bold(true)
	t.detail = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "245"})
	t.added = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "42"})
	t.removed = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"})
	t.highlight = lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.AdaptiveColor{Light: "26", Dark: "39"})
	return t
}

// Style accessors. Each is the identity when colour is off, so callers never
// branch.

func (t *Theme) OK(s string) string        { return t.ok.Render(s) }
func (t *Theme) Fail(s string) string      { return t.fail.Render(s) }
func (t *Theme) Warn(s string) string      { return t.warn.Render(s) }
func (t *Theme) Active(s string) string    { return t.active.Render(s) }
func (t *Theme) Dim(s string) string       { return t.dim.Render(s) }
func (t *Theme) Bold(s string) string      { return t.bold.Render(s) }
func (t *Theme) Detail(s string) string    { return t.detail.Render(s) }
func (t *Theme) Added(s string) string     { return t.added.Render(s) }
func (t *Theme) Removed(s string) string   { return t.removed.Render(s) }
func (t *Theme) Highlight(s string) string { return t.highlight.Render(s) }

// Unicode reports whether the terminal can be expected to render the symbols
// above.
//
// Decided from the locale, which is the only signal available without probing
// the terminal and reading a response -- and probing means writing bytes to a
// stream that may not be a terminal at all. A false negative gives ASCII, which
// is always readable; a false positive gives replacement characters, which are
// not. So the test is deliberately conservative: it wants to see UTF-8 named.
func Unicode(lookup func(string) (string, bool)) bool {
	if lookup == nil {
		return false
	}

	// The Linux virtual console renders a fixed 512-glyph font that has no
	// braille and no check mark, whatever the locale claims.
	if term, _ := lookup("TERM"); term == "linux" || term == "dumb" || term == "" {
		return false
	}

	// Highest-precedence locale variable first, as POSIX defines it.
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		value, set := lookup(name)
		if !set || value == "" {
			continue
		}
		normalised := strings.ToUpper(strings.ReplaceAll(value, "-", ""))
		return strings.Contains(normalised, "UTF8")
	}
	return false
}
