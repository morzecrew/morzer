package suite

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// launchd does not run a schedule missed while the Mac was off, so the four
// scheduled runs systemd catches up stamp when they reach the engine, and the
// start at boot has the supervisor run each one overdue (RFC 0055 §5.6). A run
// that fires before a login defers to that rather than fail; where nothing
// catches it up, Linux, it reports as it always has (decisions 29 and 31).

// catchingUp is a supervisor that catches up as launchd's does, recording each
// time it is asked and whether the deployment lock was free then.
type catchingUp struct {
	*fakes.Supervisor
	h *harness

	calls    int
	lockFree bool
	ran      []string
	err      error
}

func (c *catchingUp) CatchUp(ctx context.Context, _ ports.UnitParams) ([]string, error) {
	c.calls++
	release, err := c.h.Deps.Locker.Acquire(ctx, "deployment", ports.LockOptions{})
	c.lockFree = err == nil
	if err == nil {
		_ = release()
	}
	return c.ran, c.err
}

func withCatchUp(h *harness) *catchingUp {
	c := &catchingUp{Supervisor: h.Supervisor, h: h, ran: []string{"dev.morzer.demo-backup"}}
	h.Deps.Supervisor = c
	return c
}

func stamped(h *harness, job string) bool {
	_, err := os.Stat(h.Paths.StampFile(job))
	return err == nil
}

// scheduledRuns are the four runs that stamp, as their jobs start them.
var scheduledRuns = []struct {
	job string
	run func(context.Context, *ops.Deps) error
}{
	{domain.StampBackup, func(ctx context.Context, d *ops.Deps) error {
		_, err := ops.Backup(ctx, d, ops.BackupOptions{Unattended: true})
		return err
	}},
	{domain.StampUpdate, func(ctx context.Context, d *ops.Deps) error {
		_, err := ops.RunUnattended(ctx, d, ops.UnattendedOptions{})
		return err
	}},
	{domain.StampFleet, func(ctx context.Context, d *ops.Deps) error {
		_, err := ops.FleetPublish(ctx, d, ops.FleetPublishOptions{})
		return err
	}},
	{domain.StampDoctor, func(ctx context.Context, d *ops.Deps) error {
		_, err := ops.DoctorUnattended(ctx, d)
		return err
	}},
}

func TestAScheduledRunBeforeTheEngineAnswersOnAMacDefersWithExitSeven(t *testing.T) {
	for _, s := range scheduledRuns {
		t.Run(s.job, func(t *testing.T) {
			ctx := context.Background()
			h := darwinInstalled(t)
			withCatchUp(h)
			notifier := &recordingNotifier{}
			h.Deps.Notifier = notifier
			h.Runtime.Fail["Status"] = errors.New("Cannot connect to the daemon at unix:///var/run/docker.sock")
			before := operations(t, h)

			err := s.run(ctx, h.Deps)

			require.Error(t, err)
			assert.Equal(t, domain.ExitRuntime, domain.ExitCode(err), err.Error())
			assert.Contains(t, err.Error(), "the engine does not answer yet")
			assert.Contains(t, domain.AsError(err).Hint, "the start at boot runs this once the engine answers")
			assert.False(t, stamped(h, s.job), "a run that did not reach the engine moved its stamp")
			assert.Equal(t, before, operations(t, h), "a deferred run journalled an operation")
			assert.Empty(t, notifier.kinds(), "a deferred run was reported")

			// Once the engine answers, the run stamps, whatever it then
			// makes of the installation.
			delete(h.Runtime.Fail, "Status")
			_ = s.run(ctx, h.Deps)
			assert.True(t, stamped(h, s.job), "a run that reached the engine left no stamp")
		})
	}
}

// Linux has nothing to catch a run up: dockerd down is a failure the run
// reports as it always has, and it stamps only once the engine answers.
func TestAScheduledRunWithNothingToCatchItUpReportsAsBefore(t *testing.T) {
	for _, s := range scheduledRuns {
		t.Run(s.job, func(t *testing.T) {
			ctx := context.Background()
			h := newHarness(t)
			h.install()
			h.setHookEnv()
			h.Deps.Supervisor = h.Supervisor
			h.Runtime.Fail["Status"] = errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock")

			err := s.run(ctx, h.Deps)

			if err != nil {
				assert.NotContains(t, domain.AsError(err).Hint, "the start at boot runs this",
					"a run with nothing to catch it up deferred")
			}
			assert.False(t, stamped(h, s.job), "a run that did not reach the engine moved its stamp")

			delete(h.Runtime.Fail, "Status")
			_ = s.run(ctx, h.Deps)
			assert.True(t, stamped(h, s.job), "Linux does not stamp, as decision 29 has it do")
		})
	}
}

