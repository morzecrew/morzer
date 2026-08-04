// Package localdir implements ports.BackupTarget for a directory: a second
// disk, an NFS or SMB mount, removable media.
//
// It is the "worst case, picked up manually" answer, and it is in the first
// milestone rather than treated as the toy case for one reason: it needs no
// credential. Every other transport has a chicken-and-egg problem during a
// recovery -- the credentials are secrets, the secrets are in the backup, the
// backup is behind the credentials -- and this one does not. An operator with
// the disk in their hand and the offline age key in their pocket can restore
// with nothing else.
//
// It is also the reference implementation of the port. Everything it does is
// blob.Push and friends over os; if a contract test fails here it is the
// choreography that is wrong, not a transport.
package localdir

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/adapters/target/blob"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// Scheme is the URL scheme this target handles.
const Scheme = "file"

type Target struct{}

func New() *Target { return &Target{} }

var _ ports.BackupTarget = (*Target)(nil)

func (t *Target) Schemes() []string { return []string{Scheme} }

func (t *Target) Push(ctx context.Context, ref ports.TargetRef, localDir, id string) (ports.RemoteRef, error) {
	store, err := t.store(ref, true)
	if err != nil {
		return ports.RemoteRef{}, err
	}
	// Refusing to push a directory onto itself. It would half-work -- every
	// file copied over itself -- and the failure would surface later as a
	// backup that appears to be on a target and is not.
	if same, err := sameDir(localDir, filepath.Join(store.root, id)); err == nil && same {
		return ports.RemoteRef{}, domain.BackupError(nil,
			"the backup target %s is where this backup already lives", ref).
			WithHint("a target has to be somewhere the machine's disk failing does not take with it")
	}
	return blob.Push(ctx, store, ref, localDir, id)
}

func (t *Target) List(ctx context.Context, ref ports.TargetRef) ([]ports.BackupManifest, error) {
	store, err := t.store(ref, false)
	if err != nil {
		return nil, err
	}
	// A target directory that is not there yet holds no backups. That is
	// the state before the first push, and `backup list --target` must
	// answer it rather than fail.
	if _, err := os.Stat(store.root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, t.unreachable(ref, err)
	}
	return blob.List(ctx, store)
}

func (t *Target) Fetch(ctx context.Context, ref ports.RemoteRef, destDir string) error {
	store, err := t.store(ref.Target, false)
	if err != nil {
		return err
	}
	return blob.Fetch(ctx, store, ref, destDir)
}

func (t *Target) Verify(ctx context.Context, ref ports.RemoteRef) error {
	store, err := t.store(ref.Target, false)
	if err != nil {
		return err
	}
	return blob.Verify(ctx, store, ref)
}

func (t *Target) Remove(ctx context.Context, ref ports.RemoteRef) error {
	store, err := t.store(ref.Target, false)
	if err != nil {
		return err
	}
	if err := blob.Remove(ctx, store, ref); err != nil {
		return err
	}
	// An empty directory left behind would be listed as a backup with no
	// manifest by anything less careful than blob.List.
	_ = os.Remove(filepath.Join(store.root, ref.ID))
	return nil
}

// store resolves the target's root directory, creating it when a push is about
// to need it.
//
// 0700, like the backup directory it mirrors: a target holds the same
// ciphertext, and there is no reason for it to be more readable on the second
// machine than it was on the first.
func (t *Target) store(ref ports.TargetRef, create bool) (*dirStore, error) {
	if ref.Path == "" || ref.Path == "/" {
		return nil, domain.Usage("the file:// backup target has no path").
			WithHint("write file:///mnt/usb/backups")
	}
	root := filepath.Clean(ref.Path)

	if create {
		if err := atomicfs.MkdirAll(root, 0o700); err != nil {
			return nil, t.unreachable(ref, err)
		}
	}
	return &dirStore{root: root, ref: ref, target: t}, nil
}

// unreachable turns a filesystem failure into the diagnosis an operator needs,
// which is almost always that the medium is not mounted.
func (t *Target) unreachable(ref ports.TargetRef, cause error) error {
	return domain.BackupError(cause, "cannot reach the backup target %s", ref).
		WithHint("check that the filesystem is mounted and writable; " +
			"a target on an unmounted path would silently fill the root disk instead")
}

