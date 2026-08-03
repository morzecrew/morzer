// Package local implements ports.ReleaseSource for bundles already on this
// machine: an unpacked directory, or a `tar.zst` archive.
//
// Both are one adapter rather than two because ParseRef gives them the same
// scheme -- a bare path is `file` whether it names a directory or an archive --
// and a registry that dispatches on scheme cannot choose between two sources
// claiming it. Deciding by what the path actually *is* belongs here, where the
// filesystem is already being touched.
//
// It is also the reference implementation of the port: resolve without side
// effects, fetch into a destination it does not choose, and never let an entry
// escape that destination.
package local

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

func (s *Source) Schemes() []string { return []string{Scheme} }

// Resolve reads the manifest and computes the digest.
//
// For a directory this touches nothing. For an archive it extracts to a
// temporary directory and removes it again, because the content digest is
// defined over the unpacked tree -- which is what makes a digest recorded from
// one transport verify from another, and is worth the extra unpack of a bundle
// measured in kilobytes.
func (s *Source) Resolve(ctx context.Context, ref ports.Ref) (ports.ResolvedRelease, error) {
	dir, cleanup, err := s.materialise(ref)
	if err != nil {
		return ports.ResolvedRelease{}, err
	}
	defer cleanup()

	rel, err := release.Load(dir)
	if err != nil {
		return ports.ResolvedRelease{}, err
	}

	// A digest supplied on the reference is a claim to check, not a value
	// to trust. Verifying it here means a mismatch is caught before
	// anything is copied into the release store.
	if ref.Digest != "" && !atomicfs.SameDigest(rel.Digest, ref.Digest) {
		return ports.ResolvedRelease{}, domain.ValidationError(domain.ErrDigestMismatch,
			"the bundle at %s hashes to %s, but %s was expected",
			ref.Location, rel.Digest, ref.Digest).
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

// Fetch places the bundle at destDir.
//
// A directory is copied rather than symlinked even though it is already local:
// a release under /opt must be immutable, and a symlink to an operator's
// working directory is a release whose contents can change after it was
// verified. An archive is extracted, under the same limits.
func (s *Source) Fetch(ctx context.Context, ref ports.Ref, destDir string) (ports.BundlePath, error) {
	path, err := s.locate(ref)
	if err != nil {
		return "", err
	}

	if atomicfs.IsTarZst(path) {
		if err := atomicfs.ExtractTarZst(path, destDir, s.limits); err != nil {
			return "", err
		}
		if _, err := release.Load(destDir); err != nil {
			return "", err
		}
		return ports.BundlePath(destDir), nil
	}

	// Refusing to copy a directory into itself: the walk would otherwise
	// recurse into its own output.
	absSrc, _ := filepath.Abs(path)
	absDest, _ := filepath.Abs(destDir)
	if absSrc == absDest {
		return ports.BundlePath(absDest), nil
	}

	if err := atomicfs.CopyTree(path, destDir, s.limits); err != nil {
		return "", err
	}
	return ports.BundlePath(destDir), nil
}

// List enumerates versions under a directory of release directories.
//
// A single bundle -- directory or archive -- has nothing to enumerate, which is
// reported as ErrUnsupported rather than as an empty list: "no versions here"
// and "this source cannot answer that" are different answers, and only one of
// them means the operator should look somewhere else.
func (s *Source) List(ctx context.Context, ref ports.Ref) ([]domain.Version, error) {
	if atomicfs.IsTarZst(ref.Location) {
		return nil, domain.ValidationError(domain.ErrUnsupported,
			"%s is a single bundle, not a version index", ref.Location)
	}

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

// materialise returns a directory holding the bundle, unpacking an archive into
// a temporary one when needed.
//
// The returned cleanup is always non-nil, so callers can defer it
// unconditionally rather than branching on how the bundle arrived.
func (s *Source) materialise(ref ports.Ref) (dir string, cleanup func(), err error) {
	path, err := s.locate(ref)
	if err != nil {
		return "", func() {}, err
	}
	if !atomicfs.IsTarZst(path) {
		return path, func() {}, nil
	}

	tmp, err := os.MkdirTemp("", "morzer-resolve-")
	if err != nil {
		return "", func() {}, domain.Internal(err, "cannot create a temporary directory")
	}
	cleanup = func() { _ = atomicfs.RemoveAll(tmp) }

	if err := atomicfs.ExtractTarZst(path, tmp, s.limits); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return tmp, cleanup, nil
}

// locate resolves the reference to a bundle directory or archive file.
func (s *Source) locate(ref ports.Ref) (string, error) {
	if ref.Location == "" {
		return "", domain.Usage("no release path was given")
	}

	target, err := filepath.Abs(ref.Location)
	if err != nil {
		return "", domain.ValidationError(err, "cannot resolve %s", ref.Location)
	}

	info, err := os.Stat(target)
	if err != nil {
		return "", domain.ValidationError(domain.ErrReleaseNotFound,
			"no release bundle at %s", target).
			WithHint("check the path; it should be a directory containing %s, or a tar.zst archive",
				release.ManifestFileName)
	}

	if !info.IsDir() {
		if !atomicfs.IsTarZst(target) {
			return "", domain.ValidationError(nil, "%s is not a release bundle", target).
				WithHint("point at a directory containing %s, or a tar.zst archive; "+
					"https and oci sources arrive in a later milestone",
					release.ManifestFileName)
		}
		if !info.Mode().IsRegular() {
			return "", domain.ValidationError(nil, "%s is not a regular file", target)
		}
		return target, nil
	}

	// A version-qualified reference into a store of releases: /opt/x/releases + 1.2.0.
	if !ref.Version.IsZero() {
		versioned := filepath.Join(target, ref.Version.String())
		if _, err := os.Stat(filepath.Join(versioned, release.ManifestFileName)); err == nil {
			return versioned, nil
		}
	}

	if _, err := os.Stat(filepath.Join(target, release.ManifestFileName)); err != nil {
		return "", domain.ValidationError(domain.ErrReleaseNotFound,
			"%s contains no %s", target, release.ManifestFileName).
			WithHint("point at the directory holding the manifest, not its parent")
	}
	return target, nil
}
