package suite

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// `morzer restart <service>` (RFC 0050 P1).

func restartHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	h.Runtime.ValidateResult = ports.Rendered{Services: []string{"app", "db"}}
	return h
}

func restart(h *harness, services ...string) (ops.Result, error) {
	return ops.Restart(context.Background(), h.Deps, ops.RestartOptions{Services: services})
}

func restartRecords(t *testing.T, h *harness) []domain.OperationRecord {
	t.Helper()
	recs, err := h.Deps.State.Operations(context.Background(), ports.Filter{})
	require.NoError(t, err)
	var out []domain.OperationRecord
	for _, rec := range recs {
		if rec.Type == domain.OpTypeRestart {
			out = append(out, rec)
		}
	}
	return out
}

// The named services are re-created, forced, from the installed release, and
// the restart is journalled as one.
func TestRestartRecreatesTheNamedServices(t *testing.T) {
	h := restartHarness(t)
	before := len(h.Runtime.UpOptions)

	res, err := restart(h, "app", "app")
	require.NoError(t, err)
	require.Len(t, h.Runtime.UpOptions, before+1)
	up := h.Runtime.UpOptions[before]
	assert.Equal(t, []string{"app"}, up.Services, "a name given twice was restarted twice")
	assert.True(t, up.ForceRecreate, "a plain up re-creates nothing whose definition is unchanged")
	assert.Equal(t, "re-created app from 1.2.0", res.Summary)

	recs := restartRecords(t, h)
	require.Len(t, recs, 1)
	assert.Equal(t, domain.StatusSucceeded, recs[0].Status)
}

// A name the release does not start is refused before anything runs: `up`
// would start a service behind an inactive profile as a side effect.
func TestRestartRefusesAServiceTheReleaseDoesNotStart(t *testing.T) {
	h := restartHarness(t)
	before := len(h.Runtime.UpOptions)

	_, err := restart(h, "app", "migrate")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"migrate"`)
	assert.Contains(t, domain.AsError(err).Hint, "app, db")
	assert.Len(t, h.Runtime.UpOptions, before)
	assert.Empty(t, restartRecords(t, h))

	_, err = restart(h)
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "morzer stop")
}

// A stopped product stays down: a restart would bring a service back up
// behind the operator's back.
func TestRestartIsRefusedWhileStopped(t *testing.T) {
	h := restartHarness(t)
	_, err := ops.Stop(context.Background(), h.Deps, ops.StopOptions{})
	require.NoError(t, err)
	before := len(h.Runtime.UpOptions)

	_, err = restart(h, "app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
	assert.Len(t, h.Runtime.UpOptions, before)
}

// It changes the deployment, so it waits for nobody's lock.
func TestRestartTakesTheDeploymentLock(t *testing.T) {
	h := restartHarness(t)
	release, err := h.Locker.Acquire(context.Background(), "deployment", ports.LockOptions{})
	require.NoError(t, err)
	defer func() { _ = release() }()

	_, err = restart(h, "app")
	require.Error(t, err)
	assert.Empty(t, restartRecords(t, h))
}

// A service that does not come back healthy fails the restart. Nothing was put
// back, so the record says failed, not compensated.
func TestARestartThatFailsItsHealthChecksIsFailed(t *testing.T) {
	h := restartHarness(t)
	h.Health.Healthy = false

	_, err := restart(h, "app")
	require.Error(t, err)
	recs := restartRecords(t, h)
	require.Len(t, recs, 1)
	assert.Equal(t, domain.StatusFailed, recs[0].Status)
}

// A restart moves neither a version nor a configuration, so it files no
// attestation (RFC 0050 decision 3).
func TestARestartIsNotAttested(t *testing.T) {
	h := restartHarness(t)
	before, err := os.ReadDir(h.Paths.AttestationsDir())
	require.NoError(t, err)

	_, err = restart(h, "app")
	require.NoError(t, err)
	after, err := os.ReadDir(h.Paths.AttestationsDir())
	require.NoError(t, err)
	assert.Len(t, after, len(before))
}
