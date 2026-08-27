package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// notesWidth is where release notes wrap.
//
// Fixed rather than measured from the terminal: these are printed after an
// operation from a command that may be running under a timer, in a pipe or in a
// CI log, where "the terminal" is 80 columns by convention and nothing else.
const notesWidth = 80

// RenderNotes turns a bundle's release notes into something worth reading.
//
// This is RFC 0002's P5, which sat unbuilt for months behind "gated on a bundle
// actually shipping a RELEASE.md" -- a gate nothing in the project could open,
// because no scaffold wrote such a file and no page mentioned one. It lands here
// because two other changes opened it: `release new` now writes the stub, and a
// staged-but-unapplied release is a moment where "what changes" is the question
// an operator is actually asking.
//
// Rich mode wraps; it does not restyle. What a vendor wrote is Markdown, which
// is a format designed to be read as source, and the whole of what rendering it
// added was colour -- at the price of a Markdown parser, a syntax highlighter
// and an HTML sanitiser linked into a deployment tool. Wrapping is the part that
// was doing work, because a paragraph written as one long line is genuinely hard
// to read, and `ansi.Wordwrap` is already here for the tables.
//
// Plain mode gets the source untouched. Plain is defined as line-oriented output
// that is stable in a log, and a wrap point is a decision about a terminal that
// a journal entry outlives.
func RenderNotes(mode Mode, notes string) string {
	notes = strings.TrimSpace(notes)
	if notes == "" || mode != ModeRich {
		return notes
	}
	return strings.TrimRight(ansi.Wordwrap(notes, notesWidth, ""), "\n")
}
