package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/release"
)

// A smoke test asks whether the product answers on its public name, so a hook
// is told the domains as Compose and the templates are (#288).

// smokeTestReportingDomains replaces the release's smoke test with one that
// records what it was told, one line per run.
func smokeTestReportingDomains(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	script := `#!/bin/sh
printf '%s|%s|%s\n' "${DEMO_DOMAIN-unset}" "${DEMO_DOMAINS-unset}" "${DEMO_URL-unset}" >> "$DEMO_DATA_DIR/smoke-env"
printf '{"message":"smoke test passed"}' >&"${DEMO_RESULT_FD:-3}"
`
	require.NoError(t, os.WriteFile(filepath.Join(h.Release.Root, "hooks", "smoke-test"), []byte(script), 0o755))
	var err error
	h.Release, err = release.Load(h.Release.Root)
	require.NoError(t, err)
	h.install()
	h.setHookEnv()
	return h
}

func lastSmokeEnv(t *testing.T, h *harness) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.Paths.DataDir(), "smoke-env"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	return lines[len(lines)-1]
}

func TestASmokeTestIsToldTheDomains(t *testing.T) {
	ctx := context.Background()
	h := smokeTestReportingDomains(t)

	applyBaseline(t, h)
	assert.Equal(t, "demo.example|demo.example|https://demo.example", lastSmokeEnv(t, h))

	_, err := setSetting(t, h, false, map[string]string{"installation.domains": "a.test,b.test"})
	require.NoError(t, err)
	applyBaseline(t, h)
	assert.Equal(t, "a.test|a.test,b.test|https://a.test", lastSmokeEnv(t, h),
		"the next hook was told the domains from before the change")

	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Unset: []string{"installation.domains"}})
	require.NoError(t, err)
	applyBaseline(t, h)
	assert.Equal(t, "unset|unset|unset", lastSmokeEnv(t, h), "an installation with no domain was told one")
}
