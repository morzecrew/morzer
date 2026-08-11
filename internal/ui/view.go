package ui

import (
	"fmt"
	"io"
	"reflect"

	"github.com/morzecrew/morzer/internal/ui/theme"
)

// View is how one report type is drawn, once per mode that draws anything.
//
// JSON needs no function. The value *is* the machine-readable contract, encoded
// by the envelope at the end of the run; a view that reshaped it for JSON would
// be declaring a second contract for the same data and turning every
// presentation change into a breaking one.
type View[T any] struct {
	// Rich draws the styled rendering.
	Rich func(w io.Writer, t *theme.Theme, v T)

	// Plain draws the line-oriented one: no ANSI, no cursor movement,
	// stable in a journal or a CI log. Not "rich without colour" -- it is
	// what a systemd unit and a log grep read, so its shape outlives any
	// improvement to the styled layout.
	Plain func(w io.Writer, v T)
}

// registry maps a report's concrete type to its view.
//
// Written only during package initialisation -- every Register call is in a
// view file's init -- so it is read-only by the time any command runs and needs
// no lock. That is the same reason it is safe for the registry test to walk it.
var registry = map[reflect.Type]any{}

// Register binds a view to its report type.
//
// Called from each view file's init, so a view that exists is a view that is
// registered: there is no central list of registrations to forget to append to,
// which is the failure mode a registry usually trades for.
//
// Registering a type twice panics. It happens at process start, in a test as
// much as in the binary, and the alternative -- last writer wins -- is two views
// for one report where the one you are reading may not be the one that draws.
func Register[T any](v View[T]) {
	rt := reflect.TypeFor[T]()
	if _, exists := registry[rt]; exists {
		panic(fmt.Sprintf("ui: two views registered for %s", rt))
	}
	if v.Rich == nil && v.Plain == nil {
		// A view with neither rendering satisfies every check this
		// package makes -- it is registered, Render finds it and returns
		// no error -- and prints nothing. Refused at startup, because
		// the alternative is a command that exits 0 with an empty
		// terminal and no way to tell that from a report with no rows.
		panic(fmt.Sprintf("ui: the view for %s renders in no mode", rt))
	}
	registry[rt] = v
}

// Render draws one report in one mode.
//
// The generic dispatch lives here rather than at the call site because the call
// site has an `any`: a command holds a concrete report, hands it over, and this
// is where the type is recovered. A report with no registered view is an error
// rather than a fallback to `%v` -- §6's registry test makes it unreachable in
// a shipped binary, and "unreachable" and "prints nothing on the operator's
// terminal" must not be the same code path.
//
// Quiet is not handled here and never will be. It is plain plus suppression at
// the end of the run, applied after a view has rendered; a quiet rendering per
// type would be a third contract for every report, to buy what `2>/dev/null`
// already produces.
func Render(w io.Writer, mode Mode, t *theme.Theme, value any) error {
	rt := reflect.TypeOf(value)
	entry, ok := registry[rt]
	if !ok {
		return fmt.Errorf("no view is registered for %s", rt)
	}

	// The registry is keyed by the type the view was registered for, so
	// this assertion holds by construction; it is written out rather than
	// reflected through because a type assertion is the whole of the
	// dispatch and reflection here would be slower and harder to read.
	renderer, ok := entry.(interface {
		render(w io.Writer, mode Mode, t *theme.Theme, value any)
	})
	if !ok {
		return fmt.Errorf("the view registered for %s cannot render", rt)
	}
	renderer.render(w, mode, t, value)
	return nil
}

// render is View's half of the dispatch above.
//
// A method on View[T] rather than a closure stored beside it: the type
// parameter is what recovers T from the `any` the registry holds, and doing it
// with a method keeps Register's signature honest about what it stores.
func (v View[T]) render(w io.Writer, mode Mode, t *theme.Theme, value any) {
	report, ok := value.(T)
	if !ok {
		// Unreachable: the registry key is reflect.TypeFor[T] and the
		// lookup used reflect.TypeOf(value). Written as a return rather
		// than a panic because a presentation fault must never be how a
		// command fails.
		return
	}
	if mode == ModeRich && v.Rich != nil {
		v.Rich(w, t, report)
		return
	}
	if v.Plain != nil {
		v.Plain(w, report)
	}
}
