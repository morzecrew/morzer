package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An edit session is root's, beside the secrets directory and not in it: on a
// Mac that directory belongs to the user who runs Docker Desktop, who could
// otherwise swap a session for a link and redirect what root writes (RFC 0055).
func TestAnEditSessionIsOutsideTheSecretsDirectory(t *testing.T) {
	app, _ := restartApp(t)
	dir, path, err := app.newEditSession(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	assert.Equal(t, app.Deps.Paths.EditDir(), filepath.Dir(dir))
	assert.False(t, strings.HasPrefix(path, app.Deps.Paths.SecretsRenderDir()+string(filepath.Separator)))
	info, err := os.Stat(app.Deps.Paths.EditDir())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}
