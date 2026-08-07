// Package atomicfs provides crash-safe file writes and traversal-proof
// filesystem access.
//
// Two guarantees matter here. First, a write either happens completely or not
// at all: a half-written installation.yaml would make every subsequent command
// fail on a parse error rather than on the original problem. Second, paths
// derived from release-supplied input cannot escape the directory they are
// meant to stay in -- enforced through os.Root, so containment is the kernel's
// job rather than a string-comparison the next refactor might weaken.
package atomicfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/renameio/v2"

	"github.com/morzecrew/morzer/internal/domain"
)

// WriteFile writes data to path atomically: a temporary file in the same
// directory, fsync, rename, then fsync of the directory.
//
// The temp file is created in the destination directory rather than /tmp
// because rename is only atomic within a filesystem, and /tmp is frequently a
// different one.
func WriteFile(path string, data []byte, mode fs.FileMode) error {
	// Captured before MkdirAll: every directory it is about to create needs
	// its own entry made durable afterwards, up to and including the first
	// ancestor that already existed -- whose entry for the new chain is the
	// one that must land.
	created := missingAncestors(filepath.Dir(path))
	if err := MkdirAll(filepath.Dir(path), parentDirMode(mode)); err != nil {
		return err
	}

	t, err := renameio.TempFile("", path)
	if err != nil {
		return domain.Internal(err, "cannot create temporary file next to %s", path)
	}
	defer func() { _ = t.Cleanup() }()

	// Chmod before the rename: a file that exists briefly with default
	// permissions is a file that could be read in that window, and secret
	// state passes through here.
	if err := t.Chmod(mode); err != nil {
		return domain.Internal(err, "cannot set mode %04o on temporary file for %s", mode.Perm(), path)
	}
	if _, err := t.Write(data); err != nil {
		return domain.Internal(err, "cannot write %s", path)
	}
	if err := t.CloseAtomicallyReplace(); err != nil {
		return domain.Internal(err, "cannot atomically replace %s", path)
	}
	// renameio fsyncs the file but not the directory, so without this the
	// rename's directory entry can be lost to a power cut after this
	// function reported success -- state and reality then disagree about
	// which file exists. Deepest first, then each newly created ancestor:
	// a synced child entry inside an unsynced parent is still lost.
	SyncDir(filepath.Dir(path))
	for _, d := range created {
		SyncDir(d)
	}
	return nil
}

// missingAncestors lists dir and every ancestor that does not exist yet,
// deepest first, ending with the first one that does -- the set whose entries
// a following MkdirAll creates and a durability-conscious caller must sync.
func missingAncestors(dir string) []string {
	var out []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			if len(out) > 0 {
				// Only interesting when something below it was
				// created: its entry for the new chain.
				out = append(out, d)
			}
			return out
		}
		out = append(out, d)
		if d == filepath.Dir(d) {
			return out
		}
	}
}

// SyncDir fsyncs a directory, making a rename or create inside it durable.
//
// Failure is deliberately swallowed, matching syncDirIn: the data itself is
// already written, only the ordering guarantee weakens, and failing an
// operation because a directory could not be fsynced would be worse.
func SyncDir(path string) {
	d, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = d.Close() }()
	_ = d.Sync()
}

// SyncFile fsyncs an already-written file by path, and the directory holding
// it: file bytes without a durable directory entry are still a file that never
// existed.
//
// For artifacts written by something else -- a helper container streaming a
// volume tarball, a subprocess -- where the writer's descriptor is out of
// reach. An artifact whose entire purpose is surviving a crash is worth the
// explicit flush.
func SyncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return domain.Internal(err, "cannot open %s to flush it", path)
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		return domain.Internal(err, "cannot flush %s to disk", path)
	}
	SyncDir(filepath.Dir(path))
	return nil
}

// SyncTree fsyncs every directory under root, root included, so each file and
// subdirectory entry created inside the tree is durable. File *contents* are
// the writers' business (extractFile and copyFileIn fsync their own); this is
// the directory half they cannot reach. Same swallowed-failure stance as
// SyncDir.
func SyncTree(root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			SyncDir(p)
		}
		return nil
	})
}

