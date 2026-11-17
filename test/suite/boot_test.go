package suite

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// A Mac's boot job is started again every 30 seconds until it succeeds, and
// launchd cannot be told that exit 12 ends that (RFC 0055 §5.6). These are the
// two things the manager does about it: an attempt before the engine answers
// exits 7 before the lock, and an exit 12 holds every later start at boot
// until an operator's run succeeds.

// darwinInstalled is an installation on a Mac of a release made for it.
func darwinInstalled(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	madeForDarwin(t, h.Release.Root)
	rel, err := release.Load(h.Release.Root)
	require.NoError(t, err)
	h.Release = rel
	h.install()
	h.setHookEnv()
	h.Deps.Platform = "darwin"
	return h
}

func operations(t *testing.T, h *harness) int {
	t.Helper()
	recs, err := h.Deps.State.Operations(context.Background(), ports.Filter{})
	require.NoError(t, err)
	return len(recs)
}

func TestAStartAtBootBeforeTheEngineAnswersExitsSevenAndJournalsNothing(t *testing.T) {
	ctx := context.Background()
	h := darwinInstalled(t)
	h.Runtime.Fail["Status"] = errors.New("Cannot connect to the daemon at unix:///var/run/docker.sock")
	before := operations(t, h)

	_, err := ops.Apply(ctx, h.Deps, ops.Options{Startup: true})

	require.Error(t, err)
	assert.Equal(t, domain.ExitRuntime, domain.ExitCode(err), err.Error())
	assert.Contains(t, err.Error(), "the engine does not answer yet")
	assert.Equal(t, before, operations(t, h), "an attempt before the engine answers journalled an operation")
	assert.Empty(t, h.Runtime.UpOptions)

	// An operator's apply is not boot's, and Linux has systemd order the
	// unit after the engine: neither asks first, and each journals its run.
	_, _ = ops.Apply(ctx, h.Deps, ops.Options{})
	assert.Equal(t, before+1, operations(t, h), "an operator's apply asked about the engine first")
	h.Deps.Platform = "linux"
	_, _ = ops.Apply(ctx, h.Deps, ops.Options{Startup: true})
	assert.Equal(t, before+2, operations(t, h), "a start at boot on Linux asked about the engine first")
}

// exitTwelve has a start at boot end needing a human: a first start whose
// containers cannot be taken down again. It leaves the operation put right
// by nobody who ran `apply`, with the containers up.
func exitTwelve(t *testing.T, h *harness) {
	t.Helper()
	ctx := context.Background()
	failingFirstUp(h)
	failingTeardown(h)
	res, err := ops.Apply(ctx, h.Deps, ops.Options{Startup: true})
	require.Equal(t, domain.ExitManualIntervention, domain.ExitCode(err), "%v", err)

	delete(h.Runtime.Fail, "Down")
	h.Runtime.OnCall = nil
	_, err = ops.ClearIntervention(ctx, h.Deps, res.Record.ID)
	require.NoError(t, err)
}

func TestAnExitTwelveAtBootHoldsEveryLaterStartUntilAnOperatorApplies(t *testing.T) {
	ctx := context.Background()
	h := darwinInstalled(t)
	h.Deps.Supervisor = h.Supervisor
	exitTwelve(t, h)

	// The next start at boot does nothing, and says why.
	calls := len(h.Runtime.Calls)
	res, err := ops.Apply(ctx, h.Deps, ops.Options{Startup: true})
	require.NoError(t, err, "a held start at boot must exit 0, or launchd starts it again")
	assert.Contains(t, res.Summary, "held since it ended needing a human (exit 12 at ")
	assert.Contains(t, res.Summary, "sudo morzer apply")
	assert.Len(t, h.Runtime.Calls, calls, "a held start at boot reached the engine")

	units := doctorCheck(t, h, "system.units")
	assert.Equal(t, events.CheckWarn, units.Status)
	assert.Contains(t, units.Message, "the start at boot is held (exit 12 at ")

	// An operator's dry run releases nothing; their apply does.
	_, err = ops.Apply(ctx, h.Deps, ops.Options{DryRun: true})
	require.NoError(t, err)
	res, err = ops.Apply(ctx, h.Deps, ops.Options{Startup: true})
	require.NoError(t, err)
	assert.Contains(t, res.Summary, "held")

	_, err = ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	calls = len(h.Runtime.Calls)
	res, err = ops.Apply(ctx, h.Deps, ops.Options{Startup: true})
	require.NoError(t, err)
	assert.NotContains(t, res.Summary, "held")
	assert.Greater(t, len(h.Runtime.Calls), calls, "the start at boot after the operator's apply did not converge")
	assert.NotContains(t, doctorCheck(t, h, "system.units").Message, "held")

	// systemd stops retrying on exit 12 itself, and Linux converges at
	// every boot.
	linux := darwinInstalled(t)
	linux.Deps.Platform = "linux"
	exitTwelve(t, linux)
	linux.Deps.Platform = "darwin"
	res, err = ops.Apply(ctx, linux.Deps, ops.Options{Startup: true})
	require.NoError(t, err)
	assert.NotContains(t, res.Summary, "held", "an exit 12 on Linux held a start at boot")
}

