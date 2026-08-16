package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// The hazard this wave exists for: a release that changes what the runtime
// names things by. Under compose the project is the prefix on every volume,
// network and container, so a changed one deploys against storage nothing has
// ever written to and leaves the real data on the disk, unreferenced.
//
// Nothing else in the manager would notice. The backup taken afterwards
// captures the new empty volumes and `doctor` reports them covered, so the
// refusal is the only thing standing between a vendor's typo and an operator's
// data.

// recordProject writes what this installation was created with.
//
// The disagreement is driven from the state side rather than by editing the
// release, and the reason is worth keeping: a release directory edited in place
// fails the digest check first -- "the release directory has been modified" --
// which is a different guard doing its own job. A vendor renames a project by
// publishing a new bundle, and by the time that bundle is installed the pair
// disagrees exactly as it does here.
func recordProject(t *testing.T, h *harness, project string) {
	t.Helper()

	inst, err := h.Deps.State.LoadInstallation(context.Background())
	require.NoError(t, err)
	inst.RuntimeOptions = map[string]string{"project": project}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
}

func TestAReleaseThatRenamesTheProjectIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	h.install()
	// This machine's volumes are all prefixed `was-myapp_`; the release now
	// declares `demo`.
	recordProject(t, h, "was-myapp")

	_, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.Error(t, err, "a release that renames the project must not be applied silently")

	assert.Contains(t, err.Error(), "project")
	// The exit code an operator's script can act on, and the same one a
	// state file from a newer manager produces: this release and this
	// installation cannot be put together.
	assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err))
	// The way out, in the error rather than in a doc nobody has open.
	assert.Contains(t, domain.AsError(err).Hint, "restore")
}

// The refusal is what the operator meets, so it has to arrive before anything
// is done rather than after the deployment has been reconfigured.
func TestARenamedProjectIsRefusedBeforeTheRuntimeIsTouched(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	h.install()
	recordProject(t, h, "was-myapp")

	_, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.Error(t, err)

	assert.Empty(t, h.Runtime.Calls, "nothing may reach the runtime once the options disagree")
}

// Every installation that existed before schema 10 records nothing at all.
// Refusing those would refuse every machine upgrading to this version, so the
// first operation that knows both halves writes down what it is running under.
func TestAnInstallationFromBeforeTheFieldAdoptsWhatItIsRunning(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	inst := h.install()
	require.Nil(t, inst.RuntimeOptions, "the fixture stands in for a machine created before schema 10")

	_, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)

	after, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	require.NotNil(t, after.RuntimeOptions, "the baseline must exist after the first apply")
	assert.Equal(t, "demo", after.RuntimeOptions["project"],
		"what it adopted must be what it is running, never what a later release proposes")

	// And having adopted, it is protected: a release declaring a different
	// project is now refused where a moment ago there was nothing to
	// compare against.
	recordProject(t, h, "was-myapp")
	_, err = ops.Apply(ctx, h.Deps, ops.Options{})
	require.Error(t, err)
}

// A dry run answers a question and changes nothing -- including the baseline,
// which is state.
func TestAPlanDoesNotAdoptTheBaseline(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()

	_, err := ops.Apply(ctx, h.Deps, ops.Options{DryRun: true})
	require.NoError(t, err)

	after, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Nil(t, after.RuntimeOptions, "a plan must not write what an apply would")
}

// The hazard as a vendor actually ships it: a new bundle whose project differs
// from the running one. This is the path the whole wave exists for -- and the
// one where adopting from the wrong release would defeat the check, since the
// candidate is the thing being refused.
//
// The installation deliberately starts with no recorded options, which is every
// machine that existed before schema 10: it has to adopt from what it is
// running before the candidate is examined, or there is nothing to compare.
func TestAnUpdateThatRenamesTheProjectIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	inst := h.install()
	require.Nil(t, inst.RuntimeOptions)
	h.setHookEnv()

	src := stageUpgradeSource(t, h)
	renameProjectIn(t, src, "renamed")

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err, "an update that renames the project must be refused, not applied")
	assert.Contains(t, err.Error(), "project")
	assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err))

	// A refused operation writes nothing, including the baseline it derived
	// to refuse with. The derivation is in memory; only an operation that
	// reaches its lock records it.
	after, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Nil(t, after.RuntimeOptions, "a refusal before the lock must leave state untouched")

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", current.Version.String(), "nothing may have moved")
}

// An update that keeps the project goes through, so the refusal is not simply
// "updates are refused".
func TestAnUpdateThatKeepsTheProjectIsApplied(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	src := stageUpgradeSource(t, h)
	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String())
}

// renameProjectIn rewrites a staged bundle's project, the way a vendor
// publishing the next release would.
func renameProjectIn(t *testing.T, bundleRoot, project string) {
	t.Helper()

	path := filepath.Join(bundleRoot, "manifest.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	rewritten := strings.Replace(string(data), "  project: demo\n", "  project: "+project+"\n", 1)
	require.NotEqual(t, string(data), rewritten, "the fixture must carry a project line to rewrite")
	require.NoError(t, os.WriteFile(path, []byte(rewritten), 0o644))
}

// The baseline for an installation created before schema 10 comes from the
// release it is running, so an unreadable one leaves the comparison with
// nothing to compare. That case used to be skipped silently -- and it is the
// one where a renamed project could still take a deployment's volumes away,
// because "no baseline" reads exactly like "created before the field".
//
// Reported by two reviewers on PR #52, reproduced red before the fix.
func TestAnUpdateWithNoResolvableBaselineIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	// Back to a pre-schema-10 record, then break the release it is running.
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.RuntimeOptions = nil
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	live := filepath.Join(h.Paths.ReleasesDir(), "1.2.0")
	require.NoError(t, os.Remove(filepath.Join(live, "manifest.yaml")))

	src := stageUpgradeSource(t, h)
	renameProjectIn(t, src, "renamed")

	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err, "an update with no resolvable baseline must not rename the project")
	// The refusal names the release it could not read, which is the thing an
	// operator has to fix; the project is not their problem yet.
	assert.Contains(t, err.Error(), "1.2.0")

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", current.Version.String(), "nothing may have moved")
}

// A rollback plan compares the same baseline the rollback will, so it cannot
// report a target the real operation then refuses.
//
// The installation records nothing, which is the case that matters: the plan
// has to *derive* the baseline from the release it is running. Skipping the
// derivation on a dry run -- which is what happened when derivation and
// persistence were one step -- left nothing to compare against, and every
// target looked acceptable.
func TestARollbackPlanRefusesWhatTheRollbackWouldRefuse(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	src := stageUpgradeSource(t, h)
	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)

	// The release now running names a different project from the one it
	// would roll back to, and the installation predates schema 10 -- so the
	// only baseline available is the one derived from what is running.
	installed := filepath.Join(h.Paths.ReleasesDir(), "1.3.0")
	renameProjectIn(t, installed, "renamed")

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.RuntimeOptions = nil
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	_, planErr := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{Options: ops.Options{DryRun: true}})
	require.Error(t, planErr, "the plan must refuse what the rollback refuses")
	assert.Contains(t, planErr.Error(), "project")

	after, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Nil(t, after.RuntimeOptions, "and it must still have written nothing")
}
