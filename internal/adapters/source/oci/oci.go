// Package oci implements ports.ReleaseSource for bundles published as OCI
// artifacts.
//
// A registry a vendor already runs for their container images is a registry
// they can publish bundles to, with the same credentials, the same mirroring
// and the same retention. That is the whole argument for this transport: not
// that it is better than an HTTPS URL, but that it is somewhere the bundle can
// live next to the images it pins.
//
// Like the HTTPS source, this is transport only. The artifact's single layer is
// a `tar.zst`, and everything after it lands -- extraction limits, the refusal
// of links and device nodes, the content digest -- is the local source's job.
//
// Credentials come from the ambient Docker configuration. An operator who has
// run `docker login` for the registry their images come from should not have to
// log in a second time for the bundle that names those images, and this package
// stores nothing of its own.
package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/morzecrew/morzer/internal/adapters/source"
	"github.com/morzecrew/morzer/internal/adapters/source/local"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// Scheme is the reference scheme this source handles.
const Scheme = "oci"

// MediaType is what a release bundle is published as.
//
// A media type of its own, rather than a generic tarball: a registry client
// pulling this by accident should be able to tell it is not a container image,
// and a vendor's tooling should be able to select the right layer without
// guessing from a filename.
const MediaType = "application/vnd.morzer.release.bundle.v1.tar+zstd"

// MaxBlobSize bounds a layer before it is written. The archive limits apply
// afterwards; this one stops a registry handing over a terabyte.
const MaxBlobSize = 512 << 20

type Source struct {
	local *local.Source
	cache *source.TempCache

	maxBlob int64

	// newRepository is injectable so a caller can supply their own client.
	newRepository func(reference string) (Registry, error)
}

// Registry is the slice of the distribution API a bundle pull needs.
//
// Narrow on purpose, and exported for one reason: it is the only way to reach a
// registry this package would not otherwise talk to. Plain HTTP is not an
// option on the Source and never will be -- refusing an unauthenticated
// transport for bundles while offering a flag to enable it would be a policy
// with a switch on it. Supplying a client that speaks plaintext takes code,
// which is what the test suite does and what an operator with an internal
// registry would have to mean deliberately.
type Registry interface {
	// FetchReference resolves a tag or digest and returns the manifest.
	FetchReference(ctx context.Context, reference string) (ocispec.Descriptor, io.ReadCloser, error)

	// Fetch returns a blob, verified against its descriptor while it
	// streams.
	Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error)

	// Tags enumerates the repository, in pages.
	Tags(ctx context.Context, last string, fn func(tags []string) error) error
}

type Option func(*Source)

// WithLimits overrides the extraction limits applied after the pull.
func WithLimits(l atomicfs.ExtractLimits) Option {
	return func(s *Source) { s.local = s.local.WithLimits(l) }
}

// WithMaxBlobSize bounds a layer.
func WithMaxBlobSize(n int64) Option { return func(s *Source) { s.maxBlob = n } }

// WithRepositoryFactory replaces how repositories are opened. See Registry.
func WithRepositoryFactory(f func(reference string) (Registry, error)) Option {
	return func(s *Source) { s.newRepository = f }
}

func New(opts ...Option) *Source {
	s := &Source{
		local:   local.New(),
		cache:   source.NewTempCache("oci"),
		maxBlob: MaxBlobSize,
	}
	s.newRepository = defaultRepository
	for _, o := range opts {
		o(s)
	}
	return s
}

var (
	_ ports.ReleaseSource = (*Source)(nil)
	_ io.Closer           = (*Source)(nil)
)

func (s *Source) Schemes() []string { return []string{Scheme} }

func (s *Source) Close() error { return s.cache.Close() }

func (s *Source) Resolve(ctx context.Context, ref ports.Ref) (ports.ResolvedRelease, error) {
	archive, err := s.pull(ctx, ref)
	if err != nil {
		return ports.ResolvedRelease{}, err
	}

	resolved, err := s.local.Resolve(ctx, ports.Ref{
		Scheme:   local.Scheme,
		Location: archive,
		Digest:   ref.Digest,
	})
	if err != nil {
		return ports.ResolvedRelease{}, err
	}
	resolved.Ref = ref
	return resolved, nil
}

func (s *Source) Fetch(ctx context.Context, ref ports.Ref, destDir string) (ports.BundlePath, error) {
	archive, err := s.pull(ctx, ref)
	if err != nil {
		return "", err
	}
	return s.local.Fetch(ctx, ports.Ref{Scheme: local.Scheme, Location: archive}, destDir)
}

