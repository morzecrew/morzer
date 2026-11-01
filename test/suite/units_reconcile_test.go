package suite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// A machine an older manager set up has the units that manager wrote. The next
// apply or update rewrites them to what this one writes (RFC 0040 decision
// 13): the backup unit's `--unattended`, and the doctor timer a notify target
// already configured now wants (RFC 0043 decision 4).
func TestAnApplyReconcilesUnitsAnOlderManagerWrote(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	h.Supervisor.Present = true
	h.Deps.Supervisor = h.Supervisor
	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableAll))

	// A target configured before the doctor timer existed: nothing has
	// reconciled the units since.
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Notify.Targets = []domain.NotifyTargetConfig{{Name: "chat", URL: "https://hooks.example/morzer"}}
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	require.NotContains(t, h.Supervisor.Installed, "demo-doctor.timer")

	_, err = ops.Apply(ctx, h.Deps, ops.Options{DryRun: true})
	require.NoError(t, err)
	assert.NotContains(t, h.Supervisor.Installed, "demo-doctor.timer", "a dry run reconciled the units")

	// Boot's apply runs inside the product's own unit, and leaves them.
	_, err = ops.Apply(ctx, h.Deps, ops.Options{Startup: true})
	require.NoError(t, err)
	assert.NotContains(t, h.Supervisor.Installed, "demo-doctor.timer", "the boot-time apply reconciled the units")

	_, err = ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Contains(t, h.Supervisor.Installed, "demo-doctor.timer", "the apply left the units as the older manager wrote them")
}

// An update reconciles them as an apply does.
func TestAnUpdateReconcilesUnitsAnOlderManagerWrote(t *testing.T) {
	h, _, _, _ := followingHarnessWith(t, recoverableUpdate)
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()
	h.Supervisor.Present = true
	h.Deps.Supervisor = h.Supervisor
	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableAll))
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Notify.Targets = []domain.NotifyTargetConfig{{Name: "chat", URL: "https://hooks.example/morzer"}}
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	_, err = ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)
	candidate, err := h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{To: candidate.Version.String()})
	require.NoError(t, err)
	assert.Contains(t, h.Supervisor.Installed, "demo-doctor.timer", "the update left the units as the older manager wrote them")
}
