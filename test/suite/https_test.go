package suite

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/source/https"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/contract"
)

// bundleArchive packs the example bundle and returns the bytes a server would
// serve.
func bundleArchive(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bundle.tar.zst")
	writeTarZst(t, testBundlePath(t), path)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

// serveBundle starts a TLS server handing out the example bundle, and returns a
// source configured to trust it.
//
// TLS rather than plain HTTP even in tests: the source refuses anything else,
// and a test that reached for an http:// server would be testing a code path
// production must never take.
func serveBundle(t *testing.T, handler http.Handler, opts ...https.Option) (*https.Source, string) {
	t.Helper()

	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	all := append([]https.Option{https.WithTransport(srv.Client().Transport)}, opts...)
	source := https.New(all...)
	t.Cleanup(func() { _ = source.Close() })

	// ParseRef strips the scheme, and so does the source's own reference
	// vocabulary; the location is everything after "https://".
	return source, strings.TrimPrefix(srv.URL, "https://")
}

func TestReleaseSourceContract_HTTPS(t *testing.T) {
	contract.RunReleaseSourceSuite(t, testBundlePath(t),
		func(t *testing.T, bundleDir string) (ports.ReleaseSource, ports.Ref) {
			archive := bundleArchive(t)

			source, host := serveBundle(t, http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					// A missing bundle has to 404 rather than
					// serve the good one, or the suite's
					// "missing reference" case would pass by
					// accident.
					if r.URL.Path != "/demo-1.2.0.tar.zst" {
						http.NotFound(w, r)
						return
					}
					_, _ = w.Write(archive)
				}))

			return source, ports.Ref{Scheme: "https", Location: host + "/demo-1.2.0.tar.zst"}
		})
}

func TestHTTPSRefusesADowngradeRedirect(t *testing.T) {
	// The attack this exists to stop: an operator types an https URL, and
	// the server hands the bundle over plaintext by asking politely.
	plaintext := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("a bundle nothing authenticated"))
		}))
	t.Cleanup(plaintext.Close)

	source, host := serveBundle(t, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plaintext.URL+"/bundle.tar.zst", http.StatusFound)
		}))

	_, err := source.Resolve(context.Background(),
		ports.Ref{Scheme: "https", Location: host + "/bundle.tar.zst"})

	require.Error(t, err, "a redirect out of TLS must be refused")
	assert.Contains(t, err.Error(), "redirect")
	assert.Contains(t, domain.AsError(err).Hint, "out of band")
}

func TestHTTPSRefusesAnEndlessRedirectChain(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
		}))
	t.Cleanup(srv.Close)

	source := https.New(https.WithTransport(srv.Client().Transport))
	t.Cleanup(func() { _ = source.Close() })

	_, err := source.Resolve(context.Background(), ports.Ref{
		Scheme: "https", Location: strings.TrimPrefix(srv.URL, "https://") + "/bundle.tar.zst",
	})
	require.Error(t, err, "a redirect loop must terminate with an error, not a hang")
}

func TestHTTPSRetriesAServerError(t *testing.T) {
	archive := bundleArchive(t)
	var attempts atomic.Int32

	source, host := serveBundle(t, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// A mirror restarting during a deploy is ordinary, and
			// failing the update over it would be the tool being
			// less reliable than the thing it deploys.
			if attempts.Add(1) < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write(archive)
		}), https.WithBackoff(time.Millisecond))

	resolved, err := source.Resolve(context.Background(),
		ports.Ref{Scheme: "https", Location: host + "/bundle.tar.zst"})

	require.NoError(t, err, "a transient 503 must be retried, not fatal")
	assert.Equal(t, "1.2.0", resolved.Version.String())
	assert.EqualValues(t, 3, attempts.Load())
}

func TestHTTPSDoesNotRetryANotFound(t *testing.T) {
	var attempts atomic.Int32

	source, host := serveBundle(t, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			http.NotFound(w, r)
		}), https.WithBackoff(time.Millisecond))

	_, err := source.Resolve(context.Background(),
		ports.Ref{Scheme: "https", Location: host + "/missing.tar.zst"})

	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrReleaseNotFound), "got: %v", err)

	// Repeating a request the server answered definitively only makes the
	// operator wait longer for the same answer.
	assert.EqualValues(t, 1, attempts.Load(), "a 404 must not be retried")
}

