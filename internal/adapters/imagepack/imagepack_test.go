package imagepack_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"

	"github.com/morzecrew/morzer/internal/adapters/imagepack"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/release"
)

// content builds a descriptor for bytes.
func content(mediaType string, data []byte) ocispec.Descriptor {
	return ocispec.Descriptor{
		MediaType: mediaType,
		Digest:    digest.FromBytes(data),
		Size:      int64(len(data)),
	}
}

func specVersioned() specs.Versioned { return specs.Versioned{SchemaVersion: 2} }

func digestOfBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sortStrings(s []string) { sort.Strings(s) }

// What is worth proving here is that the copy produces a layout carrying the
// digest the source reported -- the property the whole bundled-image design
// rests on, and the one `docker save` does not have -- and that the comparison
// against the manifest's pin actually fires.
//
// Both need a real copy into a real layout. Neither needs a network, so the
// source is an in-memory store holding a real image manifest.

func TestPackWritesALayoutCarryingThePinnedDigest(t *testing.T) {
	dir := t.TempDir()
	src, digest := imageInMemory(t, "hello from the vendor")

	m := manifestWith(map[string]domain.ImageSpec{
		"app": {Ref: "registry.example/demo/app@" + digest, From: domain.ImageFromBundle},
	})

	packed, err := imagepack.New("")
	if err != nil {
		t.Fatal(err)
	}
	names, err := packed.WithSource(constantSource(src)).Pack(context.Background(), dir, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "app" {
		t.Fatalf("packed %v, want [app]", names)
	}

	// The layout answers by the digest the manifest pins, which is what
	// makes a bundled image satisfy the reference the runtime resolves.
	digests, err := release.ImageLayoutDigests(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(digests) != 1 || digests[0] != digest {
		t.Errorf("layout carries %v, want [%s]", digests, digest)
	}

	// And the blob is really there, rather than an index pointing at
	// nothing.
	blob := filepath.Join(dir, "images", "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
	if _, err := os.Stat(blob); err != nil {
		t.Errorf("the manifest blob was not written: %v", err)
	}
}

// TestPackRefusesAnImageTheSourceDoesNotHave.
//
// The ordinary failure: a reference that resolves to nothing. Named for what it
// actually exercises -- the copy failing -- because an earlier version of this
// test claimed to prove the digest comparison and never reached it.
func TestPackRefusesAnImageTheSourceDoesNotHave(t *testing.T) {
	dir := t.TempDir()
	src, _ := imageInMemory(t, "hello from the vendor")

	absent := "sha256:" + strings.Repeat("a", 64)
	m := manifestWith(map[string]domain.ImageSpec{
		"app": {Ref: "registry.example/demo/app@" + absent, From: domain.ImageFromBundle},
	})

	packed, _ := imagepack.New("")
	_, err := packed.WithSource(constantSource(src)).Pack(context.Background(), dir, m)
	if err == nil {
		t.Fatal("an image the source does not have was packed anyway")
	}
	if !strings.Contains(err.Error(), "app") {
		t.Errorf("the refusal does not name the image: %v", err)
	}
}

// TestPackRefusesWhenTheCopyResolvesToADifferentDigest.
//
// The check that gives a bundle its provenance, reached the way it is reached
// in practice: a manifest pinned to a *multi-platform index* digest, packed
// with --platform, resolves to a per-platform manifest whose digest is not the
// one pinned. Without the comparison, the bundle would carry an image the
// manifest does not describe -- and the pin would decide nothing, which is the
// property an acceptance run once found missing for the images map as a whole.
func TestPackRefusesWhenTheCopyResolvesToADifferentDigest(t *testing.T) {
	dir := t.TempDir()
	src, indexDigest, amd64Digest := multiPlatformImageInMemory(t)
	if indexDigest == amd64Digest {
		t.Fatal("the fixture's index and per-platform digests are the same")
	}

	m := manifestWith(map[string]domain.ImageSpec{
		"app": {Ref: "registry.example/demo/app@" + indexDigest, From: domain.ImageFromBundle},
	})

	packer, err := imagepack.New("linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	_, err = packer.WithSource(constantSource(src)).Pack(context.Background(), dir, m)
	if err == nil {
		t.Fatal("a copy that resolved to a different image was accepted")
	}
	if !strings.Contains(err.Error(), amd64Digest) || !strings.Contains(err.Error(), indexDigest) {
		t.Errorf("the refusal should name both digests: %v", err)
	}
	if !strings.Contains(domain.AsError(err).Hint, "--platform") {
		t.Errorf("the refusal should point at the likely cause: %v", err)
	}
}

// TestPackIsIdempotent, because the layout is content-addressed.
//
// A vendor re-runs this on every build. If the second run rewrote the layout,
// `release archive`'s byte-for-byte reproducibility would hold only for the
// bundles nobody repacked.
func TestPackIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	src, digest := imageInMemory(t, "hello from the vendor")
	m := manifestWith(map[string]domain.ImageSpec{
		"app": {Ref: "registry.example/demo/app@" + digest, From: domain.ImageFromBundle},
	})

	packer, _ := imagepack.New("")
	packer = packer.WithSource(constantSource(src))

	if _, err := packer.Pack(context.Background(), dir, m); err != nil {
		t.Fatal(err)
	}
	first := treeOf(t, filepath.Join(dir, "images"))

	if _, err := packer.Pack(context.Background(), dir, m); err != nil {
		t.Fatalf("packing a second time failed: %v", err)
	}
	second := treeOf(t, filepath.Join(dir, "images"))

	if first != second {
		t.Errorf("packing twice changed the layout:\n%s\n--- and then ---\n%s", first, second)
	}
}

// TestPackLeavesRegistryImagesAlone.
//
// Per-image is the design: a release bundles what is private and keeps pulling
// what is public. A packer that copied everything would put a gigabyte of
// Postgres in every bundle.
func TestPackLeavesRegistryImagesAlone(t *testing.T) {
	dir := t.TempDir()
	src, digest := imageInMemory(t, "hello from the vendor")

	m := manifestWith(map[string]domain.ImageSpec{
		"app": {Ref: "registry.example/demo/app@" + digest, From: domain.ImageFromBundle},
		"db":  {Ref: "postgres@sha256:" + strings.Repeat("b", 64)},
	})

	packer, _ := imagepack.New("")
	names, err := packer.WithSource(constantSource(src)).Pack(context.Background(), dir, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "app" {
		t.Fatalf("packed %v, want only [app]", names)
	}

	digests, err := release.ImageLayoutDigests(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(digests) != 1 {
		t.Errorf("the layout carries %d images, want 1: %v", len(digests), digests)
	}
}

// TestPackRefusesAPlatformItCannotRead, before it touches a registry.
func TestPackRefusesAPlatformItCannotRead(t *testing.T) {
	for _, bad := range []string{"linux", "", "/amd64", "linux/"} {
		if bad == "" {
			continue // empty means "whatever the registry resolves"
		}
		if _, err := imagepack.New(bad); err == nil {
			t.Errorf("%q was accepted as a platform", bad)
		}
	}
	if _, err := imagepack.New("linux/amd64"); err != nil {
		t.Errorf("linux/amd64 was refused: %v", err)
	}
	if _, err := imagepack.New("linux/arm64/v8"); err != nil {
		t.Errorf("linux/arm64/v8 was refused: %v", err)
	}
}

// imageInMemory publishes a minimal but real OCI image into a memory store and
// returns it with the digest of its manifest.
func imageInMemory(t *testing.T, layer string) (oras.ReadOnlyTarget, string) {
	t.Helper()
	ctx := context.Background()
	store := memory.New()

	push := func(mediaType string, data []byte) ocispec.Descriptor {
		t.Helper()
		desc := content(mediaType, data)
		if err := store.Push(ctx, desc, strings.NewReader(string(data))); err != nil {
			t.Fatal(err)
		}
		return desc
	}

	layerDesc := push(ocispec.MediaTypeImageLayer, []byte(layer))
	configDesc := push(ocispec.MediaTypeImageConfig, []byte(`{"architecture":"amd64","os":"linux"}`))

	manifest := ocispec.Manifest{
		Versioned: specVersioned(),
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    []ocispec.Descriptor{layerDesc},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDesc := push(ocispec.MediaTypeImageManifest, raw)

	// Tagged by its own digest, which is how a pinned reference resolves.
	if err := store.Tag(ctx, manifestDesc, manifestDesc.Digest.String()); err != nil {
		t.Fatal(err)
	}
	return store, manifestDesc.Digest.String()
}

func constantSource(src oras.ReadOnlyTarget) imagepack.OpenSource {
	return func(string) (oras.ReadOnlyTarget, error) { return src, nil }
}

func manifestWith(images map[string]domain.ImageSpec) domain.Manifest {
	return domain.Manifest{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       domain.KindApplicationRelease,
		Metadata:   domain.Metadata{Name: "demo", Version: domain.MustParseVersion("1.0.0")},
		Images:     images,
	}
}

// treeOf renders a directory as sorted "path size" lines, so two packs can be
// compared without reading every blob.
func treeOf(t *testing.T, dir string) string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel)+" "+digestOfBytes(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sortStrings(out)
	return strings.Join(out, "\n")
}

// multiPlatformImageInMemory publishes an index over two per-platform images,
// and returns the store with the index's digest and the amd64 manifest's.
func multiPlatformImageInMemory(t *testing.T) (oras.ReadOnlyTarget, string, string) {
	t.Helper()
	ctx := context.Background()
	store := memory.New()

	push := func(mediaType string, data []byte) ocispec.Descriptor {
		t.Helper()
		desc := content(mediaType, data)
		if err := store.Push(ctx, desc, strings.NewReader(string(data))); err != nil {
			t.Fatal(err)
		}
		return desc
	}

	perPlatform := func(arch string) ocispec.Descriptor {
		t.Helper()
		layer := push(ocispec.MediaTypeImageLayer, []byte("layer for "+arch))
		config := push(ocispec.MediaTypeImageConfig,
			[]byte(`{"architecture":"`+arch+`","os":"linux"}`))
		raw, err := json.Marshal(ocispec.Manifest{
			Versioned: specVersioned(),
			MediaType: ocispec.MediaTypeImageManifest,
			Config:    config,
			Layers:    []ocispec.Descriptor{layer},
		})
		if err != nil {
			t.Fatal(err)
		}
		desc := push(ocispec.MediaTypeImageManifest, raw)
		desc.Platform = &ocispec.Platform{OS: "linux", Architecture: arch}
		return desc
	}

	amd64 := perPlatform("amd64")
	arm64 := perPlatform("arm64")

	raw, err := json.Marshal(ocispec.Index{
		Versioned: specVersioned(),
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{amd64, arm64},
	})
	if err != nil {
		t.Fatal(err)
	}
	index := push(ocispec.MediaTypeImageIndex, raw)
	if err := store.Tag(ctx, index, index.Digest.String()); err != nil {
		t.Fatal(err)
	}
	return store, index.Digest.String(), amd64.Digest.String()
}
