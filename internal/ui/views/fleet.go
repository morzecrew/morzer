package views

import (
	"fmt"
	"io"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// The fleet view (RFC 0026 §3.2 and §3.4).
//
// A table, and underneath it the sentence saying what the table does not know.
// That sentence is not a footnote: RFC 0026 §8 permits `fleet ls` to ship
// before the roster *only* because the reader states that it cannot
// authenticate a row and cannot show an absent installation. A complete-looking
// table presented as complete is the failure mode of every fleet view ever
// built, and printing the limitation on every run is the one thing that stops
// this from being one.

// Fleet is what `fleet ls` prints, and the published `--json` shape.
type Fleet struct {
	Targets []string   `json:"targets"`
	Rows    []FleetRow `json:"rows"`

	// Expected is how many installations the roster named, zero when none
	// was given.
	Expected int `json:"expected,omitempty"`

	StaleAfter  string   `json:"stale_after,omitempty"`
	Limitations []string `json:"limitations"`
}

// FleetRow is one installation's line.
type FleetRow struct {
	Target         string `json:"target"`
	Key            string `json:"key"`
	Product        string `json:"product,omitempty"`
	InstallationID string `json:"installation_id,omitempty"`

	// Row is the payload as published, nil when it could not be read --
	// and, once a roster names a key for this installation, nil when it
	// could not be authenticated either. A payload here has been checked
	// against the roster; that is what makes the field worth reading.
	Row *domain.FleetRow `json:"row,omitempty"`

	// Signature is what the reader established. Without a roster it says
	// only that a signature is *there*, never that it checks out.
	Signature string `json:"signature"`

	// Expected says the roster names this installation.
	Expected bool `json:"expected,omitempty"`

	// Absent says the roster expects it and no target holds a row. The
	// line every other line exists to make legible.
	Absent bool `json:"absent,omitempty"`

	Age     string `json:"age,omitempty"`
	Stale   bool   `json:"stale,omitempty"`
	Problem string `json:"problem,omitempty"`
}

func init() {
	ui.Register(ui.View[Fleet]{
		Rich:  func(w io.Writer, t *theme.Theme, v Fleet) { emit(w, fleetDoc(doc(w, t), v)) },
		Plain: func(w io.Writer, v Fleet) { emit(w, fleetDoc(plainDoc(w), v)) },
	})
}

func fleetDoc(d *ui.Doc, v Fleet) *ui.Doc {
	// Rows that came off a target, which is what the sentence claims. An
	// absent row is synthesised from the roster precisely because no target
	// holds it, so counting it here would make the first line an operator
	// reads say the bucket holds a row that is missing from it. How much of
	// the expected fleet reported is the footer's sentence, and it can say
	// it in words rather than by inflating a total.
	d.Title(fmt.Sprintf("%d row(s) on %s",
		len(v.Rows)-fleetAbsent(v), strings.Join(v.Targets, ", ")))

	// The target column earns its place only when there is more than one.
	// On the ordinary single-target run it would repeat the same string on
	// every line, pushing the columns an operator is reading off the edge of
	// a narrow terminal.
	multi := len(v.Targets) > 1

	columns := []ui.Column{
		{Header: "product", Essential: true},
		{Header: "version", Essential: true},
		{Header: "health", Essential: true},
		{Header: "published"},
		{Header: "drift"},
		{Header: "signature"},
	}
	if multi {
		columns = append(columns, ui.Column{Header: "target"})
	}

	rows := make([][]string, 0, len(v.Rows))
	for _, r := range v.Rows {
		cells := []string{
			fleetName(r),
			fleetVersion(r),
			fleetHealth(r),
			fleetPublished(r),
			fleetDrift(r),
			fleetSignature(r),
		}
		if multi {
			cells = append(cells, r.Target)
		}
		rows = append(rows, cells)
	}

	d.Table(2, ui.Table{Columns: columns, Rows: rows, Empty: "no rows have been published here"})

	// The diagnostics, in full, below the table. They are messages rather
	// than cells: a row that will not parse says why in a sentence, and a
	// sentence squeezed into a column is a sentence nobody reads.
	//
	// Three sources, one table. A row that could not be read at all carries
	// its problem; a row that *was* read can still say the runtime did not
	// answer or that drift could not be measured, and those two were
	// collected by the publisher precisely so somebody could act on them.
	// Rendering only `not checked` in the cell and dropping the sentence
	// left the operator knowing a measurement is missing and not why --
	// which, on a fleet screen, means going to each machine to find out.
	// Sequential rather than a switch, because a row can have more than one
	// thing to say: a row that failed verification carries its problem, and
	// a row that passed can still report that the runtime did not answer.
	var problems [][]string
	for _, r := range v.Rows {
		if r.Problem != "" {
			problems = append(problems, []string{fleetName(r), r.Problem})
		}
		// A note rather than a problem, and deliberately not counted as
		// one: a roster covering three of twelve machines is a legitimate
		// way to adopt this, and reporting the other nine as findings
		// would make it unusable on the way in. It is still worth a line
		// -- an unexpected row is a machine somebody forgot to add, or
		// one somebody added to the bucket.
		if v.Expected > 0 && !r.Expected {
			problems = append(problems, []string{fleetName(r),
				"the roster does not name this installation"})
		}
		if r.Row != nil {
			if p := r.Row.Health.Problem; p != "" {
				problems = append(problems, []string{fleetName(r), "health: " + p})
			}
			if p := r.Row.Drift.Problem; p != "" {
				problems = append(problems, []string{fleetName(r), "drift: " + p})
			}
		}
	}
	if len(problems) > 0 {
		d.Table(2, ui.Table{
			Columns: []ui.Column{
				{Header: "row", Essential: true},
				{Header: "what it says", Essential: true},
			},
			Rows: problems,
		})
	}

	if v.Expected > 0 {
		// The headline number a roster buys, said as a count rather than
		// left for somebody to derive from the table. "3 rows" is a fleet
		// or an incident depending on a denominator only the roster has.
		d.Blank()
		d.Text(2, "the roster expects %d installation(s); %d published a row and %d did not",
			v.Expected, v.Expected-fleetAbsent(v), fleetAbsent(v))
	}

	if v.StaleAfter != "" {
		d.Blank()
		d.Text(2, "a row is called stale once it is older than %s", v.StaleAfter)
	}

	// Every run. See the package comment.
	for _, limitation := range v.Limitations {
		d.Blank()
		d.Text(2, "%s", limitation)
	}
	return d
}

// fleetName is what an operator scans down the left-hand column.
//
// The product, and the installation id only when the row could not be read --
// there the id is the only handle on which machine this is, and it is worth the
// width. A production row is named by its product because that is what somebody
// is looking for.
func fleetName(r FleetRow) string {
	switch {
	case r.Product == "":
		return r.Key
	case r.Row == nil:
		return r.Product + "/" + r.InstallationID
	case r.Row.Mode != "":
		// A sandbox, marked. This is the field somebody scanning twelve
		// rows for "why is that one weird" reads first, and RFC 0016
		// made mode permanently visible for the same reason.
		return r.Product + " (" + string(r.Row.Mode) + ")"
	default:
		return r.Product
	}
}

func fleetVersion(r FleetRow) string {
	switch {
	case r.Row == nil:
		return "—"
	case r.Row.Version == "":
		return "none installed"
	default:
		return r.Row.Version
	}
}

// fleetHealth renders the running count, or why there is none.
//
// The distinction the pointer in the payload exists for: "0/3" is a deployment
// that is down, and "not checked" is a machine whose runtime did not answer.
// Rendering both as "0/0" would make the second look like the first, which is
// the whole reason the count is nil-able.
func fleetHealth(r FleetRow) string {
	if r.Row == nil {
		return "—"
	}

	h := r.Row.Health
	out := "not checked"
	if h.Running != nil && h.Services != nil {
		out = fmt.Sprintf("%d/%d up", *h.Running, *h.Services)
	}
	if h.Attention > 0 {
		// Appended rather than replacing the count: an installation
		// needing manual intervention may also have everything running,
		// and those are different sentences.
		out += fmt.Sprintf(", %d need attention", h.Attention)
	}
	return out
}

func fleetPublished(r FleetRow) string {
	switch {
	case r.Age == "":
		return "—"
	case r.Stale:
		return r.Age + " (stale)"
	default:
		return r.Age
	}
}

// fleetAbsent counts the installations the roster expects and no target holds.
func fleetAbsent(v Fleet) int {
	var n int
	for _, r := range v.Rows {
		if r.Absent {
			n++
		}
	}
	return n
}

// fleetSignature renders what the reader established.
//
// An absent row gets a dash rather than `unsigned`: there is no row, so there
// is nothing that could have been signed, and printing the ordinary state of a
// machine with no key next to an installation that has vanished would be the
// most reassuring possible spelling of the most alarming line in the table.
func fleetSignature(r FleetRow) string {
	if r.Absent {
		return "—"
	}
	return r.Signature
}

// fleetDrift renders the count, never a diff -- there is none to render.
func fleetDrift(r FleetRow) string {
	if r.Row == nil {
		return "—"
	}

	switch drift := r.Row.Drift; {
	case drift.Targets == nil:
		return "not checked"
	case *drift.Targets == 0:
		return "none"
	case *drift.Targets == 1:
		return "1 target"
	default:
		return fmt.Sprintf("%d targets", *drift.Targets)
	}
}
