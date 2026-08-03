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
}

func New(stderr io.Writer, verbose bool) *Presenter {
	return &Presenter{w: stderr, verbose: verbose}
}

var _ events.Sink = (*Presenter)(nil)

// Handle renders one event.
//
// Everything goes to stderr, including successes: stdout belongs to the
// command's result, and mixing progress into it would break every pipeline.
func (p *Presenter) Handle(e events.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch e.Kind {
	case events.KindOperationStarted:
		p.totalSteps = e.StepCount
		if e.DryRun {
			p.line("plan: %s (%d steps, nothing will be changed)", e.Description, e.StepCount)
			return
		}
		p.line("%s (%d steps)", e.Description, e.StepCount)

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

func (p *Presenter) line(format string, args ...any) {
	fmt.Fprintf(p.w, format+"\n", args...)
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
	f := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

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
	f := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

	// Results arrive in execution order, which interleaves categories: a
	// storage check runs early, another late. Grouping them here keeps the
	// table scannable instead of showing "storage" three times.
	var categories []string
	grouped := map[string][]events.CheckResult{}
	for _, res := range report.Results {
		if _, seen := grouped[res.Category]; !seen {
			categories = append(categories, res.Category)
		}
		grouped[res.Category] = append(grouped[res.Category], res)
	}

	for i, category := range categories {
		if i > 0 {
			f("")
		}
		f("%s", category)

		for _, res := range grouped[category] {

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

	// Remedies are collected into their own section rather than inlined,
	// so the table stays scannable and the actions stay together.
	var remedies []events.CheckResult
	for _, res := range report.Results {
		if res.Status != events.CheckOK && res.Remedy != "" {
			remedies = append(remedies, res)
		}
	}
	if len(remedies) > 0 {
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
