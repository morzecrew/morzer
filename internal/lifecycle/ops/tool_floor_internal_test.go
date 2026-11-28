package ops

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/infra/tools"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// toolAt answers one catalogued tool's version probe with one version, or
// reports the tool missing when the version is empty. Which tool does not
// matter here; the floor is the runtime's, whatever it names.
type toolAt struct{ version string }

func (r toolAt) Look(name string) (string, error) {
	if name != tools.Restic || r.version == "" {
		return "", domain.Preflight(domain.ErrToolMissing, "required tool %q was not found in PATH", name)
	}
	return "/usr/bin/" + name, nil
}

func (r toolAt) Run(_ context.Context, cmd exec.Command) (exec.Result, error) {
	if filepath.Base(cmd.Argv[0]) != tools.Restic {
		return exec.Result{ExitCode: 127}, domain.RuntimeError(nil, "not the tool under test")
	}
	return exec.Result{Stdout: "restic " + r.version + " compiled with go1.25 on linux/amd64\n"}, nil
}

// flooredRuntime is a runtime holding its tool to 0.18.
type flooredRuntime struct{ ports.Runtime }

func (flooredRuntime) Name() string            { return "floored" }
func (flooredRuntime) RequiredTools() []string { return []string{tools.Restic} }
func (flooredRuntime) ToolFloors() map[string]domain.Constraint {
	floor, _ := domain.ParseConstraint(">=0.18.0")
	return map[string]domain.Constraint{tools.Restic: floor}
}

func flooredDeps(version string) *Deps {
	return &Deps{Runtime: flooredRuntime{fakes.NewRuntime()}, Tools: tools.NewRegistry(toolAt{version})}
}

// A tool older than its runtime's floor refuses every command that asks; a
// tool that is missing is left to the commands that run it, as before.
func TestAToolBelowItsRuntimesFloorRefuses(t *testing.T) {
	ctx := context.Background()

	err := flooredDeps("0.16.0").refuseUnfitRuntime(ctx)
	require.ErrorIs(t, err, domain.ErrToolIncompatible)
	require.ErrorIs(t, err, domain.ErrPreflight, "a refusal before anything changes is a preflight one")
	assert.Contains(t, err.Error(), "restic 0.16.0 is older than the floored runtime can drive, which needs restic >=0.18.0")
	assert.Contains(t, domain.AsError(err).Hint, "install with another runtime")

	for _, version := range []string{"0.18.0", "0.19.1", ""} {
		assert.NoError(t, flooredDeps(version).refuseUnfitRuntime(ctx), "version %q", version)
	}
	assert.NoError(t, (&Deps{Runtime: fakes.NewRuntime(), Tools: tools.NewRegistry(toolAt{"0.16.0"})}).
		refuseUnfitRuntime(ctx), "a runtime that names no floor is held to none")
}

// doctor reports the version against the floor, with an installation and
// without one, where the floor decides whether the runtime is ready.
func TestDoctorHoldsAToolToItsRuntimesFloor(t *testing.T) {
	ctx := context.Background()
	for version, want := range map[string]events.CheckStatus{
		"0.16.0": events.CheckFail, "": events.CheckFail, "0.19.1": events.CheckOK,
	} {
		d := flooredDeps(version)
		check, ok := d.checkToolFloors()
		require.True(t, ok)
		got := check.Run(ctx)
		assert.Equal(t, want, got.Status, "version %q: %s", version, got.Message)

		d.Runtimes = []ports.Runtime{d.Runtime}
		byID := map[string]events.CheckResult{}
		for _, c := range d.runtimeToolChecks() {
			byID[c.ID] = c.Run(ctx)
		}
		assert.Equal(t, want, byID["tools."+tools.Restic].Status, "version %q, no installation", version)
	}

	got := flooredDeps("0.19.1")
	check, _ := got.checkToolFloors()
	assert.Equal(t, "restic 0.19.1", check.Run(ctx).Message)

	_, ok := (&Deps{Runtime: fakes.NewRuntime(), Tools: tools.NewRegistry(toolAt{"0.16.0"})}).checkToolFloors()
	assert.False(t, ok, "a runtime with no floor gets no check")
}