// dirStore is blob.Store over a directory.
type dirStore struct {
	root   string
	ref    ports.TargetRef
	target *Target
}

var _ blob.Store = (*dirStore)(nil)

func (s *dirStore) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	path, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := atomicfs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return s.target.unreachable(s.ref, err)
	}

	// Written to a neighbour and renamed, so a target never holds a
	// half-written component even for the moment the copy takes. Push's
	// manifest-last rule covers the interrupted-push case; this covers the
	// interrupted-file case, which is what a full disk produces.
	//
	// The neighbour's name is unique per write, not `<name>.partial`. A shared
	// name means two pushes of the same backup -- a manual `backup push`
	// overlapping the scheduled one, or two machines writing to one NFS mount,
	// which no lock of ours reaches -- truncate each other's staging file, and
	// the survivor renames bytes it did not write.
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".partial-")
	if err != nil {
		return s.target.unreachable(s.ref, err)
	}
	tmp := f.Name()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return s.target.unreachable(s.ref, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return s.target.unreachable(s.ref, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return s.target.unreachable(s.ref, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return s.target.unreachable(s.ref, err)
	}
	return nil
}

// Get opens a component, refusing to follow a symlink out of the target.
//
// The lexical check in resolve is not enough on its own. A target is somewhere
// this deployment does not control -- that is the entire premise -- so whoever
// owns the medium can replace `20260101T000000Z/database.sql.age` with a link
// to /etc/shadow. The manifest names an innocent path, resolve agrees, and the
// manager reads a local file into the backup it is fetching.
//
// os.Root is what closes that: every component is opened relative to the
// target's own directory, and the kernel refuses a traversal the path string
// never showed.
func (s *dirStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if _, err := s.resolve(key); err != nil {
		return nil, err
	}

	root, err := atomicfs.OpenRoot(s.root)
	if err != nil {
		return nil, s.target.unreachable(s.ref, err)
	}

	f, err := root.Open(filepath.FromSlash(key))
	if err != nil {
		_ = root.Close()
		if os.IsNotExist(err) {
			// Passed through unwrapped so blob can tell "not there"
			// from "the medium is gone".
			return nil, err
		}
		return nil, s.target.unreachable(s.ref, err)
	}
	return rootFile{File: f, root: root}, nil
}

// rootFile closes the os.Root the file was opened through, so a fetch does not
// leak a directory handle per component.
type rootFile struct {
	*os.File
	root *os.Root
}

func (f rootFile) Close() error {
	err := f.File.Close()
	_ = f.root.Close()
	return err
}

func (s *dirStore) Keys(ctx context.Context, prefix string) ([]string, error) {
	var out []string

	err := filepath.WalkDir(s.root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(s.root, p)
		if relErr != nil {
			return relErr
		}
		key := filepath.ToSlash(rel)
		if strings.HasSuffix(key, ".partial") {
			return nil // an upload that did not finish
		}
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			return nil
		}
		out = append(out, key)
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, s.target.unreachable(s.ref, err)
	}

	sort.Strings(out)
	return out, nil
}

func (s *dirStore) Delete(ctx context.Context, key string) error {
	path, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return s.target.unreachable(s.ref, err)
	}
	return nil
}

// resolve joins a key onto the root and refuses one that escapes it.
//
// Keys come from a manifest, and a manifest on a target is a file this manager
// may not have written. `"../../etc/passwd"` as a component path must not be a
// way for a target to decide where a fetch writes -- or, on a push, what a
// remove deletes.
func (s *dirStore) resolve(key string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(key))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", domain.BackupError(nil,
			"the backup names a component outside the target: %q", key).
			WithHint("this backup was not written by this manager; do not restore from it")
	}
	return filepath.Join(s.root, clean), nil
}

// sameDir reports whether two paths are the same directory, resolving symlinks
// so a bind mount or a symlinked /mnt does not defeat the check.
func sameDir(a, b string) (bool, error) {
	infoA, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	infoB, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(infoA, infoB), nil
}
