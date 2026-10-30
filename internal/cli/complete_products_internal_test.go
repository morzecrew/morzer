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
	// With and without --config: the template writes it only when given one.
	for _, config := range []string{"/etc/demo/installation.yaml", ""} {
		units, err := systemd.BuildUnits(systemd.UnitParams{
			Product:     "demo",
			ManagerPath: "/usr/local/bin/morzer",
			ConfigPath:  config,
		})
		require.NoError(t, err)

		dir := t.TempDir()
		for _, u := range units {
			require.NoError(t, os.WriteFile(filepath.Join(dir, u.Name), u.Contents, 0o644))
		}

		assert.Equal(t, []string{"demo"}, unitProducts(dir),
			"config %q: the service unit names the product once; its backup and update units name nothing", config)
	}
}
