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
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/infra/tools"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// podmanReports answers `podman --version` with one version and leaves every
// other command to the machine.
type podmanReports struct {
	infraexec.Runner
	version string
}

func (r podmanReports) Look(name string) (string, error) {
	if name == "podman" {
		return "/usr/bin/podman", nil
	}
	return r.Runner.Look(name)
}

func (r podmanReports) Run(ctx context.Context, cmd infraexec.Command) (infraexec.Result, error) {
	if filepath.Base(cmd.Argv[0]) == "podman" {
		return infraexec.Result{Stdout: "podman version " + r.version + "\n"}, nil
	}
	return r.Runner.Run(ctx, cmd)
}

// runtimeWithFloor stands in for the quadlet runtime, holding podman to 5.3.
type runtimeWithFloor struct{ runtimeNamingTools }

func (runtimeWithFloor) ToolFloors() map[string]domain.Constraint {
	floor, _ := domain.ParseConstraint(">=5.3.0")
	return map[string]domain.Constraint{"podman": floor}
}

// `init` refuses a runtime whose tool is older than it can drive, before it
// creates anything, and installs with it once the tool is new enough.
func TestInitRefusesAToolBelowTheRuntimesFloor(t *testing.T) {
	ctx := context.Background()
	initWith := func(version string) (*harness, error) {
		h := newHarnessAt(t, "demo", userLayout)
		h.setHookEnv()
		realIdentity(t, h)
		h.Deps.Tools = tools.NewRegistry(podmanReports{infraexec.New(), version})
		h.Deps.Runtimes = []ports.Runtime{h.Runtime,
			runtimeWithFloor{runtimeNamingTools{fakes.NewRuntime(), "quadlet", []string{"podman"}}}}
		src := filepath.Join(t.TempDir(), "bundle-1.2.0")
		copyBundle(t, testBundlePath(t), src)
		retargetManifest(t, src, h.Root)
		manifest := filepath.Join(src, "manifest.yaml")
		raw, err := os.ReadFile(manifest)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(manifest, []byte(strings.Replace(string(raw),
			"runtimes:\n", "runtimes:\n  quadlet:\n    files: [compose/compose.yaml]\n", 1)), 0o644))
		_, err = ops.Init(ctx, h.Deps, ops.InitOptions{Product: "demo", NoRecoveryKey: true, ReleasePath: src, Runtime: "quadlet"})
		return h, err
	}
	exists := func(h *harness) bool {
		ok, err := h.Deps.State.InstallationExists(ctx)
		require.NoError(t, err)
		return ok
	}

	h, err := initWith("4.9.3")
	require.ErrorIs(t, err, domain.ErrToolIncompatible)
	assert.ErrorContains(t, err, "podman 4.9.3 is older than the quadlet runtime can drive")
	assert.False(t, exists(h), "the tool is asked about before anything is created")

	h, err = initWith("5.7.0")
	require.NoError(t, err)
	assert.True(t, exists(h))
}

// A release declaring quadlet alone names its runtime only once it is staged,
// so the refusal comes after the release is made current. The refused `init`
// still leaves nothing behind: no installation, and no current release
// pointing at a directory its undo removed.
func TestARefusedInitOfAStagedReleaseLeavesNoCurrentRelease(t *testing.T) {
	ctx := context.Background()
	h := newHarnessAt(t, "demo", userLayout)
	h.setHookEnv()
	realIdentity(t, h)
	h.Deps.Tools = tools.NewRegistry(podmanReports{infraexec.New(), "4.9.3"})
	h.Deps.Runtimes = []ports.Runtime{h.Runtime,
		runtimeWithFloor{runtimeNamingTools{fakes.NewRuntime(), "quadlet", []string{"podman"}}}}
	src := filepath.Join(t.TempDir(), "bundle-1.2.0")
	copyBundle(t, testBundlePath(t), src)
	retargetManifest(t, src, h.Root)
	manifest := filepath.Join(src, "manifest.yaml")
	raw, err := os.ReadFile(manifest)
	require.NoError(t, err)
	text := string(raw)
	start := strings.Index(text, "runtimes:\n")
	end := strings.Index(text, "\nrequirements:")
	require.True(t, start >= 0 && end > start, "the fixture's runtimes block moved")
	text = text[:start] + "runtimes:\n  quadlet:\n    files: [compose/compose.yaml]\n" + text[end:]
	require.NoError(t, os.WriteFile(manifest, []byte(text), 0o644))

	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{Product: "demo", NoRecoveryKey: true, ReleasePath: src})
	require.ErrorIs(t, err, domain.ErrToolIncompatible)

	exists, err := h.Deps.State.InstallationExists(ctx)
	require.NoError(t, err)
	assert.False(t, exists)
	_, err = os.Lstat(h.Deps.Paths.CurrentLink())
	assert.ErrorIs(t, err, os.ErrNotExist, "the current link outlived the release it pointed at")
	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.True(t, current.IsZero(), "a current release is recorded for a release the undo removed: %+v", current)
}
