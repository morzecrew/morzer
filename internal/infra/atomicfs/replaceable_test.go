package atomicfs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

// A binary root's units run is one only root may replace: the file and every
// directory above it owned by root and writable by nobody else.
func TestReplaceableByNonRoot(t *testing.T) {
	const system = "/usr/bin/env"
	if _, err := os.Stat(system); err != nil {
		t.Skip("no /usr/bin/env to stand for a system binary")
	}

	t.Run("a system binary", func(t *testing.T) {
		who, err := atomicfs.ReplaceableByNonRoot(system)
		require.NoError(t, err)
		assert.Empty(t, who)
	})

	t.Run("a link to one, in a user's directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root owns every directory it creates")
		}
		dir := t.TempDir()
		link := filepath.Join(dir, "morzer")
		require.NoError(t, os.Symlink(system, link))
		who, err := atomicfs.ReplaceableByNonRoot(link)
		require.NoError(t, err)
		require.NotEmpty(t, who, "a link its user can repoint was judged by what it names")
		assert.True(t, strings.HasPrefix(link, strings.Fields(who)[0]), "%q names no entry on the way to %s", who, link)
	})

	t.Run("a link only root can repoint", func(t *testing.T) {
		info, err := os.Lstat("/bin")
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Skip("/bin is not a link on this machine")
		}
		who, err := atomicfs.ReplaceableByNonRoot("/bin/env")
		require.NoError(t, err)
		assert.Empty(t, who)
	})

	t.Run("a binary a user owns", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root owns every file it creates")
		}
		path := filepath.Join(t.TempDir(), "morzer")
		require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755)) //nolint:gosec // a stand-in binary
		who, err := atomicfs.ReplaceableByNonRoot(path)
		require.NoError(t, err)
		assert.Contains(t, who, "not root")
		assert.True(t, strings.HasPrefix(path, strings.Fields(who)[0]), "%q names no entry on the way to %s", who, path)
	})

	t.Run("a path that does not exist", func(t *testing.T) {
		_, err := atomicfs.ReplaceableByNonRoot(filepath.Join(filepath.Dir(system), "morzer-absent-"+filepath.Base(t.TempDir())))
		assert.Error(t, err)
	})
}
