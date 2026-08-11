package views

import (
	"fmt"
	"io"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

func init() {
	ui.Register(ui.View[[]ops.InstallationEntry]{
		Rich: func(w io.Writer, t *theme.Theme, v []ops.InstallationEntry) {
			emit(w, installationsDoc(doc(w, t), v, false))
		},
		Plain: func(w io.Writer, v []ops.InstallationEntry) {
			emit(w, installationsDoc(plainDoc(w), v, false))
		},
	})
	ui.Register(ui.View[WithServices]{
		Rich: func(w io.Writer, t *theme.Theme, v WithServices) {
			emit(w, installationsDoc(doc(w, t), v, true))
		},
		Plain: func(w io.Writer, v WithServices) {
			emit(w, installationsDoc(plainDoc(w), v, true))
		},
	})
}

// WithServices is the same listing with `--status` asked for.
//
// A named slice rather than a field on the entry, for the reason `Verbose` is a
// type: whether the operator asked for a Docker call is a presentation choice,
// and a flag inside the report would travel through the lifecycle layer into
// `--json`. A named slice type marshals exactly as the slice does, so the
// machine contract is one array either way -- with a `services` key on each row
// when it was asked for and none when it was not.
type WithServices []ops.InstallationEntry

// installationsDoc lists what this machine holds.
//
// The columns are the questions somebody logging into an unfamiliar machine
// asks in order: what is installed, which release, is it a sandbox, are its
// units in place, and where does it live. `schema_version` is not among them --
// it matters on one day in a hundred and the table's width is the scarce thing,
// so it is carried in `--json` alone.
func installationsDoc(d *ui.Doc, entries []ops.InstallationEntry, status bool) *ui.Doc {
	columns := []ui.Column{
		{Header: "product", Essential: true},
		{Header: "release", Essential: true},
		{Header: "mode"},
		{Header: "units", Right: true},
	}
	if status {
		columns = append(columns, ui.Column{Header: "services", Essential: true})
	}
	// Last, because it is the widest and the least often read: a listing
	// that drops the path on a narrow terminal has lost the least.
	columns = append(columns, ui.Column{Header: "path"})

	t := d.Theme()
	rows := make([][]string, 0, len(entries))
	var notes []ui.CheckRow

	for _, e := range entries {
		release := t.Dim("none")
		if !e.Release.IsZero() {
			release = e.Release.String()
		}
		// A sandbox says so and a production machine says nothing. The
		// mode is shown wherever it is shown at all -- the moment it
		// matters is months later, when somebody is deciding whether the
		// data on a machine is real.
		mode := ""
		if e.Mode != "" {
			mode = t.Warn(string(e.Mode))
		}

		// A row that could not be read keeps its product, its units and
		// its path, and says `unreadable` in place of what it could not
		// interpret. Dropping it would report the installation as absent
		// at the one moment it must not: when its state has just broken.
		if e.Problem != "" {
			release, mode = t.Fail("unreadable"), ""
		}

		row := []string{e.Product, release, mode, fmt.Sprint(e.Units)}
		if status {
			row = append(row, serviceCell(t, e))
		}
		row = append(row, t.Dim(e.Path))
		rows = append(rows, row)

		// The reasons go under the table, never in it. A cell holds a
		// state and a note holds a sentence: an unreadable state file
		// names a path, a wedged daemon names an error, and either in a
		// column makes every row as tall as the worst line on the
		// machine -- which is the alignment a table exists for, spent on
		// the one row that has prose.
		if e.Problem != "" {
			notes = append(notes, ui.CheckRow{
				State: ui.CheckFailed, Description: e.Product, Message: e.Problem,
			})
		}
		if e.ServicesProblem != "" {
			notes = append(notes, ui.CheckRow{
				State:       ui.CheckWarned,
				Description: e.Product,
				Message:     "services: " + e.ServicesProblem,
			})
		}
	}

	d.Table(0, ui.Table{
		Columns: columns,
		Rows:    rows,
		Empty:   "this machine has no installations — `morzer init` creates one",
	})

	if len(notes) > 0 {
		d.Blank()
		d.Checks(0, notes)
	}

	if status && len(entries) > 0 {
		// In the output rather than only in the docs. The counts are
		// read without the deployment lock, deliberately -- a listing
		// that blocked behind a running update would be useless exactly
		// when somebody is watching one -- and the price is that a row
		// can be a moment stale.
		d.Blank()
		d.Text(0, "%s", t.Dim("services are sampled without the deployment lock, "+
			"so a row may be a moment out of date"))
	}
	return d
}

// serviceCell is one installation's runtime answer as a state, never as a
// sentence: the reason for a missing count is a note under the table.
func serviceCell(t *theme.Theme, e ops.InstallationEntry) string {
	if e.Services == nil {
		// Either refused with a reason -- which is in the notes -- or
		// never asked, because the row failed before the question came
		// up. Both are "not known", and neither is a count.
		return t.Dim("unknown")
	}
	count := fmt.Sprintf("%d/%d", e.Services.Running, e.Services.Total)
	if e.Services.Running == e.Services.Total && e.Services.Total > 0 {
		return t.OK(count)
	}
	return t.Fail(count)
}
