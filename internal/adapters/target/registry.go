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
	"reflect"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Registry dispatches to the target registered for a URL's scheme.
type Registry struct {
	byScheme map[string]ports.BackupTarget

	// registered is the argument list, in order, one entry per target
	// however many schemes it claims. Close walks it rather than
	// deduplicating the scheme map through a set keyed by the interface
	// value: hashing an interface whose dynamic type is not comparable
	// panics, and shutdown is the worst place to find that out.
	registered []ports.BackupTarget
}

var _ ports.BackupTarget = (*Registry)(nil)

// NewRegistry indexes each target under every scheme it declares.
//
// Two targets claiming one scheme is a wiring mistake with no sensible
// resolution, and an empty registry means a build in which every configured
// target would fail at push time -- late, during the nightly backup, rather
// than at startup with the rest.
//
// A nil target is the same kind of mistake, and is refused rather than skipped
// for the same reason: dropping it quietly leaves a build whose sftp:// URLs
// fail at push time as though the transport had never been compiled in. The
// check cannot be t == nil alone -- a nil *sftp.Target satisfies the interface,
// registers happily, and panics at shutdown when Close dereferences it.
func NewRegistry(targets ...ports.BackupTarget) (*Registry, error) {
	r := &Registry{byScheme: make(map[string]ports.BackupTarget, len(targets))}

	for _, t := range targets {
		if isNil(t) {
			return nil, domain.Internal(nil, "a nil backup target was registered")
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
		r.registered = append(r.registered, t)
	}

	if len(r.byScheme) == 0 {
		return nil, domain.Internal(nil, "no backup targets were registered")
	}
	return r, nil
}

// isNil reports a target that carries no value. An interface holding a typed
// nil pointer is not == nil, so the plain comparison lets one through.
func isNil(t ports.BackupTarget) bool {
	if t == nil {
		return true
	}
	switch v := reflect.ValueOf(t); v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
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

var _ io.Closer = (*Registry)(nil)

// Close releases anything a target holds -- an SSH connection above all.
//
// Every target is closed even after one fails, so a transport that cannot tidy
// up does not leave the next one's socket open. Each target appears once in
// registered however many schemes it answers for, so the loop needs no
// deduplication of its own.
func (r *Registry) Close() error {
	var errs []error

	for _, t := range r.registered {
		if closer, ok := t.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
