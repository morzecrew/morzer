// Package imagepack copies container images out of a registry into the OCI
// layout a release bundle carries.
//
// It is the producing half of RFC 0011's format: that RFC defines what a bundle
// carrying images looks like and how it is verified, and this writes one.
//
// oras-go rather than shelling out to skopeo or crane, for two reasons. Packing
// stays available to any vendor who has the manager, which is the point of
// shipping it as a command; and the digest comparison happens in Go, next to
// the manifest that pins it, rather than in a shell pipeline comparing strings.
package imagepack

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"

	ocisource "github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/release"
)

// OpenSource resolves an image reference to something to copy from. A
// registry, in production.
type OpenSource func(ref string) (oras.ReadOnlyTarget, error)

// Packer copies images into a bundle's layout.
type Packer struct {
	// platform selects one image from a multi-platform index.
	platform *ocispec.Platform

	// open is the registry, unless a test replaced it.
	//
	// A seam rather than a mock of the copy itself: what is worth testing
	// here is that oras.Copy writes a layout carrying the digest the
	// registry reported, and that the comparison against the manifest
	// fires. Both need a real copy into a real layout; neither needs a
	// network.
	open OpenSource
}

// New returns a packer for the given platform, e.g. "linux/amd64". An empty
// platform lets the registry's default resolution stand.
func New(platform string) (*Packer, error) {
	p := &Packer{open: openRegistry}
	if platform == "" {
		return p, nil
	}
	parsed, err := parsePlatform(platform)
	if err != nil {
		return nil, err
	}
	p.platform = parsed
	return p, nil
}

// WithSource overrides where images are read from. Tests only.
func (p *Packer) WithSource(open OpenSource) *Packer {
	p.open = open
	return p
}

func openRegistry(ref string) (oras.ReadOnlyTarget, error) {
	repo, err := ocisource.OpenRepository(ref)
	if err != nil {
		return nil, err
	}
	return repo, nil
}

// Pack copies every image the manifest marks `from: bundle` into
// <dir>/images/.
//
// Idempotent, because the layout is content-addressed: running it twice copies
// nothing the second time, and re-running after adding an image adds only that
// image's blobs.
//
// It reports the names it copied, in the order it copied them, so the caller
// can say what happened rather than only that something did.
func (p *Packer) Pack(ctx context.Context, dir string, m domain.Manifest) ([]string, error) {
	bundled := m.BundledImages()
	if len(bundled) == 0 {
		return nil, nil
	}

	layout := filepath.Join(dir, release.ImagesDirName)
	before, err := indexDigests(layout)
	if err != nil {
		return nil, err
	}
	store, err := oci.New(layout)
	if err != nil {
		return nil, domain.ValidationError(err,
			"cannot open the image layout in %s", dir)
	}

	packed := make([]string, 0, len(bundled))
	pinned := make(map[digest.Digest]bool, len(bundled))
	for _, name := range bundled {
		if err := p.copyOne(ctx, store, name, m.Images[name].Ref); err != nil {
			// No sums are written by the caller on this path, so the
			// half-populated tree fails `release verify` until a
			// later pack completes. Blobs are content-addressed, so
			// what a failure leaves behind is orphans rather than
			// corruption.
			return packed, err
		}
		d, _ := domain.ImageSpec{Ref: m.Images[name].Ref}.Digest()
		pinned[digest.Digest(d)] = true
		packed = append(packed, name)
	}
	return packed, keepOnly(layout, before, pinned)
}

// keepOnly leaves index.json naming the pinned images, and whatever it named
// before this pack except the manifests inside a pinned index. oras's store
// records every manifest it is given, and saves index.json as each one lands,
// so copying an index -- in this pack, an earlier one, or one that failed part
// way -- named each manifest inside it beside it, and `release verify` refuses
// a layout naming anything the manifest does not. The manifests stay in
// blobs/, reachable through the index that names them.
func keepOnly(layout string, before, pinned map[digest.Digest]bool) error {
	inside, err := insideIndexes(layout, pinned)
	if err != nil {
		return err
	}

	path := filepath.Join(layout, release.ImageIndexFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.ValidationError(err, "cannot read %s", path)
	}
	var index ocispec.Index
	if err := json.Unmarshal(data, &index); err != nil {
		return domain.ValidationError(err, "%s is not a valid OCI image index", path)
	}
	kept := index.Manifests[:0]
	for _, desc := range index.Manifests {
		if pinned[desc.Digest] || (before[desc.Digest] && !inside[desc.Digest]) {
			kept = append(kept, desc)
		}
	}
	if len(kept) == len(index.Manifests) {
		return nil
	}
	index.Manifests = kept
	out, err := json.Marshal(index)
	if err != nil {
		return domain.Internal(err, "cannot encode %s", path)
	}
	return atomicfs.WriteFile(path, out, 0o644)
}

