package views

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// `morzer history` publishes the journal's own records under `--json`: the
// journal is already a versioned format, and a second shape of the same facts
// would be one more thing to keep in step with it.
func init() {
	ui.Register(ui.View[ops.History]{
		Rich:  func(w io.Writer, t *theme.Theme, v ops.History) { emit(w, historyDoc(doc(w, t), v)) },
		Plain: func(w io.Writer, v ops.History) { emit(w, historyDoc(plainDoc(w), v)) },
	})
	ui.Register(ui.View[domain.OperationRecord]{
		Rich:  func(w io.Writer, t *theme.Theme, v domain.OperationRecord) { emit(w, operationDoc(doc(w, t), v)) },
		Plain: func(w io.Writer, v domain.OperationRecord) { emit(w, operationDoc(plainDoc(w), v)) },
	})
}

func historyDoc(d *ui.Doc, h ops.History) *ui.Doc {
	rows := make([][]string, 0, len(h.Operations))
	for _, rec := range h.Operations {
		rows = append(rows, []string{
			rec.ID,
			operationKind(rec),
			operationStatus(d.Theme(), rec.Status),
			rec.StartedAt.Format(stamp),
			versionMove(rec.From.String(), rec.To.String()),
			took(rec.Duration()),
		})
	}

	title := fmt.Sprintf("%d operation(s), newest first", len(h.Operations))
	if len(h.Operations) < h.Total {
		title = fmt.Sprintf("%d of %d operations, newest first; --limit 0 lists them all", len(h.Operations), h.Total)
	}
	d.Title(title)
	d.Table(2, ui.Table{
		Columns: []ui.Column{
			{Header: "operation", Essential: true},
			{Header: "type", Essential: true},
			{Header: "status", Essential: true},
			{Header: "started"},
			{Header: "release"},
			{Header: "took", Right: true},
		},
		Rows:  rows,
		Empty: "no operations recorded",
	})
	return d
}

func operationDoc(d *ui.Doc, rec domain.OperationRecord) *ui.Doc {
	t := d.Theme()
	fields := []ui.Field{
		{Label: "type", Value: operationKind(rec)},
		{Label: "status", Value: operationStatus(t, rec.Status)},
		{Label: "started", Value: rec.StartedAt.Format(stamp)},
	}
	if !rec.FinishedAt.IsZero() {
		fields = append(fields, ui.Field{Label: "finished", Value: rec.FinishedAt.Format(stamp)})
	}
	fields = append(fields, ui.Field{Label: "took", Value: took(rec.Duration())})
	if move := versionMove(rec.From.String(), rec.To.String()); move != "" {
		fields = append(fields, ui.Field{Label: "release", Value: move})
	}
	fields = append(fields, ui.Field{Label: "manager", Value: rec.ManagerVersion})
	// Sorted, because the journal keeps them as a map and a listing that
	// reorders between runs reads as a change.
	keys := make([]string, 0, len(rec.Flags))
	for k := range rec.Flags {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fields = append(fields, ui.Field{Label: k, Value: rec.Flags[k]})
	}

	d.Title("Operation " + rec.ID)
	d.Fields(2, fields)

	if len(rec.Steps) > 0 {
		rows := make([][]string, 0, len(rec.Steps))
		for _, s := range rec.Steps {
			rows = append(rows, []string{
				s.ID, string(s.Status), took(time.Duration(s.DurationMS) * time.Millisecond),
				strings.TrimSpace(s.Error + " " + s.Message),
			})
		}
		d.Blank()
		d.Table(2, ui.Table{
			Columns: []ui.Column{
				{Header: "step", Essential: true},
				{Header: "status", Essential: true},
				{Header: "took", Right: true},
				{Header: "detail"},
			},
			Rows: rows,
		})
	}

	if rec.Error != nil {
		body := []string{rec.Error.Message}
		if rec.Error.Hint != "" {
			body = append(body, rec.Error.Hint)
		}
		d.Callout(2, ui.Callout{Title: "error", Body: body})
	}
	return d
}

// operationKind is the operation's type, marked when nothing was changed.
func operationKind(rec domain.OperationRecord) string {
	if rec.DryRun {
		return string(rec.Type) + " (dry run)"
	}
	return string(rec.Type)
}

func operationStatus(t *theme.Theme, s domain.OperationStatus) string {
	switch s {
	case domain.StatusSucceeded:
		return t.OK(string(s))
	case domain.StatusRunning:
		return t.Active(string(s))
	case domain.StatusCompensated, domain.StatusInterrupted:
		return t.Warn(string(s))
	default:
		return t.Fail(string(s))
	}
}

// took renders a duration at the precision a reader of a timeline wants:
// milliseconds for a step that barely ran, seconds for the rest.
func took(d time.Duration) string {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	default:
		return d.Round(time.Second).String()
	}
}
