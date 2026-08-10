package ui

import (
	"strings"

	"github.com/charmbracelet/glamour"
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
// Markdown is rendered only in rich mode. Plain mode is defined as line-oriented
// output that is stable in a log, and ANSI colour in a journal entry is noise
// that outlives the terminal that wanted it -- so plain gets the source text,
// which is what a vendor wrote and is readable on its own.
//
// A rendering failure returns the source rather than an error. These are notes:
// failing an update check because a vendor's Markdown upset a parser would be
// the tail wagging the dog.
func RenderNotes(mode Mode, notes string) string {
	notes = strings.TrimSpace(notes)
	if notes == "" || mode != ModeRich {
		return notes
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(notesWidth),
	)
	if err != nil {
		return notes
	}
	out, err := renderer.Render(notes)
	if err != nil {
		return notes
	}
	return strings.Trim(out, "\n")
}
