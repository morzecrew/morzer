package ui_test

import (
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/ui"
)

const releaseNotes = "# demo 1.4.0\n\nFixes the thing that broke.\n\n- one\n- two\n"

// TestPlainModeGetsTheSourceText.
//
// Plain output is defined as line-oriented and stable in a log, and ANSI colour
// in a journal entry outlives the terminal that wanted it. What a vendor wrote
// is already readable, so plain mode passes it through rather than degrading it
// into something else.
func TestPlainModeGetsTheSourceText(t *testing.T) {
	got := ui.RenderNotes(ui.ModePlain, releaseNotes)

	if got != strings.TrimSpace(releaseNotes) {
		t.Errorf("plain mode rewrote the notes:\n%q", got)
	}
}

// TestRichModeRendersAndKeepsTheContent.
//
// The assertion is on the words rather than on the escape codes: which colours
// glamour chooses is its business and changes between versions, but notes that
// arrive rendered and *missing a line* would be a renderer quietly dropping what
// an operator is deciding on.
func TestRichModeRendersAndKeepsTheContent(t *testing.T) {
	got := ui.RenderNotes(ui.ModeRich, releaseNotes)

	if got == "" {
		t.Fatal("rich mode rendered release notes to nothing")
	}
	for _, want := range []string{"demo 1.4.0", "Fixes the thing that broke", "one", "two"} {
		if !strings.Contains(got, want) {
			t.Errorf("the rendered notes lost %q:\n%s", want, got)
		}
	}
}

// TestNothingRendersToNothing.
//
// A bundle without release notes renders nothing at all, exactly as `release
// show` behaves. The caller prints only what comes back, so an empty string is
// what keeps a blank section out of the output.
func TestNothingRendersToNothing(t *testing.T) {
	for _, mode := range []ui.Mode{ui.ModeRich, ui.ModePlain, ui.ModeJSON} {
		if got := ui.RenderNotes(mode, "   \n\n"); got != "" {
			t.Errorf("%s rendered whitespace-only notes as %q", mode, got)
		}
	}
}

// TestJSONModeIsNotStyled.
//
// `--json` is a machine contract, and a mode that decided to colour its stderr
// because the notes happened to be Markdown would put escape codes into
// whatever is reading it.
func TestJSONModeIsNotStyled(t *testing.T) {
	got := ui.RenderNotes(ui.ModeJSON, releaseNotes)

	if strings.Contains(got, "\x1b[") {
		t.Errorf("json mode styled the notes:\n%q", got)
	}
}
