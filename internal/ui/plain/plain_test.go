package plain_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ui/plain"
)

// Plain output is the reference every other presenter is measured against: it
// is what systemd journals, what CI logs, and what gets pasted into a bug
// report. These pin the parts the rich renderer's parity test cannot reach --
// the plan, the diagnostics, and muting.

func present(t *testing.T, verbose bool, emit func(p *plain.Presenter)) string {
	t.Helper()
	var buf bytes.Buffer
	p := plain.New(&buf, verbose)
	emit(p)
	return buf.String()
}

func TestAPlanIsMarkedAsIntentionsNotFacts(t *testing.T) {
	out := present(t, false, func(p *plain.Presenter) {
		p.Handle(events.Event{
			Kind: events.KindPlan, DryRun: true, OpID: "op_01",
			Plan: []events.PlanStep{
				{ID: "pull", Description: "pull images", WillRun: true},
				{ID: "migrate", Description: "run migrations", WillRun: false,
					Reason: "already satisfied"},
				{ID: "render", Description: "render configuration", WillRun: true,
					Diff: "--- a\n+++ b\n-WORKERS=2\n+WORKERS=4\n"},
			},
		})
	})

	for _, want := range []string{
		"pull images", "run migrations", "already satisfied",
		"render configuration", "-WORKERS=2", "+WORKERS=4",
		"nothing was changed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan omits %q:\n%s", want, out)
		}
	}

	// A step that will not run is marked differently from one that will:
	// a plan showing three steps when two act overstates the change.
	if !strings.Contains(out, "= [2/3]") {
		t.Errorf("the skipped step is not distinguishable:\n%s", out)
	}
}

func TestADiagnosticCarriesItsRemedy(t *testing.T) {
	out := present(t, false, func(p *plain.Presenter) {
		p.Handle(events.Check(events.CheckResult{
			ID: "tools.compose", Category: "tools", Status: events.CheckFail,
			Description: "compose version", Message: "2.20.0 is below 2.24.0",
			Remedy: "upgrade the Compose plugin",
		}))
		p.Handle(events.Check(events.CheckResult{
			ID: "tools.docker", Category: "tools", Status: events.CheckOK,
			Description: "docker version", Message: "27.3.1",
		}))
	})

	for _, want := range []string{
		"compose version", "2.20.0 is below 2.24.0",
		"upgrade the Compose plugin", // the actionable half is never dropped
		"docker version",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the diagnostics omit %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("a failing check is not marked:\n%s", out)
	}
}

func TestEveryOperationOutcomeIsDistinguishable(t *testing.T) {
	outcomes := map[domain.OperationStatus]string{
		domain.StatusSucceeded:          "done in",
		domain.StatusCompensated:        "rolled back",
		domain.StatusManualIntervention: "MANUAL INTERVENTION REQUIRED",
		domain.StatusInterrupted:        "interrupted",
	}

	for status, want := range outcomes {
		out := present(t, false, func(p *plain.Presenter) {
			p.Handle(events.OperationFinished("op_01", domain.OpTypeUpdate,
				status, 7*time.Second, nil))
		})
		if !strings.Contains(out, want) {
			t.Errorf("status %q rendered as %q, want it to mention %q", status, out, want)
		}
	}
}

func TestAFailedStepPrintsItsHintEvenWhenNotVerbose(t *testing.T) {
	failure := domain.HealthError(nil, "api did not become healthy").
		WithHint("check `docker compose logs api`")

	out := present(t, false, func(p *plain.Presenter) {
		p.Handle(events.StepFinished("op", "wait", domain.StepFailed, time.Second, failure))
	})

	// The hint is the actionable half of an error, so it is never dropped.
	if !strings.Contains(out, "check `docker compose logs api`") {
		t.Errorf("the hint was dropped in non-verbose mode:\n%s", out)
	}
}

func TestSubprocessOutputIsVerboseOnly(t *testing.T) {
	emit := func(p *plain.Presenter) {
		p.Handle(events.StepOutput("op", "pull", "sha256:9f3a extracting"))
		p.Handle(events.StepProgress("op", "pull", 0.5, "pulling layer 7 of 11"))
	}

	// A plain log of an update should be a dozen lines, not the full output
	// of every tool it ran.
	if out := present(t, false, emit); strings.Contains(out, "extracting") {
		t.Errorf("subprocess output appeared without --verbose:\n%s", out)
	}
	if out := present(t, true, emit); !strings.Contains(out, "extracting") {
		t.Errorf("--verbose did not include subprocess output:\n%s", out)
	}
}

// TestMutingIsWhatLetsTheLiveViewTakeOver pins the mechanism the rich renderer
// depends on: this presenter stays subscribed for the whole process and is
// silenced while another renderer owns the terminal.
func TestMutingIsWhatLetsTheLiveViewTakeOver(t *testing.T) {
	var buf bytes.Buffer
	p := plain.New(&buf, false)

	p.Mute()
	p.Handle(events.Message(events.LevelWarn, "while muted"))
	if buf.Len() != 0 {
		t.Errorf("a muted presenter wrote:\n%s", buf.String())
	}

	p.Unmute()
	p.Handle(events.Message(events.LevelWarn, "after unmuting"))
	if !strings.Contains(buf.String(), "after unmuting") {
		t.Errorf("unmuting did not restore output:\n%s", buf.String())
	}
}

func TestMessagesAreStyledBySeverity(t *testing.T) {
	out := present(t, false, func(p *plain.Presenter) {
		p.Handle(events.Message(events.LevelWarn, "a warning"))
		p.Handle(events.Message(events.LevelError, "an error"))
		p.Handle(events.Message(events.LevelInfo, "a note"))
	})

	for _, want := range []string{"warning: a warning", "error: an error", "a note"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output omits %q:\n%s", want, out)
		}
	}

	// Debug is verbose-only, for the same reason subprocess output is.
	if out := present(t, false, func(p *plain.Presenter) {
		p.Handle(events.Message(events.LevelDebug, "a debug line"))
	}); strings.Contains(out, "a debug line") {
		t.Errorf("a debug message appeared without --verbose:\n%s", out)
	}
}