// What each run reports on Linux with dockerd down, which decision 31 keeps:
// the backup journals and notifies its run, doctor notifies, and the fleet row
// says the runtime did not answer.
func TestOnLinuxADownEngineIsStillReported(t *testing.T) {
	ctx := context.Background()
	down := errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock")

	h := newHarness(t)
	h.install()
	h.setHookEnv()
	h.Deps.Supervisor = h.Supervisor
	notifier := &recordingNotifier{}
	h.Deps.Notifier = notifier
	h.Runtime.Fail["Status"] = down
	before := operations(t, h)
	_, _ = ops.Backup(ctx, h.Deps, ops.BackupOptions{Unattended: true})
	assert.Equal(t, before+1, operations(t, h), "the backup did not journal its run")
	assert.True(t, notified(notifier, events.KindOperationFinished, domain.OpTypeBackup),
		"the backup's outcome was not notified: %v", notifier.kinds())
	_, err := ops.DoctorUnattended(ctx, h.Deps)
	require.NoError(t, err)
	assert.Contains(t, notifier.kinds(), events.KindDoctorFinished)

	f := fleetHarness(t)
	inst, offsite := f.withFleetTarget(t)
	f.Deps.Supervisor = f.Supervisor
	f.Runtime.Fail["Status"] = down
	_, err = ops.FleetPublish(ctx, f.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)
	row, _ := publishedRow(t, offsite, inst)
	assert.NotEmpty(t, row.Health.Problem, "the row did not say the runtime did not answer")
}

// A dry run neither defers nor stamps: it changes nothing.
func TestAScheduledDryRunLeavesTheStampAlone(t *testing.T) {
	ctx := context.Background()
	h := darwinInstalled(t)
	withCatchUp(h)
	h.Runtime.Fail["Status"] = errors.New("Cannot connect to the daemon")

	_, err := ops.Backup(ctx, h.Deps, ops.BackupOptions{Unattended: true, Options: ops.Options{DryRun: true}})
	assert.NotContains(t, errString(err), "the engine does not answer yet")
	_, err = ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{TargetOptions: ops.TargetOptions{
		Options: ops.Options{DryRun: true}}})
	assert.NotContains(t, errString(err), "the engine does not answer yet")
	delete(h.Runtime.Fail, "Status")
	_, _ = ops.Backup(ctx, h.Deps, ops.BackupOptions{Unattended: true, Options: ops.Options{DryRun: true}})
	assert.False(t, stamped(h, domain.StampBackup), "a dry run stamped")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// The start at boot that converges has the supervisor catch up, once, after
// it released the lock; one that did not converge, or an operator's apply, does
// not.
func TestAStartAtBootThatConvergesCatchesUpWithTheLockFree(t *testing.T) {
	ctx := context.Background()
	h := darwinInstalled(t)
	c := withCatchUp(h)

	res, err := ops.Apply(ctx, h.Deps, ops.Options{Startup: true})

	require.NoError(t, err)
	assert.Equal(t, 1, c.calls)
	assert.True(t, c.lockFree, "the catch-up ran with the deployment lock held, so a backup would be refused")
	assert.Contains(t, res.Summary, "; caught up dev.morzer.demo-backup")

	for name, opts := range map[string]ops.Options{
		"an operator's apply": {},
		"a dry run":           {Startup: true, DryRun: true},
	} {
		_, err := ops.Apply(ctx, h.Deps, opts)
		require.NoError(t, err, name)
		assert.Equal(t, 1, c.calls, "%s caught up", name)
	}

	h.Runtime.Fail["Status"] = errors.New("Cannot connect to the daemon")
	_, err = ops.Apply(ctx, h.Deps, ops.Options{Startup: true})
	require.Error(t, err)
	assert.Equal(t, 1, c.calls, "a start at boot before the engine answers caught up")
}

// A start at boot that converged succeeds whatever the catch-up could not do:
// a failed exit would converge again in 30 seconds.
func TestACatchUpThatFailsLeavesTheStartAtBootSucceeded(t *testing.T) {
	ctx := context.Background()
	h := darwinInstalled(t)
	c := withCatchUp(h)
	c.ran, c.err = nil, domain.Internal(nil, "launchctl kickstart system/dev.morzer.demo-backup failed")

	res, err := ops.Apply(ctx, h.Deps, ops.Options{Startup: true})

	require.NoError(t, err)
	assert.Equal(t, 1, c.calls)
	assert.NotContains(t, res.Summary, "caught up")
}

