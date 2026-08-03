// Package dir implements ports.ReleaseSource for a local directory.
//
// This is the v1 source and the one the acceptance scenario runs on. It is
// also the simplest possible implementation of the port, which makes it the
// reference for what a source must do: resolve without side effects, fetch
// into a destination it does not choose, and never let an entry escape that
// destination.
package dir

import (
	"context"
	"os"
	"path/filepath"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// Scheme is the reference scheme this source handles.
const Scheme = "file"

type Source struct {
	limits atomicfs.ExtractLimits
}

func New() *Source {
	return &Source{limits: atomicfs.DefaultExtractLimits()}
}

// WithLimits overrides the extraction limits.
func (s *Source) WithLimits(l atomicfs.ExtractLimits) *Source {
	s.limits = l
	return s
}

var _ ports.ReleaseSource = (*Source)(nil)

func (s *Source) Scheme() string { return Scheme }

// Resolve reads the manifest and computes the digest without copying anything.
func (s *Source) Resolve(ctx context.Context, ref ports.Ref) (ports.ResolvedRelease, error) {
	dir, err := s.locate(ref)
	if err != nil {
		return ports.ResolvedRelease{}, err
	}

	rel, err := release.Load(dir)
	if err != nil {
		return ports.ResolvedRelease{}, err
	}

	// A digest supplied on the reference is a claim to check, not a value
	// to trust. Verifying it here means a mismatch is caught before
	// anything is copied into the release store.
	if ref.Digest != "" && !atomicfs.SameDigest(rel.Digest, ref.Digest) {
		return ports.ResolvedRelease{}, domain.ValidationError(domain.ErrDigestMismatch,
			"the bundle at %s hashes to %s, but %s was expected", dir, rel.Digest, ref.Digest).
			WithHint("the bundle has been modified since that digest was recorded")
	}

	size, _ := atomicfs.DirSize(dir)
	return ports.ResolvedRelease{
		Ref:     ref,
		Version: rel.Version(),
		Digest:  rel.Digest,
		Size:    size,
	}, nil
}

// Fetch copies the bundle into destDir.
//
// It copies rather than symlinking even though the source is already local:
// a release under /opt must be immutable, and a symlink to an operator's
// working directory is a release whose contents can change after it was
// verified.
func (s *Source) Fetch(ctx context.Context, ref ports.Ref, destDir string) (ports.BundlePath, error) {
	src, err := s.locate(ref)
	if err != nil {
		return "", err
	}

	// Refusing to copy a directory into itself: the walk would otherwise
	// recurse into its own output.
	absSrc, _ := filepath.Abs(src)
	absDest, _ := filepath.Abs(destDir)
	if absSrc == absDest {
		return ports.BundlePath(absDest), nil
	}

	if err := atomicfs.CopyTree(src, destDir, s.limits); err != nil {
		return "", err
	}
	return ports.BundlePath(destDir), nil
}

// List enumerates versions under a directory of release directories.
//
// A directory holding one bundle has nothing to enumerate, which is reported
// as ErrUnsupported rather than as an empty list -- "no versions here" and
// "this source cannot answer that" are different answers.
func (s *Source) List(ctx context.Context, ref ports.Ref) ([]domain.Version, error) {
	dir, err := filepath.Abs(ref.Location)
	if err != nil {
		return nil, domain.ValidationError(err, "cannot resolve %s", ref.Location)
	}

	if _, err := os.Stat(filepath.Join(dir, release.ManifestFileName)); err == nil {
		return nil, domain.ValidationError(domain.ErrUnsupported,
			"%s holds a single bundle, not a version index", dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, domain.ValidationError(err, "cannot list %s", dir)
	}

	var out []domain.Version
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifest, err := release.LoadManifest(filepath.Join(dir, e.Name(), release.ManifestFileName))
		if err != nil {
			// A subdirectory that is not a bundle is not an error:
			// release stores accumulate stray directories.
			continue
		}
		out = append(out, manifest.Metadata.Version)
	}
	return out, nil
}

// locate resolves the reference to a directory containing a manifest.
func (s *Source) locate(ref ports.Ref) (string, error) {
	if ref.Location == "" {
		return "", domain.Usage("no release path was given")
	}

	dir, err := filepath.Abs(ref.Location)
	if err != nil {
		return "", domain.ValidationError(err, "cannot resolve %s", ref.Location)
	}

	info, err := os.Stat(dir)
	if err != nil {
		return "", domain.ValidationError(domain.ErrReleaseNotFound,
			"no release bundle at %s", dir).
			WithHint("check the path; it should be a directory containing %s",
				release.ManifestFileName)
	}
	if !info.IsDir() {
		return "", domain.ValidationError(nil, "%s is not a directory", dir).
			WithHint("archive sources (tar.zst, https, oci) arrive in a later milestone; " +
				"unpack the bundle and point at the directory")
	}

	// A version-qualified reference into a store of releases: /opt/x/releases + 1.2.0.
	if !ref.Version.IsZero() {
		versioned := filepath.Join(dir, ref.Version.String())
		if _, err := os.Stat(filepath.Join(versioned, release.ManifestFileName)); err == nil {
			return versioned, nil
		}
	}

	if _, err := os.Stat(filepath.Join(dir, release.ManifestFileName)); err != nil {
		return "", domain.ValidationError(domain.ErrReleaseNotFound,
			"%s contains no %s", dir, release.ManifestFileName).
			WithHint("point at the directory holding the manifest, not its parent")
	}
	return dir, nil
}
