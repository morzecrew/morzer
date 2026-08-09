package suite

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// A bundled image is the one case where the reference a manifest pins is not
// the reference the deployment uses. Everything here is about that gap and the
// guard that keeps it from being papered over: the daemon cannot be made to
// resolve `registry.example/demo/app@sha256:…` for a repository it never
// pulled from, so ingest leaves an alias behind, and an image that is *not*
// loaded has to be a refusal rather than a pull.

// bundleImage rewrites the harness release so `app` travels in the bundle, and
// writes the layout that has to accompany it.
//
// The layout is real enough for `release.Load` to accept: an oci-layout
// marker, an index naming the digest the manifest pins, and a blob file at
// that digest -- which is the completeness rule the verifier already enforces
// in both directions.
func (h *harness) bundleImage(t *testing.T) (ref, alias string) {
	t.Helper()
	ref, alias = bundleImageIn(t, h.Release.Root)

	// Re-read, so the harness holds the release the operations will load.
	reloaded, err := release.Load(h.Release.Root)
	require.NoError(t, err, "the bundled fixture is not a valid release")
	h.Release = reloaded
	return ref, alias
}

// bundleImageIn marks `app` as travelling in the bundle at dir, for a bundle
// the harness has not adopted -- an update source, above all.
func bundleImageIn(t *testing.T, dir string) (ref, alias string) {
	t.Helper()

	manifestPath := filepath.Join(dir, release.ManifestFileName)
	data, err := os.ReadFile(manifestPath)
	require.NoError(t, err)

	loaded, err := release.Load(dir)
	require.NoError(t, err)
	ref = loaded.Manifest.Images["app"].Ref
	digest, ok := domain.ImageSpec{Ref: ref}.Digest()
	require.True(t, ok, "the fixture's app image is not pinned by digest")

	old := "  app: " + ref + "\n"
	require.Contains(t, string(data), old, "the fixture no longer spells app as a scalar")
	require.NoError(t, os.WriteFile(manifestPath,
		[]byte(strings.Replace(string(data), old,
			"  app:\n    ref: "+ref+"\n    from: bundle\n", 1)), 0o644))

	images := filepath.Join(dir, release.ImagesDirName)
	algorithm, encoded, _ := strings.Cut(digest, ":")
	blob := filepath.Join(images, "blobs", algorithm, encoded)
	require.NoError(t, os.MkdirAll(filepath.Dir(blob), 0o755))
	require.NoError(t, os.WriteFile(blob, []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(images, release.ImageLayoutMarkerFileName),
		[]byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644))

	index, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest":    digest,
			"size":      2,
		}},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(images, release.ImageIndexFileName), index, 0o644))

	// A valid release, or nothing below is testing what it claims.
	_, err = release.Load(dir)
	require.NoError(t, err, "the bundled fixture is not a valid release")

	alias, ok = domain.ImageSpec{Ref: ref, From: domain.ImageFromBundle}.LocalAlias()
	require.True(t, ok)
	return ref, alias
}

// TestApplyRefusesWhenABundledImageIsNotLoaded.
//
// The load-bearing refusal of the whole scheme. A bundled image is deployed
// under a tag the manager creates, and a tag is mutable -- so if this were a
// warning, Compose would resolve the missing image the only way it can, by
// asking the vendor's registry for whatever that tag points at. A
// digest-pinned deployment would then be running bytes nobody verified, which
// is the failure digest pinning exists to prevent.
func TestApplyRefusesWhenABundledImageIsNotLoaded(t *testing.T) {
	h := newHarness(t)
	ref, _ := h.bundleImage(t)
	h.install()
	h.setHookEnv()

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.Error(t, err, "a release whose bundled image is absent was converged")

	assert.Contains(t, err.Error(), "demo/app",
		"the refusal must name the image an operator has to act on")
	assert.Contains(t, err.Error(), "release ingest",
		"the refusal must name the one command that fixes it")

	// And nothing was pulled in its place, which is the half a warning
	// would have got wrong.
	assert.NotContains(t, h.Runtime.PulledImages, ref,
		"a bundled image was fetched from the registry the bundle exists to avoid")
	assert.Zero(t, h.Runtime.UpCount, "services were started without their image")
}