// Neither a held start nor a stopped product converged, so neither catches up.
func TestAStartAtBootThatDidNotConvergeCatchesNothingUp(t *testing.T) {
	ctx := context.Background()
	h := darwinInstalled(t)
	c := withCatchUp(h)
	exitTwelve(t, h)
	res, err := ops.Apply(ctx, h.Deps, ops.Options{Startup: true})
	require.NoError(t, err)
	require.Contains(t, res.Summary, "held")
	assert.Zero(t, c.calls, "a held start at boot caught up")

	stopped := darwinInstalled(t)
	c = withCatchUp(stopped)
	_, err = ops.Apply(ctx, stopped.Deps, ops.Options{})
	require.NoError(t, err)
	_, err = ops.Stop(ctx, stopped.Deps, ops.StopOptions{})
	require.NoError(t, err)
	res, err = ops.Apply(ctx, stopped.Deps, ops.Options{Startup: true})
	require.NoError(t, err)
	require.Contains(t, res.Summary, "leaving it down")
	assert.Zero(t, c.calls, "a start at boot that left the product down caught up")
}

// A scheduled doctor sends the outbox before it asks the engine, which the
// outbox does not need: what failed to leave at boot leaves before a login.
func TestAScheduledDoctorBeforeTheEngineAnswersStillSendsTheOutbox(t *testing.T) {
	ctx := context.Background()
	h := darwinInstalled(t)
	target := newHook(t, http.StatusServiceUnavailable)
	configure(t, h, "chat")
	h.Deps.Notifier = ports.Notifiers{target.target(t, "chat", "")}
	finishAnOperation(t, h)
	require.Equal(t, 1, entries(t, h))
	target.answer(http.StatusNoContent)
	withCatchUp(h)
	h.Runtime.Fail["Status"] = errors.New("Cannot connect to the daemon")

	_, err := ops.DoctorUnattended(ctx, h.Deps)

	assert.Equal(t, domain.ExitRuntime, domain.ExitCode(err), "%v", err)
	assert.Zero(t, entries(t, h), "the outbox waited for the engine")
}

// notified is whether an event of that kind left for that operation.
func notified(n *recordingNotifier, kind events.Kind, op domain.OperationType) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, ev := range n.events {
		if ev.Kind == kind && ev.OpType == op {
			return true
		}
	}
	return false
}

// A stopped product's scheduled backup is skipped, exit 0, before the engine is
// asked: a Mac stopped on purpose with Docker Desktop quit is not a failure.
func TestAScheduledBackupOfAStoppedMacIsSkippedWhateverTheEngine(t *testing.T) {
	ctx := context.Background()
	h := darwinInstalled(t)
	withCatchUp(h)
	_, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	_, err = ops.Stop(ctx, h.Deps, ops.StopOptions{})
	require.NoError(t, err)
	h.Runtime.Fail["Status"] = errors.New("Cannot connect to the daemon")

	res, err := ops.Backup(ctx, h.Deps, ops.BackupOptions{Unattended: true})

	require.NoError(t, err)
	assert.Contains(t, res.Summary, "scheduled backup skipped")
	assert.True(t, stamped(h, domain.StampBackup), "the skipped run did not stamp, as the timer's run")
}

// An operation waiting for a person is what a tick reports, before the engine is
// asked, so the refusal names it rather than a login.
func TestATickWithAnOperationWaitingForAPersonSaysSoWhateverTheEngine(t *testing.T) {
	ctx := context.Background()
	h := darwinInstalled(t)
	withCatchUp(h)
	failingFirstUp(h)
	failingTeardown(h)
	_, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.Equal(t, domain.ExitManualIntervention, domain.ExitCode(err), "%v", err)
	h.Runtime.Fail["Status"] = errors.New("Cannot connect to the daemon")

	_, err = ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires manual intervention")
	assert.NotContains(t, err.Error(), "the engine does not answer yet")
	assert.True(t, stamped(h, domain.StampUpdate), "the refused tick did not stamp")
}

