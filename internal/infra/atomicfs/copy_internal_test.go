package atomicfs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The copy's read side refuses a symlink at every component. The published
// tests reach it through CopyTree, where the walk rejects a visible symlink
// first -- which is the observable behaviour, and not this code path. These
// call the descent directly, so what is asserted is the open itself: the thing
// that has to hold when an entry changes *after* the walk decided about it, and
// the only way to reach it without a race a test cannot win.

func TestOpenFileNoFollowRefusesASymlinkAtTheLastComponent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(dir, "alias.txt")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	fd, err := openDirNoFollow(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()

	if f, err := openFileNoFollow(fd, "alias.txt"); err == nil {
		_ = f.Close()
		t.Fatal("a symlink was opened as the file the walk saw")
	} else if !errors.Is(err, errSymlinkComponent) {
		t.Errorf("error is %v, want the symlink marker so the caller can name it", err)
	}

	// The file it points at opens normally, which is what makes the
	// refusal about the link rather than about the name.
	f, err := openFileNoFollow(fd, "real.txt")
	if err != nil {
		t.Fatalf("a regular file was refused: %v", err)
	}
	_ = f.Close()
}

// The component that matters most is an intermediate one: os.Root would have
// followed a directory symlink that stays inside the tree, so `hooks/migrate`
// could be opened through a `hooks` that had become a link to somewhere else in
// the same bundle -- different bytes under the walked name.
func TestOpenFileNoFollowRefusesASymlinkedDirectoryComponent(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"hooks", "other"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "other", "migrate"), []byte("elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "hooks")); err != nil {
		t.Fatal(err)
	}
	// A link that resolves *inside* the tree, which containment permits and
	// this refuses.
	if err := os.Symlink("other", filepath.Join(dir, "hooks")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	fd, err := openDirNoFollow(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()

	// ENOTDIR rather than ELOOP on this one -- O_DIRECTORY sees a link that
	// is not a directory before O_NOFOLLOW gets to complain -- which is why
	// the descent classifies rather than reading the errno.
	if f, err := openFileNoFollow(fd, "hooks/migrate"); err == nil {
		_ = f.Close()
		t.Fatal("a symlinked directory component was traversed")
	} else if !errors.Is(err, errSymlinkComponent) {
		t.Errorf("error is %v, want the symlink marker", err)
	}
}

func TestOpenFileNoFollowRefusesATraversingName(t *testing.T) {
	dir := t.TempDir()
	fd, err := openDirNoFollow(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()

	// The walk cannot produce these, and the descent must not depend on
	// that: no containment check follows it, so a ".." would be the escape.
	for _, rel := range []string{"../escape", "a/../../escape", ".", ""} {
		if f, err := openFileNoFollow(fd, rel); err == nil {
			_ = f.Close()
			t.Errorf("%q was resolved", rel)
		}
	}
}

func TestOpenDirNoFollowRefusesASymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	if fd, err := openDirNoFollow(link); err == nil {
		_ = syscall.Close(fd)
		t.Error("a symlinked source directory was opened")
	}
}
