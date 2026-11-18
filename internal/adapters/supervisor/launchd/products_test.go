package launchd_test

import (
	"os"
	"path/filepath"
	"strings"
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
			// Each differs from a boot job for this root in one way only.
			bootArgs := func(product string) []string {
				return []string{"/usr/local/bin/morzer", "apply", "--startup", "--plain", "--log-format", "json",
					"--config", domain.PathsUnder(root, product).InstallationFile()}
			}
			tool := bootArgs("tool")
			tool[0] = "/usr/bin/true"
			extra := bootArgs("extra")
			extra[1] = "status"
			for name, contents := range map[string]string{
				"tool":     plistOf(tool, ""),
				"extra":    plistOf(extra, ""),
				"override": plistOf(bootArgs("override"), "<key>Program</key><string>/usr/bin/true</string>"),
				"cut":      strings.TrimSuffix(plistOf(bootArgs("cut"), ""), "</dict></plist>\n"),
				"binary":   "bplist00\x00\x01",
			} {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "dev.morzer."+name+".plist"), []byte(contents), 0o644))
			}
			// The fixture itself is a boot job when nothing is changed.
			require.NoError(t, os.WriteFile(filepath.Join(dir, "dev.morzer.whole.plist"),
				[]byte(plistOf(bootArgs("whole"), "")), 0o644))

			assert.ElementsMatch(t, []string{"demo", "whole"}, launchd.BootJobProducts(dir, root),
				"only a boot job of an installation under this root names a product")
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

// plistOf is a job whose ProgramArguments are args, with extra beside them.
func plistOf(args []string, extra string) string {
	var b strings.Builder
	b.WriteString("<plist><dict>" + extra + "<key>ProgramArguments</key><array>")
	for _, a := range args {
		b.WriteString("<string>" + a + "</string>")
	}
	b.WriteString("</array></dict></plist>\n")
	return b.String()
}
