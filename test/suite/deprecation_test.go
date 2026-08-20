package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// An update is how a vendor ships the next bundle, so it is where an operator
// meets a manifest still written the old way. Until 0.3.0 that produced a
// warning and the update went through; `runtime:` stopped being read in 0.3.0
// (RFC 0023 decision 23), so it is now a refusal.
//
// The refusal is the safe direction and the reason is not squeamishness: a
// manifest whose runtime declaration this manager cannot read is one it would
// otherwise install with no compose files at all.
func TestAnUpdateToADeprecatedBundleIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	src := legacyUpgradeSource(t, h)

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})

	require.Error(t, err, "a bundle written in a spelling this manager no longer reads must not install")
	assert.Contains(t, err.Error(), "is no longer read",
		"the operator cannot edit the bundle, so the error has to name what "+
			"their vendor must change: %v", err)

	// And the deployment is left where it was.
	current, cerr := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, cerr)
	assert.Equal(t, "1.2.0", current.Version.String(),
		"a refused update must not have moved the installation")
}

// The operation still happens for a bundle written the current way. This used
// to assert that a *deprecated* bundle still installed -- the claim that a
// deprecation must not stand between an operator and their vendor's fix --
// which stops being true the moment the field stops being read.
func TestACurrentBundleStillInstalls(t *testing.T) {
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

// A plan is a question, and it must be answered the same way the operation
// would answer it -- otherwise `--dry-run` is the one way to look at a bundle
// that hides what is wrong with it.
//
// This asserted a shared warning; it now asserts a shared refusal, which is a
// stronger claim about the same property.
func TestAPlanRefusesWhatTheUpdateWouldRefuse(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	src := legacyUpgradeSource(t, h)

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{
		Ref:     src,
		Options: ops.Options{DryRun: true},
	})

	require.Error(t, err, "a plan that hides the refusal misreports the bundle")
	assert.Contains(t, err.Error(), "is no longer read")
}

// legacyUpgradeSource is the 1.3.0 bundle rewritten into the spelling this
// manager no longer reads.
//
// It has to be built rather than checked in: every fixture in the tree moved to
// `runtimes:` when the old block stopped being read, so a test for the rejected
// input is the only thing left that still needs one.
func legacyUpgradeSource(t *testing.T, h *harness) string {
	t.Helper()
	src := stageUpgradeSource(t, h)

	manifest := filepath.Join(src, "manifest.yaml")
	data, err := os.ReadFile(manifest)
	require.NoError(t, err)

	// The whole block, profiles included: replacing a prefix of it leaves
	// the remainder at the wrong indentation and the manifest fails to
	// parse, which is a different refusal than the one under test.
	const current = `runtimes:
  compose:
    options:
      project: demo
    files:
      - compose/compose.yaml
    profiles:
      embedded: [compose/compose.embedded.yaml]
      external-db: [compose/compose.external-db.yaml]`
	const legacy = `runtime:
  project: demo
  files:
    - compose/compose.yaml
  profiles:
    embedded: [compose/compose.embedded.yaml]
    external-db: [compose/compose.external-db.yaml]`
	rewritten := strings.Replace(string(data), current, legacy, 1)
	require.NotEqual(t, string(data), rewritten,
		"the 1.3.0 fixture no longer carries the block this helper rewrites")
	require.NoError(t, os.WriteFile(manifest, []byte(rewritten), 0o644))
	return src
}
