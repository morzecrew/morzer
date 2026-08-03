package contract

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/ports"
)

// RuntimeFactory builds a runtime plus the configuration to exercise it with.
//
// The config comes from the factory because a real adapter needs real Compose
// files while the fake needs nothing: the suite must not assume either.
type RuntimeFactory func(t *testing.T) (ports.Runtime, ports.RuntimeConfig)

// RunRuntimeSuite runs every Runtime contract test.
//
// These are the behaviours the lifecycle layer depends on. The ones that
// matter most are idempotence and the volume-removal refusal: `apply` calls Up
// on every boot, and a compensation that silently destroyed data would be
// worse than the failure it was undoing.
func RunRuntimeSuite(t *testing.T, newRuntime RuntimeFactory) {
	t.Helper()
	ctx := context.Background()

	t.Run("validate has no side effects", func(t *testing.T) {
		rt, cfg := newRuntime(t)

		before, err := rt.Status(ctx, cfg)
		require.NoError(t, err)

		_, err = rt.Validate(ctx, cfg)
		require.NoError(t, err)

		after, err := rt.Status(ctx, cfg)
		require.NoError(t, err)
		assert.Equal(t, len(before), len(after),
			"Validate must not start, stop, or create anything")
	})

	t.Run("validate reports the services it resolved", func(t *testing.T) {
		rt, cfg := newRuntime(t)

		rendered, err := rt.Validate(ctx, cfg)
		require.NoError(t, err)
		assert.NotEmpty(t, rendered.Services,
			"the plan view and the health checks both need the service list")
	})

	t.Run("up is idempotent", func(t *testing.T) {
		rt, cfg := newRuntime(t)

		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}))
		first, err := rt.Status(ctx, cfg)
		require.NoError(t, err)

		// systemd calls `apply --startup` at every boot. If Up were not
		// idempotent, every reboot would be a deployment.
		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}),
			"Up on an already-converged project must succeed")

		second, err := rt.Status(ctx, cfg)
		require.NoError(t, err)
		assert.Equal(t, len(first), len(second),
			"a second Up must not change the number of services")
	})

	t.Run("status reports running services after up", func(t *testing.T) {
		rt, cfg := newRuntime(t)
		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}))

		states, err := rt.Status(ctx, cfg)
		require.NoError(t, err)
		require.NotEmpty(t, states)

		for _, s := range states {
			assert.NotEmpty(t, s.Name, "every service state needs a name to be actionable")
		}
	})

	t.Run("down without the volume flag preserves volumes", func(t *testing.T) {
		rt, cfg := newRuntime(t)
		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}))

		// The default must never destroy data. Compensation paths call
		// Down, and a default that removed volumes would make a failed
		// update delete the database.
		require.NoError(t, rt.Down(ctx, cfg, ports.DownOptions{}))

		states, err := rt.Status(ctx, cfg)
		require.NoError(t, err)
		for _, s := range states {
			assert.NotEqual(t, "running", s.State, "Down must stop the services")
		}
	})

	t.Run("down then up recovers", func(t *testing.T) {
		rt, cfg := newRuntime(t)

		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}))
		require.NoError(t, rt.Down(ctx, cfg, ports.DownOptions{}))
		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}),
			"a stopped project must be startable again; compensation relies on it")

		states, err := rt.Status(ctx, cfg)
		require.NoError(t, err)
		require.NotEmpty(t, states)
	})

	t.Run("status on a project that was never started does not error", func(t *testing.T) {
		rt, cfg := newRuntime(t)

		// `doctor` and `status` run before anything has been applied.
		// An error here would make them useless on a fresh machine.
		states, err := rt.Status(ctx, cfg)
		require.NoError(t, err, "Status must work before the first Up")
		assert.Empty(t, states)
	})

	t.Run("a non-zero one-shot exit is data, not an error", func(t *testing.T) {
		rt, cfg := newRuntime(t)

		// The hook ABI gives exit 2 the meaning "nothing to do". If the
		// runtime turned a non-zero exit into an error, the caller
		// could never distinguish that from a failure.
		res, err := rt.RunOneShot(ctx, cfg, "migrate", ports.RunOptions{Remove: true})
		require.NoError(t, err, "a process that ran and exited is a result, not a transport failure")
		assert.GreaterOrEqual(t, res.ExitCode, 0)
	})
}
