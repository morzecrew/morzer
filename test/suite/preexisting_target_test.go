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
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// RFC 0048: a release that adds a configuration target where a file already
// is. The installed release never rendered the path, so the drift checks
// cannot see the file, and the update used to replace it and keep nothing.

const proxyRendered = "server_name demo;\n"

// addTarget declares a new configuration target in a retargeted bundle, at
// a path under the same test root as the bundle's own target, and returns it.
func addTarget(t *testing.T, bundle string) string {
	t.Helper()
	path := filepath.Join(bundle, "manifest.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	// The bundle's own target, already retargeted under the test root: the
	// value of its `target:` line, whatever the root's path holds.
	const own = "/etc/demo/application.yaml"
	var root string
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "target: "); ok && strings.HasSuffix(value, own) {
			root = strings.TrimSuffix(value, own)
		}
	}
	require.NotEmpty(t, root, "the bundle no longer declares its own target")
	target := filepath.Join(root, "etc", "nginx", "conf.d", "demo.conf")

	block := "  - template: templates/proxy.conf.tmpl\n    target: " + target + "\n"
	out := strings.Replace(string(data), "configuration:\n", "configuration:\n"+block, 1)
	require.NotEqual(t, string(data), out, "manifest has no configuration block")
	require.NoError(t, os.WriteFile(path, []byte(out), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "templates", "proxy.conf.tmpl"),
		[]byte(proxyRendered), 0o644))
	return target
}

func writeOperatorFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func renderPlanStep(t *testing.T, h *harness) events.PlanStep {
	t.Helper()
	plans := h.Events.OfKind(events.KindPlan)
	require.NotEmpty(t, plans)
	for _, step := range plans[len(plans)-1].Plan {
		if step.ID == "render-configuration" {
			return step
		}
	}
	t.Fatal("the plan has no render-configuration step")
	return events.PlanStep{}
}

func TestAnUpdateKeepsAFileAtANewTargetAndSaysWhere(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()
	src := stageRecoverableUpgradeSource(t, h)
	target := addTarget(t, src)
	writeOperatorFile(t, target, "the operator's own block\n")

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src, Options: ops.Options{DryRun: true}})
	require.NoError(t, err)
	assert.Contains(t, renderPlanStep(t, h).Description, target+" exists, and 1.2.0 does not render it")
	assert.Equal(t, "the operator's own block\n", readFile(t, target), "a plan replaced the file")

	res, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)
	copied := filepath.Join(h.Paths.VarDir, "replaced", res.Record.ID, target)
	assert.Equal(t, proxyRendered, readFile(t, target))
	assert.Equal(t, "the operator's own block\n", readFile(t, copied), "the file was replaced and not kept")
	info, err := os.Stat(copied)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Contains(t, res.Summary, copied, "the summary does not say where the old file is")

	back, err := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{Options: ops.Options{Force: true}})
	require.NoError(t, err)
	assert.Contains(t, back.Summary, copied, "rollback does not name the copy of a file 1.2.0 does not render")
}

// A file that already holds what the release renders loses nothing, so it is
// neither named nor copied.
func TestAnUpdateLeavesAFileThatAlreadyMatches(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()
	src := stageUpgradeSource(t, h)
	target := addTarget(t, src)
	writeOperatorFile(t, target, proxyRendered)

	res, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)
	assert.NotContains(t, res.Summary, "replaced")
	assert.NoDirExists(t, filepath.Join(h.Paths.VarDir, "replaced"))
}

// A failed update puts the file back through the render step's compensator,
// so the copy it kept would only be a second, misleading one.
func TestAFailedUpdateRestoresTheFileAndKeepsNoCopy(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()
	src := stageUpgradeSource(t, h)
	target := addTarget(t, src)
	writeOperatorFile(t, target, "the operator's own block\n")
	h.Runtime.Fail["Pull"] = domain.RuntimeError(nil, "injected: registry unreachable")

	res, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	assert.Equal(t, "the operator's own block\n", readFile(t, target))
	assert.NoDirExists(t, filepath.Join(h.Paths.VarDir, "replaced", res.Record.ID))
}

