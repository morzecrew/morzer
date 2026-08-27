// Package scheme indexes adapters by the reference scheme each one declares.
//
// Two ports select an adapter the same way: a release source by a reference's
// scheme, a backup target by a URL's. Both were written out in full, and the
// copies had already drifted -- the target registry closes over the argument
// list because hashing an interface value panics for a type that is not
// comparable, while the source registry still deduplicated its close loop
// through exactly such a set.
//
// One copy, so "a nil adapter is refused", "two adapters may not claim one
// scheme" and "each adapter closes once however many schemes it answers for"
// are facts about both rather than about whichever was edited last.
package scheme

import (
	"errors"
	"io"
	"maps"
	"reflect"
	"slices"

	"github.com/morzecrew/morzer/internal/domain"
)

// Adapter is anything that answers for a set of schemes.
type Adapter interface {
	Schemes() []string
}

// Index dispatches to the adapter registered for a scheme.
type Index[T Adapter] struct {
	byScheme map[string]T

	// registered is the argument list, in order, one entry per adapter
	// however many schemes it claims. Close walks it rather than
	// deduplicating the scheme map through a set keyed by the interface
	// value: hashing an interface whose dynamic type is not comparable
	// panics, and shutdown is the worst place to find that out.
	registered []T
}

// NewIndex indexes each adapter under every scheme it declares. kind names what
// is being registered -- "release source", "backup target" -- and appears in
// the refusals, pluralised by an "s".
//
// Two adapters claiming one scheme is a wiring mistake with no sensible
// resolution: last-wins would make behaviour depend on argument order, and
// first-wins would silently ignore an adapter someone deliberately added. An
// empty index is the same kind of mistake seen later -- a build in which every
// operation fails when it runs rather than when it is assembled.
//
// A nil adapter is refused rather than skipped, for the same reason: dropping
// it quietly leaves a build whose sftp:// URLs fail at push time as though the
// transport had never been compiled in. The check cannot be `a == nil` alone --
// a nil *sftp.Target satisfies the interface, registers happily, and panics at
// shutdown when Close dereferences it.
//
// These are errors rather than panics because the caller assembling the graph
// already returns one, so they surface at startup with the rest.
func NewIndex[T Adapter](kind string, adapters ...T) (*Index[T], error) {
	idx := &Index[T]{byScheme: make(map[string]T, len(adapters))}

	for _, a := range adapters {
		if isNil(a) {
			return nil, domain.Internal(nil, "a nil %s was registered", kind)
		}
		schemes := a.Schemes()
		if len(schemes) == 0 {
			return nil, domain.Internal(nil, "a %s declares no schemes", kind)
		}
		for _, s := range schemes {
			if _, taken := idx.byScheme[s]; taken {
				return nil, domain.Internal(nil,
					"two %ss both claim the %q scheme", kind, s)
			}
			idx.byScheme[s] = a
		}
		idx.registered = append(idx.registered, a)
	}

	if len(idx.byScheme) == 0 {
		return nil, domain.Internal(nil, "no %ss were registered", kind)
	}
	return idx, nil
}

// isNil reports an adapter that carries no value. An interface holding a typed
// nil pointer is not == nil, so the plain comparison lets one through.
func isNil[T Adapter](a T) bool {
	switch v := reflect.ValueOf(a); v.Kind() {
	case reflect.Invalid:
		// An untyped nil: the interface holds no type at all.
		return true
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
}

// Schemes lists what this build has, sorted.
func (i *Index[T]) Schemes() []string { return slices.Sorted(maps.Keys(i.byScheme)) }

// Lookup returns the adapter registered for a scheme.
func (i *Index[T]) Lookup(scheme string) (T, bool) {
	a, ok := i.byScheme[scheme]
	return a, ok
}

// Close releases every adapter that holds anything -- an SSH connection or a
// download directory above all.
//
// Every adapter is closed even after one fails: one that cannot tidy up must
// not leave the next one's socket open or its bundle on disk.
func (i *Index[T]) Close() error {
	var errs []error
	for _, a := range i.registered {
		if closer, ok := any(a).(io.Closer); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
