package atomicfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two directories swap places, each keeping its own contents.
func TestSwapDirsExchangesTwoDirectories(t *testing.T) {
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	require.NoError(t, os.MkdirAll(a, 0o755))
	require.NoError(t, os.MkdirAll(b, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(a, "from"), []byte("a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(b, "from"), []byte("b"), 0o600))

	require.NoError(t, SwapDirs(a, b))

	got, err := os.ReadFile(filepath.Join(a, "from"))
	require.NoError(t, err)
	assert.Equal(t, "b", string(got))
	got, err = os.ReadFile(filepath.Join(b, "from"))
	require.NoError(t, err)
	assert.Equal(t, "a", string(got))
}
