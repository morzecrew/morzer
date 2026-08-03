package suite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// The manifest's images are what a release *is*. These lock in the connection
// between declaring them and running them, which was missing until the
// acceptance run against real Docker exposed it: the pull ignored the list it
// was given, and the Compose file's `${DEMO_IMAGE_APP:-…}` never resolved to
// anything but its placeholder default.
//
// Every fake-backed test passed throughout, because a fake Runtime records the
// images it is handed rather than resolving a Compose file. So these assert the
// two halves the fake *can* see, and the acceptance scenario covers the rest.

func TestRuntimeConfigExportsEveryManifestImage(t *testing.T) {
	h := newHarness(t)
	inst := h.install()

	cfg, err := h.Deps.RuntimeConfigFor(h.Release, inst)
	require.NoError(t, err)

	// Named exactly as the example bundle's Compose file interpolates them.
	// A rename here silently returns every service to its placeholder image,
	// which is why the assertion is on the literal variable names.
	assert.Equal(t, h.Release.Manifest.Images["app"], cfg.Env["DEMO_IMAGE_APP"],
		"the Compose file interpolates DEMO_IMAGE_APP; without it the release "+
			"runs whatever default that file carries")
	assert.Equal(t, h.Release.Manifest.Images["db"], cfg.Env["DEMO_IMAGE_DB"])

	for name, ref := range h.Release.Manifest.Images {
		assert.Contains(t, cfg.Env, "DEMO_IMAGE_"+upper(name),
			"every declared image must be reachable from the topology file")
		assert.Contains(t, ref, "@sha256:",
			"the exported reference must be the pinned one, not a tag")
	}
}

func TestApplyPullsTheImagesTheManifestPins(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	// Not "some images were pulled": the exact set the manifest declares.
	// The defect this guards against was a pull that took this list as an
	// argument and then pulled whatever the Compose file happened to name.
	assert.ElementsMatch(t, h.Release.Manifest.ImageRefs(), h.Runtime.PulledImages,
		"the pull must follow the manifest, which is the authority on what a "+
			"release consists of")
}

// upper is the same transformation runtimeConfig applies to an image key.
func upper(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			out = append(out, r-32)
		case r == '-' || r == '.':
			out = append(out, '_')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
