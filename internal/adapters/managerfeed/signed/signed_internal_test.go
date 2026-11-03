package signed

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A root shell with umask 077 must not install a manager other users cannot
// run: the mode is the one asked for, not the one the umask leaves.
func TestTheBinaryKeepsItsModeUnderAnyUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	src := filepath.Join(t.TempDir(), "morzer")
	require.NoError(t, os.WriteFile(src, []byte("#!/bin/sh\n"), 0o600))
	dst := filepath.Join(t.TempDir(), "staged")
	require.NoError(t, copyFile(src, dst, 0o755))

	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}
