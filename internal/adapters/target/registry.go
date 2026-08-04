// Package target holds the registry that selects a backup target by the scheme
// of the URL it is given.
//
// It is deliberately the same shape as the release-source registry: one adapter
// per scheme, a refusal that names the schemes this build actually has, and a
// registry that itself satisfies the port so nothing above it knows there is
// more than one way for a backup to leave the machine. A second registry shape
// for the same problem would be a second thing to keep honest.
package target

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Registry dispatches to the target registered for a URL's scheme.
type Registry struct {
	byScheme map[string]ports.BackupTarget
}

var _ ports.BackupTarget = (*Registry)(nil)

// NewRegistry indexes each target under every scheme it declares.
//
// Two targets claiming one scheme is a wiring mistake with no sensible
// resolution, and an empty registry means a build in which every configured
// target would fail at push time -- late, during the nightly backup, rather
// than at startup with the rest.
func NewRegistry(targets ...ports.BackupTarget) (*Registry, error) {
	r := &Registry{byScheme: make(map[string]ports.BackupTarget, len(targets))}

	for _, t := range targets {
		if t == nil {
			continue
		}
		schemes := t.Schemes()
		if len(schemes) == 0 {
			return nil, domain.Internal(nil, "a backup target declares no schemes")
		}
		for _, scheme := range schemes {
			if _, taken := r.byScheme[scheme]; taken {
				return nil, domain.Internal(nil,
					"two backup targets both claim the %q scheme", scheme)
			}
			r.byScheme[scheme] = t
		}
	}

	if len(r.byScheme) == 0 {
		return nil, domain.Internal(nil, "no backup targets were registered")
	}
	return r, nil
}

// Schemes lists what this build can push to, sorted.
func (r *Registry) Schemes() []string {
	out := make([]string, 0, len(r.byScheme))
	for scheme := range r.byScheme {
		out = append(out, scheme)
	}
	sort.Strings(out)
	return out
}

// For selects the target for a reference.
//
// The refusal names what this build does support, because a URL whose scheme is
// valid but unbuilt is an operator asking for something reasonable, and the
// answer should tell them what to do instead rather than only that they are
// wrong.
func (r *Registry) For(ref ports.TargetRef) (ports.BackupTarget, error) {
	if t, ok := r.byScheme[ref.Scheme]; ok {
		return t, nil
	}
	return nil, domain.Usage("no backup target is configured for %q URLs", ref.Scheme).
		WithHint("this build supports: %s", strings.Join(r.Schemes(), ", "))
}

func (r *Registry) Push(ctx context.Context, ref ports.TargetRef, localDir, id string) (ports.RemoteRef, error) {
	t, err := r.For(ref)
	if err != nil {
		return ports.RemoteRef{}, err
	}
	return t.Push(ctx, ref, localDir, id)
}

func (r *Registry) List(ctx context.Context, ref ports.TargetRef) ([]ports.BackupManifest, error) {
	t, err := r.For(ref)
	if err != nil {
		return nil, err
	}
	return t.List(ctx, ref)
}

func (r *Registry) Fetch(ctx context.Context, ref ports.RemoteRef, destDir string) error {
	t, err := r.For(ref.Target)
	if err != nil {
		return err
	}
	return t.Fetch(ctx, ref, destDir)
}

func (r *Registry) Verify(ctx context.Context, ref ports.RemoteRef) error {
	t, err := r.For(ref.Target)
	if err != nil {
		return err
	}
	return t.Verify(ctx, ref)
}

func (r *Registry) Remove(ctx context.Context, ref ports.RemoteRef) error {
	t, err := r.For(ref.Target)
	if err != nil {
		return err
	}
	return t.Remove(ctx, ref)
}

var _ io.Closer = (*Registry)(nil)

// Close releases anything a target holds -- an SSH connection above all.
//
// Every target is closed even after one fails, so a transport that cannot tidy
// up does not leave the next one's socket open.
func (r *Registry) Close() error {
	var errs []error
	seen := make(map[ports.BackupTarget]bool, len(r.byScheme))

	for _, t := range r.byScheme {
		if seen[t] {
			continue // one target may answer for several schemes
		}
		seen[t] = true

		if closer, ok := t.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
