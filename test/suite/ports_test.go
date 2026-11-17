package suite

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// A UDP port only a profile publishes is required of an installation that
// selects the profile, and of no other (RFC 0059 §6).
func TestAPortOnlyAProfilePublishesIsRequiredOnlyWithIt(t *testing.T) {
	held, err := net.ListenPacket("udp", ":0")
	require.NoError(t, err)
	defer func() { _ = held.Close() }()
	addr, ok := held.LocalAddr().(*net.UDPAddr)
	require.True(t, ok)
	port := strconv.Itoa(addr.Port)

	h := newHarness(t)
	patchManifest(t, h.Release.Root, `ports: ["{{ .Parameters.http_port }}"]`,
		`ports: ["{{ .Parameters.http_port }}", '{{ if has "external-db" .Profiles }}`+port+`/udp{{ end }}']`)
	patchManifest(t, h.Release.Root, `min_manager_version: "0.0.0"`,
		`min_manager_version: "`+domain.PortsMinManagerVersion+`"`)
	rel, err := release.Load(h.Release.Root)
	require.NoError(t, err)
	h.Release = rel // so install records the patched bundle's digest
	h.install()
	h.setHookEnv()

	_, err = ops.Apply(context.Background(), h.Deps, ops.Options{Profiles: []string{"external-db"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "port "+port+"/udp already in use")
	assert.Empty(t, h.Runtime.UpOptions, "a refused port started nothing")

	_, err = ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err, "the installation's own profile publishes no UDP port")
}

// Reading what runs works whatever its selection: an installation whose
// profile this platform would not deploy is still probed for health, and its
// ports still claimed (RFC 0055 decision 3, RFC 0059 decision 6).
func TestAProfileThisPlatformWouldNotDeployIsStillRead(t *testing.T) {
	h := newHarness(t)
	madeForDarwin(t, h.Release.Root, "embedded")
	rel, err := release.Load(h.Release.Root)
	require.NoError(t, err)
	h.Release = rel // so install records the patched bundle's digest
	h.install()
	h.Deps.Platform = "darwin"
	h.Runtime.Services = map[string]ports.ServiceState{
		"app": {Name: "app", State: "running", Health: ports.HealthHealthy},
	}

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)
	health := findResult(t, report, "runtime.health")
	assert.Equal(t, "ok", health.Status, health.Message)
}
