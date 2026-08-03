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
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/ui"
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

// RenderStatus prints the status card.
func RenderStatus(w io.Writer, s ops.Status) {
	f := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }

	f("%s", s.Product)
	f("  installation   %s", s.InstallationID)

	if s.CurrentRelease == nil {
		f("  release        none installed")
	} else {
		f("  release        %s", s.CurrentRelease.Version)
		if s.PreviousRelease != nil {
			f("  previous       %s", s.PreviousRelease.Version)
		}
	}
	if s.Profile != "" {
		f("  profile        %s", s.Profile)
	}
	if s.PublicURL != "" {
		f("  url            %s", s.PublicURL)
	}

	if len(s.Services) > 0 {
		f("")
		f("  services")
		for _, svc := range s.Services {
			state := svc.State
			if svc.Health != ports.HealthNone && svc.Health != "" {
				state += ", " + string(svc.Health)
			}
			f("    %-24s %s", svc.Name, state)
		}
	}

	if len(s.Health) > 0 {
		f("")
		f("  health")
		for _, h := range s.Health {
			marker := "ok"
			if !h.OK {
				marker = "FAIL"
			}
			f("    %-24s %s  %s", h.Name, marker, h.Message)
		}
	}

	f("")
	if s.LastBackup != nil {
		f("  last backup    %s (%s ago)", s.LastBackup.ID, s.LastBackup.Age)
	} else {
		f("  last backup    none")
	}

	if s.LastOperation != nil {
		f("  last operation %s %s (%s)",
			s.LastOperation.Type, s.LastOperation.Status, s.LastOperation.ID)
	}

	if s.LockHeldBy != nil {
		f("  lock           held by %s operation %s (pid %d)",
			s.LockHeldBy.Type, s.LockHeldBy.OperationID, s.LockHeldBy.PID)
	}

	// Anything needing attention goes last and loudest: it is the reason
	// the operator ran this command, whether or not they knew it.
	for _, rec := range s.NeedsAttention {
		f("")
		f("  ATTENTION: operation %s (%s) requires manual intervention", rec.ID, rec.Type)
		if rec.Error != nil {
			f("             %s", rec.Error.Message)
			if rec.Error.Hint != "" {
				f("             %s", rec.Error.Hint)
			}
		}
	}

	for _, problem := range s.Problems {
		f("")
		f("  warning: %s", problem)
	}
}

// RenderDoctor prints the diagnostic table grouped by category.
func RenderDoctor(w io.Writer, report ops.DoctorReport) {
	f := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }

	// Grouping is shared with the rich renderer, not reimplemented: two
	// implementations of the same table is how the two start disagreeing
	// about what the system found.
	for i, group := range ui.GroupChecks(report.Results) {
		if i > 0 {
			f("")
		}
		f("%s", group.Category)

		for _, res := range group.Results {

			marker := "ok  "
			switch res.Status {
			case events.CheckWarn:
				marker = "warn"
			case events.CheckFail:
				marker = "FAIL"
			}

			line := fmt.Sprintf("  [%s] %s", marker, res.Description)
			if res.Message != "" {
				line += ": " + res.Message
			}
			f("%s", line)
		}
	}

	if remedies := ui.Remedies(report.Results); len(remedies) > 0 {
		f("")
		f("what to do")
		for _, res := range remedies {
			f("  %s", res.Description)
			f("    %s", res.Remedy)
		}
	}

	f("")
	f("%d ok, %d warning, %d failed", report.Summary.OK, report.Summary.Warn, report.Summary.Fail)
}

// RenderConfig prints the release's parameters and their effective values.
func RenderConfig(w io.Writer, report ops.ConfigReport) {
	f := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }

	if len(report.Parameters) == 0 {
		f("%s %s declares no parameters", report.Product, report.Release)
		return
	}

	f("%-20s %-10s %-16s %s", "NAME", "TYPE", "VALUE", "SOURCE")
	for _, p := range report.Parameters {
		f("%-20s %-10s %-16s %s", p.Name, p.Type, p.Value, p.Source)
	}

	// The detail goes below the table rather than in it: these are
	// sentences, and a sentence in a column makes every row as tall as the
	// longest. It is here at all because the styled view shows it, and
	// nothing may be visible only on a terminal.
	for _, p := range report.Parameters {
		f("")
		f("  %s", p.Name)
		if p.Description != "" {
			f("    %s", p.Description)
		}
		if len(p.Values) > 0 {
			f("    one of: %s", strings.Join(p.Values, ", "))
		}
		if len(p.Services) > 0 {
			f("    changing it re-creates: %s", strings.Join(p.Services, ", "))
		} else {
			f("    changing it takes effect on the next apply")
		}
	}

	if len(report.Stale) > 0 {
		f("")
		f("stale (recorded, but %s declares no such parameter): %s",
			report.Release, strings.Join(report.Stale, ", "))
		f("clear with: morzer config unset %s", strings.Join(report.Stale, " "))
	}
}