// List enumerates a repository's tags.
//
// This is the one transport that *can* answer, because a registry keeps a tag
// list — which is why `List` is on the port at all. A tag that is not a version
// is skipped rather than reported: repositories accumulate `latest` and
// `edge`, and neither is something to install by number.
func (s *Source) List(ctx context.Context, ref ports.Ref) ([]domain.Version, error) {
	repo, err := s.newRepository(ref.Location)
	if err != nil {
		return nil, err
	}

	var out []domain.Version
	err = repo.Tags(ctx, "", func(tags []string) error {
		for _, tag := range tags {
			if v, parseErr := domain.ParseVersion(tag); parseErr == nil {
				out = append(out, v)
			}
		}
		return nil
	})
	if err != nil {
		return nil, domain.RuntimeError(err, "cannot list tags for %s", ref.Location).
			WithHint("check the reference and that `docker login` covers this registry")
	}
	return out, nil
}

// pull downloads the bundle layer once per source.
func (s *Source) pull(ctx context.Context, ref ports.Ref) (string, error) {
	reference, err := s.reference(ref)
	if err != nil {
		return "", err
	}

	if cached, ok := s.cache.Lookup(reference); ok {
		return cached, nil
	}

	repo, err := s.newRepository(reference)
	if err != nil {
		return "", err
	}

	manifest, err := fetchManifest(ctx, repo, reference)
	if err != nil {
		return "", err
	}

	layer, err := bundleLayer(manifest, reference)
	if err != nil {
		return "", err
	}
	if s.maxBlob > 0 && layer.Size > s.maxBlob {
		return "", domain.ValidationError(nil,
			"%s declares a %d byte bundle, over the limit of %d",
			reference, layer.Size, s.maxBlob)
	}

	path, err := s.cache.Reserve()
	if err != nil {
		return "", err
	}
	if err := s.writeLayer(ctx, repo, layer, path); err != nil {
		_ = os.Remove(path)
		return "", err
	}

	s.cache.Store(reference, path)
	return path, nil
}

// reference rebuilds the registry reference from the parsed ref.
//
// A digest on the reference is passed through to the registry, which then
// verifies the bytes it returns against it. That is a different guarantee from
// `ref.Digest`, which is the *bundle's* content digest and is checked after
// extraction: one says the registry gave us what we asked for, the other says
// what we asked for is what the vendor published.
func (s *Source) reference(ref ports.Ref) (string, error) {
	location := strings.TrimSpace(ref.Location)
	if location == "" {
		return "", domain.Usage("no oci reference was given").
			WithHint("references look like oci://registry.example/demo/bundle:1.2.0")
	}
	if strings.Contains(location, "://") {
		return "", domain.Usage("malformed oci reference %q", location)
	}

	// A bare repository with no tag or digest would resolve to whatever
	// `latest` happens to be, which for a release is the thing a content
	// digest exists to prevent.
	if _, tag, err := splitReference(location); err != nil {
		return "", err
	} else if tag == "" {
		return "", domain.Usage("the oci reference %q names no version", location).
			WithHint("add a tag or a digest, e.g. oci://%s:1.2.0", location)
	}
	return location, nil
}

// splitReference separates the repository from its tag or digest.
func splitReference(location string) (repo, version string, err error) {
	if at := strings.LastIndex(location, "@"); at > 0 {
		return location[:at], location[at+1:], nil
	}

	// A colon inside the last path element is a tag; one before a slash is
	// a port on the registry host.
	slash := strings.LastIndex(location, "/")
	if colon := strings.LastIndex(location, ":"); colon > slash {
		return location[:colon], location[colon+1:], nil
	}
	return location, "", nil
}

func fetchManifest(ctx context.Context, repo Registry, reference string) (ocispec.Manifest, error) {
	desc, body, err := repo.FetchReference(ctx, reference)
	if err != nil {
		return ocispec.Manifest{}, registryError(err, reference)
	}
	defer func() { _ = body.Close() }()

	data, err := readExactly(body, desc.Size)
	if err != nil {
		return ocispec.Manifest{}, domain.RuntimeError(err,
			"cannot read the manifest of %s", reference)
	}

	// Verified here, not by the client.
	//
	// oras checks the Content-Length and the Docker-Content-Digest *header*
	// and then hands back the raw body -- so a registry that sends a correct
	// header and different bytes gets through. Everything downstream trusts
	// this document to say which layer is the bundle, so it is checked
	// against the digest that was resolved rather than assumed.
	if got := digest.FromBytes(data); got != desc.Digest {
		return ocispec.Manifest{}, domain.ValidationError(domain.ErrDigestMismatch,
			"the manifest of %s does not match its digest", reference).
			WithHint("the registry returned content other than what it advertised; " +
				"do not install from it")
	}

	var manifest ocispec.Manifest
	if err := unmarshal(data, &manifest); err != nil {
		return ocispec.Manifest{}, domain.ValidationError(err,
			"%s is not an OCI artifact manifest", reference).
			WithHint("a release bundle is published as an artifact, not as a container image")
	}
	return manifest, nil
}