// TestApplyPullsOnlyTheImagesARegistryServes.
//
// After ingest the bundled image is present, so the converge proceeds -- and
// the pull must still exclude it. Passing it to Pull would contact the
// vendor's registry for bytes already on the machine, under a reference that
// no longer resolves anywhere.
func TestApplyPullsOnlyTheImagesARegistryServes(t *testing.T) {
	h := newHarness(t)
	ref, alias := h.bundleImage(t)
	h.install()
	h.setHookEnv()

	_, err := ops.IngestImages(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	_, err = ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	assert.NotContains(t, h.Runtime.PulledImages, ref,
		"the bundled image was pulled from its registry")
	assert.NotContains(t, h.Runtime.PulledImages, alias,
		"the alias was pulled, which no registry has ever served")
	assert.ElementsMatch(t, h.Release.Manifest.PulledImageRefs(), h.Runtime.PulledImages,
		"the pull must be exactly the images a registry serves")

	// The other half: what was pulled is not empty, so this is not passing
	// because nothing was pulled at all.
	assert.NotEmpty(t, h.Runtime.PulledImages,
		"the release's registry-sourced images were not pulled either")
}

// TestTheDeploymentResolvesTheAliasAndNotTheManifestReference.
//
// Half regression guard: an implementation that quietly went back to handing
// Compose the manifest's reference would pass any test that only checked the
// alias was created somewhere. The reference must be absent from the value the
// runtime is given, because that value is what the daemon looks up.
func TestTheDeploymentResolvesTheAliasAndNotTheManifestReference(t *testing.T) {
	h := newHarness(t)
	ref, alias := h.bundleImage(t)
	inst := h.install()

	cfg, err := h.Deps.RuntimeConfigFor(h.Release, inst)
	require.NoError(t, err)

	assert.Equal(t, alias, cfg.Env["DEMO_IMAGE_APP"],
		"a bundled image must be deployed under the alias ingest leaves behind")
	assert.NotEqual(t, ref, cfg.Env["DEMO_IMAGE_APP"],
		"the manifest's reference does not resolve for a repository the daemon "+
			"never pulled from, so handing it to Compose deploys nothing")

	// The images that are not bundled are untouched, which is what keeps
	// this from being a change to every release.
	assert.Equal(t, h.Release.Manifest.Images["db"].Ref, cfg.Env["DEMO_IMAGE_DB"])
}

// TestIngestIsIdempotentAndReadsTheBundleItStages.
func TestIngestIsIdempotentAndReadsTheBundleItStages(t *testing.T) {
	h := newHarness(t)
	ref, alias := h.bundleImage(t)
	h.install()

	_, err := ops.IngestImages(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	assert.Equal(t, []string{ref}, h.Runtime.IngestedRefs,
		"ingest is asked for the manifest's reference, which is what addresses the layout")
	assert.Equal(t, filepath.Join(h.Release.Root, release.ImagesDirName), h.Runtime.IngestedFrom,
		"ingest must read the layout inside the staged release")

	present, err := h.Runtime.HasImage(context.Background(), alias)
	require.NoError(t, err)
	assert.True(t, present, "the alias is not resolvable after an ingest")

	// A second run has nothing to do. The step's Check asks the store
	// rather than trusting a flag, so this also proves the postcondition is
	// expressed in terms of what is on the machine.
	before := len(h.Runtime.IngestedRefs)
	_, err = ops.IngestImages(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Equal(t, before, len(h.Runtime.IngestedRefs),
		"a second ingest re-read images that were already loaded")
}

// TestARelease_ThatBundlesNothingIngestsNothing.
//
// The common case. An ordinary release must not acquire a step that opens a
// directory it does not have.
func TestAReleaseThatBundlesNothingIngestsNothing(t *testing.T) {
	h := newHarness(t)
	h.install()

	res, err := ops.IngestImages(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Contains(t, res.Summary, "bundles no images")
	assert.Empty(t, h.Runtime.IngestedRefs)
}

// runtimeOnly hides every optional capability the Compose adapter happens to
// have, leaving a runtime that can converge but has no local image store.
//
// Embedding the interface rather than the fake: a wrapper that forwarded the
// optional methods would still satisfy the type assertions, and what is under
// test is what happens when they fail.
type runtimeOnly struct{ ports.Runtime }

// TestARuntimeThatCannotIngestRefusesRatherThanSkipping.
//
// The release says its images travel in the bundle. A runtime with no image
// store will not find them anywhere else, so converging would fail later and
// further from the cause -- inside Compose, about a name nothing can resolve.
func TestARuntimeThatCannotIngestRefusesRatherThanSkipping(t *testing.T) {
	h := newHarness(t)
	h.bundleImage(t)
	h.install()
	h.Deps.Runtime = runtimeOnly{h.Runtime}

	_, err := ops.IngestImages(context.Background(), h.Deps, ops.Options{})
	require.Error(t, err, "a runtime with no image store reported the images as loaded")
	assert.Contains(t, err.Error(), "cannot load images out of a bundle")

	// The remedy is on the cause, not on the error the engine returns: a
	// step whose policy is Compensate is classified as `Compensated` once
	// rollback succeeds, and that classification carries the exit code
	// rather than the diagnosis. Asserting the top-level Hint here would
	// be asserting the engine's contract, not this step's.
	assert.Contains(t, domain.AsError(errors.Unwrap(err)).Hint, "from: bundle",
		"the remedy must say which manifest field put the manager here")

	// And nothing was ingested behind the refusal.
	assert.Empty(t, h.Runtime.IngestedRefs)
}

// TestAFailedIngestLeavesThePreviousReleaseCurrent.
//
// The invariant TestUpdateFaultInjection asserts for every other step, applied
// to this one: no partial update leaves the manager pointing at a release it
// did not finish installing. It matters more here than elsewhere, because the
// half-installed state is specifically a release whose images are absent --
// and the preflight added in this branch then refuses every apply against it.
func TestAFailedIngestLeavesThePreviousReleaseCurrent(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	before, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	require.Equal(t, "1.2.0", before.Version.String())

	src := stageUpgradeSource(t, h)
	bundleImageIn(t, src)
	h.Runtime.Fail["IngestImages"] = domain.RuntimeError(nil, "injected: the layout is unreadable")

	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err, "the injected failure must fail the update")

	after, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, before.Version.String(), after.Version.String(),
		"a failed ingest must leave the release that was running current")
	assert.Equal(t, before.Digest, after.Digest)

	link, err := os.Readlink(h.Paths.CurrentLink())
	require.NoError(t, err)
	assert.Equal(t, before.Root, link, "the symlink must agree with the pointer")
}

// TestAFailedIngestDuringInitUnwindsTheInstallation.
//
// `init` is the one operation that adopts a release *before* ingest runs:
// stepStageRelease writes the symlink and the current-release record inside its
// own Execute. So a failure here, unlike one during `update`, has something to
// unwind -- and leaving it would produce a machine whose installation names a
// current release that no `apply` will converge, because the preflight this
// branch added refuses every one of them.
func TestAFailedIngestDuringInitUnwindsTheInstallation(t *testing.T) {
	h := newHarness(t)
	h.setHookEnv()
	realIdentity(t, h)

	// A release directory the init will stage from, with its images bundled.
	src := filepath.Join(t.TempDir(), "bundle-1.2.0")
	copyBundle(t, testBundlePath(t), src)
	retargetManifest(t, src, h.Root)
	bundleImageIn(t, src)

	h.Runtime.Fail["IngestImages"] = domain.RuntimeError(nil, "injected: the layout is unreadable")

	_, err := ops.Init(context.Background(), h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, ReleasePath: src,
	})
	require.Error(t, err, "init reported success after the ingest step failed")

	// The installation is unwound, so the machine does not present as
	// installed with a release that cannot converge. This is what
	// OnFailure: Compensate buys -- with Abort, both survived.
	exists, err := h.Deps.State.InstallationExists(context.Background())
	require.NoError(t, err)
	assert.False(t, exists, "a failed init left an installation behind")
	assert.NoDirExists(t, stagedReleaseRoot(h), "a failed init left the staged release behind")

	// What is *not* asserted, because it is not true and pretending
	// otherwise would hide it: `current-release.json` and the `current`
	// symlink survive. stepStageRelease writes all three in one Execute and
	// its compensator removes only the directory, which is pre-existing --
	// every init step after it has always left the same residue. Un-adopting
	// safely needs the compensator to know whether *this* Execute created
	// the pointer, since `init --repair` runs over an installation that
	// already had one. Harmless here: with the installation gone the machine
	// reads as uninstalled and nothing consults the record.
	stale, err := h.Deps.State.CurrentRelease(context.Background())
	require.NoError(t, err)
	assert.False(t, stale.IsZero(),
		"the stale pointer is gone -- if stepStageRelease learned to un-adopt, "+
			"delete this assertion and assert IsZero above instead")
}

// stagedReleaseRoot is where init stages the 1.2.0 fixture.
func stagedReleaseRoot(h *harness) string {
	return filepath.Join(h.Paths.ReleasesDir(), "1.2.0")
}
