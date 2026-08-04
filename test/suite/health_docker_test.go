//go:build docker

package suite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/health"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/dockerlab"
)

// "Healthy" is the claim `apply` waits for before calling an update
// successful, so what these probes report decides whether a deployment is kept
// or rolled back. The unit tests answer that against `httptest`; these answer
// it against a real web server and a real database port, which is the question
// RFC 0008 §5.4 actually asked.

func httpCheck(name, url string) ports.CheckSpec {
	return ports.CheckSpec{Check: domain.HealthCheck{
		Name: name, Type: domain.HealthHTTP, URL: url,
	}}
}

// TestHTTPProbeAgainstCaddy checks the prober against a server that was not
// written to make it pass.
func TestHTTPProbeAgainstCaddy(t *testing.T) {
	dockerlab.Require(t)

	caddy := dockerlab.Start(t, dockerlab.ImageCaddy, []int{80}, nil)
	addr := caddy.HostPort(t, 80)

	prober := health.NewHTTP()
	ctx := context.Background()

	// Wait for the server itself rather than sleeping: a fixed sleep tuned
	// to one machine is how a suite ends up only failing in CI.
	var res ports.HealthResult
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		res, err = prober.Check(ctx, httpCheck("web", "http://"+addr+"/"))
		require.NoError(t, err)
		if res.OK {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.True(t, res.OK, "Caddy never answered a probe: %s", res.Message)
	assert.Equal(t, "web", res.Name)
	assert.Greater(t, res.Duration, time.Duration(0),
		"the result carries no duration, which the status report prints")

	// A path that is not there. This is the shape of a misconfigured
	// health endpoint, and the commonest reason a correct deployment is
	// reported unhealthy.
	res, err := prober.Check(ctx, httpCheck("web", "http://"+addr+"/health"))
	require.NoError(t, err, "a 404 is a result, not a transport failure")
	assert.False(t, res.OK)
	assert.Contains(t, res.Message, "404",
		"the message must carry the status, or the operator cannot tell a "+
			"missing endpoint from a broken service")
}

// TestTCPProbeAgainstRedis is the check a database gets: something is
// listening and completing a handshake, which is all a TCP probe can honestly
// claim.
func TestTCPProbeAgainstRedis(t *testing.T) {
	dockerlab.Require(t)

	redis := dockerlab.Start(t, dockerlab.ImageRedis, []int{6379}, nil,
		"redis-server", "--save", "", "--appendonly", "no")
	addr := redis.HostPort(t, 6379)

	prober := health.NewTCP()
	spec := ports.CheckSpec{Check: domain.HealthCheck{
		Name: "cache", Type: domain.HealthTCP, Address: addr,
	}}

	var res ports.HealthResult
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		res, err = prober.Check(context.Background(), spec)
		require.NoError(t, err)
		if res.OK {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	require.True(t, res.OK, "a running Redis was reported unhealthy: %s", res.Message)
	assert.Equal(t, "accepting connections", res.Message)

	// And once it is gone, the same probe says so rather than hanging.
	_, err := redis.Exec(t, "redis-cli", "shutdown", "nosave")
	_ = err // shutdown drops the connection, so a non-zero exit is expected

	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		res, err = prober.Check(context.Background(), spec)
		require.NoError(t, err)
		if !res.OK {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	assert.False(t, res.OK, "a stopped service was still reported healthy")
	assert.Contains(t, res.Message, "refused")
}

// TestWaitReadyAgainstAServiceThatStartsSlowly is the case the whole polling
// design exists for, run against a container that really is not ready yet.
//
// A fake can only report the sequence it was given. This asserts that the
// waiter survives a service which refuses connections for several seconds and
// then starts answering -- the ordinary shape of a database boot.
func TestWaitReadyAgainstAServiceThatStartsSlowly(t *testing.T) {
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageBusybox)

	slow := dockerlab.Start(t, dockerlab.ImageBusybox, []int{8080}, nil,
		"sh", "-c",
		"sleep 5; mkdir -p /www; echo ready > /www/index.html; httpd -f -p 8080 -h /www")
	addr := slow.HostPort(t, 8080)

	waiter := health.NewWaiter(health.NewHTTP(), health.NewTCP()).
		WithInterval(500 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	results, err := waiter.WaitReady(ctx, []ports.CheckSpec{
		httpCheck("api", "http://"+addr+"/"),
	})
	require.NoError(t, err, "a service that took five seconds to start was "+
		"reported as never having come up")
	require.Len(t, results, 1)
	assert.True(t, results[0].OK)
	assert.Greater(t, results[0].Attempts, 1,
		"the service was not actually slow, so this test proves nothing about "+
			"polling -- raise the delay in the fixture")
}

// TestWaitReadyGivesUpOnAServiceThatNeverListens produces the message an
// operator reads after a failed update, from a real refused connection rather
// than a scripted one.
func TestWaitReadyGivesUpOnAServiceThatNeverListens(t *testing.T) {
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageBusybox)

	dead := dockerlab.Start(t, dockerlab.ImageBusybox, []int{8080}, nil,
		"sh", "-c", "while true; do sleep 1; done")
	addr := dead.HostPort(t, 8080)

	waiter := health.NewWaiter(health.NewHTTP()).WithInterval(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := waiter.WaitReady(ctx, []ports.CheckSpec{httpCheck("api", "http://"+addr+"/")})
	require.Error(t, err, "a service that never listened was reported healthy")

	de := domain.AsError(err)
	assert.Equal(t, domain.CodeHealth, de.Code,
		"the exit status is how a unit file tells this from a crash")
	assert.Contains(t, de.Message, "api", "the message must name the check that failed")
	assert.NotEmpty(t, de.Hint)
	assert.Less(t, len(de.Message), 200,
		"the summary is a line an operator reads, not a wrapped syscall trace: %q",
		de.Message)
	assert.NotContains(t, strings.ToLower(de.Message), "goroutine")
}
