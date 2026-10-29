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
	"github.com/morzecrew/morzer/internal/release"
)

// RFC 0036: an update generates a secret the release it installs adds.

const webhookKey = "  - name: webhook_key\n" +
	"    description: Signs outbound webhooks\n" +
	"    required: true\n" +
	"    generator:\n      kind: hex\n      length: 32\n" +
	"    services: [app]\n"

// declareSecret appends a declaration to a bundle's secret schema.
func declareSecret(t *testing.T, bundle, decl string) {
	t.Helper()
	path := filepath.Join(bundle, "secrets.schema.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(string(raw), "\n"), "the schema no longer ends in a newline")
	require.NoError(t, os.WriteFile(path, append(raw, decl...), 0o644))
}

// generationMessages is what the operation said in every mode, which is where
// the generate-secrets step reports.
func generationMessages(h *harness) string {
	var out []string
	for _, e := range h.Events.OfKind(events.KindMessage) {
		out = append(out, e.Message)
	}
	return strings.Join(out, "\n")
}

func secretValue(t *testing.T, h *harness, name string) (string, bool) {
	t.Helper()
	set, err := h.Secrets.Load(context.Background())
	require.NoError(t, err)
	if !set.Has(name) {
		return "", false
	}
	value, _ := set.Get(name)
	return value.Reveal(), true
}

func TestAnUpdateGeneratesASecretTheNewReleaseAdds(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	src := stageUpgradeSource(t, h)
	declareSecret(t, src, webhookKey)

	result, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)

	value, ok := secretValue(t, h, "webhook_key")
	require.True(t, ok, "the secret 1.3.0 adds was not generated")
	assert.FileExists(t, filepath.Join(h.Paths.SecretsRenderDir(), "webhook_key"),
		"the generated secret was not rendered by the same update")
	assert.Contains(t, generationMessages(h), "generated 1 secret 1.3.0 adds: webhook_key")

	journal, err := os.ReadFile(h.Paths.JournalFile())
	require.NoError(t, err)
	assert.NotContains(t, string(journal), value, "the generated value reached the journal")
	assert.NotContains(t, generationMessages(h), value, "the generated value was reported")
}

// A secret both releases declare and this machine has no value for was left
// unset by a person, and an update keeps that choice (RFC 0036 §5.2).
func TestAnUpdateDoesNotGenerateASecretTheRunningReleaseDeclared(t *testing.T) {
	const metricsToken = "  - name: metrics_token\n" +
		"    required: false\n" +
		"    generator:\n      kind: hex\n      length: 32\n"

	h := newHarness(t)
	declareSecret(t, h.Release.Root, metricsToken)
	rel, err := release.Load(h.Release.Root)
	require.NoError(t, err)
	h.Release = rel
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	src := stageUpgradeSource(t, h)
	declareSecret(t, src, metricsToken)

	_, err = ops.Update(context.Background(), h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)
	_, ok := secretValue(t, h, "metrics_token")
	assert.False(t, ok, "an update filled a secret the running release declared and nobody set")
}

// Only the operator can supply a secret with no generator, and the update still
// stops before anything is rendered.
func TestARequiredSecretWithNoGeneratorStillStopsTheUpdate(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	src := stageUpgradeSource(t, h)
	declareSecret(t, src, "  - name: partner_api_key\n    required: true\n    services: [app]\n")

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required secret(s) not set: partner_api_key")
}

func TestAPlannedUpdateNamesTheSecretItWouldGenerate(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	src := stageUpgradeSource(t, h)
	declareSecret(t, src, webhookKey)

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: src, Options: ops.Options{DryRun: true},
	})
	require.NoError(t, err)

	plans := h.Events.OfKind(events.KindPlan)
	require.Len(t, plans, 1)
	var line string
	for _, step := range plans[0].Plan {
		if step.ID == "generate-secrets" {
			line = step.Description
			assert.True(t, step.WillRun, "the plan says nothing would be generated")
		}
	}
	assert.Contains(t, line, "would generate webhook_key")
	_, ok := secretValue(t, h, "webhook_key")
	assert.False(t, ok, "a plan generated a secret")
}

func TestAnUpdateToldNotToGenerateLeavesTheSecretToTheOperator(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	src := stageUpgradeSource(t, h)
	declareSecret(t, src, webhookKey)

	result, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: src, Options: ops.Options{NoGenerateSecrets: true},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required secret(s) not set: webhook_key")
	assert.Equal(t, "false", result.Record.Flags["generate_secrets"],
		"the choice not to generate is not in the journal")
	_, ok := secretValue(t, h, "webhook_key")
	assert.False(t, ok)
}

// A failed update keeps what it generated, so the retry does not generate a
// second value (RFC 0036 §5.4).
func TestAFailedUpdateKeepsTheSecretItGenerated(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)

	src := stageUpgradeSource(t, h)
	declareSecret(t, src, webhookKey)

	h.Runtime.Fail["Pull"] = domain.RuntimeError(nil, "injected: registry unreachable")
	result, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	assert.Equal(t, domain.StatusCompensated, result.Record.Status)

	first, ok := secretValue(t, h, "webhook_key")
	require.True(t, ok, "compensation removed the generated value")
	assert.NoFileExists(t, filepath.Join(h.Paths.SecretsRenderDir(), "webhook_key"),
		"1.2.0 is running again, and the file of a secret it does not declare is still rendered")

	delete(h.Runtime.Fail, "Pull")
	_, err = ops.Update(context.Background(), h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)
	again, _ := secretValue(t, h, "webhook_key")
	assert.Equal(t, first, again, "the retry generated a second value")
}
