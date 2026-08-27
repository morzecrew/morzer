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
	"io"
	"strings"

	"github.com/morzecrew/morzer/internal/adapters/scheme"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Registry dispatches to the source registered for a reference's scheme.
//
// Schemes and Close come from the embedded index, which also holds the wiring
// refusals -- see internal/adapters/scheme.
type Registry struct {
	*scheme.Index[ports.ReleaseSource]
}

var (
	_ ports.ReleaseSource = (*Registry)(nil)
	_ io.Closer           = (*Registry)(nil)
)

// NewRegistry indexes each source under every scheme it declares.
func NewRegistry(sources ...ports.ReleaseSource) (*Registry, error) {
	index, err := scheme.NewIndex("release source", sources...)
	if err != nil {
		return nil, err
	}
	return &Registry{Index: index}, nil
}

// For selects the source for a reference.
//
// The refusal names what this build does support. A reference whose scheme is
// valid but unbuilt -- `oci://` on a binary compiled without it -- is an
// operator asking for something reasonable, and the answer should tell them
// what to do instead rather than only that they are wrong.
func (r *Registry) For(ref ports.Ref) (ports.ReleaseSource, error) {
	if s, ok := r.Lookup(ref.Scheme); ok {
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
