package views

import (
	"fmt"
	"io"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

func init() {
	ui.Register(ui.View[ops.Status]{
		Rich:  func(w io.Writer, t *theme.Theme, s ops.Status) { emit(w, StatusDoc(doc(t), s)) },
		Plain: func(w io.Writer, s ops.Status) { emit(w, StatusDoc(plainDoc(), s)) },
	})
}

// StatusDoc draws the deployment's state.
//
// Exported and taking its document because `status --watch` redraws this body
// inside a running terminal program, and two implementations of "what status
// looks like" is how a live view and a printed one start disagreeing about what
// the machine is doing.
func StatusDoc(d *ui.Doc, s ops.Status) *ui.Doc {
	t := d.Theme()

	d.Title(s.Product)

	fields := []ui.Field{{Label: "installation", Value: t.Dim(s.InstallationID)}}
	if s.Mode != "" {
		// Permanently, not once: the failure mode is a machine nobody
		// remembers the provenance of, and the moment that matters is
		// months later when somebody decides whether its data is real.
		fields = append(fields, ui.Field{
			Label: "mode", Value: t.Warn(s.Mode),
			Note: "a sandbox: relaxed retention, no pre-update backup guarantee",
		})
	}

	if s.CurrentRelease == nil {
		fields = append(fields, ui.Field{Label: "release", Value: t.Dim("none installed")})
	} else {
		fields = append(fields, ui.Field{
			Label: "release", Value: t.Highlight(s.CurrentRelease.Version.String())})
		if s.PreviousRelease != nil {
			fields = append(fields, ui.Field{
				Label: "previous", Value: t.Dim(s.PreviousRelease.Version.String())})
		}
	}
	// Beside the release rather than in a section of its own: it is the
	// answer to "what is deployed", and an operator who reads that line and
	// stops reading has still seen that a decision is waiting.
	if s.StagedRelease != nil {
		fields = append(fields, ui.Field{
			Label: "staged", Value: t.Active(s.StagedRelease.Version.String()),
			Note: "(not installed)"})
	}
	for _, f := range []ui.Field{
		{Label: "profile", Value: s.Profile},
		{Label: "url", Value: s.PublicURL},
		{Label: "support", Value: s.SupportURL},
	} {
		if f.Value != "" {
			fields = append(fields, f)
		}
	}
	d.Fields(2, fields)

	statusServices(d, s)
	statusHealth(d, s)
	statusFooter(d, s)
	statusAttention(d, s)
	return d
}

func statusServices(d *ui.Doc, s ops.Status) {
	if len(s.Services) == 0 {
		return
	}
	t := d.Theme()

	d.Heading("services")
	rows := make([][]string, 0, len(s.Services))
	for _, svc := range s.Services {
		// The symbol carries the verdict; the state string is Compose's
		// own word for it and is shown unchanged, because "exited (137)"
		// is the thing worth reading.
		state := svc.State
		if svc.Health != ports.HealthNone && svc.Health != "" {
			state += ", " + string(svc.Health)
		}
		symbol, style := t.Symbols.OK, t.OK
		if !svc.Running() {
			symbol, style = t.Symbols.Fail, t.Fail
		}
		// The marker joins the name rather than taking a column of its
		// own: a column would put the gutter between a symbol and the
		// thing it marks.
		rows = append(rows, []string{style(symbol) + " " + svc.Name, style(state)})
	}
	d.Table(4, ui.Table{
		Columns:  []ui.Column{{Header: "service", Essential: true}, {Header: "state"}},
		Rows:     rows,
		NoHeader: true,
	})
}

func statusHealth(d *ui.Doc, s ops.Status) {
	if len(s.Health) == 0 {
		return
	}
	t := d.Theme()

	d.Heading("health")
	rows := make([][]string, 0, len(s.Health))
	for _, h := range s.Health {
		symbol, style, word := t.Symbols.OK, t.OK, "ok"
		if !h.OK {
			symbol, style, word = t.Symbols.Fail, t.Fail, "FAIL"
		}
		rows = append(rows, []string{style(symbol) + " " + h.Name, style(word), t.Dim(h.Message)})
	}
	d.Table(4, ui.Table{
		Columns: []ui.Column{
			{Header: "check", Essential: true},
			{Header: "result", Essential: true},
			{Header: "detail"},
		},
		Rows:     rows,
		NoHeader: true,
	})
}

func statusFooter(d *ui.Doc, s ops.Status) {
	t := d.Theme()

	fields := []ui.Field{}
	if s.LastBackup != nil {
		fields = append(fields, ui.Field{
			Label: "last backup",
			Value: fmt.Sprintf("%s (%s ago)", s.LastBackup.ID, s.LastBackup.Age)})
	} else {
		fields = append(fields, ui.Field{Label: "last backup", Value: t.Dim("none")})
	}
	if op := s.LastOperation; op != nil {
		fields = append(fields, ui.Field{
			Label: "last operation",
			Value: fmt.Sprintf("%s %s (%s)", op.Type, op.Status, op.ID)})
	}
	if l := s.LockHeldBy; l != nil {
		fields = append(fields, ui.Field{
			Label: "lock",
			Value: fmt.Sprintf("held by %s operation %s (pid %d)", l.Type, l.OperationID, l.PID)})
	}

	d.Blank()
	d.Fields(2, fields)
}

// statusAttention draws what the operator has to do something about.
//
// Last and loudest: it is the reason they ran this command, whether or not they
// knew it. An operation flagged requires-manual-intervention keeps appearing
// here until it is cleared explicitly, so a callout rather than a line is the
// weight it should have carried all along.
func statusAttention(d *ui.Doc, s ops.Status) {
	for _, rec := range s.NeedsAttention {
		body := []string{fmt.Sprintf(
			"Operation %s (%s) stopped in a state the manager will not resolve on its own.",
			rec.ID, rec.Type)}
		if rec.Error != nil {
			body = append(body, rec.Error.Message)
			if rec.Error.Hint != "" {
				body = append(body, rec.Error.Hint)
			}
		}
		d.Callout(2, ui.Callout{Title: "attention", Body: body})
	}

	// A warning row rather than a paragraph, so a problem too long for one
	// line hangs under itself instead of wrapping back under its own marker.
	if len(s.Problems) > 0 {
		d.Blank()
		rows := make([]ui.CheckRow, 0, len(s.Problems))
		for _, problem := range s.Problems {
			rows = append(rows, ui.CheckRow{State: ui.CheckWarned, Description: problem})
		}
		d.Checks(2, rows)
	}
}
