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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/registry/remote"

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
		_, _ = w.Write(r.manifest)

	// The one thing a registry can answer that a URL cannot: what versions
	// exist. `latest` is in the list on purpose -- repositories accumulate
	// tags that are not versions, and installing one by number would be
	// installing whatever it pointed at today.
	case strings.HasSuffix(req.URL.Path, "/tags/list"):
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"demo/bundle","tags":["1.2.0","1.3.0","latest"]}`))

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