// insideIndexes is every manifest reachable from the pinned digests that are
// indexes, read from the layout's blobs.
func insideIndexes(layout string, pinned map[digest.Digest]bool) (map[digest.Digest]bool, error) {
	inside := map[digest.Digest]bool{}
	queue := make([]digest.Digest, 0, len(pinned))
	for d := range pinned {
		queue = append(queue, d)
	}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		data, err := os.ReadFile(filepath.Join(layout, "blobs", d.Algorithm().String(), d.Encoded()))
		if err != nil {
			return nil, domain.ValidationError(err, "cannot read %s from the image layout", d)
		}
		// An image manifest has no manifests list, so only an index
		// contributes.
		var index ocispec.Index
		if err := json.Unmarshal(data, &index); err != nil {
			return nil, domain.ValidationError(err, "%s in the image layout is not valid JSON", d)
		}
		for _, desc := range index.Manifests {
			if !inside[desc.Digest] {
				inside[desc.Digest] = true
				queue = append(queue, desc.Digest)
			}
		}
	}
	return inside, nil
}

// indexDigests is what index.json names before a pack, and nothing for a
// layout that does not exist yet.
func indexDigests(layout string) (map[digest.Digest]bool, error) {
	data, err := os.ReadFile(filepath.Join(layout, release.ImageIndexFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, domain.ValidationError(err, "cannot read the image layout in %s", layout)
	}
	var index ocispec.Index
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, domain.ValidationError(err,
			"%s/%s is not a valid OCI image index", release.ImagesDirName, release.ImageIndexFileName)
	}
	out := make(map[digest.Digest]bool, len(index.Manifests))
	for _, desc := range index.Manifests {
		out[desc.Digest] = true
	}
	return out, nil
}

// copyOne copies a single image and checks it is the one the manifest pins.
func (p *Packer) copyOne(ctx context.Context, store oras.Target, name, ref string) error {
	repo, err := p.open(ref)
	if err != nil {
		return err
	}

	wanted, ok := domain.ImageSpec{Ref: ref}.Digest()
	if !ok {
		return domain.ValidationError(nil,
			"the reference for image %s is not pinned by digest: %q", name, ref)
	}

	opts := oras.DefaultCopyOptions
	if p.platform != nil {
		opts.WithTargetPlatform(p.platform)
	}

	desc, err := oras.Copy(ctx, repo, wanted, store, wanted, opts)
	if err != nil {
		return domain.ValidationError(err,
			"cannot copy the image %s (%s) into the bundle", name, ref).
			WithHint("credentials come from the ambient Docker configuration; " +
				"check `docker login` for that registry")
	}

	// The check that gives the bundle its provenance. Without it, `pack`
	// would copy whatever the registry served and the manifest's digest
	// would decide nothing -- which is the property an acceptance run once
	// found missing for the images map as a whole.
	//
	// Reachable in practice through a platform selection: copying a
	// multi-platform index with --platform resolves to a per-platform
	// manifest whose digest is not the index digest the manifest pins.
	if got := desc.Digest.String(); got != wanted {
		return domain.ValidationError(domain.ErrDigestMismatch,
			"the registry served %s for image %s, and the manifest pins %s",
			got, name, wanted).
			WithHint("pin the digest the registry actually serves, or drop " +
				"--platform if you pinned a multi-platform index")
	}
	return nil
}

// parsePlatform reads os/arch[/variant].
func parsePlatform(s string) (*ocispec.Platform, error) {
	var out ocispec.Platform
	parts := strings.SplitN(s, "/", 3)
	switch len(parts) {
	case 3:
		out.Variant = parts[2]
		fallthrough
	case 2:
		out.OS, out.Architecture = parts[0], parts[1]
	default:
		return nil, domain.Usage("%q is not a platform", s).
			WithHint("platforms look like linux/amd64 or linux/arm64/v8")
	}
	if out.OS == "" || out.Architecture == "" {
		return nil, domain.Usage("%q is not a platform", s).
			WithHint("platforms look like linux/amd64 or linux/arm64/v8")
	}
	return &out, nil
}
