package contract

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/ports"
)

// HealthScenario is the state the waiter under test has to be driven into.
//
// The suite cannot construct specs itself: the real waiter answers to probers
// and the fake answers to fields, and forcing one shape on both would test the
// adaptor rather than the implementation. So each implementation is asked for a
// waiter *and* the specs that put it in the named state, and the assertions
// below are about what happens next -- which is the part that must not differ.
type HealthScenario int

const (
	// HealthPasses: every check passes on the first probe.
	HealthPasses HealthScenario = iota

	// HealthPassesEventually: the checks fail twice and pass on the third
	// probe. This is the case "polls until" exists for.
	HealthPassesEventually

	// HealthNeverPasses: no check ever passes, however long the waiter
	// waits.
	HealthNeverPasses
)

// HealthWaiterFactory builds a waiter already driven into the given scenario,
// with the specs to hand to it.
type HealthWaiterFactory func(t *testing.T, s HealthScenario) (ports.HealthWaiter, []ports.CheckSpec)

// RunHealthWaiterSuite runs every HealthWaiter contract test.
//
// The port's central promise is one sentence -- "polls until every check passes
// or the context expires" -- and it was kept by the real waiter and broken by
// the fake, which probed once and gave up in a microsecond. Every operation
// test that drove a product to "never healthy" was therefore proving something
// nothing in production does, and the fifteen-minute wait an operator actually
// sits through appeared in no test at all.
func RunHealthWaiterSuite(t *testing.T, newWaiter HealthWaiterFactory) {
	t.Helper()

	t.Run("WaitReady returns as soon as everything passes", func(t *testing.T) {
		w, specs := newWaiter(t, HealthPasses)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		started := time.Now()
		results, err := w.WaitReady(ctx, specs)
		require.NoError(t, err)
		require.Len(t, results, len(specs))
		for _, r := range results {
			assert.True(t, r.OK, "check %q reported not ok in the passing scenario", r.Name)
			assert.NotEmpty(t, r.Name, "a result with no name cannot be reported to anyone")
		}
		assert.Less(t, time.Since(started), 5*time.Second,
			"a waiter that has nothing to wait for must not wait")
	})

	t.Run("WaitReady keeps probing until the checks come up", func(t *testing.T) {
		w, specs := newWaiter(t, HealthPassesEventually)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		results, err := w.WaitReady(ctx, specs)
		require.NoError(t, err,
			"a service that comes up on the third probe must be waited for, not failed")
		for _, r := range results {
			assert.True(t, r.OK)
			assert.GreaterOrEqual(t, r.Attempts, 2,
				"check %q reports %d attempt(s), so a probe that eventually "+
					"succeeded is indistinguishable from one that succeeded at once",
				r.Name, r.Attempts)
		}
	})

	// The finding this suite was written for. A waiter that gives up early
	// turns "not ready yet" into "will never be ready", which on an update
	// is a compensated rollback of a deployment that was two seconds from
	// healthy.
	t.Run("WaitReady waits for the context rather than giving up", func(t *testing.T) {
		w, specs := newWaiter(t, HealthNeverPasses)

		const budget = 300 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()

		started := time.Now()
		_, err := w.WaitReady(ctx, specs)
		elapsed := time.Since(started)

		require.Error(t, err, "checks that never pass must not report ready")
		assert.GreaterOrEqual(t, elapsed, budget/2,
			"gave up after %s of a %s budget: the caller asked to wait and was not waited for",
			elapsed, budget)
		assert.Less(t, elapsed, 5*time.Second,
			"the deadline was overrun by %s", elapsed-budget)
	})

	t.Run("WaitReady names what never came up", func(t *testing.T) {
		w, specs := newWaiter(t, HealthNeverPasses)
		require.NotEmpty(t, specs, "the never-passes scenario needs at least one check")

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		_, err := w.WaitReady(ctx, specs)
		require.Error(t, err)
		// Without the name the operator's next step is reading logs to
		// learn something the manager already knew.
		assert.Contains(t, err.Error(), specs[0].Name(),
			"the refusal does not name the check that never passed")
	})

	t.Run("WaitReady on no checks returns immediately", func(t *testing.T) {
		w, _ := newWaiter(t, HealthPasses)

		results, err := w.WaitReady(context.Background(), nil)
		require.NoError(t, err, "a release with no health checks is not a failure")
		assert.Empty(t, results)
	})

	t.Run("a cancelled wait stops promptly", func(t *testing.T) {
		w, specs := newWaiter(t, HealthNeverPasses)

		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)

		started := time.Now()
		_, err := w.WaitReady(ctx, specs)
		require.Error(t, err, "a cancelled wait must not report ready")
		assert.Less(t, time.Since(started), 5*time.Second,
			"ctrl-c during a health wait has to be felt within the second, not the minute")
	})

	// CheckOnce is the other half, and the difference between the two is the
	// reason both exist: `status` and `doctor` report the current state, and
	// a diagnostic that blocks for two minutes waiting for health is a
	// diagnostic nobody runs.
	t.Run("CheckOnce reports the current state without waiting", func(t *testing.T) {
		w, specs := newWaiter(t, HealthNeverPasses)

		started := time.Now()
		results, err := w.CheckOnce(context.Background(), specs)
		require.NoError(t, err,
			"a failing check is a result with OK false, not an error -- an error "+
				"means the probe could not be run at all")
		require.Len(t, results, len(specs))
		for _, r := range results {
			assert.False(t, r.OK, "check %q passed in the never-passes scenario", r.Name)
		}
		assert.Less(t, time.Since(started), 2*time.Second,
			"CheckOnce waited for a better answer instead of reporting the current one")
	})

	t.Run("CheckOnce reports every check it was given", func(t *testing.T) {
		w, specs := newWaiter(t, HealthPasses)

		results, err := w.CheckOnce(context.Background(), specs)
		require.NoError(t, err)
		require.Len(t, results, len(specs),
			"a check that is dropped from the results is a check nobody can see failing")
		for i, r := range results {
			assert.Equal(t, specs[i].Name(), r.Name,
				"results must line up with the specs they came from")
		}
	})
}
