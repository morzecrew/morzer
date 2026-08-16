package suite

import (
	"context"
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
