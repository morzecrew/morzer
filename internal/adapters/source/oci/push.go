package oci

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// AnnotationContentDigest carries the bundle's content digest on a manifest
// `release push` wrote.
//
// A hint, never proof: anyone who can push to the repository can write it. So a
// later push trusts it only to refuse (RFC 0039 decision 6) -- a manifest that
// lies about it can make push refuse, never make it accept different content.
const AnnotationContentDigest = "dev.morzer.bundle.digest"

// PushRequest is one archive, already checked, published as one version.
type PushRequest struct {
	// Archive is the tar.zst uploaded as the layer, byte for byte.
	Archive string

	// Reference is registry/repository, optionally with :version. Omitted,
	// the tag is the version; given, it must equal it.
	Reference string

	Version domain.Version
	Product string

	// ContentDigest is the bundle's content digest, computed from the
	// unpacked tree. It is what identity is decided by: the archive's own
	// bytes carry its timestamp, so the same bundle archived twice differs.
	ContentDigest string

	// Channels are moved, in order, once the version is in place.
	Channels []string

	// DryRun does every check and every read, and writes nothing.
	DryRun bool
}

// ChannelMove is what happened to one requested channel. Neither Moved nor
// Error means an earlier channel failed and this one was not tried.
type ChannelMove struct {
	Name  string `json:"name"`
	Moved bool   `json:"moved"`
	Error string `json:"error,omitempty"`
}

// PushResult is what a push did, or under DryRun would do.
type PushResult struct {
	// Reference is the version's reference, repository:version.
	Reference string

	// ManifestDigest is the registry's identity for the version: what
	// `oci://…@sha256:` pins. Empty for a dry run of a version not yet
	// published, because no manifest exists to name.
	ManifestDigest string

	// Published is false when the registry already held this bundle under
	// this version, which is not an error: a pipeline whose channel move
	// failed after the push re-runs the same step.
	Published bool

	Channels []ChannelMove

	// Warnings are for the caller to print; a push does not refuse over them.
	Warnings []string
}

// Push publishes an archive under a version, then moves channel tags to it
// (RFC 0039 §5.1). The archive's own checks -- extraction, sums, templates, the
// signature -- are the caller's, and done before this is called: nothing here
// looks inside the layer except to compare it with one already published.
func (s *Source) Push(ctx context.Context, req PushRequest) (PushResult, error) {
	tag := req.Version.String()
	repository, err := pushTarget(req)
	if err != nil {
		return PushResult{}, err
	}
	reference := repository + ":" + tag
	res := PushResult{Reference: reference}

	layer, err := describeArchive(req.Archive)
	if err != nil {
		return res, err
	}
	// The pull side's limit, applied before anything is sent: a layer past it
	// is published where no installation can install it from.
	if s.maxBlob > 0 && layer.Size > s.maxBlob {
		return res, domain.ValidationError(nil,
			"the archive is %d bytes, over the limit of %d an installation pulls from a registry",
			layer.Size, s.maxBlob).
			WithHint("hand a bundle this large over as a file")
	}

	repo, err := remoteRepository(reference)
	if err != nil {
		return res, err
	}

	target, err := s.published(ctx, repo, repository, layer, req)
	if err != nil {
		return res, err
	}
	if target.Digest == "" {
		res.Published = true
		if !req.DryRun {
			if target, err = publish(ctx, repo, reference, layer, req); err != nil {
				return res, err
			}
		}
	}
	res.ManifestDigest = target.Digest.String()

	if !req.DryRun {
		// The existence check and the tag write are two requests with
		// nothing holding the tag between them; distribution has no
		// conditional write. A version that no longer names what this run
		// confirmed is another pipeline's bundle, and a channel moved to it
		// now would point machines at something this run never checked.
		now, err := repo.Resolve(ctx, tag)
		if err != nil {
			return res, registryError(err, reference)
		}
		if now.Digest != target.Digest {
			return res, domain.ValidationError(nil,
				"%s now resolves to %s, not to %s, which this push confirmed; no channel was moved",
				reference, now.Digest, target.Digest).
				WithHint("another push wrote the same version at the same time; " +
					"publish to a repository from one pipeline at a time")
		}
	}

	for i, channel := range req.Channels {
		channelRef := repository + ":" + channel
		if warning := backwards(ctx, repo, channelRef, channel, req.Version); warning != "" {
			res.Warnings = append(res.Warnings, warning)
		}
		if !req.DryRun {
			if err := repo.Tag(ctx, target, channel); err != nil {
				err = registryError(err, channelRef)
				res.Channels = append(res.Channels, ChannelMove{Name: channel, Error: domain.AsError(err).Message})
				// One at a time, and the first failure stops the rest
				// (decision 7): a re-run moves them all, and channels
				// half-moved past a failure are harder to reason about
				// than ones not tried.
				for _, rest := range req.Channels[i+1:] {
					res.Channels = append(res.Channels, ChannelMove{Name: rest})
				}
				return res, err
			}
		}
		res.Channels = append(res.Channels, ChannelMove{Name: channel, Moved: true})
	}
	return res, nil
}

