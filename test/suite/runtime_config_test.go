package suite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
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

// capturingBackup refuses as the real engine does when the runtime has no
// volume helper: the fake engine never reads a volume, so it could not see one
// missing.
type capturingBackup struct {
	*fakes.Backup
	rt *fakes.Runtime
}

func (b capturingBackup) Create(ctx context.Context, scope ports.Scope, labels map[string]string) (ports.BackupRef, error) {
	if b.rt.HelperMissing {
		return ports.BackupRef{}, domain.RuntimeError(domain.ErrToolMissing,
			"the volume helper image %s is not on this machine", b.rt.HelperImage())
	}
	return b.Backup.Create(ctx, scope, labels)
}

// An update takes its backup before it pulls anything, so the pull step came
// too late for a machine without the helper: one installed before apply
// pulled it, or one whose unused busybox `docker image prune -a` removed. The
// update about to reach a registry fetches the helper before the backup.
func TestAnUpdatePullsTheVolumeHelperBeforeItsBackup(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	h.Deps.Backup = capturingBackup{Backup: fakes.NewBackup(), rt: h.Runtime}
	h.Runtime.HelperMissing = true

	_, err := ops.Update(context.Background(), h.Deps,
		ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.NoError(t, err, "the pre-update backup refused for want of a helper the update could have pulled")
	assert.False(t, h.Runtime.HelperMissing)
}

// pinnedHelper reports a helper override that capture would refuse, the way
// the Compose adapter judges one.
type pinnedHelper struct {
	*fakes.Runtime
	ref    string
	pinned bool
}

func (r pinnedHelper) HelperImage() string     { return r.ref }
func (r pinnedHelper) HelperImagePinned() bool { return r.pinned }

// An override capture refuses is not pulled. `name@anything` names something
// after the `@`, and is still not a digest every capture accepts.
func TestAMalformedHelperOverrideIsNotPulled(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	h.Deps.Runtime = pinnedHelper{Runtime: h.Runtime, ref: "busybox@not-a-digest"}

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.NotContains(t, h.Runtime.PulledImages, "busybox@not-a-digest",
		"an override every capture refuses was pulled, and its failure warned on every apply")
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
