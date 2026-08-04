package fakes

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"github.com/morzecrew/morzer/internal/adapters/target/blob"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// BackupTarget is an in-memory ports.BackupTarget.
//
// It runs the same blob choreography the real adapters do, over a map instead of
// a filesystem or a bucket. That is deliberate: a fake that reimplemented push
// and list would be a fake that could pass the contract suite while the shared
// code that decides whether an interrupted push is restorable went untested.
// Only the byte movement is faked.
type BackupTarget struct {
	// Scheme is what this fake answers for. Overridable so a registry test
	// can build two of them.
	Scheme string

	mu      sync.Mutex
	objects map[string][]byte

	// FailPushAt makes Put fail at the named key, which is how the suite
	// arranges a target that goes away mid-push.
	FailPushAt string

	// FailWith is the error every operation returns when set, which is how
	// the suite arranges a target that is simply unreachable.
	FailWith error

	// FailRemoveWith fails only removal, which is how the suite arranges a
	// target that accepts a push and refuses a prune -- the case that
	// separates "the backup is safe" from "the disk is tidy".
	FailRemoveWith error

	// Pushes counts successful pushes, so a test can assert a re-push
	// happened rather than inferring it from the contents.
	Pushes int
}

func NewBackupTarget() *BackupTarget {
	return &BackupTarget{Scheme: "memory", objects: map[string][]byte{}}
}

var _ ports.BackupTarget = (*BackupTarget)(nil)

func (t *BackupTarget) Schemes() []string { return []string{t.Scheme} }

func (t *BackupTarget) Push(ctx context.Context, ref ports.TargetRef, localDir, id string) (ports.RemoteRef, error) {
	if t.FailWith != nil {
		return ports.RemoteRef{}, t.FailWith
	}
	remote, err := blob.Push(ctx, t.store(ref), ref, localDir, id)
	if err == nil {
		t.mu.Lock()
		t.Pushes++
		t.mu.Unlock()
	}
	return remote, err
}

func (t *BackupTarget) List(ctx context.Context, ref ports.TargetRef) ([]ports.BackupManifest, error) {
	if t.FailWith != nil {
		return nil, t.FailWith
	}
	return blob.List(ctx, t.store(ref))
}

func (t *BackupTarget) Fetch(ctx context.Context, ref ports.RemoteRef, destDir string) error {
	if t.FailWith != nil {
		return t.FailWith
	}
	return blob.Fetch(ctx, t.store(ref.Target), ref, destDir)
}

func (t *BackupTarget) Verify(ctx context.Context, ref ports.RemoteRef) error {
	if t.FailWith != nil {
		return t.FailWith
	}
	return blob.Verify(ctx, t.store(ref.Target), ref)
}

func (t *BackupTarget) Remove(ctx context.Context, ref ports.RemoteRef) error {
	if t.FailWith != nil {
		return t.FailWith
	}
	if t.FailRemoveWith != nil {
		return t.FailRemoveWith
	}
	return blob.Remove(ctx, t.store(ref.Target), ref)
}

// Objects returns the keys currently held, sorted. For assertions about what a
// half-finished push left behind.
func (t *BackupTarget) Objects() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]string, 0, len(t.objects))
	for key := range t.objects {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// store namespaces the map by target path, so one fake can answer for several
// configured targets the way a real adapter does.
func (t *BackupTarget) store(ref ports.TargetRef) blob.Store {
	return &memStore{target: t, prefix: strings.Trim(ref.Path, "/") + "/"}
}

type memStore struct {
	target *BackupTarget
	prefix string
}

func (s *memStore) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	if s.target.FailPushAt != "" && strings.HasSuffix(key, s.target.FailPushAt) {
		return domain.BackupError(nil, "the target went away while writing %s", key)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	s.target.mu.Lock()
	defer s.target.mu.Unlock()
	s.target.objects[s.prefix+key] = data
	return nil
}

func (s *memStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	s.target.mu.Lock()
	defer s.target.mu.Unlock()

	data, ok := s.target.objects[s.prefix+key]
	if !ok {
		// fs.ErrNotExist, because blob distinguishes "not there" from
		// "unreachable" and so must every adapter.
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *memStore) Keys(ctx context.Context, prefix string) ([]string, error) {
	s.target.mu.Lock()
	defer s.target.mu.Unlock()

	var out []string
	for key := range s.target.objects {
		rel, ok := strings.CutPrefix(key, s.prefix)
		if !ok || !strings.HasPrefix(rel, prefix) {
			continue
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

func (s *memStore) Delete(ctx context.Context, key string) error {
	s.target.mu.Lock()
	defer s.target.mu.Unlock()
	delete(s.target.objects, s.prefix+key)
	return nil
}
