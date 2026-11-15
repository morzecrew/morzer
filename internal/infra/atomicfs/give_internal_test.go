package atomicfs

import (
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
	require.NoError(t, giveTree(dir, os.Getuid()+1, os.Getgid(), record))
	assert.ElementsMatch(t, []string{".", "once", "sub", "deep"}, given,
		"a link was followed, or a file with a second name was changed")

	given = nil
	require.NoError(t, giveTree(dir, os.Getuid(), os.Getgid(), record))
	assert.Empty(t, given, "an entry its owner already owns was changed")

	require.NoError(t, giveTree(filepath.Join(dir, "missing"), 0, 0, record))
}
