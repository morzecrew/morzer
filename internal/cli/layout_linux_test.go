package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The user's tree is the user's by any spelling that resolves to it, before it
// exists as well as after: a --config through a symlink's target can name a
// tree that is not there yet.
func TestTheUsersTreeIsKnownBeforeItExists(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root has no user tree")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	require.NoError(t, os.Mkdir(real, 0o750))
	require.NoError(t, os.Symlink(real, filepath.Join(dir, "data")))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))

	assert.True(t, isUserTree(filepath.Join(real, "morzer")), "the target's spelling of a tree not yet made")
	assert.True(t, isUserTree(filepath.Join(dir, "data", "morzer")))
	assert.False(t, isUserTree(filepath.Join(real, "other")))
	assert.False(t, isUserTree(""))

	// A data home that links to a directory not yet made, relatively.
	dangling := t.TempDir()
	require.NoError(t, os.Symlink("later", filepath.Join(dangling, "data")))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dangling, "data"))
	assert.True(t, isUserTree(filepath.Join(dangling, "later", "morzer")), "a dangling link's target")

	// A `..` in a link's target follows the links ahead of it: data is
	// alias/../later, and alias is real/nested, so data is real/later.
	dotdot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dotdot, "real", "nested"), 0o750))
	require.NoError(t, os.Symlink(filepath.Join("real", "nested"), filepath.Join(dotdot, "alias")))
	require.NoError(t, os.Symlink("alias/../later", filepath.Join(dotdot, "data")))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dotdot, "data"))
	assert.True(t, isUserTree(filepath.Join(dotdot, "real", "later", "morzer")), "where the kernel would make it")
	assert.False(t, isUserTree(filepath.Join(dotdot, "later", "morzer")), "where cleaning the target first puts it")

	// A trailing slash in a link's target does not stop it being followed:
	// data is alias/, and alias is later, not yet made.
	slashed := t.TempDir()
	require.NoError(t, os.Symlink("alias/", filepath.Join(slashed, "data")))
	require.NoError(t, os.Symlink("later", filepath.Join(slashed, "alias")))
	t.Setenv("XDG_DATA_HOME", filepath.Join(slashed, "data"))
	assert.True(t, isUserTree(filepath.Join(slashed, "later", "morzer")), "through a target with a trailing slash")

	// A loop ends, naming nothing the user's.
	loop := t.TempDir()
	require.NoError(t, os.Symlink("b", filepath.Join(loop, "a")))
	require.NoError(t, os.Symlink("a", filepath.Join(loop, "b")))
	t.Setenv("XDG_DATA_HOME", filepath.Join(loop, "a"))
	assert.False(t, isUserTree(filepath.Join(real, "morzer")))
}
