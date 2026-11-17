package preflight_test

import (
	"context"
	"net"
	"runtime"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
)

// probe runs network.ports on one entry.
func probe(t *testing.T, entry string, parameters ...string) events.CheckResult {
	t.Helper()
	r, err := domain.ParsePortEntry(entry)
	require.NoError(t, err)
	r.Parameters = parameters
	return preflight.Ports([]domain.PortRequirement{r}).Run(context.Background())
}

// hold binds network on address until the test ends, and returns the port.
func hold(t *testing.T, network, address string, reuse bool) int {
	t.Helper()
	var lc net.ListenConfig
	if reuse {
		lc.Control = func(_, _ string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			})
		}
	}
	if network[:3] == "udp" {
		conn, err := lc.ListenPacket(context.Background(), network, address)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		return conn.LocalAddr().(*net.UDPAddr).Port
	}
	ln, err := lc.Listen(context.Background(), network, address)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// RFC 0059 §5.3: each entry is bound on its own address and protocol, so a
// listener on one address leaves another free, and every address busy.
func TestAPortIsProbedOnTheAddressAndProtocolItNames(t *testing.T) {
	for _, protocol := range []string{"tcp", "udp"} {
		t.Run(protocol, func(t *testing.T) {
			p := strconv.Itoa(hold(t, protocol+"4", "127.0.0.1:0", false))

			assert.Equal(t, events.CheckFail, probe(t, "127.0.0.1:"+p+"/"+protocol).Status)
			if runtime.GOOS != "linux" {
				// BSD lets a wildcard bind with SO_REUSEADDR, which Go
				// sets on a TCP listener and the probe on UDP, beside a
				// specific one, and 127.0.0.2 is not a Mac's. A Mac is
				// RFC 0055's acceptance run's (RFC 0059 §8).
				t.Skip("the rest is Linux's socket rule")
			}
			res := probe(t, p+"/"+protocol)
			assert.Equal(t, events.CheckFail, res.Status, "every address includes 127.0.0.1")
			assert.Equal(t, "port "+p+"/"+protocol+" already in use", res.Message)
			assert.Equal(t, events.CheckOK, probe(t, "127.0.0.2:"+p+"/"+protocol).Status,
				"another address is free: 127.0.0.0/8 is all local on Linux")
		})
	}
}

// RFC 0059 decision 11: a UDP socket that allows sharing, as systemd-resolved's
// stub on 127.0.0.53 does, leaves the port free for every address, as Docker
// finds it. One that does not is in use.
func TestAUDPPortHeldBySomethingThatSharesIsFree(t *testing.T) {
	p := strconv.Itoa(hold(t, "udp4", "127.0.0.1:0", true))
	assert.Equal(t, events.CheckOK, probe(t, p+"/udp").Status)

	wildcard := strconv.Itoa(hold(t, "udp", ":0", false))
	assert.Equal(t, events.CheckFail, probe(t, wildcard+"/udp").Status)
}

// A TCP listener never makes the UDP entry on its port busy, nor the reverse.
func TestTCPAndUDPAreDifferentPorts(t *testing.T) {
	tcp := strconv.Itoa(hold(t, "tcp", ":0", false))
	assert.Equal(t, events.CheckFail, probe(t, tcp).Status)
	assert.Equal(t, events.CheckOK, probe(t, tcp+"/udp").Status)
}

// An address the machine lacks is the installation's setting, not someone's
// listener, and the hint names the parameter to change (RFC 0059 decision 3).
func TestAnAddressThisMachineLacksIsNotAPortInUse(t *testing.T) {
	res := probe(t, "192.0.2.1:443", "address")
	assert.Equal(t, events.CheckFail, res.Status)
	assert.Equal(t, "192.0.2.1 is not an address of this machine", res.Message)
	assert.Contains(t, res.Remedy, "set address to an address of this machine")

	assert.NotContains(t, probe(t, "192.0.2.1:443").Remedy, "set ", "a literal entry names no parameter")
}

// `[::]` binds IPv6 alone, as Docker 27 and later publish `::`, so an IPv4
// listener does not make it busy.
func TestAnIPv4ListenerLeavesTheIPv6WildcardFree(t *testing.T) {
	if ln, err := net.Listen("tcp6", "[::1]:0"); err != nil {
		t.Skip("this host has no IPv6")
	} else {
		_ = ln.Close()
	}
	p := strconv.Itoa(hold(t, "tcp4", "0.0.0.0:0", false))
	assert.Equal(t, events.CheckOK, probe(t, "[::]:"+p).Status)
	if runtime.GOOS == "linux" {
		assert.Equal(t, events.CheckFail, probe(t, p).Status, "every address includes IPv4")
	}
}
