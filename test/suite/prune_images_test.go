package suite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// RFC 0031: a pruned release's images leave the store with it, and only when no
// release directory left on the machine -- in any installation, whatever its
// role -- names them by any spelling the daemon resolves to the same name.

const (
	appRepo = "registry.example/demo/app"
	dbRepo  = "registry.example/demo/db"
)

// digestN is a pinned digest a test can tell from every other by n.
func digestN(n int) string { return fmt.Sprintf("sha256:%064x", n) }

func pulled(repo string, n int) string { return repo + "@" + digestN(n) }

// bundled is the manifest spelling of an image that travels in the bundle.
func bundled(repo string, n int) string { return "{ref: " + pulled(repo, n) + ", from: bundle}" }

// storeRelease writes the example bundle into store at version, with its two
// images spelled as given. Only the manifest has to load: that is all a prune
// reads.
func storeRelease(t *testing.T, store, version, app, db string) string {
	t.Helper()
	root := filepath.Join(store, version)
	copyBundle(t, testBundlePath(t), root)

	path := filepath.Join(root, release.ManifestFileName)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	manifest := string(raw)
	for _, edit := range [][2]string{
		{"  version: 1.2.0\n", "  version: " + version + "\n"},
		{"  app: " + pulled(appRepo, 1) + "\n", "  app: " + app + "\n"},
		{"  db: " + pulled(dbRepo, 2) + "\n", "  db: " + db + "\n"},
	} {
		require.Contains(t, manifest, edit[0], "the example manifest changed shape")
		manifest = strings.Replace(manifest, edit[0], edit[1], 1)
	}
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0o644))

	_, err = release.LoadManifest(path)
	require.NoError(t, err, "the fixture release does not load")
	return root
}

// otherInstallation puts a second product under the harness root -- the marker
// discovery looks for -- and returns its release store.
func (h *harness) otherInstallation(t *testing.T) string {
	t.Helper()
	other := domain.PathsUnder(h.Root, "other")
	require.NoError(t, os.MkdirAll(other.EtcDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(other.EtcDir, domain.InstallationFileName), nil, 0o644))
	return other.ReleasesDir()
}

// pruneable stores 1.3.0 with two names of its own, and a newer 1.4.0 naming
// only what the current release does, so a prune keeping one removes 1.3.0.
func (h *harness) pruneable(t *testing.T) {
	t.Helper()
	storeRelease(t, h.Paths.ReleasesDir(), "1.3.0", pulled(appRepo, 3), pulled(dbRepo, 3))
	storeRelease(t, h.Paths.ReleasesDir(), "1.4.0", pulled(appRepo, 1), pulled(dbRepo, 2))
}

func TestPruneRemovesOnlyTheImagesNoRemainingReleaseNames(t *testing.T) {
	h := newHarness(t)
	h.install()
	store := h.Paths.ReleasesDir()
	// 1.2.0 is current, with app@1 and db@2.
	storeRelease(t, store, "1.3.0", pulled(appRepo, 3), pulled(dbRepo, 2))
	storeRelease(t, store, "1.4.0", pulled(appRepo, 4), pulled(dbRepo, 4))
	storeRelease(t, store, "1.5.0", pulled(appRepo, 5), pulled(dbRepo, 4))

	res, err := ops.PruneReleases(context.Background(), h.Deps, 1, false)
	require.NoError(t, err)

	assert.Equal(t, []string{"1.4.0", "1.3.0"}, res.Removed)
	assert.Equal(t, []string{pulled(appRepo, 3), pulled(appRepo, 4)}, h.Runtime.RemovedImages,
		"db@2 is the current release's and db@4 the retained 1.5.0's")
	assert.Equal(t, h.Runtime.RemovedImages, res.Images.Removed)
	assert.Empty(t, res.Images.Kept)
	assert.Empty(t, res.Images.Skipped)
}

// TestPruneNeverRemovesWhatAnotherInstallationNames is the row the design is
// not "let the daemon refuse" for (RFC 0031 §4.8): the other installation's
// release has no container, only a name, and no role either.
//
// Both of its names are spelled differently from ours, as P1 measured is
// harmful: our bundled alias takes the other's pulled digest reference with
// it, and `postgres:17@D` is the daemon's `docker.io/library/postgres@D`.
func TestPruneNeverRemovesWhatAnotherInstallationNames(t *testing.T) {
	h := newHarness(t)
	h.install()
	ours, other := h.Paths.ReleasesDir(), h.otherInstallation(t)

	storeRelease(t, ours, "1.3.0", bundled(appRepo, 3), "postgres:17@"+digestN(6))
	storeRelease(t, ours, "1.3.5", pulled(appRepo, 9), pulled(dbRepo, 2))
	storeRelease(t, ours, "1.4.0", pulled(appRepo, 4), pulled(dbRepo, 4))
	storeRelease(t, other, "9.0.0", pulled(appRepo, 3), "docker.io/library/postgres@"+digestN(6))

	res, err := ops.PruneReleases(context.Background(), h.Deps, 1, false)
	require.NoError(t, err)

	assert.Equal(t, []string{"1.3.5", "1.3.0"}, res.Removed)
	assert.Equal(t, []string{pulled(appRepo, 9)}, h.Runtime.RemovedImages,
		"removed a name the other installation's release resolves by")
}

