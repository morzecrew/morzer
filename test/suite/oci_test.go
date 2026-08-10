package suite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/morzecrew/morzer/internal/adapters/source"
	"github.com/morzecrew/morzer/internal/adapters/source/local"
	"github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/contract"
)

// A registry, in about as few lines as the distribution API allows.
//
// Running a real registry would mean Docker in the test suite, which is the one
// thing this suite exists to avoid: every other adapter is exercised against
// something in-process, and a transport that needed a container to test would
// be the transport nobody ran the tests for.
//
// It implements exactly the three requests a bundle pull makes -- a version
// probe, a manifest fetch, a blob fetch -- and computes real digests, so the
// oras client's own verification is genuinely exercised rather than bypassed.
type fakeRegistry struct {
	t *testing.T

	manifest    []byte
	manifestDig string
	blob        []byte
	blobDig     string

	// blobStatus overrides the blob response, so a test can serve a
	// failure the client has to interpret.
	blobStatus int
	// corruptBlob serves bytes that do not match the advertised digest.
	corruptBlob bool

	// tags overrides what the repository enumerates. Empty means the default
	// list, so every existing test keeps the shape it was written against.
	tags []string

	// served counts the bytes each kind of request cost, which is the only
	// way to assert that a poll is cheap. A test asserting that Peek
	// returned the right digest would pass just as happily if it had
	// downloaded the bundle to find it out.
	manifestBytes atomic.Int64
	blobBytes     atomic.Int64
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// newFakeRegistry publishes one artifact carrying the given archive.
func newFakeRegistry(t *testing.T, archive []byte, mediaType string, extraLayers int) *httptest.Server {
	t.Helper()

	reg := &fakeRegistry{t: t, blob: archive, blobDig: digestOf(archive)}

	config := []byte(`{}`)
	layers := []map[string]any{}
	for range extraLayers {
		junk := []byte("not the bundle")
		layers = append(layers, map[string]any{
			"mediaType": "application/vnd.oci.image.layer.v1.tar",
			"digest":    digestOf(junk),
			"size":      len(junk),
		})
	}
	layers = append(layers, map[string]any{
		"mediaType": mediaType,
		"digest":    reg.blobDig,
		"size":      len(archive),
	})

	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"artifactType":  oci.MediaType,
		"config": map[string]any{
			"mediaType": "application/vnd.oci.empty.v1+json",
			"digest":    digestOf(config),
			"size":      len(config),
		},
		"layers": layers,
	})
	require.NoError(t, err)

	reg.manifest = manifest
	reg.manifestDig = digestOf(manifest)

	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)
	return srv
}

func (r *fakeRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch {
	// The version probe every distribution client makes first.
	case req.URL.Path == "/v2/":
		w.WriteHeader(http.StatusOK)

	case strings.Contains(req.URL.Path, "/manifests/"):
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Docker-Content-Digest", r.manifestDig)
		w.Header().Set("Content-Length", fmt.Sprint(len(r.manifest)))
		if req.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		r.manifestBytes.Add(int64(len(r.manifest)))
		_, _ = w.Write(r.manifest)

	// The one thing a registry can answer that a URL cannot: what versions
	// exist. `latest` is in the list on purpose -- repositories accumulate
	// tags that are not versions, and installing one by number would be
	// installing whatever it pointed at today.
	case strings.HasSuffix(req.URL.Path, "/tags/list"):
		w.Header().Set("Content-Type", "application/json")
		tags := r.tags
		if len(tags) == 0 {
			tags = []string{"1.2.0", "1.3.0", "latest"}
		}
		body, err := json.Marshal(map[string]any{"name": "demo/bundle", "tags": tags})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)

	case strings.Contains(req.URL.Path, "/blobs/"):
		if r.blobStatus != 0 {
			w.WriteHeader(r.blobStatus)
			return
		}
		body := r.blob
		if r.corruptBlob {
			// Same length, different bytes: the digest check has to
			// be what catches this, not an accounting mismatch that
			// would also fire on a truncated download.
			body = append([]byte("tampered"), r.blob[len("tampered"):]...)
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		r.blobBytes.Add(int64(len(body)))
		_, _ = w.Write(body)

	default:
		http.NotFound(w, req)
	}
}

// ociRef builds the reference form ParseRef produces: everything after
// "oci://", against a plain-HTTP test registry.
func ociRef(srv *httptest.Server, repo, tag string) ports.Ref {
	host := strings.TrimPrefix(srv.URL, "http://")
	return ports.Ref{Scheme: "oci", Location: host + "/" + repo + ":" + tag}
}

