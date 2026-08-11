package tty_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ui/theme"
	"github.com/morzecrew/morzer/internal/ui/tty"
)

// planEvent is a dry run of an update: one step already satisfied, one that
// will run, and one carrying a configuration diff.
func planEvent() events.Event {
	return events.Event{
		Kind: events.KindPlan, DryRun: true, OpID: "01J8ZR",
		Description: "update 1.2.0 → 1.3.0",
		Plan: []events.PlanStep{
			{ID: "verify", Description: "verify bundle signature", WillRun: true},
			{
				ID: "render", Description: "render configuration", WillRun: true,
				Diff: "--- a/app.env\n+++ b/app.env\n@@ -1,3 +1,3 @@\n" +
					" LOG_LEVEL=info\n-WORKERS=2\n+WORKERS=4\n",
			},
			{
				ID: "pull", Description: "pull images", WillRun: false,
				Reason: "already satisfied",
			},
		},
	}
}

func TestThePlanMarksWhatWillActuallyRun(t *testing.T) {
	var buf bytes.Buffer
	tty.RenderPlan(&buf, theme.New(false, false), planEvent(), 100)
	out := buf.String()

	for _, want := range []string{
		"verify bundle signature",
		"render configuration",
		"pull images",
		"already satisfied",
		"nothing was changed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan omits %q:\n%s", want, out)
		}
	}

	// The step that will not run is marked differently from the ones that
	// will. A plan showing three steps when two will act overstates the
	// change, which is the one thing a plan must not do.
	sym := theme.ASCIISymbols
	if !strings.Contains(out, sym.Skipped) || !strings.Contains(out, sym.Active) {
		t.Errorf("will-run and will-skip steps are not distinguishable:\n%s", out)
	}
}

func TestThePlanKeepsTheDiffReadable(t *testing.T) {
	var buf bytes.Buffer
	tty.RenderPlan(&buf, theme.New(false, false), planEvent(), 100)
	out := buf.String()

	// Every diff line survives. Colouring a diff must not drop any of it:
	// the diff is the reason to read a plan at all.
	for _, want := range []string{"-WORKERS=2", "+WORKERS=4", "@@ -1,3 +1,3 @@", "LOG_LEVEL=info"} {
		if !strings.Contains(out, want) {
			t.Errorf("the diff lost %q:\n%s", want, out)
		}
	}
}
