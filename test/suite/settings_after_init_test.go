package suite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// Four values `init` fixed that nothing changed afterwards (RFC 0042): whether a
// release signature is required, the domains, and the two retention counts.

func setSetting(t *testing.T, h *harness, dryRun bool, set map[string]string) (ops.Result, error) {
	t.Helper()
	return ops.SetSettings(context.Background(), h.Deps, ops.SetSettingsOptions{
		Options: ops.Options{DryRun: dryRun}, Set: set,
	})
}

func installation(t *testing.T, h *harness) domain.Installation {
	t.Helper()
	inst, err := h.Deps.State.LoadInstallation(context.Background())
	require.NoError(t, err)
	return inst
}

func configRecords(t *testing.T, h *harness) []domain.OperationRecord {
	t.Helper()
	records, err := h.Deps.State.Operations(context.Background(), ports.Filter{Type: domain.OpTypeConfig})
	require.NoError(t, err)
	return records
}

// An installation created without `--require-signature` could never turn on
// unattended updates. It needs a trusted key first, and the refusal says how to
// get one; with one, an unsigned bundle is then refused at fetch.
func TestASignatureIsRequiredAfterInit(t *testing.T) {
	h := trustingMachine(t)
	ctx := context.Background()

	_, err := setSetting(t, h, false, map[string]string{"release.require_signature": "true"})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "morzer release trust add")
	assert.False(t, installation(t, h).Policy.RequireSignature)

	// A plan is refused the same way, rather than promising a change the
	// real run would refuse.
	_, err = setSetting(t, h, true, map[string]string{"release.require_signature": "true"})
	require.Error(t, err)

	_, key := minisignKeypair(t)
	_, err = ops.TrustAdd(ctx, h.Deps, ops.Options{}, key)
	require.NoError(t, err)

	plan, err := setSetting(t, h, true, map[string]string{"release.require_signature": "true"})
	require.NoError(t, err)
	assert.Contains(t, plan.Summary, "would set release.require_signature")
	assert.False(t, installation(t, h).Policy.RequireSignature, "a plan required a signature")

	_, err = setSetting(t, h, false, map[string]string{"release.require_signature": "true"})
	require.NoError(t, err)
	assert.True(t, installation(t, h).Policy.RequireSignature)

	ref, err := ports.ParseRef(filepath.Join(testBundlePath(t), "..", "bundle-1.3.0"))
	require.NoError(t, err)
	resolved, err := h.Deps.Source.Resolve(ctx, ref)
	require.NoError(t, err)
	_, _, err = ops.FetchIntoStore(ctx, h.Deps, ref, resolved)
	require.Error(t, err, "an unsigned bundle was fetched with a signature required")
}

// Turning the requirement off is refused while `update.auto_apply` is on, which
// needs it. Turned off with auto-apply in the same command it is accepted, and
// the downgrade is journalled with its flag so an incident review finds it.
func TestTurningOffTheSignatureRequirementIsRefusedThenJournalled(t *testing.T) {
	h := trustingMachine(t)
	_, key := minisignKeypair(t)
	requireSignatures(t, h, key, true)

	_, err := setSetting(t, h, false, map[string]string{"release.require_signature": "false"})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "update.auto_apply")
	assert.True(t, installation(t, h).Policy.RequireSignature)

	_, err = ops.SetSettings(context.Background(), h.Deps, ops.SetSettingsOptions{
		Unset: []string{"release.require_signature"},
	})
	require.Error(t, err, "an unset turned the requirement off under auto-apply")

	plan, err := setSetting(t, h, true, map[string]string{
		"update.auto_apply": "false", "release.require_signature": "false",
	})
	require.NoError(t, err)
	assert.True(t, plan.Record.DryRun)
	assert.Empty(t, configRecords(t, h), "a plan journalled a downgrade")

	_, err = setSetting(t, h, false, map[string]string{
		"update.auto_apply": "false", "release.require_signature": "false",
	})
	require.NoError(t, err)
	inst := installation(t, h)
	assert.False(t, inst.Policy.RequireSignature)
	assert.False(t, inst.Update.AutoApply)

	records := configRecords(t, h)
	require.Len(t, records, 1)
	assert.Equal(t, "false", records[0].Flags["release.require_signature"])
	assert.Equal(t, domain.StatusSucceeded, records[0].Status)

	// Turning it back on is not a downgrade and writes no record.
	_, err = setSetting(t, h, false, map[string]string{"release.require_signature": "true"})
	require.NoError(t, err)
	assert.Len(t, configRecords(t, h), 1)
}

// `update.auto_apply` on an installation that requires no signature names the
// setting that fixes it, rather than leaving re-creating the installation.
func TestAutoApplyNamesTheSettingItNeeds(t *testing.T) {
	h := newHarness(t)
	h.install()

	_, err := setSetting(t, h, false, map[string]string{"update.auto_apply": "true"})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "release.require_signature=true")
}

func renderedApplication(t *testing.T, h *harness) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.Root, "etc", "demo", "application.yaml"))
	require.NoError(t, err)
	return string(data)
}

