package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
)

// TestTheFallbackRecognisesTheUnitsTheManagerWrites renders the real units
// rather than a hand-written copy: unitProducts matches serviceTemplate's
// argument shape, and a change to the template that this did not follow would
// leave sudo completion offering nothing, silently (#210).
func TestTheFallbackRecognisesTheUnitsTheManagerWrites(t *testing.T) {
	units, err := systemd.BuildUnits(systemd.UnitParams{
		Product:     "demo",
		ManagerPath: "/usr/local/bin/morzer",
		ConfigPath:  "/etc/demo/installation.yaml",
	})
	require.NoError(t, err)

	dir := t.TempDir()
	for _, u := range units {
		require.NoError(t, os.WriteFile(filepath.Join(dir, u.Name), u.Contents, 0o644))
	}

	assert.Equal(t, []string{"demo"}, unitProducts(dir),
		"the service unit names the product once; its backup and update units name nothing")
}
