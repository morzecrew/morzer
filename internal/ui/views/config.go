package views

import (
	"io"
	"strings"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

func init() {
	ui.Register(ui.View[ops.ConfigReport]{
		Rich:  func(w io.Writer, t *theme.Theme, r ops.ConfigReport) { emit(w, configDoc(doc(t), r)) },
		Plain: func(w io.Writer, r ops.ConfigReport) { emit(w, configDoc(plainDoc(), r)) },
	})
}

// configDoc draws the release's parameters and their effective values.
//
// A table of the values, then the sentences beneath it. The two used to be one
// four-column printf whose widths were `%-20s %-10s %-16s` -- a 21-character
// parameter name broke the alignment for every row after it, and a description
// in a column made every row as tall as the longest sentence in the release.
func configDoc(d *ui.Doc, report ops.ConfigReport) *ui.Doc {
	t := d.Theme()

	if len(report.Parameters) == 0 {
		d.Text(0, "%s", t.Dim(report.Product+" "+report.Release+" declares no parameters"))
		return d
	}

	d.Title(report.Product + " " + report.Release)

	rows := make([][]string, 0, len(report.Parameters))
	for _, p := range report.Parameters {
		// The operator's own value is highlighted and the release's
		// default is dim, so "what did I change here" is answerable
		// without reading the source column.
		value := t.Dim(p.Value)
		if p.Source == "installation" {
			value = t.Highlight(p.Value)
		}
		rows = append(rows, []string{p.Name, value, string(p.Type), p.Source})
	}
	d.Blank()
	d.Table(2, ui.Table{
		Columns: []ui.Column{
			{Header: "name", Essential: true},
			{Header: "value", Essential: true},
			{Header: "type"},
			{Header: "source"},
		},
		Rows: rows,
	})

	// The detail goes below the table rather than in it: these are
	// sentences, and a sentence in a column makes every row as tall as the
	// longest one in the release.
	for _, p := range report.Parameters {
		d.Blank()
		d.Text(2, "%s", t.Bold(p.Name))
		if p.Description != "" {
			d.Text(4, "%s", p.Description)
		}
		if len(p.Values) > 0 {
			d.Text(4, "%s", t.Dim("one of: "+strings.Join(p.Values, ", ")))
		}
		if len(p.Services) > 0 {
			d.Text(4, "%s", t.Dim("changing it re-creates: "+strings.Join(p.Services, ", ")))
		} else {
			d.Text(4, "%s", t.Dim("changing it takes effect on the next apply"))
		}
	}

	if len(report.Stale) > 0 {
		d.Blank()
		d.Checks(2, []ui.CheckRow{{
			State:       ui.CheckWarned,
			Description: "recorded but no longer declared: " + strings.Join(report.Stale, ", "),
			Message:     "clear with: morzer config unset " + strings.Join(report.Stale, " "),
		}})
	}
	return d
}