// A domain change renders the configuration and re-creates every running
// service, forced: a service that reads the domain from a rendered file has an
// unchanged Compose definition, and a plain `up` would leave it on the old one.
func TestADomainChangeRendersAndRecreatesEveryRunningService(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	running := len(h.Runtime.UpOptions)

	plan, err := setSetting(t, h, true, map[string]string{"installation.domains": "new.example, www.new.example"})
	require.NoError(t, err)
	assert.Contains(t, plan.Summary, "would set installation.domains, render the configuration and re-create")
	require.NotEmpty(t, plan.Record.Steps)
	assert.Equal(t, []string{"demo.example"}, installation(t, h).Domains, "a plan recorded the domains")
	assert.Len(t, h.Runtime.UpOptions, running, "a plan re-created a service")

	result, err := setSetting(t, h, false, map[string]string{"installation.domains": "new.example, www.new.example"})
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "rendered the configuration and re-created")

	assert.Equal(t, []string{"new.example", "www.new.example"}, installation(t, h).Domains)
	assert.Contains(t, renderedApplication(t, h), "- www.new.example")

	require.Len(t, h.Runtime.UpOptions, running+1)
	up := h.Runtime.UpOptions[running]
	assert.True(t, up.ForceRecreate, "the services were brought up without being re-created")
	assert.NotEmpty(t, up.Services)
	assert.Equal(t, "new.example", h.Runtime.UpConfigs[running].Env["DEMO_DOMAIN"])

	records := configRecords(t, h)
	require.Len(t, records, 1)
	var steps []string
	for _, step := range records[0].Steps {
		steps = append(steps, step.ID)
	}
	assert.Equal(t, []string{"record-settings", "render-configuration", "recreate-services"}, steps)
}

// A render that fails puts the recorded domains back, so the record and the
// files never disagree.
func TestAFailedDomainRenderLeavesTheOldDomains(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	before := renderedApplication(t, h)

	h.Deps.Renderer = &fakes.Renderer{Err: domain.ValidationError(errInjected, "template refused")}
	_, err := setSetting(t, h, false, map[string]string{"installation.domains": "new.example"})
	require.Error(t, err)

	assert.Equal(t, []string{"demo.example"}, installation(t, h).Domains)
	assert.Equal(t, before, renderedApplication(t, h))
}

// Under a stop the configuration is rendered and nothing is re-created, even
// where a killed `stop` left a service running: `start` runs `apply`, which
// brings every service up with the new value.
func TestADomainChangeUnderAStopRecreatesNothing(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	inst := installation(t, h)
	inst.Stopped = &domain.Stop{At: domain.NewTime(h.Deps.Now()), OperationID: "op_stop"}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
	ups := len(h.Runtime.UpOptions)

	result, err := setSetting(t, h, false, map[string]string{"installation.domains": "new.example"})
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "morzer start")
	assert.Len(t, h.Runtime.UpOptions, ups, "a stopped product's services were re-created")
	assert.Contains(t, renderedApplication(t, h), "- new.example")
	assert.NotNil(t, installation(t, h).Stopped, "the domain change lost the stop record")
}

// A bad entry is refused before anything is written, and `unset` empties the
// list, as an installation created without a domain has.
func TestADomainListIsCheckedAndCanBeCleared(t *testing.T) {
	h := newHarness(t)
	h.install()

	for _, bad := range []string{"new.example,", "http://new.example", "a.example,A.example"} {
		_, err := setSetting(t, h, false, map[string]string{"installation.domains": bad})
		require.Error(t, err, bad)
	}
	assert.Equal(t, []string{"demo.example"}, installation(t, h).Domains)
	assert.Empty(t, configRecords(t, h))

	_, err := ops.SetSettings(context.Background(), h.Deps, ops.SetSettingsOptions{
		Unset: []string{"installation.domains"},
	})
	require.NoError(t, err)
	assert.Empty(t, installation(t, h).Domains)
	assert.NotContains(t, renderedApplication(t, h), "demo.example")
}

// The retention overrides had no writer. Zero is refused, and a count set here
// is the one the next update prunes to: 1 keeps one release beside the current
// and previous ones.
func TestRetentionIsSetAfterInit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	inst := h.install()
	require.NoError(t, os.Remove(h.Paths.InstallationState()))
	inst.Mode = domain.ModeDev
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	h.setHookEnv()
	applyBaseline(t, h)

	for _, name := range []string{"retention.releases", "retention.backups"} {
		_, err := setSetting(t, h, false, map[string]string{name: "0"})
		require.Error(t, err, name)
	}

	_, err := setSetting(t, h, true, map[string]string{"retention.releases": "1", "retention.backups": "14"})
	require.NoError(t, err)
	assert.Zero(t, installation(t, h).Policy.RetainReleases, "a plan recorded a count")

	_, err = setSetting(t, h, false, map[string]string{"retention.releases": "1", "retention.backups": "14"})
	require.NoError(t, err)
	assert.Equal(t, 14, installation(t, h).Policy.RetainBackups)

	storeRelease(t, h.Paths.ReleasesDir(), "1.0.0", pulled(appRepo, 10), pulled(dbRepo, 2))
	storeRelease(t, h.Paths.ReleasesDir(), "1.1.0", pulled(appRepo, 11), pulled(dbRepo, 2))
	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.NoError(t, err)

	assert.NoDirExists(t, filepath.Join(h.Paths.ReleasesDir(), "1.0.0"))
	assert.DirExists(t, filepath.Join(h.Paths.ReleasesDir(), "1.1.0"))

	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Unset: []string{"retention.releases"}})
	require.NoError(t, err)
	entry, err := ops.GetSetting(ctx, h.Deps, "retention.releases")
	require.NoError(t, err)
	assert.Empty(t, entry.Value, "unset reads as following the release")
}