// Decision 5: a manifest nobody can read names images nobody can enumerate, so
// nothing is removed -- and the directories are still pruned.
func TestAnUnreadableReleaseAnywhereWithholdsImageRemoval(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.pruneable(t)

	// Another installation's fetch in progress: its images are about to be
	// pulled, and its manifest is not there yet.
	staging := filepath.Join(h.otherInstallation(t), ".staging-123")
	require.NoError(t, os.MkdirAll(staging, 0o755))

	res, err := ops.PruneReleases(context.Background(), h.Deps, 1, false)
	require.NoError(t, err)

	assert.Equal(t, []string{"1.3.0"}, res.Removed)
	assert.NoDirExists(t, filepath.Join(h.Paths.ReleasesDir(), "1.3.0"))
	assert.Empty(t, h.Runtime.RemovedImages)
	assert.Contains(t, res.Images.Skipped, staging, "the report must name what to remove")
}

func TestAnUndecidableDirectoryWithholdsImageRemoval(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root traverses a 0000 directory, so this arrangement proves nothing")
	}
	h := newHarness(t)
	h.install()
	h.pruneable(t)

	sealed := filepath.Join(h.Root, "etc", "sealed")
	require.NoError(t, os.MkdirAll(sealed, 0o755))
	require.NoError(t, os.Chmod(sealed, 0o000))
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })

	res, err := ops.PruneReleases(context.Background(), h.Deps, 1, false)
	require.NoError(t, err)

	assert.Equal(t, []string{"1.3.0"}, res.Removed)
	assert.Empty(t, h.Runtime.RemovedImages)
	assert.Contains(t, res.Images.Skipped, sealed)
}

// Decision 7: directories first. A prune that could not remove a directory
// leaves that release a rollback target, so its images have to stay.
func TestAPruneThatCannotRemoveADirectoryRemovesNoImage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes from a 0555 directory, so this arrangement proves nothing")
	}
	h := newHarness(t)
	h.install()
	h.pruneable(t)
	store := h.Paths.ReleasesDir()
	require.NoError(t, os.Chmod(store, 0o555))
	t.Cleanup(func() { _ = os.Chmod(store, 0o755) })

	_, err := ops.PruneReleases(context.Background(), h.Deps, 1, false)
	require.Error(t, err)
	assert.Empty(t, h.Runtime.RemovedImages)
}

// Decision 8: an image the runtime will not remove is reported, and the prune
// still succeeds, because the directories were the operation.
func TestAnImageInUseIsKeptAndThePruneSucceeds(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.pruneable(t)
	h.Runtime.ImagesInUse = []string{pulled(appRepo, 3)}

	res, err := ops.PruneReleases(context.Background(), h.Deps, 1, false)
	require.NoError(t, err)

	assert.Equal(t, []string{pulled(dbRepo, 3)}, res.Images.Removed)
	assert.Equal(t, []ops.KeptImage{{Ref: pulled(appRepo, 3), Why: "a container uses it"}}, res.Images.Kept)
}

func TestARuntimeThatCannotRemoveImagesSaysSo(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.pruneable(t)
	h.Deps.Runtime = struct{ ports.Runtime }{h.Runtime}

	res, err := ops.PruneReleases(context.Background(), h.Deps, 1, false)
	require.NoError(t, err)

	assert.Equal(t, []string{"1.3.0"}, res.Removed)
	assert.Equal(t, "this runtime cannot remove images", res.Images.Skipped)
	assert.Empty(t, res.Images.Removed)
}

func TestADryRunListsTheImagesAndRemovesNothing(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.pruneable(t)

	res, err := ops.PruneReleasesLocked(context.Background(), h.Deps, 1, ops.Options{DryRun: true})
	require.NoError(t, err)

	assert.True(t, res.DryRun, "a plan reads the same as an act without it")
	assert.Equal(t, []string{"1.3.0"}, res.Removed)
	assert.Equal(t, []string{pulled(appRepo, 3), pulled(dbRepo, 3)}, res.Images.Removed)
	assert.DirExists(t, filepath.Join(h.Paths.ReleasesDir(), "1.3.0"))
	assert.NotContains(t, h.Runtime.Calls, "RemoveImage")
}

// Decision 11: `release prune` takes the deployment lock, so it refuses beside
// an update exactly as `update` does.
func TestReleasePruneRefusesWhileTheDeploymentIsLocked(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.pruneable(t)
	ctx := context.Background()

	unlock, err := h.Locker.Acquire(ctx, "deployment", ports.LockOptions{
		Owner: ports.LockOwner{PID: 4321, Type: "update"},
	})
	require.NoError(t, err)
	defer func() { _ = unlock() }()

	_, err = ops.PruneReleasesLocked(ctx, h.Deps, 1, ops.Options{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrLocked), "got %v", err)
	assert.DirExists(t, filepath.Join(h.Paths.ReleasesDir(), "1.3.0"))
	assert.Empty(t, h.Runtime.RemovedImages)
}

// Dev mode's prune is a step of `update`, which holds the lock already; a prune
// that took it itself would refuse its own caller here.
func TestADevModeUpdateRemovesThePrunedReleasesImages(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	inst := h.install()
	require.NoError(t, os.Remove(h.Paths.InstallationState()))
	inst.Mode = domain.ModeDev
	inst.Policy.RetainReleases = 1
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	h.setHookEnv()
	applyBaseline(t, h)

	storeRelease(t, h.Paths.ReleasesDir(), "1.0.0", pulled(appRepo, 10), pulled(dbRepo, 2))
	storeRelease(t, h.Paths.ReleasesDir(), "1.1.0", pulled(appRepo, 11), pulled(dbRepo, 2))

	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.NoError(t, err)
	require.Equal(t, domain.StatusSucceeded, result.Record.Status)

	assert.NoDirExists(t, filepath.Join(h.Paths.ReleasesDir(), "1.0.0"))
	assert.Equal(t, []string{pulled(appRepo, 10)}, h.Runtime.RemovedImages,
		"1.1.0 is retained, and 1.2.0 is the previous release")
	assert.True(t, slices.Contains(h.Runtime.Calls, "RemoveImage"))
}
