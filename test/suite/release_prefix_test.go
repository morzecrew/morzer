package suite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/release"
)

// An installation may be named other than its release, as two installations of
// one release on a machine are. Its files and hooks still read the variables
// under the release's metadata.name, which is what their author wrote against;
// the installation's own name stays on its paths, its project and PRODUCT
// (#298).

func TestAnInstallationNamedOtherThanItsReleaseKeepsTheReleasesPrefix(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	inst.Product = "sandbox"

	cfg, err := h.Deps.RuntimeConfigFor(h.Release, inst)
	require.NoError(t, err)

	for _, name := range []string{"DEMO_DATA_DIR", "DEMO_SECRETS_DIR", "DEMO_PARAM_HTTP_PORT", "DEMO_IMAGE_APP"} {
		assert.Contains(t, cfg.Env, name)
	}
	for name := range cfg.Env {
		assert.False(t, strings.HasPrefix(name, "SANDBOX_"), "%s is under the installation's name", name)
	}
	assert.Equal(t, "sandbox", cfg.Product, "the project is the installation's")
}

func TestAHookOfAnInstallationNamedOtherThanItsReleaseReadsTheReleasesPrefix(t *testing.T) {
	h := newHarness(t)
	script := `#!/bin/sh
printf '%s|%s|%s\n' "${DEMO_PRODUCT-unset}" "${DEMO_RELEASE_VERSION-unset}" "${SANDBOX_PRODUCT-unset}" >> "$DEMO_DATA_DIR/smoke-env"
printf '{"message":"smoke test passed"}' >&"${DEMO_RESULT_FD:-3}"
`
	require.NoError(t, os.WriteFile(filepath.Join(h.Release.Root, "hooks", "smoke-test"), []byte(script), 0o755))
	var err error
	h.Release, err = release.Load(h.Release.Root)
	require.NoError(t, err)
	inst := h.install()
	inst.Product = "sandbox"
	require.NoError(t, h.Deps.State.SaveInstallation(t.Context(), inst))
	h.setHookEnv()

	applyBaseline(t, h)

	assert.Equal(t, "sandbox|1.2.0|unset", lastSmokeEnv(t, h),
		"the hook was told its variables under the installation's name")
}
