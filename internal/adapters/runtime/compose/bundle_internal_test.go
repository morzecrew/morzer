package compose

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReadBundleReportsWhatTheFilesDefineAndInterpolate reads the files the
// way `release verify` does, with no Docker: services from every file, and the
// variables in values only.
func TestReadBundleReportsWhatTheFilesDefineAndInterpolate(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0o600))
	}
	write("compose/compose.yaml", `services:
  app:
    # image: ${DEMO_IMAGE_OLD}
    image: ${DEMO_IMAGE_APP:-registry.example/app@sha256:1}
    command: ["sh", "-c", "echo $$HOME $DEMO_PARAM_PORT"]
    environment:
      URL: http://${DEMO_DOMAIN:-${DEMO_FALLBACK}}/
`)
	write("compose/db.yaml", "services:\n  db:\n    image: ${DEMO_IMAGE_DB}\n")

	got, err := (&Runtime{}).ReadBundle(root, []string{"compose/compose.yaml", "compose/db.yaml"})
	require.NoError(t, err)

	assert.Equal(t, []string{"app", "db"}, got.Services)
	assert.False(t, got.ServicesPartial)
	// Not DEMO_IMAGE_OLD, which is a comment, nor HOME, which `$$`
	// escapes; DEMO_FALLBACK, which a default nests.
	assert.Equal(t, []string{
		"DEMO_DOMAIN", "DEMO_FALLBACK", "DEMO_IMAGE_APP", "DEMO_IMAGE_DB", "DEMO_PARAM_PORT",
	}, got.Variables)
}

func TestReadBundleSaysWhenAFileIncludesServicesItDoesNotRead(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "compose.yaml"),
		[]byte("include:\n  - other.yaml\nservices:\n  app:\n    image: x\n"), 0o600))

	got, err := (&Runtime{}).ReadBundle(root, []string{"compose.yaml"})
	require.NoError(t, err)
	assert.Equal(t, []string{"app"}, got.Services)
	assert.True(t, got.ServicesPartial)
}