// bundleLayer picks the layer holding the bundle.
//
// An artifact with exactly one layer is taken whatever its media type, because
// requiring the type would break every bundle published before this package
// existed. More than one layer, and the type is what disambiguates -- guessing
// would mean installing whichever layer happened to be first.
func bundleLayer(manifest ocispec.Manifest, reference string) (ocispec.Descriptor, error) {
	switch len(manifest.Layers) {
	case 0:
		return ocispec.Descriptor{}, domain.ValidationError(nil,
			"%s has no layers, so it carries no bundle", reference)
	case 1:
		return manifest.Layers[0], nil
	}

	for _, layer := range manifest.Layers {
		if layer.MediaType == MediaType {
			return layer, nil
		}
	}
	return ocispec.Descriptor{}, domain.ValidationError(nil,
		"%s has %d layers and none is a release bundle", reference, len(manifest.Layers)).
		WithHint("publish the bundle with media type %s", MediaType)
}

// writeLayer streams a blob to disk, bounded.
func (s *Source) writeLayer(ctx context.Context, repo Registry, layer ocispec.Descriptor, path string) error {
	body, err := repo.Fetch(ctx, layer)
	if err != nil {
		// Through the same interpreter as the manifest fetch: a 401 on
		// the blob is the same problem with the same remedy, and a
		// transport error an operator has to decode is not an answer.
		return registryError(err, "the bundle layer")
	}
	defer func() { _ = body.Close() }()

	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return domain.Internal(err, "cannot create the download file")
	}
	defer func() { _ = out.Close() }()

	// The client does not do this for us. `blobStore.Fetch` compares the
	// Content-Length and the Docker-Content-Digest header against the
	// descriptor and then returns the response body untouched, so a registry
	// serving arbitrary bytes under a correct header is caught by nothing
	// until -- at best -- the operator happened to pin --digest.
	verifier := content.NewVerifyReader(body, layer)

	var reader io.Reader = verifier
	if s.maxBlob > 0 {
		reader = io.LimitReader(verifier, s.maxBlob+1)
	}

	written, err := io.Copy(out, reader)
	if err != nil {
		return domain.RuntimeError(err, "the bundle layer did not download completely")
	}
	if s.maxBlob > 0 && written > s.maxBlob {
		return domain.ValidationError(nil,
			"the bundle layer exceeds the limit of %d bytes", s.maxBlob)
	}

	if err := verifier.Verify(); err != nil {
		return domain.ValidationError(domain.ErrDigestMismatch,
			"the bundle layer does not match the digest %s advertises for it",
			layer.Digest).
			WithHint("the registry returned content other than what it advertised; " +
				"do not install from it")
	}
	return nil
}

func registryError(err error, reference string) error {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not found") || strings.Contains(msg, "404"):
		return domain.ValidationError(domain.ErrReleaseNotFound,
			"no artifact at %s", reference).
			WithHint("check the reference; a tag may have been moved or removed")
	case strings.Contains(msg, "unauthorized") || strings.Contains(msg, "401") ||
		strings.Contains(msg, "denied") || strings.Contains(msg, "403"):
		return domain.ValidationError(err, "access to %s was refused", reference).
			WithHint("run `docker login <registry>` as the user the manager runs as; " +
				"credentials are read from the ambient Docker configuration")
	default:
		return domain.RuntimeError(err, "cannot reach %s", reference)
	}
}

// defaultRepository opens a registry repository with ambient credentials.
func defaultRepository(reference string) (Registry, error) {
	repo, err := remoteRepository(reference)
	if err != nil {
		return nil, err
	}
	return repo, nil
}

func remoteRepository(reference string) (*remote.Repository, error) {
	repoRef, _, err := splitReference(reference)
	if err != nil {
		return nil, err
	}

	repo, err := remote.NewRepository(repoRef)
	if err != nil {
		return nil, domain.Usage("invalid oci reference %q: %v", repoRef, err)
	}

	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{
		AllowPlaintextPut: false,
	})
	if err != nil {
		// No Docker configuration is not an error: a public registry
		// needs none, and failing here would make an anonymous pull
		// impossible on a machine that has never logged in.
		repo.Client = &auth.Client{Client: retry.DefaultClient, Cache: auth.NewCache()}
		return repo, nil
	}

	repo.Client = &auth.Client{
		Client:     retry.DefaultClient,
		Cache:      auth.NewCache(),
		Credential: credentials.Credential(store),
	}
	return repo, nil
}

// readExactly reads a body with a declared size, refusing one that overruns it.
func readExactly(r io.Reader, size int64) ([]byte, error) {
	if size <= 0 || size > 4<<20 {
		return nil, fmt.Errorf("implausible manifest size %d", size)
	}
	data, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, errors.New("the manifest does not match its declared size")
	}
	return data, nil
}

// unmarshal decodes an artifact manifest.
func unmarshal(data []byte, out *ocispec.Manifest) error {
	return json.Unmarshal(data, out)
}
