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
	"io"
	"strings"

	"github.com/morzecrew/morzer/internal/adapters/scheme"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Registry dispatches to the target registered for a URL's scheme.
//
// Schemes and Close come from the embedded index, which also holds the wiring
// refusals -- see internal/adapters/scheme.
type Registry struct {
	*scheme.Index[ports.BackupTarget]
}

var (
	_ ports.BackupTarget = (*Registry)(nil)
	_ io.Closer          = (*Registry)(nil)
)

// NewRegistry indexes each target under every scheme it declares.
func NewRegistry(targets ...ports.BackupTarget) (*Registry, error) {
	index, err := scheme.NewIndex("backup target", targets...)
	if err != nil {
		return nil, err
	}
	return &Registry{Index: index}, nil
}

// For selects the target for a reference.
//
// The refusal names what this build does support, because a URL whose scheme is
// valid but unbuilt is an operator asking for something reasonable, and the
// answer should tell them what to do instead rather than only that they are
// wrong.
func (r *Registry) For(ref ports.TargetRef) (ports.BackupTarget, error) {
	if t, ok := r.Lookup(ref.Scheme); ok {
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

func (r *Registry) FetchFile(ctx context.Context, ref ports.RemoteRef, name, destDir string) error {
	t, err := r.For(ref.Target)
	if err != nil {
		return err
	}
	return t.FetchFile(ctx, ref, name, destDir)
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

var _ ports.ObjectStore = (*Registry)(nil)

// PutObject writes an object, and ObjectKeys and GetObject read them back --
// the ports.ObjectStore half of the port, dispatched like the rest.
//
// The selected target may not implement it, which is a refusal rather than a
// silent skip: a build whose sftp:// transport could not hold attestations
// would otherwise report every push as done and leave the record on the
// machine. Every transport in this build does implement it, so the message is
// about a future one -- and it names the target rather than the interface,
// because the operator did not choose an interface.
func (r *Registry) PutObject(ctx context.Context, ref ports.TargetRef, key string, data []byte) error {
	store, err := r.objectStore(ref)
	if err != nil {
		return err
	}
	return store.PutObject(ctx, ref, key, data)
}

func (r *Registry) ObjectKeys(ctx context.Context, ref ports.TargetRef, prefix string) ([]string, error) {
	store, err := r.objectStore(ref)
	if err != nil {
		return nil, err
	}
	return store.ObjectKeys(ctx, ref, prefix)
}

func (r *Registry) GetObject(ctx context.Context, ref ports.TargetRef, key string) ([]byte, error) {
	store, err := r.objectStore(ref)
	if err != nil {
		return nil, err
	}
	return store.GetObject(ctx, ref, key)
}

func (r *Registry) objectStore(ref ports.TargetRef) (ports.ObjectStore, error) {
	t, err := r.For(ref)
	if err != nil {
		return nil, err
	}
	store, ok := t.(ports.ObjectStore)
	if !ok {
		return nil, domain.Usage(
			"%s keeps backups but cannot hold anything else", ref).
			WithHint("attestations and fleet rows go to file://, ssh:// and s3:// targets")
	}
	return store, nil
}
