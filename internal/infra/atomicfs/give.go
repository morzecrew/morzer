package atomicfs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/morzecrew/morzer/internal/domain"
)

// GiveTree gives dir, and every entry under it that another user owns, to
// uid:gid. It is for a directory whose writer is not root: a Mac's Docker
// Desktop writes as the user who runs it, and a file a hook left there as root
// is one no container can write (RFC 0055 decision 16).
//
// That user owns the tree, so it is walked as an adversary's. The walk stays
// inside an os.Root, so a link cannot take it outside dir. Links are skipped,
// never followed or changed. Every change is an fchown of a file opened and
// checked first, so an entry swapped after the walk saw it is not the one
// changed. And a file with a second link is left alone, since its other name
// may be outside dir: root handing over /etc/sudoers because someone
// hard-linked it into a data directory is the old attack on `chown -R`.
//
// A walk of a large tree stops when ctx is done. An entry that vanished, or
// was swapped for something else, while the walk reached it is skipped; any
// other failure is returned, so a tree left half given is not reported given.
func GiveTree(ctx context.Context, dir string, uid, gid int) error {
	return giveTree(ctx, dir, uid, gid, func(f *os.File) error { return f.Chown(uid, gid) })
}

func giveTree(ctx context.Context, dir string, uid, gid int, chown func(*os.File) error) error {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return domain.Internal(err, "cannot open %s", dir)
	}
	defer func() { _ = root.Close() }()

	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return domain.Internal(err, "cannot read %s", filepath.Join(dir, name))
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return nil
		}
		seen, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil // gone since the directory was read
		}
		if err != nil {
			return domain.Internal(err, "cannot stat %s", filepath.Join(dir, name))
		}
		f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			// Gone, or swapped for a link, perhaps one out of the tree,
			// since it was read; anything else is a failure.
			if now, statErr := root.Lstat(name); statErr != nil || (!now.IsDir() && !now.Mode().IsRegular()) {
				return nil
			}
			return domain.Internal(err, "cannot open %s", filepath.Join(dir, name))
		}
		defer func() { _ = f.Close() }()
		opened, err := f.Stat()
		if err != nil {
			return domain.Internal(err, "cannot stat %s", filepath.Join(dir, name))
		}
		st, ok := opened.Sys().(*syscall.Stat_t)
		if !ok || !os.SameFile(seen, opened) || (!opened.IsDir() && !opened.Mode().IsRegular()) {
			return nil
		}
		if (int(st.Uid) == uid && int(st.Gid) == gid) || (!opened.IsDir() && st.Nlink > 1) {
			return nil
		}
		if err := chown(f); err != nil {
			return domain.Internal(err, "cannot give %s to %d:%d", filepath.Join(dir, name), uid, gid)
		}
		return nil
	})
}