// A Mac's doctor reads its jobs as root, and says what root alone can read
// rather than calling the jobs missing.
func TestAMacsDoctorReadsItsJobsAsRoot(t *testing.T) {
	h := darwinInstalled(t)
	h.Deps.Supervisor = h.Supervisor
	h.Supervisor.Present = false

	mac := doctorCheck(t, h, "system.units")
	assert.Equal(t, events.CheckWarn, mac.Status)
	assert.Equal(t, "launchd's jobs can be read only as root", mac.Message)
	assert.Equal(t, "run `sudo morzer doctor`", mac.Remedy)

	h.Deps.Platform = "linux"
	linux := doctorCheck(t, h, "system.units")
	assert.Equal(t, events.CheckOK, linux.Status, linux.Message)

	// As root, with every job loaded, it says what a Mac does without.
	h.Supervisor.Present = true
	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(context.Background(), units, ports.EnableAll))
	h.Deps.Platform = "darwin"
	mac = doctorCheck(t, h, "system.units")
	assert.Equal(t, events.CheckOK, mac.Status, mac.Message)
	assert.Equal(t, "all jobs installed and enabled; each schedule runs at the time it names, with no spread", mac.Message)
	h.Deps.Platform = "linux"
	assert.Equal(t, "all units loaded", doctorCheck(t, h, "system.units").Message)
}

// operatorMeanwhile is a notifier that, once the start at boot has finished
// needing a human, has an operator put it right and apply before the start's
// own run returns: the window after its lock is released.
type operatorMeanwhile struct {
	t    *testing.T
	h    *harness
	done bool
}

func (n *operatorMeanwhile) Name() string { return "operator" }

func (n *operatorMeanwhile) Notify(ctx context.Context, ev events.Event) error {
	if n.done || ev.Kind != events.KindOperationFinished || ev.Status != string(domain.StatusManualIntervention) {
		return nil
	}
	n.done = true
	delete(n.h.Runtime.Fail, "Down")
	n.h.Runtime.OnCall = nil
	_, err := ops.ClearIntervention(ctx, n.h.Deps, ev.OpID)
	require.NoError(n.t, err)
	_, err = ops.Apply(ctx, n.h.Deps, ops.Options{})
	require.NoError(n.t, err)
	return nil
}

// The record is of the run the lock was held for: an operator's apply that
// succeeds after the start at boot released the lock is not undone by that
// start writing its record afterwards.
func TestAnOperatorsApplyAfterTheLockIsReleasedIsNotUndoneByTheRecord(t *testing.T) {
	ctx := context.Background()
	h := darwinInstalled(t)
	operator := &operatorMeanwhile{t: t, h: h}
	h.Deps.Notifier = operator
	failingFirstUp(h)
	failingTeardown(h)

	_, err := ops.Apply(ctx, h.Deps, ops.Options{Startup: true})
	require.Equal(t, domain.ExitManualIntervention, domain.ExitCode(err), "%v", err)
	require.True(t, operator.done, "the operator never ran")

	res, err := ops.Apply(ctx, h.Deps, ops.Options{Startup: true})
	require.NoError(t, err)
	assert.NotContains(t, res.Summary, "held", "the record of a run before the operator's apply held the boot after it")
}

// A schedule launchd has not loaded does not run until the next boot, so a
// job with its plist in place and enabled, but unloaded, is reported.
func TestAnUnloadedScheduleOnAMacIsReported(t *testing.T) {
	ctx := context.Background()
	h := darwinInstalled(t)
	h.Deps.Supervisor = h.Supervisor
	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableAll))
	state, err := h.Supervisor.Status(ctx, "demo-backup.timer")
	require.NoError(t, err)
	state.Active, state.Sub = "inactive", "unloaded"
	h.Supervisor.States["demo-backup.timer"] = state

	got := doctorCheck(t, h, "system.units")
	assert.Equal(t, events.CheckWarn, got.Status, got.Message)
	assert.Contains(t, got.Message, "demo-backup.timer: not loaded")
	assert.Contains(t, got.Remedy, "init --repair --install-units")
}
