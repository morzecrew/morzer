package suite

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// warnings collects what an operation told the operator, so a test can assert
// on a message the operator actually receives rather than on the helper that
// composes it.
func warnings(t *testing.T, h *harness) *[]string {
	t.Helper()
	var seen []string
	h.Deps.Bus.SubscribeFunc(func(e events.Event) {
		if e.Level == events.LevelWarn {
			seen = append(seen, e.Message)
		}
	})
	return &seen
}

func contains(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// An update is how a vendor ships the next bundle, so it is where an operator
// meets a manifest still written the old way -- and the moment they can still
// decline it or ask for one written the new way. RFC 0023 decision 9 promised
// `runtime:` would be "deprecated"; until this wave that word appeared only in
// a document.
func TestAnUpdateWarnsAboutADeprecatedBundle(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	seen := warnings(t, h)
	src := stageUpgradeSource(t, h)

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err, "a deprecated field warns; it does not refuse")

	assert.True(t, contains(*seen, "`runtime` is deprecated"),
		"the operator must be told which field, got: %v", *seen)
	assert.True(t, contains(*seen, domain.FieldRemovalRelease),
		"and when it stops being read, or there is nothing to plan against: %v", *seen)
	assert.True(t, contains(*seen, "runtimes.compose"),
		"and what to ask the vendor for: %v", *seen)
}

// The operation still happens. A deprecation that blocked an update would make
// the manager the thing standing between an operator and their vendor's fix.
func TestADeprecatedBundleStillInstalls(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	src := stageUpgradeSource(t, h)
	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String())
}

// A plan is a question, and it is answered with the same warning the operation
// would give -- otherwise `--dry-run` is the one way to look at a bundle that
// hides what is wrong with it.
func TestAPlanCarriesTheSameWarning(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	seen := warnings(t, h)
	src := stageUpgradeSource(t, h)

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{
		Ref:     src,
		Options: ops.Options{DryRun: true},
	})
	require.NoError(t, err)

	assert.True(t, contains(*seen, "`runtime` is deprecated"),
		"a plan that hides the deprecation is a plan that misreports the bundle: %v", *seen)
}
