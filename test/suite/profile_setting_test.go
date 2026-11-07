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
	"github.com/morzecrew/morzer/internal/ports"
)

// The deployment profile, which only `init` could choose and `--profile`
// overrode for one run (#286).

// composeFiles is the base name of every file a runtime call was given.
func composeFiles(cfg ports.RuntimeConfig) []string {
	var out []string
	for _, f := range cfg.Files {
		out = append(out, filepath.Base(f))
	}
	return out
}

func lastUp(t *testing.T, h *harness) (ports.RuntimeConfig, ports.UpOptions) {
	t.Helper()
	require.NotEmpty(t, h.Runtime.UpConfigs)
	n := len(h.Runtime.UpConfigs) - 1
	return h.Runtime.UpConfigs[n], h.Runtime.UpOptions[n]
}

// patchedUpgradeSource is the 1.3.0 fixture with one edit to its manifest.
func patchedUpgradeSource(t *testing.T, h *harness, from, to string) string {
	t.Helper()
	src := stageUpgradeSource(t, h)
	path := filepath.Join(src, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	patched := strings.Replace(string(raw), from, to, 1)
	require.NotEqual(t, string(raw), patched, "the manifest no longer says %q", from)
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))
	return src
}

// oneRunWarnings collects the warnings that a `--profile` lasts one run.
func oneRunWarnings(h *harness) *[]string {
	var out []string
	h.Deps.Bus.SubscribeFunc(func(e events.Event) {
		if e.Level == events.LevelWarn && strings.Contains(e.Message, "for this run only") {
			out = append(out, e.Message)
		}
	})
	return &out
}

// On a running product the change applies the release under the new profile:
// its files, its services, the orphans of the old one removed, and every
// container re-created, since a service reading `.Profile` through a rendered
// file has an unchanged definition. Every later run keeps it.
func TestAProfileChangeAppliesTheReleaseUnderIt(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ups := len(h.Runtime.UpOptions)

	plan, err := setSetting(t, h, true, map[string]string{"installation.profile": "external-db"})
	require.NoError(t, err)
	assert.Equal(t, "would set installation.profile and apply demo 1.2.0 under the profile external-db", plan.Summary)
	require.True(t, plan.Record.DryRun)
	require.Greater(t, len(plan.Record.Steps), 2, "the plan does not show the converge")
	assert.Equal(t, "restore-point", plan.Record.Steps[1].ID)
	assert.Equal(t, "record-state", plan.Record.Steps[len(plan.Record.Steps)-1].ID)
	assert.Equal(t, "embedded", installation(t, h).Profile, "a plan recorded the profile")
	assert.Len(t, h.Runtime.UpOptions, ups, "a plan started a service")

	result, err := setSetting(t, h, false, map[string]string{"installation.profile": "external-db"})
	require.NoError(t, err)
	assert.Equal(t, "set installation.profile and applied demo 1.2.0 under the profile external-db", result.Summary)
	assert.Equal(t, "external-db", installation(t, h).Profile)
	assert.Contains(t, renderedApplication(t, h), "profile: external-db")

	cfg, up := lastUp(t, h)
	assert.Contains(t, composeFiles(cfg), "compose.external-db.yaml")
	assert.NotContains(t, composeFiles(cfg), "compose.embedded.yaml")
	assert.Equal(t, "external-db", cfg.Env["DEMO_PROFILE"])
	assert.True(t, up.RemoveOrphans, "the old profile's services were left running")
	assert.True(t, up.ForceRecreate, "a service reading the profile from a rendered file kept the old one")

	records := configRecords(t, h)
	require.Len(t, records, 1)
	var steps []string
	for _, step := range records[0].Steps {
		steps = append(steps, step.ID)
	}
	assert.Equal(t, []string{"record-settings", "restore-point"}, steps[:2])
	assert.Equal(t, "record-state", steps[len(steps)-1])

	// A plain apply, the boot unit's, and an update all render it.
	for name, run := range map[string]func() error{
		"apply": func() error { _, err := ops.Apply(context.Background(), h.Deps, ops.Options{}); return err },
		"apply --startup": func() error {
			_, err := ops.Apply(context.Background(), h.Deps, ops.Options{Startup: true})
			return err
		},
	} {
		require.NoError(t, run(), name)
		cfg, up := lastUp(t, h)
		assert.Contains(t, composeFiles(cfg), "compose.external-db.yaml", name)
		assert.False(t, up.ForceRecreate, "%s re-created every container", name)
	}
	_, err = ops.Update(context.Background(), h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.NoError(t, err)
	cfg, _ = lastUp(t, h)
	assert.Contains(t, composeFiles(cfg), "compose.external-db.yaml", "update")
	assert.Contains(t, renderedApplication(t, h), "profile: external-db")
}

// `unset`, or an empty value, returns to the base files, and the profile's
// services are removed as orphans.
func TestUnsettingTheProfileReturnsToTheBaseFiles(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	_, err := ops.SetSettings(context.Background(), h.Deps, ops.SetSettingsOptions{
		Unset: []string{"installation.profile"},
	})
	require.NoError(t, err)
	assert.Empty(t, installation(t, h).Profile)
	assert.Contains(t, renderedApplication(t, h), "profile: default")
	cfg, up := lastUp(t, h)
	assert.Equal(t, []string{"compose.yaml"}, composeFiles(cfg))
	assert.True(t, up.RemoveOrphans)

	_, err = setSetting(t, h, false, map[string]string{"installation.profile": "embedded"})
	require.NoError(t, err)
	result, err := setSetting(t, h, false, map[string]string{"installation.profile": " "})
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "under the base files")
	assert.Empty(t, installation(t, h).Profile)
}