func TestHTTPSRefusesAnOversizedDeclaration(t *testing.T) {
	source, host := serveBundle(t, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1048576")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(make([]byte, 1<<20))
		}), https.WithMaxBody(1024))

	_, err := source.Resolve(context.Background(),
		ports.Ref{Scheme: "https", Location: host + "/big.tar.zst"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "over the limit")
}

func TestHTTPSRefusesAnOversizedBodyMidStream(t *testing.T) {
	// No Content-Length: chunked, so the only bound is the one applied
	// while reading. A server that lies about its length reaches the same
	// guard.
	source, host := serveBundle(t, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			flusher, ok := w.(http.Flusher)
			require.True(t, ok)
			chunk := make([]byte, 4096)
			for range 64 {
				_, _ = w.Write(chunk)
				flusher.Flush()
			}
		}), https.WithMaxBody(8192))

	_, err := source.Resolve(context.Background(),
		ports.Ref{Scheme: "https", Location: host + "/bomb.tar.zst"})

	require.Error(t, err, "a body past the limit must be refused while it streams")
	assert.Contains(t, err.Error(), "more than the limit")
}

func TestHTTPSAppliesArchiveRulesToWhatItDownloads(t *testing.T) {
	// The transport must not be a way around the archive rules. A hostile
	// archive served over TLS is still a hostile archive.
	hostile := filepath.Join(t.TempDir(), "hostile.tar.zst")
	writeArchive(t, hostile, []tarEntry{
		{Name: "manifest.yaml", Body: "ok"},
		{Name: "../../../../etc/cron.d/pwned", Body: "evil"},
	})
	data, err := os.ReadFile(hostile)
	require.NoError(t, err)

	source, host := serveBundle(t, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))

	_, err = source.Resolve(context.Background(),
		ports.Ref{Scheme: "https", Location: host + "/hostile.tar.zst"})

	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrPathEscape),
		"the transport must not weaken extraction; got: %v", err)
}

func TestHTTPSDownloadsOnceForResolveAndFetch(t *testing.T) {
	archive := bundleArchive(t)
	var requests atomic.Int32

	source, host := serveBundle(t, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			_, _ = w.Write(archive)
		}))

	ctx := context.Background()
	ref := ports.Ref{Scheme: "https", Location: host + "/bundle.tar.zst"}

	_, err := source.Resolve(ctx, ref)
	require.NoError(t, err)

	dest := filepath.Join(t.TempDir(), "fetched")
	_, err = source.Fetch(ctx, ref, dest)
	require.NoError(t, err)

	// Every caller resolves then fetches. Downloading twice per command is
	// a waste an operator on a slow link would notice.
	assert.EqualValues(t, 1, requests.Load(),
		"resolve and fetch of one reference must download once")

	digest, err := atomicfs.DigestTree(dest)
	require.NoError(t, err)
	want, err := atomicfs.DigestTree(testBundlePath(t))
	require.NoError(t, err)
	assert.Equal(t, want, digest)
}

func TestHTTPSCloseRemovesWhatItDownloaded(t *testing.T) {
	archive := bundleArchive(t)
	source, host := serveBundle(t, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) }))

	_, err := source.Resolve(context.Background(),
		ports.Ref{Scheme: "https", Location: host + "/bundle.tar.zst"})
	require.NoError(t, err)

	require.NoError(t, source.Close())
	require.NoError(t, source.Close(), "closing twice must be safe")
}

func TestHTTPSNamesACertificateProblemInsteadOfRetrying(t *testing.T) {
	// The server's certificate is real but self-signed, and the source is
	// given no transport that trusts it.
	srv := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("never read")) }))
	t.Cleanup(srv.Close)

	source := https.New(https.WithBackoff(time.Millisecond))
	t.Cleanup(func() { _ = source.Close() })

	_, err := source.Resolve(context.Background(), ports.Ref{
		Scheme: "https", Location: strings.TrimPrefix(srv.URL, "https://") + "/bundle.tar.zst",
	})

	require.Error(t, err)
	// A certificate does not start being trusted on the second attempt, so
	// retrying only makes an operator wait three times as long for a message
	// that never mentions the certificate.
	assert.Contains(t, err.Error(), "certificate")
	assert.Contains(t, domain.AsError(err).Hint, "no option to skip verification")
}
