// Package plain renders operations as one line per event.
//
// No ANSI, no cursor movement, no assumptions about a terminal. This is the
// presenter systemd, CI, and `2>&1 | tee` get, and it is the reference for
// what the richer presenters must convey: the rich mode may never show
// information plain mode omits.
package plain

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
)

// Presenter writes events line by line.
type Presenter struct {
	mu sync.Mutex
	w  io.Writer

	// verbose includes subprocess output and progress lines. Off by
	// default: a plain log of an update should be a dozen lines, not the
	// full output of every tool it ran.
	verbose bool

	// totalSteps backs the "[3/11]" prefix.
	totalSteps int

	// muted suppresses output while another renderer owns the terminal.
	//
	// This presenter stays subscribed for the whole process even in rich
	// mode, because it is what narrates everything outside an operation.
	// Muting it for the operation's duration is what guarantees exactly one
	// renderer is drawing at a time -- and unmuting is how the live view
	// hands back when it fails.
	muted bool
}

func New(stderr io.Writer, verbose bool) *Presenter {
	return &Presenter{w: stderr, verbose: verbose}
}

var _ events.Sink = (*Presenter)(nil)

// Mute stops the presenter writing anything until Unmute.
func (p *Presenter) Mute() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.muted = true
}

// Unmute resumes output.
func (p *Presenter) Unmute() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.muted = false
}

// Handle renders one event.
//
// Everything goes to stderr, including successes: stdout belongs to the
// command's result, and mixing progress into it would break every pipeline.
func (p *Presenter) Handle(e events.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.muted {
		return
	}

	switch e.Kind {
	case events.KindOperationStarted:
		p.totalSteps = e.StepCount
		if e.DryRun {
			p.line("plan: %s (%d steps, nothing will be changed)", e.Description, e.StepCount)
			return
		}
		// The operation id is on the first line rather than the last, so
		// an interrupted run still tells the operator what to pass to
		// --resume and what to look up in the journal.
		p.line("%s (%d steps)  %s", e.Description, e.StepCount, e.OpID)

	case events.KindStepStarted:
		p.line("[%d/%d] %s", e.StepIndex+1, p.totalSteps, e.Description)

	case events.KindStepProgress:
		if !p.verbose {
			return
		}
		if e.Detail != "" {
			p.line("        %s", e.Detail)
		}

	case events.KindStepOutput:
		if !p.verbose {
			return
		}
		p.line("        | %s", e.Message)

	case events.KindStepFinished:
		p.stepFinished(e)

	case events.KindOperationFinished:
		p.operationFinished(e)

	case events.KindPlan:
		p.plan(e)

	case events.KindCheck:
		p.check(e)

	case events.KindMessage:
		switch e.Level {
		case events.LevelWarn:
			p.line("warning: %s", e.Message)
		case events.LevelError:
			p.line("error: %s", e.Message)
		case events.LevelDebug:
			if p.verbose {
				p.line("        %s", e.Message)
			}
		default:
			p.line("%s", e.Message)
		}
	}
}

func (p *Presenter) stepFinished(e events.Event) {
	switch domain.StepStatus(e.Status) {
	case domain.StepSucceeded:
		p.line("        ok (%s)", shortDuration(e.Duration))
	case domain.StepSkipped:
		p.line("        skipped (already satisfied)")
	case domain.StepFailed:
		msg := "failed"
		if e.Err != nil {
			msg = "failed: " + e.Err.Message
		}
		p.line("        %s", msg)
		// The hint is the actionable half of an error, so it is never
		// dropped even in non-verbose mode.
		if e.Err != nil && e.Err.Hint != "" {
			p.line("        hint: %s", e.Err.Hint)
		}
	case domain.StepCompensated:
		p.line("        rolled back")
	case domain.StepInterrupted:
		p.line("        interrupted")
	}
}

func (p *Presenter) operationFinished(e events.Event) {
	status := domain.OperationStatus(e.Status)
	d := shortDuration(e.Duration)

	switch status {
	case domain.StatusSucceeded:
		p.line("done in %s", d)
	case domain.StatusCompensated:
		p.line("failed in %s; earlier changes were rolled back", d)
	case domain.StatusManualIntervention:
		p.line("failed in %s and could not be rolled back automatically", d)
		p.line("MANUAL INTERVENTION REQUIRED — run `morzer doctor`")
	case domain.StatusInterrupted:
		p.line("interrupted after %s", d)
	default:
		p.line("failed in %s", d)
	}

	// The error itself is deliberately not printed here. The CLI layer
	// renders it once, from the returned error, because it also has to
	// render failures that arise outside any operation -- a usage error, a
	// missing installation. Printing it in both places gave the operator
	// the same paragraph twice.
}

func (p *Presenter) plan(e events.Event) {
	for i, step := range e.Plan {
		marker := "+"
		suffix := ""
		if !step.WillRun {
			marker = "="
			suffix = "  (" + step.Reason + ")"
		} else if step.Reason != "" {
			suffix = "  (" + step.Reason + ")"
		}
		p.line("  %s [%d/%d] %s%s", marker, i+1, len(e.Plan), step.Description, suffix)

		if step.Diff != "" {
			for _, line := range strings.Split(strings.TrimRight(step.Diff, "\n"), "\n") {
				p.line("      %s", line)
			}
		}
	}
	p.line("")
	p.line("this is a plan; nothing was changed")
}

func (p *Presenter) check(e events.Event) {
	if e.Check == nil {
		return
	}
	c := *e.Check

	marker := "ok  "
	switch c.Status {
	case events.CheckWarn:
		marker = "warn"
	case events.CheckFail:
		marker = "FAIL"
	}

	line := fmt.Sprintf("  [%s] %s", marker, c.Description)
	if c.Message != "" {
		line += ": " + c.Message
	}
	p.line("%s", line)

	if c.Status != events.CheckOK && c.Remedy != "" {
		p.line("         → %s", c.Remedy)
	}
}

// line writes one rendered line.
//
// The write error is discarded deliberately: the destination is stderr, there
// is nothing useful to do when it fails, and an operation must never fail
// because its narration could not be printed.
func (p *Presenter) line(format string, args ...any) {
	_, _ = fmt.Fprintf(p.w, format+"\n", args...)
}

func shortDuration(d time.Duration) string {
	switch {
	case d == 0:
		return "0s"
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	default:
		return d.Round(time.Second).String()
	}
}

// Writer exposes the underlying stream so commands can print their own
// human-readable results through the same destination.
func (p *Presenter) Writer() io.Writer { return p.w }
