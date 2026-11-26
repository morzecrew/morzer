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
}
