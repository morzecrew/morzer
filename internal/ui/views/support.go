package views

import (
	"fmt"
	"io"
	"path/filepath"

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

	writeSupportEntries(d, r.Entries, "nothing collected")
	writeSupportOmissions(d, r.Omitted)

	if r.Path != "" {
		// Verbatim rather than a field, because this value exists to be
		// copied -- into an `scp`, an upload, an attachment dialog --
		// and `Fields` wraps at the document measure. A path broken
		// across two lines by a narrow terminal is one an operator
		// pastes wrong, and the acceptance run's own output wrapped
		// exactly that way.
		d.Blank()
		d.Text(2, "written")
		d.Verbatim(r.Path)
	}

	// Who can read it, printed in full and on a preview too.
	//
	// The refusal in decision 3a catches a recipient that cannot be parsed.
	// Nothing catches one that parses and belongs to the wrong party, and
	// the only defence against that is an operator reading the key before
	// the archive exists and comparing it with what their vendor published.
	// A truncated key would be shorter and would hide exactly the case this
	// is for: two keys that share a prefix.
	if len(r.Recipients) > 0 {
		d.Blank()
		d.Text(2, "encrypted to, and readable by, only these recipients")
		for _, key := range r.Recipients {
			d.Verbatim(key)
		}
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

	// The signature, on both runs, and the unsigned case out loud.
	//
	// A missing `.minisig` is invisible to an operator who was not looking
	// for one, and "unsigned" is the state a machine that has never minted a
	// key is in -- which is every installation that reached schema 6 by
	// migration and has not signed anything since. Decision 12 keeps the
	// archive; this is the half that stops keeping it quietly.
	d.Blank()
	switch {
	case r.Signed && r.Preview:
		d.Text(2, "an archive written now would be signed by this machine's key")
		d.Verbatim(r.SigningKey)
	case r.Signed:
		d.Text(2, "signed, with the signature beside it")
		d.Verbatim(r.SignaturePath)
	case r.Preview:
		d.Text(2, "an archive written now would be unsigned: this machine has no signing key yet")
	default:
		d.Text(2, "this archive is unsigned")
	}
	return d
}

// writeSupportEntries renders the component table.
//
// One function, called by `support bundle` and by `support inspect`, because
// the two are describing the same archive to two people who are about to
// compare notes across a ticket. A column that meant something else on one of
// those screens is the failure this exists to prevent -- and it was a real
// risk, not a hypothetical: the second copy was written before this was
// extracted, and the comment above it already claimed the reuse this now does.
func writeSupportEntries(d *ui.Doc, entries []ops.SupportEntry, empty string) {
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
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
		Empty: empty,
	})
}

// writeSupportOmissions prints the gaps, always, and below the table rather
// than folded into it: a component that is missing is not a row with a zero in
// it, and an operator deciding whether this archive answers the question they
// were asked needs to see the gaps as gaps.
func writeSupportOmissions(d *ui.Doc, omitted []ops.SupportOmission) {
	if len(omitted) == 0 {
		return
	}
	rows := make([][]string, 0, len(omitted))
	for _, o := range omitted {
		rows = append(rows, []string{o.Name, o.Reason})
	}
	d.Table(2, ui.Table{
		Columns: []ui.Column{
			{Header: "not collected", Essential: true},
			{Header: "why", Essential: true},
		},
		Rows: rows,
	})
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

func init() {
	ui.Register(ui.View[ops.RedactCheckReport]{
		Rich:  func(w io.Writer, t *theme.Theme, r ops.RedactCheckReport) { emit(w, redactCheckDoc(doc(w, t), r)) },
		Plain: func(w io.Writer, r ops.RedactCheckReport) { emit(w, redactCheckDoc(plainDoc(w), r)) },
	})
}

// redactCheckDoc says what was found, and refuses to say "clean".
//
// The unarmed case gets its own sentence rather than a zero, because a count of
// zero from a check that never ran is the one reading that would send somebody
// to paste the file with confidence.
func redactCheckDoc(d *ui.Doc, r ops.RedactCheckReport) *ui.Doc {
	if !r.Armed {
		d.Title("nothing could be checked")
		d.Text(2, "the secret values could not be loaded, so this file was not examined")
		return d
	}

	if r.Redactions == 0 {
		d.Title("no known secret found in " + filepath.Base(r.Path))
		d.Text(2, "no value this installation currently holds appears in it — which is")
		d.Text(2, "not the same as clean: a rotated or undeclared secret is not something")
		d.Text(2, "the manager can recognise")
		return d
	}

	d.Title(fmt.Sprintf("%d secret value(s) found in %s", r.Redactions, filepath.Base(r.Path)))
	d.Text(2, "do not send this file as it is")
	return d
}
