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
	const own = "/etc/demo/application.yaml"
	i := strings.Index(string(data), own)
	require.Positive(t, i, "the bundle no longer declares its own target")
	root := string(data)[strings.LastIndex(string(data[:i]), " ")+1 : i]
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
