package ops

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
)

// A user installation's units stop at logout unless the account lingers, which
// logind records as a file named after the user.
func TestALingeringAccountIsTheOneWhoseUnitsOutliveALogout(t *testing.T) {
	u, err := user.Current()
	require.NoError(t, err)
	dir := t.TempDir()
	saved := lingerDir
	lingerDir = dir
	t.Cleanup(func() { lingerDir = saved })
	run := func() events.CheckResult { return (&Deps{}).checkLinger().Run(context.Background()) }

	result := run()
	assert.Equal(t, events.CheckWarn, result.Status)
	assert.Contains(t, result.Remedy, "loginctl enable-linger "+u.Username)

	require.NoError(t, os.WriteFile(filepath.Join(dir, u.Username), nil, 0o644)) //nolint:gosec // logind's own mode
	assert.Equal(t, events.CheckOK, run().Status)
}

// A rootless engine cannot bind a port below the sysctl without a privilege
// this cannot see, so such a port warns and names the lowest one.
func TestAPortBelowTheUnprivilegedStartWarns(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ip_unprivileged_port_start")
	saved := portStartFile
	portStartFile = file
	t.Cleanup(func() { portStartFile = saved })

	rel := domain.Release{Manifest: domain.Manifest{
		Runtimes:     domain.Runtimes{"compose": domain.RuntimeDecl{Files: []string{"compose.yaml"}}},
		Requirements: domain.Requirements{Ports: []domain.PortSpec{"443", "80", "18080"}},
	}}
	run := func(start string) events.CheckResult {
		require.NoError(t, os.WriteFile(file, []byte(start+"\n"), 0o600))
		return (&Deps{}).checkUnprivilegedPorts(domain.Installation{}, rel).Run(context.Background())
	}

	result := run("1024")
	assert.Equal(t, events.CheckWarn, result.Status)
	assert.Contains(t, result.Message, "443/tcp, 80/tcp")
	assert.Contains(t, result.Remedy, "ip_unprivileged_port_start=80")

	assert.Equal(t, events.CheckOK, run("80").Status)
}
