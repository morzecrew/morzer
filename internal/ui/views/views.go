// Package views is every report's rendering, one file per report type.
//
// The boundary this package exists to create: a command produces a value and
// hands it to `app.render`; nothing in `internal/cli` decides what output looks
// like. Before it, the mode was resolved carefully for every invocation and then
// honoured by 8% of the program's output -- 59 direct `fmt.Fprint` calls in the
// command layer against 5 renderer dispatches -- so `--plain` and rich mode
// produced identical bytes for almost everything and a contributor adding a
// command had no boundary to cross.
//
// Each file registers its view from an `init`, so a view that exists is a view
// that is registered and there is no central list to forget to append to. The
// registry itself, and the five components every view draws with, are in
// `internal/ui`.
//
// It sits below `internal/ui/tty`: the live program may call a view's body (the
// watch loop redraws `status`), and no view may reach for a terminal program.
package views

import (
	"io"

	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// doc starts a document for a rich rendering.
func doc(w io.Writer, t *theme.Theme) *ui.Doc { return ui.NewDoc(t, ui.ScreenFor(w)) }

// plainDoc starts one for a line-oriented rendering.
func plainDoc(w io.Writer) *ui.Doc { return ui.NewPlainDoc(ui.ScreenFor(w)) }

// emit is the tail of every view.
func emit(w io.Writer, d *ui.Doc) { d.Emit(w) }