// Decision 4: an unattended tick holds, as it does for drift, and installs once
// the file is gone.
func TestAFileAtANewTargetHoldsAnUnattendedUpdate(t *testing.T) {
	var target string
	h, _, _, _ := followingHarnessWith(t, func(t *testing.T, dir string) {
		recoverableUpdate(t, dir)
		target = addTarget(t, dir)
	})
	h.setHookEnv()
	applyBaseline(t, h)
	armAutoApply(t, h)
	ctx := context.Background()
	writeOperatorFile(t, target, "the operator's own block\n")

	res, err := ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err, "a held tick must exit 0")
	assert.False(t, res.Applied, "the tick replaced a file nobody was watching")
	assert.Contains(t, res.Assessment.Why(), target)
	assert.Contains(t, res.Assessment.Why(), "morzer update --dry-run")
	assert.Equal(t, "the operator's own block\n", readFile(t, target))

	require.NoError(t, os.Remove(target))
	res, err = ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)
	assert.True(t, res.Applied, "the tick still held once the file was gone: %s", res.Assessment.Why())
}

// `init --release` records a release before anything renders it, so the first
// apply treats every target as new and keeps a file it finds there.
func TestTheFirstApplyKeepsAFileAtItsTarget(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	target := applicationTarget(h)
	writeOperatorFile(t, target, "written before the first apply\n")

	res, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	copied := filepath.Join(h.Paths.VarDir, "replaced", res.Record.ID, target)
	assert.Equal(t, "written before the first apply\n", readFile(t, copied))
	assert.Contains(t, res.Summary, copied)

	// Converged now, so the release owns its target and a second apply keeps
	// nothing, edited file or not.
	writeOperatorFile(t, target, "edited after\n")
	res, err = ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.NotContains(t, res.Summary, "kept at")
}

// An undo that cannot put a file back keeps the copy: it may be the only one.
func TestAFailedUpdateThatCannotRestoreKeepsTheCopy(t *testing.T) {
	requireNonRoot(t)
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()
	src := stageUpgradeSource(t, h)
	target := addTarget(t, src)
	writeOperatorFile(t, target, "the operator's own block\n")
	dir := filepath.Dir(target)
	h.Runtime.Fail["Pull"] = domain.RuntimeError(nil, "injected: registry unreachable")
	h.Runtime.OnCall = func(method string) {
		if method == "Pull" {
			require.NoError(t, os.Chmod(dir, 0o500))
		}
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	res, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	kept := filepath.Join(h.Paths.VarDir, "replaced", res.Record.ID)
	assert.Equal(t, "the operator's own block\n", readFile(t, filepath.Join(kept, target)),
		"the undo removed the only copy")
	assert.Contains(t, err.Error(), kept, "the failure does not say where the copy is")
}

// A file can appear after the tick checked and before the render: the
// unattended update refuses it rather than replace it with nobody watching.
func TestAnUnattendedUpdateRefusesAFileThatAppearedAfterTheCheck(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	src := stageUpgradeSource(t, h)
	target := addTarget(t, src)
	writeOperatorFile(t, target, "written while the backup ran\n")

	_, err := ops.Update(context.Background(), h.Deps,
		ops.UpdateOptions{Ref: src, Options: ops.Options{Unattended: true}})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Error(), "an unattended update does not replace it")
	assert.Equal(t, "written while the backup ran\n", readFile(t, target))
}

// The tick passes that it is unattended, so a file that appears once its gate
// has passed is refused by the render rather than replaced.
func TestATickRefusesAFileThatAppearsAfterItsGate(t *testing.T) {
	var target string
	h, _, _, _ := followingHarnessWith(t, func(t *testing.T, dir string) {
		recoverableUpdate(t, dir)
		target = addTarget(t, dir)
	})
	h.setHookEnv()
	applyBaseline(t, h)
	armAutoApply(t, h)
	h.Runtime.OnCall = func(method string) {
		if method == "Status" && target != "" {
			if _, err := os.Stat(target); err != nil {
				writeOperatorFile(t, target, "written while the update ran\n")
			}
		}
	}

	res, err := ops.RunUnattended(context.Background(), h.Deps, ops.UnattendedOptions{})
	require.Error(t, err, "the file appeared before the gate, so the test proves nothing")
	assert.Contains(t, domain.AsError(err).Error(), "an unattended update does not replace it")
	assert.False(t, res.Applied, "the tick replaced a file that appeared after its gate")
	assert.Equal(t, "written while the update ran\n", readFile(t, target))
}
