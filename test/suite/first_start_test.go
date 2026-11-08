package suite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// A first start that fails: `up` created the containers and they never became
// healthy, and the rollback removes the rendered files they mount (#300).

// failingFirstUp makes the next `up` create the services and then fail.
func failingFirstUp(h *harness) {
	h.Runtime.Fail["Up"] = domain.RuntimeError(nil, "injected: xray never became healthy")
	h.Runtime.OnCall = func(method string) {
		if method != "Up" {
			return
		}
		h.Runtime.Services["app"] = ports.ServiceState{Name: "app", State: "running", Health: ports.HealthUnhealthy}
		delete(h.Runtime.Fail, "Up")
		h.Runtime.OnCall = nil
	}
}

// The containers it created are taken down with it. Left, they restart over
// mounts the rollback removed, and Docker makes a directory of each missing
// file, which every later apply then trips on.
func TestAFailedFirstStartTakesDownWhatItCreated(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	failingFirstUp(h)
	calls := len(h.Runtime.Calls)

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})

	require.Error(t, err)
	assert.Contains(t, h.callsSince(calls), "Down", "the containers the first start created were left")
	assert.NotContains(t, h.callsSince(calls), "Down(volumes)", "the rollback removed the volumes")
}

// withAMigrationService makes the release's migration a one-shot whose start
// brings up a dependency, as `compose run` does, before start-services runs.
func withAMigrationService(t *testing.T, h *harness) {
	t.Helper()
	withMigrateSteps(t, h.Release.Root, serviceMigration, nil)
	rel, err := release.Load(h.Release.Root)
	require.NoError(t, err)
	h.Release = rel // so install records the patched bundle's digest
	h.Runtime.OnCall = func(method string) {
		if method == "RunOneShot" {
			h.Runtime.Services["db"] = ports.ServiceState{Name: "db", State: "running"}
		}
	}
}

// What a migration started is part of the first start: start-services, which
// then finds a container, would otherwise take it for one that was there.
func TestAFirstStartAfterAMigrationServiceTakesDownWhatItCreated(t *testing.T) {
	h := newHarness(t)
	withAMigrationService(t, h)
	h.install()
	h.setHookEnv()
	h.Runtime.Fail["Up"] = domain.RuntimeError(nil, "injected: xray never became healthy")
	calls := len(h.Runtime.Calls)

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})

	require.Error(t, err)
	assert.Contains(t, h.callsSince(calls), "Down", "the dependency the migration started was left")
}

// A migration that fails after its one-shot started a dependency leaves it
// running over the files the rollback removes, as a failed start did.
func TestAFailedFirstMigrationTakesDownWhatItStarted(t *testing.T) {
	h := newHarness(t)
	withAMigrationService(t, h)
	h.install()
	h.setHookEnv()
	h.Runtime.OneShotResults = map[string]ports.ExitResult{"app": {ExitCode: 3}}
	calls := len(h.Runtime.Calls)

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})

	require.Error(t, err)
	assert.Contains(t, h.callsSince(calls), "Down", "the dependency the migration started was left")
}

// Over containers that already existed, the rollback leaves them, as before:
// they are what was there.
func TestAFailedStartOverExistingContainersLeavesThem(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	failingFirstUp(h)
	calls := len(h.Runtime.Calls)

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})

	require.Error(t, err)
	assert.NotContains(t, h.callsSince(calls), "Down")
}

// A start that cannot read what the project has starts nothing. Started
// anyway, it could not say whether it was the first, and a failed `up` would
// leave what it created over the files the rollback removes.
func TestAStartThatCannotReadTheProjectStartsNothing(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	calls := len(h.Runtime.Calls)
	h.Runtime.OnCall = func(method string) {
		if method == "Pull" {
			h.Runtime.Fail["Status"] = domain.RuntimeError(nil, "injected: the daemon stopped answering")
		}
	}

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})

	require.Error(t, err)
	assert.NotContains(t, h.callsSince(calls), "Up", "a start that could not tell whether it was the first went ahead")
}

// A directory where a configuration file belongs is what Docker leaves for a
// bind mount whose file was missing, and the render says so.
func TestARenderOverADirectorySaysWhatItIs(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	target := filepath.Join(h.Root, "etc", "demo", "application.yaml")
	require.NoError(t, os.MkdirAll(target, 0o755))

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), target+" is a directory")
	// On the operation's error, which a rollback wraps: it is what the
	// operator reads last, and what JSON output and `history` keep.
	assert.Contains(t, domain.AsError(err).Hint, "rmdir "+target)
}

// failingTeardown makes every `down` after the next `up` fail.
func failingTeardown(h *harness) {
	up := h.Runtime.OnCall
	h.Runtime.OnCall = func(method string) {
		if up != nil {
			up(method)
		}
		if method == "Up" {
			h.Runtime.Fail["Down"] = domain.RuntimeError(nil, "injected: the daemon stopped answering")
		}
	}
}

// An update over a project with no containers is a first start too. When its
// teardown fails, the files the update replaced still go back, and the
// previous release is not brought up over the ones that could not.
func TestAFirstStartThatCannotBeTakenDownBringsNothingBack(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	config := filepath.Join(h.Root, "etc", "demo", "application.yaml")
	before, err := os.ReadFile(config)
	require.NoError(t, err)
	clear(h.Runtime.Services)
	h.Health.FailingWaits = 1
	failingTeardown(h)
	calls := len(h.Runtime.Calls)

	_, err = ops.Update(context.Background(), h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})

	require.Error(t, err)
	ups := 0
	for _, call := range h.callsSince(calls) {
		if call == "Up" {
			ups++
		}
	}
	assert.Equal(t, 1, ups, "the previous release was brought up over files the rollback did not put back")
	after, err := os.ReadFile(config)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the file the update replaced was not put back")
}

// What a failed teardown leaves running restarts over a file the rollback
// removes, and Docker makes a directory of it (#300), so the files the render
// created stay.
func TestAFirstStartThatCannotBeTakenDownKeepsTheFilesItMounts(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	failingFirstUp(h)
	failingTeardown(h)

	result, err := ops.Apply(context.Background(), h.Deps, ops.Options{})

	require.Error(t, err)
	assert.FileExists(t, filepath.Join(h.Root, "etc", "demo", "application.yaml"))
	// Not rolled back: what it started is still there.
	assert.Equal(t, domain.StatusManualIntervention, result.Record.Status)
}
