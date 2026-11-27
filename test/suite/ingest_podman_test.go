//go:build podman

package suite

import (
	"context"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/runtime/quadlet"
	"github.com/morzecrew/morzer/internal/domain"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
)

// The Quadlet adapter's ingest against this user's
// rootless Podman. What a fake cannot say is what Podman's store holds
// afterwards.

// The vendor's repository, which nothing here contacts.
const quadletVendorRepo = "registry.invalid/morzer-test/quadlet-app"

// podmanLayout writes the suite's busybox into an OCI layout with Podman's
// own transport, and returns the layout and the reference a manifest would
// pin. The digest is the layout's: `podman push` writes the manifest anew.
func podmanLayout(t *testing.T) (layoutDir, vendorRef string) {
	t.Helper()
	requirePodman(t)
	pullPodman(t, quadletBusybox)

	dir := filepath.Join(t.TempDir(), "images")
	out, err := osexec.Command("podman", "push", "--quiet", quadletBusybox, "oci:"+dir).CombinedOutput()
	require.NoError(t, err, "podman push to a layout: %s", out)

	raw, err := os.ReadFile(filepath.Join(dir, "index.json"))
	require.NoError(t, err)
	var index struct {
		Manifests []struct{ Digest string } `json:"manifests"`
	}
	require.NoError(t, json.Unmarshal(raw, &index))
	require.Len(t, index.Manifests, 1)
	return dir, quadletVendorRepo + "@" + index.Manifests[0].Digest
}

// untagAfter removes the alias a test's ingest leaves, by name only.
func untagAfter(t *testing.T, alias string) {
	t.Cleanup(func() { _ = osexec.Command("podman", "rmi", alias).Run() })
}

// After an ingest the alias resolves, a second ingest of the same bundle
// opens nothing, and no loopback name is left in the store.
func TestQuadletIngestLeavesTheAliasAndNoLoopbackName(t *testing.T) {
	layoutDir, vendorRef := podmanLayout(t)
	alias, ok := domain.ImageSpec{Ref: vendorRef, From: domain.ImageFromBundle}.LocalAlias()
	require.True(t, ok)
	untagAfter(t, alias)

	r := quadlet.New(infraexec.New())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	before, err := r.HasImage(ctx, alias)
	require.NoError(t, err)
	require.False(t, before, "the alias was here before the ingest")

	require.NoError(t, r.IngestImages(ctx, layoutDir, []string{vendorRef}))
	present, err := r.HasImage(ctx, alias)
	require.NoError(t, err)
	assert.True(t, present, "the alias does not resolve, so no unit can start")

	// The vendor's reference resolves too, as on Docker's containerd store:
	// the alias names the vendor's repository, and the image carries the
	// pinned digest (Podman 6.1.1). The alias is still what a unit runs.
	vendorPresent, err := r.HasImage(ctx, vendorRef)
	require.NoError(t, err)
	assert.True(t, vendorPresent,
		"Podman no longer resolves the vendor's reference after an ingest")

	// A directory that is not a layout: a second ingest that opened it
	// would fail.
	require.NoError(t, r.IngestImages(ctx, t.TempDir(), []string{vendorRef}),
		"an image already here was loaded again")

	out, err := osexec.Command("podman", "images", "--format", "{{.Repository}}").CombinedOutput()
	require.NoError(t, err, "%s", out)
	for _, repo := range strings.Split(string(out), "\n") {
		repo = strings.TrimSpace(repo)
		if strings.HasPrefix(repo, "127.0.0.1:") && strings.HasSuffix(repo, "/morzer-test/quadlet-app") {
			t.Errorf("a loopback name outlived the ingest that made it: %s", repo)
		}
	}
}

// A manifest whose bytes are not its digest is refused, the refusal names
// the digest, and no alias is made for it.
func TestQuadletIngestRefusesABlobThatIsNotItsDigest(t *testing.T) {
	layoutDir, vendorRef := podmanLayout(t)
	vendorRef = unknownDigestForCorruptBytes(t, layoutDir, vendorRef)
	alias, _ := domain.ImageSpec{Ref: vendorRef, From: domain.ImageFromBundle}.LocalAlias()
	untagAfter(t, alias)

	r := quadlet.New(infraexec.New())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	err := r.IngestImages(ctx, layoutDir, []string{vendorRef})
	require.Error(t, err, "a bundle whose bytes are not what its index claims was accepted")
	assert.Contains(t, err.Error(), "sha256:", "the refusal does not name the blob")

	present, hasErr := r.HasImage(ctx, alias)
	require.NoError(t, hasErr)
	assert.False(t, present, "an alias was made for bytes that were refused")
}