// WriteFileIn writes atomically inside a root, refusing paths that escape it.
// rel is interpreted relative to the root.
//
// os.Root resolves every component while refusing to traverse a symlink out of
// the tree, so a bundle whose manifest points a config target at
// ../../etc/shadow fails at the syscall rather than at a check someone
// remembered to write.
func WriteFileIn(root *os.Root, rel string, data []byte, mode fs.FileMode) error {
	rel, err := cleanRel(rel)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := mkdirAllIn(root, dir, 0o755); err != nil {
			return err
		}
	}

	// os.Root has no atomic-replace helper, so the temp-then-rename dance
	// is done by hand against the root's own Create and Rename.
	tmp := rel + ".tmp-" + randomSuffix()
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return domain.Internal(err, "cannot create %s inside %s", tmp, root.Name())
	}
	cleanup := func() { _ = f.Close(); _ = root.Remove(tmp) }

	if _, err := f.Write(data); err != nil {
		cleanup()
		return domain.Internal(err, "cannot write %s", rel)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return domain.Internal(err, "cannot fsync %s", rel)
	}
	// Create honours mode only modulo umask, so set it explicitly.
	if err := f.Chmod(mode); err != nil {
		cleanup()
		return domain.Internal(err, "cannot set mode on %s", rel)
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmp)
		return domain.Internal(err, "cannot close %s", rel)
	}
	if err := root.Rename(tmp, rel); err != nil {
		_ = root.Remove(tmp)
		return domain.Internal(err, "cannot rename %s into place", rel)
	}
	return syncDirIn(root, filepath.Dir(rel))
}

// parentDirMode derives the mode for directories WriteFile has to create from
// the file's own mode: the owner always gets rwx, group and other get rx only
// where the file grants them read. A 0640 state file lands in a 0750
// directory -- the ManagedDirs contract for the manager's own tree -- and a
// 0600 backup manifest in a 0700 one, instead of the flat 0755 that used to
// widen whatever the file's mode was trying to say.
func parentDirMode(mode fs.FileMode) fs.FileMode {
	dir := fs.FileMode(0o700)
	if mode&0o040 != 0 {
		dir |= 0o050
	}
	if mode&0o004 != 0 {
		dir |= 0o005
	}
	return dir
}

// MkdirAll creates a directory tree, applying mode to the directories it
// creates. Existing directories keep their permissions: `init` sets them, and
// a later `apply` has no business widening what an operator narrowed.
func MkdirAll(path string, mode fs.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return domain.Internal(err, "cannot create directory %s", path)
	}
	return nil
}

// MkdirFresh creates path with exactly mode, refusing a path that already
// exists. Parents are created as needed with the same mode.
//
// For directories whose name is an identity -- a backup ID -- where tolerating
// an existing directory would silently merge two artifacts into one. The
// caller detects the collision with errors.Is(err, fs.ErrExist) and picks a
// different name.
func MkdirFresh(path string, mode fs.FileMode) error {
	if err := MkdirAll(filepath.Dir(path), mode); err != nil {
		return err
	}
	if err := os.Mkdir(path, mode); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return domain.Internal(err, "directory %s already exists", path)
		}
		return domain.Internal(err, "cannot create directory %s", path)
	}
	// Mkdir honours mode only modulo umask.
	if err := os.Chmod(path, mode); err != nil {
		return domain.Internal(err, "cannot set mode %04o on %s", mode.Perm(), path)
	}
	return nil
}

// MkdirExact creates a directory and enforces its mode even if it already
// exists. Used for the directories whose permissions are a security property:
// the 0700 secret render directory above all.
func MkdirExact(path string, mode fs.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return domain.Internal(err, "cannot create directory %s", path)
	}
	if err := os.Chmod(path, mode); err != nil {
		return domain.Internal(err, "cannot set mode %04o on %s", mode.Perm(), path)
	}
	return nil
}

func mkdirAllIn(root *os.Root, rel string, mode fs.FileMode) error {
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	cur := ""
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		cur = filepath.Join(cur, p)
		if err := root.Mkdir(cur, mode); err != nil && !errors.Is(err, fs.ErrExist) {
			return domain.Internal(err, "cannot create directory %s inside %s", cur, root.Name())
		}
	}
	return nil
}

// syncDirIn fsyncs a directory so a rename survives a power loss. Without it,
// the file contents are durable but the directory entry pointing at them may
// not be.
func syncDirIn(root *os.Root, rel string) error {
	if rel == "" {
		rel = "."
	}
	d, err := root.Open(rel)
	if err != nil {
		// Not fatal: the data is written, only the ordering guarantee is
		// weakened, and failing the operation here would be worse.
		return nil
	}
	defer func() { _ = d.Close() }()
	_ = d.Sync()
	return nil
}

// OpenRoot opens a directory as a traversal-proof root.
func OpenRoot(dir string) (*os.Root, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, domain.Internal(err, "cannot open %s as a root directory", dir)
	}
	return root, nil
}

