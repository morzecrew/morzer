package suite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
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
	// The step's own failure, which is where its hint is shown: the
	// operation's error says only that it was rolled back.
	var failed *domain.Error
	h.Deps.Bus.SubscribeFunc(func(e events.Event) {
		if e.Kind == events.KindStepFinished && e.StepID == "render-configuration" && e.Err != nil {
			failed = e.Err
		}
	})

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), target+" is a directory")
	require.NotNil(t, failed, "render-configuration did not report its failure")
	assert.Contains(t, failed.Hint, "rmdir "+target)
}
