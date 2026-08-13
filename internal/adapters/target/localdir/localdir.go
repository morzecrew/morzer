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
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	root, err := rootPath(ref)
	if err != nil {
		return ports.RemoteRef{}, err
	}
	// Refusing to push a directory onto itself. It would half-work -- every
	// file copied over itself -- and the failure would surface later as a
	// backup that appears to be on a target and is not.
	//
	// Before the target directory is created, not after: creating it is
	// already a write into the backup when the target is rooted inside it.
	if selfTarget(localDir, root, id) {
		return ports.RemoteRef{}, domain.BackupError(nil,
			"the backup target %s is where this backup already lives", ref).
			WithHint("a target has to be somewhere the machine's disk failing does not take with it")
	}
	store, err := t.store(ref, true)
	if err != nil {
		return ports.RemoteRef{}, err
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

func (t *Target) FetchFile(ctx context.Context, ref ports.RemoteRef, name, destDir string) error {
	store, err := t.store(ref.Target, false)
	if err != nil {
		return err
	}
	return blob.FetchFile(ctx, store, ref, name, destDir)
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

var _ ports.ObjectStore = (*Target)(nil)

func (t *Target) PutObject(ctx context.Context, ref ports.TargetRef, key string, data []byte) error {
	store, err := t.store(ref, true)
	if err != nil {
		return err
	}
	return blob.PutObject(ctx, store, key, data)
}

func (t *Target) ObjectKeys(ctx context.Context, ref ports.TargetRef, prefix string) ([]string, error) {
	store, err := t.store(ref, false)
	if err != nil {
		return nil, err
	}
	return blob.ObjectKeys(ctx, store, prefix)
}

// GetObject reads one back. The store is opened without creating it, like
// ObjectKeys and unlike PutObject: reading a directory into existence would
// make "this target has never been published to" indistinguishable from "this
// target is now an empty directory I just made".
func (t *Target) GetObject(ctx context.Context, ref ports.TargetRef, key string) ([]byte, error) {
	store, err := t.store(ref, false)
	if err != nil {
		return nil, err
	}
	return blob.GetObject(ctx, store, key)
}

// store resolves the target's root directory, creating it when a push is about
// to need it.
//
// 0700, like the backup directory it mirrors: a target holds the same
// ciphertext, and there is no reason for it to be more readable on the second
// machine than it was on the first.
func (t *Target) store(ref ports.TargetRef, create bool) (*dirStore, error) {
	root, err := rootPath(ref)
	if err != nil {
		return nil, err
	}

	if create {
		if err := atomicfs.MkdirAll(root, 0o700); err != nil {
			return nil, t.unreachable(ref, err)
		}
	}
	return &dirStore{root: root, ref: ref, target: t}, nil
}

// rootPath is the target's directory, without touching the filesystem: the
// self-target check has to run before anything is created.
func rootPath(ref ports.TargetRef) (string, error) {
	if ref.Path == "" || ref.Path == "/" {
		return "", domain.Usage("the file:// backup target has no path").
			WithHint("write file:///mnt/usb/backups")
	}
	return filepath.Clean(ref.Path), nil
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

// Put writes a component, refusing to write through a symlink out of the
// target.
//
// Root-relative for the same reason Get is, in the other direction. Whoever
// owns the medium can leave `20260101T000000Z` behind as a link to /etc, and an
// ordinary create-and-rename would then write the backup's bytes wherever the
// link points, under a name the target chose. os.Root makes containment the
// kernel's job for the write path too.
func (s *dirStore) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rel, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := atomicfs.MkdirAll(s.root, 0o700); err != nil {
		return s.target.unreachable(s.ref, err)
	}
	root, err := atomicfs.OpenRoot(s.root)
	if err != nil {
		return s.target.unreachable(s.ref, err)
	}
	defer func() { _ = root.Close() }()

	dir := filepath.Dir(rel)
	if dir != "." {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return s.target.unreachable(s.ref, err)
		}
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
	tmp := filepath.Join(dir, "."+filepath.Base(rel)+".partial-"+randomSuffix())
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return s.target.unreachable(s.ref, err)
	}
	discard := func() {
		_ = f.Close()
		_ = root.Remove(tmp)
	}
	// O_CREATE honours the mode only modulo umask, and a target holds the same
	// ciphertext the 0700 backup directory does.
	if err := f.Chmod(0o600); err != nil {
		discard()
		return s.target.unreachable(s.ref, err)
	}
	if _, err := io.Copy(f, ctxReader{ctx: ctx, r: r}); err != nil {
		discard()
		// An abandoned copy is reported as what it was. "Cannot reach the
		// target" would send an operator to look at the disk.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return s.target.unreachable(s.ref, err)
	}
	// Flushed before the rename, and the directory after it: a push that
	// reported success and then lost the component -- or its directory entry --
	// to a power cut is the one failure a backup exists to survive. This is the
	// contract atomicfs holds for the files it writes; it holds it for a
	// []byte, and a component is too large to hold in memory.
	if err := f.Sync(); err != nil {
		discard()
		return s.target.unreachable(s.ref, err)
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmp)
		return s.target.unreachable(s.ref, err)
	}
	if err := root.Rename(tmp, rel); err != nil {
		_ = root.Remove(tmp)
		return s.target.unreachable(s.ref, err)
	}
	syncDir(root, dir)
	return nil
}