// ReadFileIn reads a file from inside a root.
func ReadFileIn(root *os.Root, rel string) ([]byte, error) {
	rel, err := cleanRel(rel)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.ValidationError(domain.ErrNotFound, "%s does not exist in %s", rel, root.Name())
		}
		return nil, domain.Internal(err, "cannot open %s in %s", rel, root.Name())
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, domain.Internal(err, "cannot read %s", rel)
	}
	return data, nil
}

// cleanRel validates a root-relative path. os.Root would reject an escape
// anyway; catching it here produces a domain error naming the offending path
// instead of a syscall error naming a file descriptor.
func cleanRel(rel string) (string, error) {
	if rel == "" {
		return "", domain.ValidationError(domain.ErrPathEscape, "empty path")
	}
	if filepath.IsAbs(rel) {
		return "", domain.ValidationError(domain.ErrPathEscape,
			"path %q must be relative to the root", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", domain.ValidationError(domain.ErrPathEscape, "path %q escapes the root", rel)
	}
	return clean, nil
}

// Exists reports whether a path exists, distinguishing "no" from "cannot
// tell". A permission error must not be reported as absence: `init` would then
// happily overwrite an installation it merely could not read.
func Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, domain.Internal(err, "cannot stat %s", path)
	}
}

// ReplaceSymlink atomically points link at target.
//
// A symlink cannot be rewritten in place, so it is created under a temporary
// name and renamed over the old one -- the swap that makes promoting a release
// atomic. At no instant does the link fail to exist.
func ReplaceSymlink(target, link string) error {
	dir := filepath.Dir(link)
	if err := MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(link)+".tmp-"+randomSuffix())
	if err := os.Symlink(target, tmp); err != nil {
		return domain.Internal(err, "cannot create symlink %s -> %s", tmp, target)
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return domain.Internal(err, "cannot move symlink %s into place", link)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// ReadSymlink returns a symlink's target, or empty when the link is absent.
func ReadSymlink(link string) (string, error) {
	target, err := os.Readlink(link)
	switch {
	case err == nil:
		return target, nil
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	default:
		return "", domain.Internal(err, "cannot read symlink %s", link)
	}
}

// CheckMode verifies a path's permission bits, returning a description of the
// mismatch rather than an error -- `doctor` reports these as findings, not as
// failures of `doctor` itself.
func CheckMode(path string, want fs.FileMode) (ok bool, detail string, err error) {
	info, statErr := os.Stat(path)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return false, "does not exist", nil
		}
		return false, "", domain.Internal(statErr, "cannot stat %s", path)
	}
	if !info.IsDir() && want.IsDir() {
		return false, "is not a directory", nil
	}
	got := info.Mode().Perm()
	if got != want.Perm() {
		return false, fmt.Sprintf("mode is %04o, expected %04o", got, want.Perm()), nil
	}
	return true, "", nil
}

// RemoveAll deletes a tree, tolerating absence.
func RemoveAll(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return domain.Internal(err, "cannot remove %s", path)
	}
	return nil
}

// RemoveWithOverwrite overwrites every regular file under path before removing
// it.
//
// What this is worth depends entirely on where it runs. On tmpfs -- which is
// where the only caller puts its files -- the bytes are pages of RAM, and
// overwriting them is exactly as final as it sounds. On a journalling or
// copy-on-write filesystem it is not: the old contents may survive in a journal,
// a snapshot, or an unreferenced extent that nothing will ever hand back but
// nothing has erased either.
//
// So it is a second line rather than the first. The first is that the file is
// on tmpfs at all, and `doctor` reports a `/run` that is not.
//
// Errors overwriting are not fatal: failing to scrub is not a reason to leave
// the file in place, and removal is the part that must happen.
func RemoveWithOverwrite(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return domain.Internal(err, "cannot inspect %s", path)
	}

	if info.IsDir() {
		entries, readErr := os.ReadDir(path)
		if readErr == nil {
			for _, e := range entries {
				_ = RemoveWithOverwrite(filepath.Join(path, e.Name()))
			}
		}
		return RemoveAll(path)
	}

	if info.Mode().IsRegular() {
		_ = overwriteFile(path, info.Size())
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return domain.Internal(err, "cannot remove %s", path)
	}
	return nil
}

// overwriteFile writes zeros over a file's contents and flushes them.
func overwriteFile(path string, size int64) error {
	if size <= 0 {
		return nil
	}

	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	// A page at a time, so a large file does not become a large allocation.
	const chunk = 32 << 10
	zeros := make([]byte, chunk)
	for remaining := size; remaining > 0; {
		n := int64(chunk)
		if remaining < n {
			n = remaining
		}
		if _, err := f.Write(zeros[:n]); err != nil {
			return err
		}
		remaining -= n
	}
	return f.Sync()
}
