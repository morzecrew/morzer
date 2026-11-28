package quadlet

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// Podman before 5.3 cannot run what this adapter installs: its Quadlet reads
// no drop-in before 5.0, and before 5.3 a rootless one does not read the
// runtime directory and refuses a variable in PublishPort=.
func TestPodmanIsHeldToItsFloor(t *testing.T) {
	floor, ok := New(nil).ToolFloors()["podman"]
	require.True(t, ok, "podman has no floor")
	require.False(t, floor.IsZero(), "the floor did not parse")
	assert.Contains(t, New(nil).RequiredTools(), "podman", "a floor on a tool the runtime does not require")

	for version, allowed := range map[string]bool{
		"4.9.3": false, "5.0.0": false, "5.2.5": false, "5.3.0": true, "5.7.0": true, "6.1.1": true,
	} {
		v, err := domain.ParseVersion(version)
		require.NoError(t, err)
		assert.Equal(t, allowed, floor.Allows(v), "Podman %s", version)
	}
}
