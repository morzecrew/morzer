package suite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
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
	assert.Equal(t, h.Release.Manifest.Images["app"].Ref, cfg.Env["DEMO_IMAGE_APP"],
		"the Compose file interpolates DEMO_IMAGE_APP; without it the release "+
			"runs whatever default that file carries")
	assert.Equal(t, h.Release.Manifest.Images["db"].Ref, cfg.Env["DEMO_IMAGE_DB"])

	for name, spec := range h.Release.Manifest.Images {
		assert.Contains(t, cfg.Env, "DEMO_IMAGE_"+upper(name),
			"every declared image must be reachable from the topology file")
		assert.Contains(t, spec.Ref, "@sha256:",
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
	// The volume helper is the manager's own image, not the release's.
	assert.ElementsMatch(t,
		append(h.Release.Manifest.ImageRefs(), h.Runtime.HelperImage()), h.Runtime.PulledImages,
		"the pull must follow the manifest, which is the authority on what a "+
			"release consists of")
}

// Nothing pulled the helper, so a networked machine's first backup refused
// with the pull command (#142), the pre-update backup included. An apply that
// reached a registry for the release's images pulls it too.
func TestAnApplyThatPullsAlsoPullsTheVolumeHelper(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	h.Runtime.HelperMissing = true

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	assert.Contains(t, h.Runtime.PulledImages, h.Runtime.HelperImage())
	assert.False(t, h.Runtime.HelperMissing,
		"the first backup on this machine would refuse for want of the helper")
}

// A registry that does not carry the helper -- a mirror that serves only the
// vendor's images -- leaves doctor's warning to say so. The release's images
// were pulled, and the deployment they make up runs without the helper.
func TestAnApplyThatCannotPullTheVolumeHelperStillSucceeds(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	h.Runtime.HelperMissing = true

	// The first pull is the release's; the hook arms the failure for the
	// one after it, which is the helper's.
	h.Runtime.OnCall = func(method string) {
		if method == "Pull" {
			h.Runtime.Fail["Pull"] = domain.RuntimeError(nil, "injected: not on this mirror")
		}
	}

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err, "an apply failed over the image backups read volumes through")
	assert.True(t, h.Runtime.HelperMissing)
	assert.Positive(t, h.Runtime.UpCount, "the release's services did not start")
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
