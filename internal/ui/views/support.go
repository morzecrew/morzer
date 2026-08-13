package views

import (
	"fmt"
	"io"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// The support bundle's view (RFC 0024 §3.5).
//
// This is not decoration. An operator who cannot see what leaves will either
// send nothing or send everything, and both are failures of the feature -- so
// the same table is what `--preview` prints before an archive exists and what a
// real run prints after one does. Two renderings of "what is in it" would be
// two chances to disagree, and the one that disagreed would be the one nobody
// checked.

func init() {
	ui.Register(ui.View[ops.SupportReport]{
		Rich:  func(w io.Writer, t *theme.Theme, r ops.SupportReport) { emit(w, supportDoc(doc(w, t), r)) },
		Plain: func(w io.Writer, r ops.SupportReport) { emit(w, supportDoc(plainDoc(w), r)) },
	})
}

func supportDoc(d *ui.Doc, r ops.SupportReport) *ui.Doc {
	if r.Preview {
		d.Title(fmt.Sprintf("%d component(s), %s — nothing written",
			len(r.Entries), humanBytes(r.TotalBytes)))
	} else {
		d.Title(fmt.Sprintf("%d component(s), %s", len(r.Entries), humanBytes(r.TotalBytes)))
	}

	rows := make([][]string, 0, len(r.Entries))
	for _, e := range r.Entries {
		rows = append(rows, []string{
			e.Name,
			e.Title,
			humanBytes(e.Bytes),
			// Always a number, never a blank. A column that is empty
			// when nothing was scrubbed reads as "not checked" on the
			// one file where the difference matters.
			fmt.Sprintf("%d", e.Redactions),
		})
	}
	d.Table(2, ui.Table{
		Columns: []ui.Column{
			{Header: "file", Essential: true},
			{Header: "component", Essential: true},
			{Header: "size", Essential: true},
			{Header: "redactions"},
		},
		Rows:  rows,
		Empty: "nothing collected",
	})

	// Omissions are printed, always, and below the table rather than
	// folded into it: a component that is missing is not a row with a zero
	// in it, and an operator deciding whether this archive answers the
	// question they were asked needs to see the gaps as gaps.
	if len(r.Omitted) > 0 {
		omitted := make([][]string, 0, len(r.Omitted))
		for _, o := range r.Omitted {
			omitted = append(omitted, []string{o.Name, o.Reason})
		}
		d.Table(2, ui.Table{
			Columns: []ui.Column{
				{Header: "not collected", Essential: true},
				{Header: "why", Essential: true},
			},
			Rows: omitted,
		})
	}

	if r.Path != "" {
		d.Fields(2, []ui.Field{{Label: "written", Value: r.Path}})
	}

	// Said on every run, not once at the end of a long help text.
	//
	// Decision 3 keeps plaintext available because the operator posting to
	// a forum is the case §2 rests on -- and the price of not refusing is
	// saying so every time, in the same breath as the path, where somebody
	// about to attach the file is looking.
	if !r.Encrypted {
		d.Blank()
		d.Text(2, "this archive is not encrypted: anyone who receives it can read all of it")
	}
	return d
}

// humanBytes renders a size an operator can weigh against an email attachment
// limit without counting digits.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