// A name the installed release does not declare is refused before anything is
// written, and nothing is journalled.
func TestAnUndeclaredProfileIsRefusedWithNothingChanged(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ups := len(h.Runtime.UpOptions)

	_, err := setSetting(t, h, false, map[string]string{"installation.profile": "xdns"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown deployment profile "xdns"`)
	assert.Contains(t, domain.AsError(err).Hint, "embedded, external-db")

	assert.Equal(t, "embedded", installation(t, h).Profile)
	assert.Contains(t, renderedApplication(t, h), "profile: embedded")
	assert.Empty(t, configRecords(t, h))
	assert.Len(t, h.Runtime.UpOptions, ups)
}

// A converge that fails brings back the old profile, its files, and its
// services, re-created against the old file rather than the new one.
func TestAFailedProfileChangeBringsTheOldProfileBack(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	h.Runtime.Fail["Up"] = domain.RuntimeError(nil, "injected: the new profile does not start")
	var ups int
	var mounted string
	h.Runtime.OnCall = func(method string) {
		if method != "Up" {
			return
		}
		if ups++; ups == 1 {
			delete(h.Runtime.Fail, "Up")
			return
		}
		mounted = renderedApplication(t, h)
	}

	_, err := setSetting(t, h, false, map[string]string{"installation.profile": "external-db"})
	require.Error(t, err)

	require.Equal(t, 2, ups, "the failed converge was not undone with an up of its own")
	assert.Contains(t, mounted, "profile: embedded", "the undo re-created the services against the new file")
	cfg, up := lastUp(t, h)
	assert.Contains(t, composeFiles(cfg), "compose.embedded.yaml")
	assert.NotContains(t, composeFiles(cfg), "compose.external-db.yaml")
	assert.True(t, up.RemoveOrphans, "the new profile's services were left running")
	assert.True(t, up.ForceRecreate, "the undo left containers on the new profile's file")
	assert.Equal(t, "embedded", installation(t, h).Profile)
}

// Under a stop the change is recorded and rendered, and nothing starts:
// `start` applies the release under it.
func TestAProfileChangeUnderAStopStartsNothing(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	inst := installation(t, h)
	inst.Stopped = &domain.Stop{At: domain.NewTime(h.Deps.Now()), OperationID: "op_stop"}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
	ups := len(h.Runtime.UpOptions)

	result, err := setSetting(t, h, false, map[string]string{"installation.profile": "external-db"})
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "morzer start")
	assert.Len(t, h.Runtime.UpOptions, ups, "a stopped product was started")
	assert.Contains(t, renderedApplication(t, h), "profile: external-db")
	assert.NotNil(t, installation(t, h).Stopped, "the profile change lost the stop record")
}

// `--profile` other than the stored one lasts the run, and says so with the
// command that keeps it.
func TestAOneRunProfileLeavesTheStoredOneAndSaysSo(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	warned := oneRunWarnings(h)

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{Profile: "external-db"})
	require.NoError(t, err)
	cfg, _ := lastUp(t, h)
	assert.Contains(t, composeFiles(cfg), "compose.external-db.yaml")
	assert.Equal(t, "embedded", installation(t, h).Profile)
	require.Len(t, *warned, 1)
	assert.Contains(t, (*warned)[0], "the installation keeps the profile embedded")
	assert.Contains(t, (*warned)[0], "`morzer config set installation.profile=external-db`")

	_, err = ops.Apply(context.Background(), h.Deps, ops.Options{Profile: "embedded"})
	require.NoError(t, err)
	assert.Len(t, *warned, 1, "the stored profile, passed again, was called an override")

	_, err = ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: stageUpgradeSource(t, h), Options: ops.Options{Profile: "external-db"},
	})
	require.NoError(t, err)
	assert.Equal(t, "embedded", installation(t, h).Profile)
	require.Len(t, *warned, 2, "update --profile did not say it lasts the run")
}

// An update whose release no longer declares the stored profile is refused
// before any step, whatever `--profile` says: the next plain apply would render
// the stored one.
func TestAnUpdateToAReleaseWithoutTheProfileIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	src := patchedUpgradeSource(t, h, "      embedded: [compose/compose.embedded.yaml]\n", "")

	for _, override := range []string{"", "external-db"} {
		_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src, Options: ops.Options{Profile: override}})
		require.Error(t, err, "--profile %q", override)
		assert.Contains(t, err.Error(), `release 1.3.0 does not declare the profile "embedded" this installation uses`)
		assert.Contains(t, domain.AsError(err).Hint, "it declares external-db")
		assert.Contains(t, domain.AsError(err).Hint, "morzer config set installation.profile=")
	}

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", current.Version.String())
	records, err := h.Deps.State.Operations(ctx, ports.Filter{Type: domain.OpTypeUpdate})
	require.NoError(t, err)
	assert.Empty(t, records, "a refused update was journalled")
}

// A rollback to a release that does not declare the stored profile is refused
// the same way: a profile the newer release added, set since.
func TestARollbackToAReleaseWithoutTheProfileIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	src := patchedUpgradeSource(t, h,
		"      external-db: [compose/compose.external-db.yaml]\n",
		"      external-db: [compose/compose.external-db.yaml]\n      extra: [compose/compose.external-db.yaml]\n")
	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)
	_, err = setSetting(t, h, false, map[string]string{"installation.profile": "extra"})
	require.NoError(t, err)

	_, err = ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `release 1.2.0 does not declare the profile "extra" this installation uses`)

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String())
}