// The question to the engine reads the project first, so a project that will
// not load fails it with the engine up: that is the run's own failure, which
// stamps and is reported by the run, not deferred as the engine down.
func TestAProjectThatWillNotLoadIsNotTheEngineDown(t *testing.T) {
	for _, s := range scheduledRuns {
		t.Run(s.job, func(t *testing.T) {
			h := darwinInstalled(t)
			withCatchUp(h)
			broken := errors.New("env file /etc/demo/app.env not found")
			h.Runtime.Fail["Status"] = broken
			h.Runtime.Fail["Validate"] = broken

			err := s.run(context.Background(), h.Deps)

			if err != nil {
				assert.NotContains(t, err.Error(), "the engine does not answer yet")
			}
			assert.True(t, stamped(h, s.job), "a run that reached the engine's question left no stamp")
		})
	}
}

// An interrupted question to the engine is an interruption, exit 130, whether
// the start at boot or a scheduled run asked it, and whatever the supervisor.
func TestAnInterruptedQuestionToTheEngineExitsAsInterrupted(t *testing.T) {
	h := darwinInstalled(t)
	withCatchUp(h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.Runtime.Fail["Status"] = context.Canceled

	_, err := ops.Apply(ctx, h.Deps, ops.Options{Startup: true})
	assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(err), "%v", err)
	_, err = ops.Backup(ctx, h.Deps, ops.BackupOptions{Unattended: true})
	assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(err), "%v", err)
	h.Deps.Supervisor = h.Supervisor
	_, err = ops.Backup(ctx, h.Deps, ops.BackupOptions{Unattended: true})
	assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(err), "%v", err)
	assert.False(t, stamped(h, domain.StampBackup))
}

// A run interrupted while it reads the project has not reached the engine
// either, so it leaves its stamp: the next boot still catches it up.
func TestARunInterruptedWhileItReadsTheProjectLeavesItsStamp(t *testing.T) {
	h := darwinInstalled(t)
	withCatchUp(h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Runtime.Fail["Status"] = errors.New("Cannot connect to the daemon")
	h.Runtime.Fail["Validate"] = context.Canceled
	h.Runtime.OnCall = func(method string) {
		if method == "Validate" {
			cancel()
		}
	}

	_, err := ops.Backup(ctx, h.Deps, ops.BackupOptions{Unattended: true})

	assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(err), "%v", err)
	assert.False(t, stamped(h, domain.StampBackup), "an interrupted run moved its stamp")
}

// Interrupted after the engine's question failed, the run is an interruption
// all the same, not the engine down.
func TestARunInterruptedAfterTheEngineDidNotAnswerIsInterrupted(t *testing.T) {
	h := darwinInstalled(t)
	withCatchUp(h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Runtime.Fail["Status"] = errors.New("Cannot connect to the daemon")
	h.Runtime.OnCall = func(method string) {
		if method == "Validate" {
			cancel()
		}
	}

	_, err := ops.Backup(ctx, h.Deps, ops.BackupOptions{Unattended: true})

	assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(err), "%v", err)
	assert.False(t, stamped(h, domain.StampBackup))
}

// A read of the project that timed out says nothing about it, so the run is
// the engine down, as the question said.
func TestAProjectReadThatTimedOutSettlesNothing(t *testing.T) {
	h := darwinInstalled(t)
	withCatchUp(h)
	h.Runtime.Fail["Status"] = errors.New("Cannot connect to the daemon")
	h.Runtime.Fail["Validate"] = domain.RuntimeError(context.DeadlineExceeded, "docker compose config timed out after 1m0s")

	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{Unattended: true})

	assert.Equal(t, domain.ExitRuntime, domain.ExitCode(err), "%v", err)
	assert.Contains(t, errString(err), "the engine does not answer yet")
	assert.False(t, stamped(h, domain.StampBackup), "a run that may not have reached the engine moved its stamp")
}

// A scheduled run that cannot read the installation fails before the engine's
// question, and stamps, as every failure but the engine's does.
func TestAScheduledRunThatCannotReadTheInstallationStamps(t *testing.T) {
	for _, s := range scheduledRuns[:2] {
		t.Run(s.job, func(t *testing.T) {
			h := darwinInstalled(t)
			withCatchUp(h)
			require.NoError(t, os.WriteFile(h.Paths.InstallationState(), []byte("{ not json\n"), 0o600))

			err := s.run(context.Background(), h.Deps)

			require.Error(t, err)
			assert.NotContains(t, err.Error(), "the engine does not answer yet")
			assert.Contains(t, err.Error(), "installation")
			assert.True(t, stamped(h, s.job), "a run that failed on its own left its stamp")
		})
	}
}