// published answers RFC 0039 §5.3: the manifest the version already names when
// it holds this bundle, a zero descriptor when the version is not published,
// and a refusal when it holds anything else.
func (s *Source) published(ctx context.Context, repo *remote.Repository, repository string,
	layer ocispec.Descriptor, req PushRequest,
) (ocispec.Descriptor, error) {
	reference := repository + ":" + req.Version.String()
	desc, err := repo.Resolve(ctx, req.Version.String())
	if errors.Is(err, errdef.ErrNotFound) {
		return ocispec.Descriptor{}, nil
	}
	if err != nil {
		return ocispec.Descriptor{}, registryError(err, reference)
	}

	// By digest from here on, so every answer below is about the one
	// manifest just resolved, even if the tag moves meanwhile.
	pinned := repository + "@" + desc.Digest.String()
	manifest, err := fetchManifest(ctx, repo, pinned)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	theirs, err := bundleLayer(manifest, reference)
	if err != nil {
		return ocispec.Descriptor{}, err
	}

	// The registry checked that blob against its digest when it was pushed,
	// and this archive hashes to the same one: the same bytes, so the same
	// bundle, and nothing to download to find that out.
	if theirs.Digest == layer.Digest {
		return desc, nil
	}

	differs := func(other string) error {
		return domain.ValidationError(nil,
			"%s is already published with different content: this archive's content digest is %s, the registry's is %s",
			reference, req.ContentDigest, other).
			WithHint("never republish a version with different content; publish this bundle as a new version")
	}
	if claimed := manifest.Annotations[AnnotationContentDigest]; claimed != "" &&
		!atomicfs.SameDigest(claimed, req.ContentDigest) {
		return ocispec.Descriptor{}, differs(claimed)
	}

	// The annotation agrees or is absent -- a bundle pushed by hand carries
	// none -- and either way it cannot be taken as proof. The layer is pulled
	// and its content digest computed, as an installation would.
	resolved, err := s.Resolve(ctx, ports.Ref{Scheme: Scheme, Location: pinned})
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	if !atomicfs.SameDigest(resolved.Digest, req.ContentDigest) {
		return ocispec.Descriptor{}, differs(resolved.Digest)
	}
	return desc, nil
}

// pushTarget is the repository a push writes to, after refusing what it cannot
// publish: everything here is the vendor's own argument, so it is refused
// before the registry is asked anything.
func pushTarget(req PushRequest) (string, error) {
	if strings.Contains(req.Reference, "@") {
		return "", domain.Usage("%s names a digest; a push publishes a version under a tag", req.Reference).
			WithHint("pass the repository, oci://%s", req.Reference[:strings.LastIndex(req.Reference, "@")])
	}
	repository, tag, err := splitReference(req.Reference)
	if err != nil {
		return "", err
	}
	// Decision 2: List reads tags as versions, so a tag that is not the
	// bundle's version is `update --check` offering one the bundle does not
	// carry.
	if tag != "" && tag != req.Version.String() {
		return "", domain.Usage("the reference names %s, but the archive is version %s", tag, req.Version).
			WithHint("leave the tag off and push names the version itself: oci://%s", repository)
	}

	for _, channel := range req.Channels {
		// The same hazard through the other door: a channel named like a
		// version is listed as one.
		if _, err := domain.ParseVersion(channel); err == nil {
			return "", domain.Usage("the channel %q is named like a version, so `update --check` would offer it as one", channel).
				WithHint("name channels for what they mean -- stable, edge -- and let the version tag carry the number")
		}
		if err := (registry.Reference{Reference: channel}).ValidateReferenceAsTag(); err != nil {
			return "", domain.Usage("the channel %q is not a valid tag", channel).
				WithHint("a tag is letters, digits, '_', '.' and '-', up to 128 characters")
		}
	}
	return repository, nil
}

