package tty

import (
	"fmt"
	"io"
	"strings"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// RenderStatus draws the deployment's state.
//
// The same content as plain.RenderStatus, styled. It is a plain print rather
// than a program because `status` answers a question and exits; only --watch
// needs a running one, and that reuses this function for its body.
func RenderStatus(w io.Writer, t *theme.Theme, s ops.Status) {
	_, _ = io.WriteString(w, statusBody(t, s))
}

func statusBody(t *theme.Theme, s ops.Status) string {
	var b strings.Builder
	f := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	field := func(label, value string) { f("  %s %s", t.Dim(pad(label, 14)), value) }

	f("%s", t.Bold(s.Product))
	field("installation", t.Dim(s.InstallationID))

	if s.CurrentRelease == nil {
		field("release", t.Dim("none installed"))
	} else {
		field("release", t.Highlight(s.CurrentRelease.Version.String()))
		if s.PreviousRelease != nil {
			field("previous", t.Dim(s.PreviousRelease.Version.String()))
		}
	}
	if s.Profile != "" {
		field("profile", s.Profile)
	}
	if s.PublicURL != "" {
		field("url", s.PublicURL)
	}

	if len(s.Services) > 0 {
		f("")
		f("  %s", t.Bold("services"))
		for _, svc := range s.Services {
			state := svc.State
			if svc.Health != ports.HealthNone && svc.Health != "" {
				state += ", " + string(svc.Health)
			}

			// The symbol carries the verdict; the state string is
			// Compose's own word for it and is shown unchanged,
			// because "exited (137)" is the thing worth reading.
			symbol, style := t.Symbols.OK, t.OK
			if !svc.Running() {
				symbol, style = t.Symbols.Fail, t.Fail
			}
			f("    %s %s %s", style(symbol), pad(svc.Name, 24), style(state))
		}
	}

	if len(s.Health) > 0 {
		f("")
		f("  %s", t.Bold("health"))
		for _, h := range s.Health {
			symbol, style, word := t.Symbols.OK, t.OK, "ok"
			if !h.OK {
				symbol, style, word = t.Symbols.Fail, t.Fail, "FAIL"
			}
			f("    %s %s %s  %s",
				style(symbol), pad(h.Name, 24), style(word), t.Dim(h.Message))
		}
	}

	f("")
	if s.LastBackup != nil {
		field("last backup", fmt.Sprintf("%s %s",
			s.LastBackup.ID, t.Dim("("+s.LastBackup.Age+" ago)")))
	} else {
		field("last backup", t.Warn("none"))
	}

	if op := s.LastOperation; op != nil {
		style := t.Dim
		if op.Status != "succeeded" {
			style = t.Warn
		}
		field("last operation", fmt.Sprintf("%s %s %s",
			op.Type, style(string(op.Status)), t.Dim(op.ID)))
	}

	if l := s.LockHeldBy; l != nil {
		field("lock", t.Warn(fmt.Sprintf(
			"held by %s operation %s (pid %d)", l.Type, l.OperationID, l.PID)))
	}

	// Anything needing attention goes last and loudest: it is the reason
	// the operator ran this command, whether or not they knew it.
	for _, rec := range s.NeedsAttention {
		f("")
		f("  %s", t.Fail(fmt.Sprintf(
			"ATTENTION: operation %s (%s) requires manual intervention",
			rec.ID, rec.Type)))
		if rec.Error != nil {
			f("             %s", rec.Error.Message)
			if rec.Error.Hint != "" {
				f("             %s", t.Dim(rec.Error.Hint))
			}
		}
	}

	for _, problem := range s.Problems {
		f("")
		f("  %s %s", t.Warn(t.Symbols.Warn), t.Warn("warning: "+problem))
	}

	return b.String()
}

// pad right-pads to a column width, measured in cells.
//
// fmt's %-24s counts bytes, so a service name with a non-ASCII character in it
// shifts the whole column.
func pad(s string, width int) string {
	if n := displayWidth(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}
