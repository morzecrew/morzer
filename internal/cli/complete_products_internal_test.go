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
	for _, c := range []struct{ root, config, manager string }{
		{"", "/etc/demo/installation.yaml", "/usr/local/bin/morzer"},
		{"/srv/m", "/srv/m/etc/demo/installation.yaml", "/usr/local/bin/morzer"},
		// The template writes --config only when given one.
		{"", "", "/usr/local/bin/morzer"},
		{"/", "", "/usr/local/bin/morzer"},
		// ManagerPath resolves symlinks, so the unit names whatever the
		// `morzer` on PATH points at: here, the test binary.
		{"", "/etc/demo/installation.yaml", systemd.ManagerPath()},
	} {
		units, err := systemd.BuildUnits(systemd.UnitParams{
			Product:     "demo",
			ManagerPath: c.manager,
			ConfigPath:  c.config,
		})
		require.NoError(t, err)

		dir := t.TempDir()
		for _, u := range units {
			require.NoError(t, os.WriteFile(filepath.Join(dir, u.Name), u.Contents, 0o644))
		}

		assert.Equal(t, []string{"demo"}, unitProducts(dir, c.root),
			"%+v: the service unit names the product once; its backup and update units name nothing", c)
	}
}

// The host's unit directory holds every service on the machine, and the
// manager's units for installations under every root.
func TestTheFallbackOffersOnlyTheUnitsItWroteForThisRoot(t *testing.T) {
	dir := t.TempDir()
	for name, execStart := range map[string]string{
		"nginx.service": "/usr/sbin/nginx -g 'daemon off;'",
		// Another tool's words in the same order.
		"other.service": "/usr/bin/other apply --startup now",
		// The manager's command line under another executable.
		"impostor.service": "/usr/bin/other apply --startup --plain --log-format json",
		// The manager's unit for one installation filed under another's name.
		"misfiled.service": "/usr/local/bin/morzer apply --startup --plain --log-format json --config /etc/sandbox/installation.yaml",
		// The manager's unit for an installation under /srv/m.
		"staged.service": "/usr/local/bin/morzer apply --startup --plain --log-format json --config /srv/m/etc/staged/installation.yaml",
		// No --config, so the production layout's.
		"plain.service": "/usr/local/bin/morzer apply --startup --plain --log-format json",
	} {
		body := "[Service]\nExecStart=" + execStart + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}

	assert.Equal(t, []string{"plain"}, unitProducts(dir, ""))
	assert.Equal(t, []string{"staged"}, unitProducts(dir, "/srv/m"))
}