// newTestSource points the source at a plaintext test registry.
//
// Production reaches registries over TLS with ambient Docker credentials; this
// swaps the repository factory rather than adding a "plaintext" option to the
// adapter, so no production path can reach an unencrypted registry by
// configuration.
func newTestSource(t *testing.T, srv *httptest.Server, opts ...oci.Option) *oci.Source {
	t.Helper()

	factory := func(reference string) (oci.Registry, error) {
		repo, err := remote.NewRepository(repositoryOf(reference))
		if err != nil {
			return nil, err
		}
		repo.PlainHTTP = true
		return repo, nil
	}

	all := append([]oci.Option{oci.WithRepositoryFactory(factory)}, opts...)
	source := oci.New(all...)
	t.Cleanup(func() { _ = source.Close() })
	return source
}

// repositoryOf strips a tag or digest, which is what remote.NewRepository
// wants.
func repositoryOf(reference string) string {
	if at := strings.LastIndex(reference, "@"); at > 0 {
		return reference[:at]
	}
	slash := strings.LastIndex(reference, "/")
	if colon := strings.LastIndex(reference, ":"); colon > slash {
		return reference[:colon]
	}
	return reference
}

func TestReleaseSourceContract_OCI(t *testing.T) {
	contract.RunReleaseSourceSuite(t, testBundlePath(t),
		func(t *testing.T, bundleDir string) (ports.ReleaseSource, ports.Ref) {
			srv := newFakeRegistry(t, bundleArchive(t), oci.MediaType, 0)
			return newTestSource(t, srv), ociRef(srv, "demo/bundle", "1.2.0")
		})
}

func TestOCIListsVersionTagsAndSkipsTheRest(t *testing.T) {
	srv := newFakeRegistry(t, bundleArchive(t), oci.MediaType, 0)
	source := newTestSource(t, srv)

	versions, err := source.List(context.Background(), ociRef(srv, "demo/bundle", "1.2.0"))
	require.NoError(t, err, "a registry keeps a tag list, which is why List is on the port")

	labels := make([]string, 0, len(versions))
	for _, v := range versions {
		labels = append(labels, v.String())
	}
	// "latest" is not a version. Offering it as one would let an operator
	// install by number and get whatever it happened to point at.
	assert.ElementsMatch(t, []string{"1.2.0", "1.3.0"}, labels)
}

func TestOCIRefusesAReferenceWithNoVersion(t *testing.T) {
	srv := newFakeRegistry(t, bundleArchive(t), oci.MediaType, 0)
	source := newTestSource(t, srv)

	host := strings.TrimPrefix(srv.URL, "http://")
	_, err := source.Resolve(context.Background(),
		ports.Ref{Scheme: "oci", Location: host + "/demo/bundle"})

	// A bare repository resolves to whatever `latest` happens to be, which
	// for a release is exactly what a content digest exists to prevent.
	require.Error(t, err, "an oci reference with no tag or digest must be refused")
	assert.Contains(t, err.Error(), "names no version")
	assert.Contains(t, domain.AsError(err).Hint, "1.2.0")
}

func TestOCIPicksTheBundleLayerByMediaType(t *testing.T) {
	// A vendor may publish signatures, provenance or documentation as
	// additional layers. Guessing would mean installing whichever came
	// first in the list.
	srv := newFakeRegistry(t, bundleArchive(t), oci.MediaType, 2)
	source := newTestSource(t, srv)

	resolved, err := source.Resolve(context.Background(), ociRef(srv, "demo/bundle", "1.2.0"))
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", resolved.Version.String())
}

func TestOCIRefusesAnArtifactWithNoIdentifiableBundle(t *testing.T) {
	srv := newFakeRegistry(t, bundleArchive(t), "application/vnd.oci.image.layer.v1.tar", 2)
	source := newTestSource(t, srv)

	_, err := source.Resolve(context.Background(), ociRef(srv, "demo/bundle", "1.2.0"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "none is a release bundle")
	assert.Contains(t, domain.AsError(err).Hint, oci.MediaType)
}

func TestOCIRefusesABlobThatDoesNotMatchItsDigest(t *testing.T) {
	archive := bundleArchive(t)
	srv := newFakeRegistry(t, archive, oci.MediaType, 0)
	handler, ok := srv.Config.Handler.(*fakeRegistry)
	require.True(t, ok)
	handler.corruptBlob = true

	source := newTestSource(t, srv)

	// A registry serving bytes other than the ones its manifest names must
	// fail here, not produce a bundle that hashes wrong three steps later
	// with no explanation of why.
	_, err := source.Resolve(context.Background(), ociRef(srv, "demo/bundle", "1.2.0"))
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "digest")
}

func TestOCIReportsAnUnauthorisedPullActionably(t *testing.T) {
	srv := newFakeRegistry(t, bundleArchive(t), oci.MediaType, 0)
	handler, ok := srv.Config.Handler.(*fakeRegistry)
	require.True(t, ok)
	handler.blobStatus = http.StatusUnauthorized

	source := newTestSource(t, srv)

	_, err := source.Resolve(context.Background(), ociRef(srv, "demo/bundle", "1.2.0"))
	require.Error(t, err)
	// The remedy is a `docker login`, and saying so beats a transport error
	// an operator has to decode.
	assert.Contains(t, strings.ToLower(err.Error()+domain.AsError(err).Hint), "docker login")
}

