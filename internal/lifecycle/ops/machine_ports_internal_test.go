package ops

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// machine.ports reports a port two installations claim on overlapping
// addresses, and names each claim as written (RFC 0059 §5.4).
func TestMachinePortsNamesTheClaimsThatMeet(t *testing.T) {
	claims := func(pairs ...string) map[portKey][]portClaim {
		out := map[portKey][]portClaim{}
		for i := 0; i < len(pairs); i += 2 {
			r, err := domain.ParsePortEntry(pairs[i+1])
			require.NoError(t, err)
			key := portKey{r.Port, r.Protocol}
			out[key] = append(out[key], portClaim{pairs[i], r})
		}
		return out
	}

	assert.Equal(t, []string{"443/tcp (dotdash on 203.0.113.11, web on every address)"},
		portCollisions(claims("dotdash", "203.0.113.11:443", "web", "443")))
	assert.Empty(t, portCollisions(claims("dotdash", "203.0.113.11:443", "web", "203.0.113.10:443")),
		"two addresses never meet")
	assert.Empty(t, portCollisions(claims("dotdash", "53/udp", "web", "53")), "UDP never meets TCP")
	assert.Equal(t, []string{"53/udp (dns on 0.0.0.0, edge on 203.0.113.11)", "443/tcp (dotdash on every address, web on every address)"},
		portCollisions(claims("dotdash", "443", "web", "443",
			"dns", "0.0.0.0:53/udp", "edge", "203.0.113.11:53/udp", "solo", "[2001:db8::1]:53/udp")),
		"ordered by port, and only the claims that meet: 0.0.0.0 never meets an IPv6 address")
}
