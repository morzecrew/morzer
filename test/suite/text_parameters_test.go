package suite

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// Multi-line text parameters, RFC 0054.

// withTextParameters declares `rules`, a text parameter with no default that
// re-creates app, and `motd`, whose default is a `|` block and so ends in a
// newline, at the manager version that reads the type.
func withTextParameters(t *testing.T, src string) string {
	t.Helper()
	patchManifest(t, src, "\nimages:\n", `  rules:
    type: text
    description: Routing rules, one per line
    services: [app]
  motd:
    type: text
    default: |
      welcome
      to demo

images:
`)
	return patchManifest(t, src, `min_manager_version: "0.0.0"`,
		`min_manager_version: "`+domain.TextParametersMinManagerVersion+`"`)
}

// onTextRelease is a converged installation on 1.3.0 with the text parameters.
func onTextRelease(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: withTextParameters(t, stageUpgradeSource(t, h)),
	})
	require.NoError(t, err)
	return h
}

func configOps(t *testing.T, h *harness) int {
	t.Helper()
	records, err := h.Deps.State.Operations(context.Background(), ports.Filter{Type: domain.OpTypeConfig})
	require.NoError(t, err)
	return len(records)
}

// A text value is recorded as `text` normalises it, delivered whole as
// <PRODUCT>_PARAM_<NAME>, and its services are re-created; the summary counts
// its lines (decisions 2 and 5, §5.5).
func TestATextParameterIsRecordedAndDelivered(t *testing.T) {
	ctx := context.Background()
	h := onTextRelease(t)
	ups := len(h.Runtime.UpConfigs)

	result, err := ops.ConfigSet(ctx, h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"rules": "DOMAIN-SUFFIX,example.ru\r\n  indented\n"},
	})
	require.NoError(t, err)
	assert.Equal(t, "set rules (0 → 2 lines); re-created app", result.Summary)

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	want := "DOMAIN-SUFFIX,example.ru\n  indented"
	assert.Equal(t, want, inst.Parameters["rules"])

	require.Greater(t, len(h.Runtime.UpConfigs), ups, "nothing was re-created")
	cfg, opts := lastUp(t, h)
	assert.Equal(t, want, cfg.Env["DEMO_PARAM_RULES"])
	assert.Equal(t, "welcome\nto demo", cfg.Env["DEMO_PARAM_MOTD"],
		"the default reached Compose with the newline its `|` block left")
	assert.Equal(t, []string{"app"}, opts.Services)
	assert.True(t, opts.ForceRecreate)
}

// The same value with a final newline an editor added is the same value: no
// operation, nothing re-created.
func TestAnUnchangedTextValueChangesNothing(t *testing.T) {
	ctx := context.Background()
	h := onTextRelease(t)
	_, err := ops.ConfigSet(ctx, h.Deps, ops.ConfigSetOptions{Set: map[string]string{"rules": "a\nb"}})
	require.NoError(t, err)
	journaled, ups := configOps(t, h), len(h.Runtime.UpConfigs)

	result, err := ops.ConfigSet(ctx, h.Deps, ops.ConfigSetOptions{Set: map[string]string{"rules": "a\r\nb\n\n"}})
	require.NoError(t, err)
	assert.Equal(t, "no change: every value already matches", result.Summary)
	assert.Equal(t, journaled, configOps(t, h), "an unchanged value was journaled")
	assert.Len(t, h.Runtime.UpConfigs, ups, "an unchanged value re-created services")
}

// A value over the limit is refused before anything changes, and says why.
func TestATextValueOverTheLimitChangesNothing(t *testing.T) {
	ctx := context.Background()
	h := onTextRelease(t)
	journaled := configOps(t, h)

	_, err := ops.ConfigSet(ctx, h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"rules": strings.Repeat("a", int(domain.TextParameterLimit)+1)},
	})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, `parameter "rules": the value is `)
	assert.Equal(t, journaled, configOps(t, h))

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.NotContains(t, inst.Parameters, "rules")
}

// `config list` and `config get` show a default as the deployment receives it:
// parsed, so a text default's final newline is gone (decision 10).
func TestTheConfigReportShowsADefaultAsDelivered(t *testing.T) {
	h := onTextRelease(t)

	motd, err := ops.ConfigGet(context.Background(), h.Deps, "motd")
	require.NoError(t, err)
	assert.Equal(t, "welcome\nto demo", motd.Value)
	assert.Equal(t, "release", motd.Source)
	assert.Equal(t, domain.ParamText, motd.Type)

	port, err := ops.ConfigGet(context.Background(), h.Deps, "http_port")
	require.NoError(t, err)
	assert.Equal(t, "18080", port.Value)
}