func TestOCIAppliesArchiveRulesToWhatItPulls(t *testing.T) {
	// The transport must not be a way around the archive rules.
	hostile := filepath.Join(t.TempDir(), "hostile.tar.zst")
	writeArchive(t, hostile, []tarEntry{
		{Name: "manifest.yaml", Body: "ok"},
		{Name: "../../../../etc/cron.d/pwned", Body: "evil"},
	})
	data, err := os.ReadFile(hostile)
	require.NoError(t, err)

	srv := newFakeRegistry(t, data, oci.MediaType, 0)
	source := newTestSource(t, srv)

	_, err = source.Resolve(context.Background(), ociRef(srv, "demo/bundle", "1.2.0"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrPathEscape), "got: %v", err)
}

// TestPeekReadsAManifestAndResolveReadsTheBundle is the measurement the whole
// of channel following rests on.
//
// RFC 0016 §5.2 priced a tick at "one `Resolve`", on the assumption that
// resolving a reference asks the registry a question. It does not: a
// ResolvedRelease carries the bundle's *content* digest, which is a property of
// the bytes, so the OCI source pulls the layer to compute one. A poll built on
// Resolve would download the entire release every tick to discover that nothing
// had changed -- at a five-minute cadence, the bundle 288 times a day.
//
// So the assertion is the byte count, not the digest. Checking that Peek
// returned the right answer would pass just as happily if it had downloaded
// everything to find it out, which is precisely the bug this is here to stop.
func TestPeekReadsAManifestAndResolveReadsTheBundle(t *testing.T) {
	archive := bundleArchive(t)
	srv := newFakeRegistry(t, archive, oci.MediaType, 0)
	handler, ok := srv.Config.Handler.(*fakeRegistry)
	require.True(t, ok)

	source := newTestSource(t, srv)
	ctx := context.Background()
	ref := ociRef(srv, "demo/bundle", "dev")

	state, err := source.Peek(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, handler.manifestDig, state.UpstreamDigest,
		"peek reports the registry's identity for the tag")

	// Not one byte of the bundle. This is the claim.
	assert.Zero(t, handler.blobBytes.Load(),
		"peek fetched %d bytes of the bundle; a poll must not download what it is watching",
		handler.blobBytes.Load())
	assert.Less(t, handler.manifestBytes.Load(), int64(len(archive)),
		"a peek cost more than the bundle it was avoiding")

	// The pinned reference addresses what was seen, by digest, so a tag that
	// moves between the decision and the fetch cannot substitute a different
	// bundle for the one that was resolved.
	assert.Contains(t, state.Pinned.Location, "@"+handler.manifestDig)
	assert.NotContains(t, state.Pinned.Location, ":dev")

	// And the contrast, measured rather than asserted from the source: the
	// call the RFC assumed was cheap moves the whole archive.
	_, err = source.Resolve(ctx, state.Pinned)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, handler.blobBytes.Load(), int64(len(archive)),
		"resolve did not transfer the bundle, so this comparison proves nothing")
}

// TestPeekIsRefusedByTransportsThatCannotWatch.
//
// The tempting fallback -- peek is unsupported, so resolve instead -- turns a
// cheap poll into a download loop. It is refused by name instead, so an
// operator configuring `update.channel` against an https URL is told at once
// rather than discovering the bandwidth later.
func TestPeekIsRefusedByTransportsThatCannotWatch(t *testing.T) {
	registry, err := source.NewRegistry(local.New())
	require.NoError(t, err)

	_, err = registry.Peek(context.Background(),
		ports.Ref{Scheme: local.Scheme, Location: testBundlePath(t)})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrUnsupported), "got: %v", err)
}

func TestOCIPullsOnceForResolveAndFetch(t *testing.T) {
	archive := bundleArchive(t)
	srv := newFakeRegistry(t, archive, oci.MediaType, 0)
	source := newTestSource(t, srv)

	ctx := context.Background()
	ref := ociRef(srv, "demo/bundle", "1.2.0")

	_, err := source.Resolve(ctx, ref)
	require.NoError(t, err)

	dest := filepath.Join(t.TempDir(), "fetched")
	_, err = source.Fetch(ctx, ref, dest)
	require.NoError(t, err)

	got, err := atomicfs.DigestTree(dest)
	require.NoError(t, err)
	want, err := atomicfs.DigestTree(testBundlePath(t))
	require.NoError(t, err)

	// Same bundle, fourth transport. This is what makes a recorded digest a
	// property of the release rather than of how it was delivered.
	assert.Equal(t, want, got)
}
