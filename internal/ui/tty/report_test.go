package tty_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui/plain"
	"github.com/morzecrew/morzer/internal/ui/theme"
	"github.com/morzecrew/morzer/internal/ui/tty"
)

// report is a doctor run with one of each status, two categories interleaved in
// execution order, and a remedy that only some checks carry.
func report() ops.DoctorReport {
	r := ops.DoctorReport{
		Results: []events.CheckResult{
			{
				ID: "tools.docker", Category: "tools", Status: events.CheckOK,
				Description: "docker version", Message: "27.3.1",
			},
			{
				ID: "storage.free", Category: "storage", Status: events.CheckWarn,
				Description: "disk headroom", Message: "3.1 GiB free",
				Remedy: "prune old releases with `morzer release prune`",
			},
			{
				ID: "tools.compose", Category: "tools", Status: events.CheckFail,
				Description: "compose version", Message: "2.20.0 is below the required 2.24.0",
				Remedy: "upgrade the Compose plugin",
			},
			{
				ID: "secrets.rotation", Category: "secrets", Status: events.CheckWarn,
				Description: "secret age", Message: "db_password is 200 days old",
				Remedy: "run `morzer secret rotate db_password`",
			},
		},
		Worst: "fail",
	}
	r.Summary.OK, r.Summary.Warn, r.Summary.Fail = 1, 2, 1
	return r
}

func TestTheDoctorTableGroupsByCategoryInFirstSeenOrder(t *testing.T) {
	var buf bytes.Buffer
	tty.RenderDoctor(&buf, theme.New(false, false), report(), 100)
	out := buf.String()

	// tools, storage, secrets: the order the categories first appeared,
	// not alphabetical and not execution order.
	tools, storage, secrets := strings.Index(out, "tools"),
		strings.Index(out, "storage"), strings.Index(out, "secrets")
	if tools < 0 || tools >= storage || storage >= secrets {
		t.Errorf("categories are not in first-seen order (tools=%d storage=%d secrets=%d):\n%s",
			tools, storage, secrets, out)
	}

	// The two tools checks are adjacent despite a storage check running
	// between them: that is what grouping is for.
	toolsBlock := out[tools:storage]
	for _, want := range []string{"docker version", "compose version"} {
		if !strings.Contains(toolsBlock, want) {
			t.Errorf("%q is not in the tools group:\n%s", want, toolsBlock)
		}
	}
}

func TestTheDoctorTableDistinguishesStatusesWithoutColour(t *testing.T) {
	var buf bytes.Buffer
	tty.RenderDoctor(&buf, theme.New(false, false), report(), 100)
	out := buf.String()

	sym := theme.ASCIISymbols
	for name, symbol := range map[string]string{
		"ok": sym.OK, "warn": sym.Warn, "fail": sym.Fail,
	} {
		if !strings.Contains(out, symbol) {
			t.Errorf("no %s marker (%q) in a monochrome report:\n%s", name, symbol, out)
		}
	}
}

// TestTheDoctorTableNeverShowsLessThanPlain is decision 3 applied to the report
// a operator is most likely to paste into a bug report.
func TestTheDoctorTableNeverShowsLessThanPlain(t *testing.T) {
	rep := report()

	var richBuf, plainBuf bytes.Buffer
	tty.RenderDoctor(&richBuf, theme.New(false, false), rep, 100)
	plain.RenderDoctor(&plainBuf, rep)

	// Whitespace-normalised: the table wraps a long message across two
	// lines and pads its columns. That is layout. The assertion is about
	// information.
	rich, plainText := flatten(richBuf.String()), flatten(plainBuf.String())

	// Both directions here, unlike the operation view: doctor produces a
	// fixed report rather than a stream, so there is no timing difference
	// to excuse a gap either way.
	for _, res := range rep.Results {
		for _, want := range []string{res.Category, res.Description, res.Message, res.Remedy} {
			if want == "" {
				continue
			}
			if !strings.Contains(rich, flatten(want)) {
				t.Errorf("the table omits %q:\n%s", want, richBuf.String())
			}
			if !strings.Contains(plainText, flatten(want)) {
				t.Errorf("plain omits %q:\n%s", want, plainBuf.String())
			}
		}
	}
}

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

// flatten collapses every run of whitespace to one space, so a wrapped or
// padded cell compares equal to the text it holds.
func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }
