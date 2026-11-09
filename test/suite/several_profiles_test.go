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

// Several profiles per installation (RFC 0053): a release that lists overlays
// in `combinable` runs any of them beside at most one other profile.

const (
	twoProfiles = "      embedded: [compose/compose.embedded.yaml]\n" +
		"      external-db: [compose/compose.external-db.yaml]\n"
	withOverlays = twoProfiles +
		"      xdns: [compose/compose.xdns.yaml]\n" +
		"      xhttp-reality: [compose/compose.xhttp-reality.yaml]\n"
	withCombinable = withOverlays + "    combinable: [xdns, xhttp-reality]\n"
)

// withCombinableProfiles makes a bundle declare two overlays, combinable or
// not, and render `.Profiles` where it rendered `.Profile`, which fails with
// several selected.
func withCombinableProfiles(t *testing.T, src string, combinable bool) string {
	t.Helper()
	for _, overlay := range []string{"xdns", "xhttp-reality"} {
		require.NoError(t, os.WriteFile(filepath.Join(src, "compose", "compose."+overlay+".yaml"),
			[]byte("services:\n  app:\n    labels:\n      overlay."+overlay+": \"on\"\n"), 0o644))
	}
	if !combinable {
		return patchManifest(t, src, twoProfiles, withOverlays)
	}
	patchManifest(t, src, twoProfiles, withCombinable)
	patchManifest(t, src, `min_manager_version: "0.0.0"`,
		`min_manager_version: "`+domain.CombinableProfilesMinManagerVersion+`"`)
	path := filepath.Join(src, "templates", "application.yaml.tmpl")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	patched := strings.Replace(string(raw), `{{ .Profile | default "default" }}`,
		`{{ join .Profiles "," | default "default" }}`, 1)
	require.NotEqual(t, string(raw), patched, "the template no longer renders .Profile")
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))
	return src
}

// onCombinableRelease is a converged installation on 1.3.0 with the overlays
// declared combinable, recording the profile embedded.
func onCombinableRelease(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: withCombinableProfiles(t, stageUpgradeSource(t, h), true),
	})
	require.NoError(t, err)
	return h
}

// A selection is applied under all of its profiles, layered in the release's
// order whatever order it was given in, and stored and shown in that order. A
// reordering alone changes nothing, and dropping a name removes its services.
func TestSeveralProfilesApplyTogether(t *testing.T) {
	ctx := context.Background()
	h := onCombinableRelease(t)

	_, err := setSetting(t, h, false, map[string]string{"installation.profile": "xhttp-reality, embedded,xdns"})
	require.NoError(t, err)

	all := []string{"embedded", "xdns", "xhttp-reality"}
	assert.Equal(t, all, installation(t, h).Profiles)
	cfg, up := lastUp(t, h)
	assert.Equal(t, []string{"compose.yaml", "compose.embedded.yaml", "compose.xdns.yaml",
		"compose.xhttp-reality.yaml"}, composeFiles(cfg))
	assert.Equal(t, "embedded,xdns,xhttp-reality", cfg.Env["DEMO_PROFILE"])
	assert.True(t, up.ForceRecreate)
	assert.Contains(t, renderedApplication(t, h), "profile: embedded,xdns,xhttp-reality")

	status, err := ops.GetStatus(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "embedded,xdns,xhttp-reality", status.Profile, "the setting's own form")
	assert.Equal(t, all, status.Profiles)

	records := len(configRecords(t, h))
	ups := len(h.Runtime.UpOptions)
	_, err = setSetting(t, h, false, map[string]string{"installation.profile": "xdns,xhttp-reality,embedded"})
	require.NoError(t, err)
	assert.Len(t, configRecords(t, h), records, "a reordered selection was journalled")
	assert.Len(t, h.Runtime.UpOptions, ups, "a reordered selection was applied")

	_, err = setSetting(t, h, false, map[string]string{"installation.profile": "embedded,xdns"})
	require.NoError(t, err)
	cfg, up = lastUp(t, h)
	assert.NotContains(t, composeFiles(cfg), "compose.xhttp-reality.yaml")
	assert.True(t, up.RemoveOrphans, "the dropped profile's services were left running")
}

// Two profiles outside `combinable` are alternatives, and are refused before
// anything is written (RFC 0053 decision 4).
func TestTwoProfilesOutsideCombinableAreRefusedWithNothingChanged(t *testing.T) {
	h := onCombinableRelease(t)
	records := len(configRecords(t, h))
	ups := len(h.Runtime.UpOptions)

	_, err := setSetting(t, h, false, map[string]string{"installation.profile": "embedded,external-db"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "profiles embedded and external-db cannot be deployed together")
	assert.Equal(t, "release 1.3.0 lets these run beside one other profile: xdns, xhttp-reality",
		domain.AsError(err).Hint)
	assert.Equal(t, []string{"embedded"}, installation(t, h).Profiles)
	assert.Len(t, configRecords(t, h), records)
	assert.Len(t, h.Runtime.UpOptions, ups)
}

// An update to a release that declares every recorded name and will not run
// them together is refused before any step, whatever `--profile` says (RFC
// 0053 decision 10), so the topology does not change as a side effect.
func TestAnUpdateToAReleaseThatWillNotRunTheSelectionIsRefused(t *testing.T) {
	ctx := context.Background()
	h := onCombinableRelease(t)
	_, err := setSetting(t, h, false, map[string]string{"installation.profile": "xdns,xhttp-reality"})
	require.NoError(t, err)

	src := withCombinableProfiles(t, stageUpgradeSource(t, h), false)
	patchManifest(t, src, "version: 1.3.0", "version: 1.4.0")
	require.NoError(t, os.WriteFile(filepath.Join(src, "VERSION"), []byte("1.4.0\n"), 0o644))
	for _, override := range [][]string{nil, {"xdns"}} {
		_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src, Options: ops.Options{Profiles: override}})
		require.Error(t, err, "--profile %q", override)
		assert.Contains(t, err.Error(), "release 1.4.0 does not let xdns and xhttp-reality run together")
		hint := domain.AsError(err).Hint
		assert.Contains(t, hint, "it runs one of them at a time")
		assert.Contains(t, hint, "morzer config set installation.profile=")
	}

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String())
}

// A rollback to a release from before the overlays is refused the same way.
func TestARollbackToAReleaseWithoutTheOverlaysIsRefused(t *testing.T) {
	h := onCombinableRelease(t)
	_, err := setSetting(t, h, false, map[string]string{"installation.profile": "embedded,xdns"})
	require.NoError(t, err)

	_, err = ops.Rollback(context.Background(), h.Deps, ops.RollbackOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `release 1.2.0 does not declare the profile "xdns" this installation uses`)
}

// A one-run `--profile` takes a selection, is checked by the same rules, and
// lasts the run; the journal records it comma-joined (RFC 0053 decision 8).
func TestAOneRunSelectionLastsTheRun(t *testing.T) {
	ctx := context.Background()
	h := onCombinableRelease(t)
	warned := oneRunWarnings(h)

	result, err := ops.Apply(ctx, h.Deps, ops.Options{Profiles: []string{"xdns", "embedded"}})
	require.NoError(t, err)
	cfg, _ := lastUp(t, h)
	assert.Equal(t, []string{"compose.yaml", "compose.embedded.yaml", "compose.xdns.yaml"}, composeFiles(cfg))
	assert.Equal(t, "xdns,embedded", result.Record.Flags["profile"])
	assert.Equal(t, []string{"embedded"}, installation(t, h).Profiles)
	require.Len(t, *warned, 1)
	assert.Contains(t, (*warned)[0], "the installation keeps the profile embedded")

	_, err = ops.Apply(ctx, h.Deps, ops.Options{Profiles: []string{"embedded", "external-db"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profiles embedded and external-db cannot be deployed together")
}

// A failed update run under a one-run selection brings the previous release
// back under the recorded one: 1.2.0 declares no xdns, and restoring under the
// override would be refused.
func TestAFailedUpdateUnderASelectionRestoresTheRecordedProfile(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	src := withCombinableProfiles(t, stageRecoverableUpgradeSource(t, h), true)
	h.Runtime.Fail["Up"] = domain.RuntimeError(nil, "injected: the new release does not start")
	h.Runtime.OnCall = func(method string) {
		if method == "Up" {
			delete(h.Runtime.Fail, "Up")
		}
	}

	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{
		Ref: src, Options: ops.Options{Profiles: []string{"embedded", "xdns"}},
	})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "unknown deployment profile")
	assert.Equal(t, domain.StatusCompensated, result.Record.Status, "%v", err)
	cfg, _ := lastUp(t, h)
	assert.Equal(t, []string{"compose.yaml", "compose.embedded.yaml"}, composeFiles(cfg))
}

// `init` checks a selection against the release it stages, so an impossible
// combination is refused before an installation exists (RFC 0053 decision 15),
// and records a possible one in the release's layering order.
func TestInitChecksTheSelectionAgainstTheRelease(t *testing.T) {
	ctx := context.Background()
	fresh := func(combinable bool) (*harness, string) {
		h := newHarness(t)
		h.setHookEnv()
		realIdentity(t, h)
		src := filepath.Join(t.TempDir(), "bundle-1.2.0")
		copyBundle(t, testBundlePath(t), src)
		retargetManifest(t, src, h.Root)
		if combinable {
			withCombinableProfiles(t, src, true)
		}
		return h, src
	}

	refused, src := fresh(false)
	_, err := ops.Init(ctx, refused.Deps, ops.InitOptions{
		Product:         "demo",
		ReleasePath:     src,
		Profiles:        []string{"embedded", "external-db"},
		NoRecoveryKey:   true,
		GenerateSecrets: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profiles embedded and external-db cannot be deployed together")
	exists, err := refused.Deps.State.InstallationExists(ctx)
	require.NoError(t, err)
	assert.False(t, exists, "a refused selection left an installation behind")

	empty, _ := fresh(false)
	_, err = ops.Init(ctx, empty.Deps, ops.InitOptions{
		Product: "demo", Profiles: []string{"embedded", ""}, NoRecoveryKey: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "an empty profile name in the selection")

	h, bundle := fresh(true)
	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{
		Product:         "demo",
		ReleasePath:     bundle,
		Profiles:        []string{"xhttp-reality", "xdns", "embedded"},
		NoRecoveryKey:   true,
		GenerateSecrets: true,
	})
	require.NoError(t, err)
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"embedded", "xdns", "xhttp-reality"}, inst.Profiles)
}
