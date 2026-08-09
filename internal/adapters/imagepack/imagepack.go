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
	"path/filepath"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"

	ocisource "github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/release"
)

// Source is where an image's bytes are read from. A registry, in production.
type Source interface {
	oras.ReadOnlyTarget
}

// OpenSource resolves an image reference to something to copy from.
type OpenSource func(ref string) (Source, error)

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

func openRegistry(ref string) (Source, error) {
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

	store, err := oci.New(filepath.Join(dir, release.ImagesDirName))
	if err != nil {
		return nil, domain.ValidationError(err,
			"cannot open the image layout in %s", dir)
	}

	packed := make([]string, 0, len(bundled))
	for _, name := range bundled {
		if err := p.copyOne(ctx, store, name, m.Images[name].Ref); err != nil {
			// No sums are written by the caller on this path, so the
			// half-populated tree fails `release verify` until a
			// later pack completes. Blobs are content-addressed, so
			// what a failure leaves behind is orphans rather than
			// corruption.
			return packed, err
		}
		packed = append(packed, name)
	}
	return packed, nil
}

// copyOne copies a single image and checks it is the one the manifest pins.
func (p *Packer) copyOne(ctx context.Context, store oras.Target, name, ref string) error {
	repo, err := p.open(ref)
	if err != nil {
		return err
	}

	_, wanted, err := splitRef(ref)
	if err != nil {
		return err
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

// splitRef separates an image reference from the digest it pins.
func splitRef(ref string) (name, digest string, err error) {
	for i := len(ref) - 1; i >= 0; i-- {
		if ref[i] == '@' {
			return ref[:i], ref[i+1:], nil
		}
	}
	return "", "", domain.ValidationError(nil,
		"the image reference %q is not pinned by digest", ref)
}

// parsePlatform reads os/arch[/variant].
func parsePlatform(s string) (*ocispec.Platform, error) {
	var out ocispec.Platform
	parts := splitN(s, '/', 3)
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

func splitN(s string, sep byte, n int) []string {
	var out []string
	start := 0
	for i := 0; i < len(s) && len(out) < n-1; i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
