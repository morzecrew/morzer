package atomicfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GiveTree changes what another user owns in the tree, and nothing it reaches
// through a link or a second name (RFC 0055 decision 16).
func TestGiveTreeChangesOnlyWhatIsInsideAndNamedOnce(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "sudoers"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "once"), []byte("x"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "deep"), []byte("x"), 0o600))
	require.NoError(t, os.Link(filepath.Join(outside, "sudoers"), filepath.Join(dir, "linked")))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "away")))
	require.NoError(t, os.Symlink(filepath.Join(dir, "once"), filepath.Join(dir, "alias")))

	var given []string
	record := func(f *os.File) error {
		given = append(given, filepath.Base(f.Name()))
		return nil
	}

	// Everything here is the test's, so another uid makes every entry
	// "someone else's".
	require.NoError(t, giveTree(context.Background(), dir, os.Getuid()+1, os.Getgid(), record))
	assert.ElementsMatch(t, []string{".", "once", "sub", "deep"}, given,
		"a link was followed, or a file with a second name was changed")

	given = nil
	require.NoError(t, giveTree(context.Background(), dir, os.Getuid(), os.Getgid(), record))
	assert.Empty(t, given, "an entry its owner already owns was changed")

	require.NoError(t, giveTree(context.Background(), filepath.Join(dir, "missing"), 0, 0, record))
}

// A directory that vanishes once its parent was read is skipped, not
// descended into and reported unreadable.
func TestGiveTreeSkipsADirectoryThatVanished(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "b", "c"), 0o755))
	removeB := func(f *os.File) error {
		if filepath.Base(f.Name()) == "a" {
			return os.RemoveAll(filepath.Join(dir, "b"))
		}
		return nil
	}

	require.NoError(t, giveTree(context.Background(), dir, os.Getuid()+1, os.Getgid(), removeB))

	// Gone after it was given and before it was read.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "b", "c"), 0o755))
	removeSelf := func(f *os.File) error {
		if filepath.Base(f.Name()) == "b" {
			return os.RemoveAll(filepath.Join(dir, "b"))
		}
		return nil
	}
	require.NoError(t, giveTree(context.Background(), dir, os.Getuid()+1, os.Getgid(), removeSelf))
}

// A cancelled walk stops rather than going on changing owners.
func TestGiveTreeStopsWhenCancelled(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := giveTree(ctx, dir, os.Getuid()+1, os.Getgid(), func(*os.File) error { called = true; return nil })
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, called)
}

// A link put where a directory belongs is refused, and what it names keeps
// its mode, rather than root changing the mode of whatever the link names.
func TestMkdirExactNoFollowRefusesALink(t *testing.T) {
	elsewhere := t.TempDir()
	require.NoError(t, os.Chmod(elsewhere, 0o755))
	link := filepath.Join(t.TempDir(), "demo")
	require.NoError(t, os.Symlink(elsewhere, link))

	require.Error(t, MkdirExactNoFollow(link, 0o700))
	info, err := os.Stat(elsewhere)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "the link's target was changed")

	dir := filepath.Join(t.TempDir(), "run", "demo")
	require.NoError(t, MkdirExactNoFollow(dir, 0o750))
	info, err = os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o750), info.Mode().Perm())
}

// A directory someone else owns is refused, before its mode is changed.
func TestMkdirMineNoFollowRefusesAnotherUsersDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	require.NoError(t, MkdirMineNoFollow(dir, 0o750))
	require.NoError(t, os.Chmod(dir, 0o700))

	err := mkdirExactNoFollow(dir, 0o750, os.Geteuid()+1)
	require.ErrorContains(t, err, "belongs to uid")
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "a directory someone else owns had its mode changed")
}
