package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

// What a repair replaces is under the name it is kept by from the moment it is
// swapped out. Under a staging name, a failure before it is renamed has the
// caller's cleanup remove it, and a crash leaves it to the next update's sweep
// of staging directories, while running containers still mount files from it.
func TestAReplacedReleaseIsKeptFromTheMomentItIsSwappedOut(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "1.2.0")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "installed"), nil, 0o644))
	staged, err := stageBeside(dest)
	require.NoError(t, err)

	var swappedOut string
	kept, err := placeReleaseWith(staged, dest, true, func(a, b string) error {
		swappedOut = a
		return atomicfs.SwapDirs(a, b)
	})

	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(filepath.Base(swappedOut), ".staging-debris-"),
		"the replaced release was swapped out to %s, a name a staging sweep removes", swappedOut)
	assert.Equal(t, swappedOut, kept)
	assert.FileExists(t, filepath.Join(kept, "installed"))
}

// An exchange that fails leaves the installed release where it is, and no
// fetched copy kept beside it as if it were one.
func TestAFailedExchangeKeepsNothingButTheInstalledRelease(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "1.2.0")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "installed"), nil, 0o644))
	staged, err := stageBeside(dest)
	require.NoError(t, err)

	_, err = placeReleaseWith(staged, dest, true, func(string, string) error { return os.ErrPermission })

	require.Error(t, err)
	assert.FileExists(t, filepath.Join(dest, "installed"))
	debris, err := filepath.Glob(filepath.Join(filepath.Dir(dest), ".staging-debris-*"))
	require.NoError(t, err)
	assert.Empty(t, debris, "a bundle that never replaced anything was kept as a replaced release")
}
