// Package source holds the registry that selects a release source by the
// scheme of the reference it is given.
//
// It exists so that adding a transport is adding an adapter and one line of
// wiring, rather than a branch in the lifecycle layer. The registry itself
// satisfies ports.ReleaseSource, so nothing above it knows there is more than
// one way for a bundle to arrive.
package source

import (
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Registry dispatches to the source registered for a reference's scheme.
type Registry struct {
	byScheme map[string]ports.ReleaseSource
}

var _ ports.ReleaseSource = (*Registry)(nil)

// NewRegistry indexes each source under every scheme it declares.
//
// Two sources claiming one scheme is a wiring mistake with no sensible
// resolution -- last-wins would make behaviour depend on argument order, and
// first-wins would silently ignore an adapter someone deliberately added. It is
// an error rather than a panic because the caller assembling the graph already
// returns one, so it surfaces at startup with the rest.
func NewRegistry(sources ...ports.ReleaseSource) (*Registry, error) {
	r := &Registry{byScheme: make(map[string]ports.ReleaseSource, len(sources))}

	for _, s := range sources {
		if s == nil {
			continue
		}
		schemes := s.Schemes()
		if len(schemes) == 0 {
			return nil, domain.Internal(nil, "a release source declares no schemes")
		}
		for _, scheme := range schemes {
			if _, taken := r.byScheme[scheme]; taken {
				return nil, domain.Internal(nil,
					"two release sources both claim the %q scheme", scheme)
			}
			r.byScheme[scheme] = s
		}
	}

	if len(r.byScheme) == 0 {
		return nil, domain.Internal(nil, "no release sources were registered")
	}
	return r, nil
}

// Schemes lists what this build can fetch, sorted.
func (r *Registry) Schemes() []string {
	out := slices.Sorted(maps.Keys(r.byScheme))
	return out
}

// For selects the source for a reference.
//
// The refusal names what this build does support. A reference whose scheme is
// valid but unbuilt -- `oci://` on a binary compiled without it -- is an
// operator asking for something reasonable, and the answer should tell them
// what to do instead rather than only that they are wrong.
func (r *Registry) For(ref ports.Ref) (ports.ReleaseSource, error) {
	if s, ok := r.byScheme[ref.Scheme]; ok {
		return s, nil
	}
	return nil, domain.Usage("no release source is configured for %q references", ref.Scheme).
		WithHint("this build supports: %s. Fetch the bundle out of band and pass a path.",
			strings.Join(r.Schemes(), ", "))
}

func (r *Registry) Resolve(ctx context.Context, ref ports.Ref) (ports.ResolvedRelease, error) {
	s, err := r.For(ref)
	if err != nil {
		return ports.ResolvedRelease{}, err
	}
	return s.Resolve(ctx, ref)
}

func (r *Registry) Fetch(ctx context.Context, ref ports.Ref, destDir string) (ports.BundlePath, error) {
	s, err := r.For(ref)
	if err != nil {
		return "", err
	}
	return s.Fetch(ctx, ref, destDir)
}

// Peek forwards to the source for the reference when it can watch a channel.
//
// A transport that cannot is refused by name rather than falling back to
// Resolve. The fallback is the tempting one and it is wrong: Resolve downloads
// the bundle for anything but a local path, so a poll that silently degraded to
// it would turn "check whether anything changed" into "fetch the whole release,
// every tick, forever".
func (r *Registry) Peek(ctx context.Context, ref ports.Ref) (ports.ChannelState, error) {
	s, err := r.For(ref)
	if err != nil {
		return ports.ChannelState{}, err
	}
	peeker, ok := s.(ports.ChannelPeeker)
	if !ok {
		return ports.ChannelState{}, domain.ValidationError(domain.ErrUnsupported,
			"%s references cannot be followed as a channel", ref.Scheme).
			WithHint("a channel is a mutable tag whose target can be read without " +
				"downloading it, which only a registry offers; update from a " +
				"reference instead")
	}
	return peeker.Peek(ctx, ref)
}

func (r *Registry) List(ctx context.Context, ref ports.Ref) ([]domain.Version, error) {
	s, err := r.For(ref)
	if err != nil {
		return nil, err
	}
	return s.List(ctx, ref)
}

var _ io.Closer = (*Registry)(nil)

// Close releases every source that holds anything, so a caller can clean up
// without knowing which transports it happens to have registered.
//
// Every source is closed even after one fails: a transport that cannot tidy up
// must not leave the next one's download on disk.
func (r *Registry) Close() error {
	var errs []error
	seen := make(map[ports.ReleaseSource]bool, len(r.byScheme))

	for _, s := range r.byScheme {
		// One source may answer for several schemes.
		if seen[s] {
			continue
		}
		seen[s] = true

		if closer, ok := s.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
