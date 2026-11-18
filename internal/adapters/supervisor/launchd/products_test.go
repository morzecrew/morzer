package launchd_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/supervisor/launchd"
	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// The jobs Units renders, rather than a hand-written copy, so a change to what
// the boot job runs that BootJobProducts did not follow would leave completion
// under sudo offering nothing.
func TestTheBootJobsTheManagerWroteNameTheirProducts(t *testing.T) {
	for _, root := range []string{"", "/srv/m"} {
		t.Run("root "+root, func(t *testing.T) {
			dir := t.TempDir()
			for product, config := range map[string]string{
				"demo":  domain.PathsUnder(root, "demo").InstallationFile(),
				"other": domain.PathsUnder("/elsewhere", "other").InstallationFile(),
			} {
				s := launchd.New(nil, domain.PathsUnder(root, product), launchd.WithDaemonDir(dir))
				p := every(t)
				p.Product, p.ConfigPath = product, config
				units, err := s.Units(p)
				require.NoError(t, err)
				for _, u := range units {
					require.NoError(t, os.WriteFile(filepath.Join(dir, u.Name+".plist"), u.Contents, 0o644))
				}
			}
			// Another tool's job under the prefix, and a plist launchd
			// reads that is not the XML Units writes.
			require.NoError(t, os.WriteFile(filepath.Join(dir, "dev.morzer.tool.plist"), []byte(
				"<plist><dict><key>ProgramArguments</key><array><string>/usr/bin/true</string></array></dict></plist>\n"),
				0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "dev.morzer.binary.plist"), []byte("bplist00\x00\x01"), 0o644))
			// The manager filed under a product's label, running something
			// other than the start at boot.
			require.NoError(t, os.WriteFile(filepath.Join(dir, "dev.morzer.extra.plist"), []byte(
				"<plist><dict><key>ProgramArguments</key><array>"+
					"<string>/usr/local/bin/morzer</string><string>status</string><string>--plain</string>"+
					"<string>--log-format</string><string>json</string><string>--verbose</string>"+
					"<string>--config</string><string>"+domain.PathsUnder(root, "extra").InstallationFile()+"</string>"+
					"</array></dict></plist>\n"), 0o644))

			assert.Equal(t, []string{"demo"}, launchd.BootJobProducts(dir, root),
				"only the boot job of the installation under this root names a product")
		})
	}
	assert.Nil(t, launchd.BootJobProducts(filepath.Join(t.TempDir(), "absent"), ""))
}

// A job written with no --config is the installation at the real root's.
func TestABootJobWithNoConfigIsTheRealRootsInstallation(t *testing.T) {
	dir := t.TempDir()
	s := launchd.New(nil, domain.PathsUnder("", "demo"), launchd.WithDaemonDir(dir))
	units, err := s.Units(ports.UnitParams{Product: "demo", ManagerPath: "/usr/local/bin/morzer"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, units[0].Name+".plist"), units[0].Contents, 0o644))

	assert.Equal(t, []string{"demo"}, launchd.BootJobProducts(dir, ""))
	assert.Empty(t, launchd.BootJobProducts(dir, "/srv/m"))
}

// A boot job names the binary a `morzer` link resolved to, a versioned file
// say, which is this manager when it is the one running.
func TestABootJobNamingTheResolvedManagerNamesItsProduct(t *testing.T) {
	dir := t.TempDir()
	s := launchd.New(nil, domain.PathsUnder("", "demo"), launchd.WithDaemonDir(dir))
	units, err := s.Units(ports.UnitParams{Product: "demo", ManagerPath: systemd.ManagerPath(),
		ConfigPath: domain.PathsUnder("", "demo").InstallationFile()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, units[0].Name+".plist"), units[0].Contents, 0o644))

	assert.Equal(t, []string{"demo"}, launchd.BootJobProducts(dir, ""))
}