// ctxReader stops a copy that is already running when the operation is
// abandoned. The checks in Put and its neighbours only cover work that has not
// started; without this, a cancelled push goes on writing gigabytes to a USB
// disk long after the operation that owned it was reported as failed.
//
// Between reads, not during one: a read already blocked on a dead NFS mount
// stays blocked, and nothing at this layer can change that.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// syncDir flushes a directory so a rename survives a power cut. Failure is not
// fatal: the bytes are written, only the ordering guarantee is weakened, and
// failing the push here would be the worse answer.
func syncDir(root *os.Root, rel string) {
	d, err := root.Open(rel)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// randomSuffix names a staging file. crypto/rand rather than a counter, because
// the writers that must not collide are separate processes -- and separate
// machines, on an NFS mount.
func randomSuffix() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rel, err := s.resolve(key)
	if err != nil {
		return nil, err
	}

	root, err := atomicfs.OpenRoot(s.root)
	if err != nil {
		return nil, s.target.unreachable(s.ref, err)
	}

	f, err := root.Open(rel)
	if err != nil {
		_ = root.Close()
		if errors.Is(err, fs.ErrNotExist) {
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

// Keys walks the target through os.Root rather than over its path.
//
// filepath.WalkDir stops at a root that is itself a symlink -- it lstats it,
// sees a link rather than a directory, and descends into nothing. A symlinked
// /mnt/usb is an ordinary way to mount a target, and pushing through one worked
// fine, so the target accepted backups and then listed none of them: `backup
// list --target` reported an empty medium that was not empty.
func (s *dirStore) Keys(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := atomicfs.OpenRoot(s.root)
	if err != nil {
		// A target directory that is not there holds no backups; that is
		// the state before the first push, not a failure.
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, s.target.unreachable(s.ref, err)
	}
	defer func() { _ = root.Close() }()

	var out []string
	err = fs.WalkDir(root.FS(), ".", func(key string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		// Staging files are listed, not filtered. The filter that used to
		// be here matched `.partial`, a name no staging file has carried
		// since they became unique per write -- and reviving it would be
		// worse than deleting it. Everything that reads Keys is
		// manifest-driven; the one caller that sees these keys is Remove,
		// which must delete them, or an interrupted push leaves a
		// directory nothing ever cleans up.
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			return nil
		}
		out = append(out, key)
		return nil
	})
	if err != nil {
		return nil, s.target.unreachable(s.ref, err)
	}

	sort.Strings(out)
	return out, nil
}

// Delete removes a component, refusing to delete through a symlink out of the
// target -- the same containment Get and Put hold, and the one that matters
// most, since retention runs unattended. A `20260101T000000Z` left behind as a
// link to /etc would otherwise turn a prune into a deletion over there.
func (s *dirStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rel, err := s.resolve(key)
	if err != nil {
		return err
	}
	root, err := atomicfs.OpenRoot(s.root)
	if err != nil {
		// Nothing to delete on a target that is not there. Remove is
		// retried after a partial failure and must stay repeatable.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return s.target.unreachable(s.ref, err)
	}
	defer func() { _ = root.Close() }()

	if err := root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return s.target.unreachable(s.ref, err)
	}
	return nil
}

// resolve cleans a key into a path relative to the target's root and refuses
// one that escapes it.
//
// Keys come from a manifest, and a manifest on a target is a file this manager
// may not have written. `"../../etc/passwd"` as a component path must not be a
// way for a target to decide where a fetch writes -- or, on a push, what a
// remove deletes. os.Root would refuse it too; refusing it here is what names
// the offending component in the error an operator reads.
func (s *dirStore) resolve(key string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(key))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", domain.BackupError(nil,
			"the backup names a component outside the target: %q", key).
			WithHint("this backup was not written by this manager; do not restore from it")
	}
	return clean, nil
}

// selfTarget reports whether pushing localDir to this target would copy the
// backup into itself.
//
// Two ways it can be, and only the second one used to be caught. Comparing the
// backup directory with <root>/<id> asks a question about a directory that does
// not exist yet on a first push: the stat failed, the check was skipped, and a
// target rooted *inside* the backup directory -- `file://` at the backup
// itself, or at a directory under it -- was accepted and given a nested copy of
// the backup it was supposed to be a copy of. So the paths are compared for
// overlap in either direction first.
//
// The inode comparison stays for what comparing paths cannot see: a bind mount
// of the backup directory under another name is one directory with two names.
func selfTarget(localDir, root, id string) bool {
	if overlaps(localDir, root) {
		return true
	}
	same, err := sameDir(localDir, filepath.Join(root, id))
	return err == nil && same
}

// overlaps reports whether two directories are the same one or one is inside
// the other.
func overlaps(a, b string) bool {
	a, b = resolved(a), resolved(b)
	sep := string(filepath.Separator)
	return a == b || strings.HasPrefix(a, b+sep) || strings.HasPrefix(b, a+sep)
}

// resolved makes a path absolute with its symlinks resolved, resolving as much
// of it as exists: the target directory is usually created by the very push
// that has to be refused, so the check cannot require it to be there.
func resolved(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	rest := ""
	for cur := abs; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
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