// publish uploads the layer, writes the manifest `oras push --artifact-type`
// would, and tags it with the version.
func publish(ctx context.Context, repo *remote.Repository, reference string,
	layer ocispec.Descriptor, req PushRequest,
) (ocispec.Descriptor, error) {
	present, err := repo.Exists(ctx, layer)
	if err != nil {
		return ocispec.Descriptor{}, registryError(err, reference)
	}
	if !present {
		f, err := os.Open(req.Archive)
		if err != nil {
			return ocispec.Descriptor{}, domain.ValidationError(err, "cannot read %s", req.Archive)
		}
		defer func() { _ = f.Close() }()
		if err := repo.Push(ctx, layer, f); err != nil {
			return ocispec.Descriptor{}, registryError(err, reference)
		}
	}

	manifest, err := oras.PackManifest(ctx, repo, oras.PackManifestVersion1_1, ArtifactType,
		oras.PackManifestOptions{
			Layers: []ocispec.Descriptor{layer},
			ManifestAnnotations: map[string]string{
				ocispec.AnnotationTitle:   req.Product,
				ocispec.AnnotationVersion: req.Version.String(),
				AnnotationContentDigest:   req.ContentDigest,
			},
		})
	if err != nil {
		return ocispec.Descriptor{}, registryError(err, reference)
	}
	if err := repo.Tag(ctx, manifest, req.Version.String()); err != nil {
		return ocispec.Descriptor{}, registryError(err, reference)
	}
	return manifest, nil
}

// describeArchive is the layer descriptor for an archive.
//
// The title annotation is the file name, as `oras push` writes it: `oras pull`
// saves a layer only under its title, so without one a customer pulling by hand
// gets nothing.
func describeArchive(path string) (ocispec.Descriptor, error) {
	f, err := os.Open(path)
	if err != nil {
		return ocispec.Descriptor{}, domain.ValidationError(err, "cannot read %s", path)
	}
	defer func() { _ = f.Close() }()

	digester := digest.SHA256.Digester()
	size, err := io.Copy(digester.Hash(), f)
	if err != nil {
		return ocispec.Descriptor{}, domain.ValidationError(err, "cannot read %s", path)
	}
	return ocispec.Descriptor{
		MediaType:   MediaType,
		Digest:      digester.Digest(),
		Size:        size,
		Annotations: map[string]string{ocispec.AnnotationTitle: filepath.Base(path)},
	}, nil
}

// backwards warns when a channel is about to move to a version older than the
// one it points at (RFC 0039 decision 4).
//
// The version is read from the annotation on the channel's manifest: one small
// request, where pulling the layer would download a bundle to print a warning.
// An artifact pushed by hand carries none, and then there is nothing to say --
// machines already refuse to follow a channel backwards, so a missed warning
// moves nobody. A channel that does not exist yet, or cannot be read, is left
// to the tag write that follows to report.
func backwards(ctx context.Context, repo *remote.Repository, reference, channel string, to domain.Version) string {
	manifest, err := fetchManifest(ctx, repo, reference)
	if err != nil {
		return ""
	}
	from, err := domain.ParseVersion(manifest.Annotations[ocispec.AnnotationVersion])
	if err != nil || !to.LessThan(from) {
		return ""
	}
	return fmt.Sprintf("the channel %s moves back from %s to %s: installations already on %s will not follow it",
		channel, from, to, from)
}
