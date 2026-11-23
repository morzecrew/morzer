package suite

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/source/https"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// fakeSourceCredentials is the installation's `sources.https` and its secrets.
type fakeSourceCredentials struct {
	entries map[string]domain.HTTPSSource
	values  map[string]string
	fail    error
	reveals atomic.Int32
}

func (f *fakeSourceCredentials) SourceFor(_ context.Context, authority string) (domain.HTTPSSource, bool, error) {
	if f.fail != nil {
		return domain.HTTPSSource{}, false, f.fail
	}
	e, ok := f.entries[authority]
	return e, ok, nil
}

func (f *fakeSourceCredentials) RevealSource(_ context.Context, e domain.HTTPSSource) (string, error) {
	f.reveals.Add(1)
	v, ok := f.values[e.Secret]
	if !ok {
		return "", domain.ValidationError(domain.ErrNotFound, "the secret %q is not set", e.Secret)
	}
	return v, nil
}

func credentialsFor(entries ...domain.HTTPSSource) *fakeSourceCredentials {
	f := &fakeSourceCredentials{entries: map[string]domain.HTTPSSource{}, values: map[string]string{}}
	for _, e := range entries {
		f.entries[e.Host] = e
	}
	return f
}

// credentialServer is one TLS server answering for every host name, so a
// test can name servers by authority: the client dials it whatever the URL
// says. It records the Authorization each request arrived with, by the host
// and port the request named.
type credentialServer struct {
	mu   sync.Mutex
	seen map[string][]string
}

func (c *credentialServer) saw(authority string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen[authority]...)
}

func serveCredentialed(t *testing.T, handler func(w http.ResponseWriter, r *http.Request), creds ports.SourceCredentials) (*https.Source, *credentialServer) {
	t.Helper()
	rec := &credentialServer{seen: map[string][]string{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.seen[r.Host] = append(rec.seen[r.Host], r.Header.Get("Authorization"))
		rec.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	base, ok := srv.Client().Transport.(*http.Transport)
	require.True(t, ok, "the test server's client has no *http.Transport")
	transport := base.Clone()
	// Every name reaches this one server, whose certificate names none of
	// them: what is under test is which request carries what.
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // a test server
	addr := srv.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	source := https.New(https.WithTransport(transport), https.WithCredentials(creds), https.WithBackoff(time.Millisecond))
	t.Cleanup(func() { _ = source.Close() })
	return source, rec
}

func serveArchive(t *testing.T) func(http.ResponseWriter, *http.Request) {
	archive := bundleArchive(t)
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) }
}

func vendorToken() domain.HTTPSSource {
	return domain.HTTPSSource{Host: "downloads.vendor.example", Auth: domain.SourceAuthBearer, Secret: "vendor_token"}
}

func TestAConfiguredServerReceivesItsCredential(t *testing.T) {
	ctx := context.Background()
	t.Run("bearer", func(t *testing.T) {
		creds := credentialsFor(vendorToken())
		creds.values["vendor_token"] = "tok-123\n"
		source, rec := serveCredentialed(t, serveArchive(t), creds)
		_, err := source.Resolve(ctx, ports.Ref{Scheme: "https", Location: "downloads.vendor.example/b.tar.zst"})
		require.NoError(t, err)
		assert.Equal(t, []string{"Bearer tok-123"}, rec.saw("downloads.vendor.example"),
			"the token was not sent as it was set, less its newline")
	})
	t.Run("basic, with a colon in the password", func(t *testing.T) {
		entry := domain.HTTPSSource{Host: "downloads.vendor.example", Auth: domain.SourceAuthBasic, Secret: "vendor_login"}
		creds := credentialsFor(entry)
		creds.values["vendor_login"] = "customer:pa:ss"
		source, rec := serveCredentialed(t, serveArchive(t), creds)
		_, err := source.Resolve(ctx, ports.Ref{Scheme: "https", Location: "downloads.vendor.example/b.tar.zst"})
		require.NoError(t, err)
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("customer:pa:ss"))
		assert.Equal(t, []string{want}, rec.saw("downloads.vendor.example"))
	})
	t.Run("a server with no entry", func(t *testing.T) {
		creds := credentialsFor(vendorToken())
		creds.values["vendor_token"] = "tok-123"
		source, rec := serveCredentialed(t, serveArchive(t), creds)
		_, err := source.Resolve(ctx, ports.Ref{Scheme: "https", Location: "mirror.example/b.tar.zst"})
		require.NoError(t, err)
		assert.Equal(t, []string{""}, rec.saw("mirror.example"), "a credential went to a server it is not for")
		assert.Zero(t, creds.reveals.Load(), "a secret was read for a server with no entry")
	})
}

// A redirect is judged on where it goes. The client itself would copy the
// header to a subdomain, and to the same host on another port.
func TestARedirectAwayFromTheServerCarriesNoCredential(t *testing.T) {
	ctx := context.Background()
	for name, to := range map[string]string{
		"to a subdomain":                 "https://cdn.downloads.vendor.example/b.tar.zst",
		"to another host":                "https://cdn.example/b.tar.zst",
		"to the same host on a port":     "https://downloads.vendor.example:8443/b.tar.zst",
		"to the same host, another path": "https://downloads.vendor.example/elsewhere/b.tar.zst",
	} {
		t.Run(name, func(t *testing.T) {
			creds := credentialsFor(vendorToken())
			creds.values["vendor_token"] = "tok-123"
			archive := serveArchive(t)
			source, rec := serveCredentialed(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Host == "downloads.vendor.example" && r.URL.Path == "/b.tar.zst" {
					http.Redirect(w, r, to, http.StatusFound)
					return
				}
				archive(w, r)
			}, creds)
			_, err := source.Resolve(ctx, ports.Ref{Scheme: "https", Location: "downloads.vendor.example/b.tar.zst"})
			require.NoError(t, err)

			target := strings.TrimPrefix(to, "https://")
			target = target[:strings.Index(target, "/")]
			if target == "downloads.vendor.example" {
				assert.Equal(t, []string{"Bearer tok-123", "Bearer tok-123"}, rec.saw(target),
					"a redirect within the server lost its credential")
				return
			}
			assert.Equal(t, []string{"Bearer tok-123"}, rec.saw("downloads.vendor.example"))
			assert.Equal(t, []string{""}, rec.saw(target), "the credential followed the redirect")
		})
	}
}

// A refusal names the secret the server was sent, never the value; and a
// server with no entry is answered with how to configure one.
func TestARefusedCredentialIsNamedByItsSecret(t *testing.T) {
	ctx := context.Background()
	refuse := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }

	creds := credentialsFor(vendorToken())
	creds.values["vendor_token"] = "tok-secret-value"
	source, _ := serveCredentialed(t, refuse, creds)
	_, err := source.Resolve(ctx, ports.Ref{Scheme: "https", Location: "downloads.vendor.example/b.tar.zst"})
	require.Error(t, err)
	text := err.Error() + " " + domain.AsError(err).Hint
	assert.Contains(t, text, "vendor_token")
	assert.NotContains(t, text, "tok-secret-value")

	source, _ = serveCredentialed(t, refuse, credentialsFor())
	_, err = source.Resolve(ctx, ports.Ref{Scheme: "https", Location: "mirror.example/b.tar.zst"})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "no credential is configured for mirror.example")
	assert.Contains(t, domain.AsError(err).Hint, "release source add https://mirror.example")
}

// A configured credential replaces a password in the reference, so one request
// never carries two (RFC 0038 decision 8).
func TestAConfiguredCredentialBeatsAPasswordInTheReference(t *testing.T) {
	creds := credentialsFor(vendorToken())
	creds.values["vendor_token"] = "tok-123"
	source, rec := serveCredentialed(t, serveArchive(t), creds)
	_, err := source.Resolve(context.Background(),
		ports.Ref{Scheme: "https", Location: "someone:hunter2@downloads.vendor.example/b.tar.zst"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Bearer tok-123"}, rec.saw("downloads.vendor.example"))
}

// A value that cannot be sent is refused without being quoted, and the
// request is not made.
func TestAnUnusableCredentialIsRefusedUnquoted(t *testing.T) {
	ctx := context.Background()
	basic := domain.HTTPSSource{Host: "downloads.vendor.example", Auth: domain.SourceAuthBasic, Secret: "vendor_login"}
	for name, tc := range map[string]struct {
		entry domain.HTTPSSource
		value string
		want  string
	}{
		"empty":               {vendorToken(), "\n", "is empty"},
		"two lines":           {vendorToken(), "tok-abc\nX-Injected: 1", "line break"},
		"basic with no colon": {basic, "tok-abc", "user:password"},
		"basic with no user":  {basic, ":tok-abc", "user:password"},
	} {
		t.Run(name, func(t *testing.T) {
			creds := credentialsFor(tc.entry)
			creds.values[tc.entry.Secret] = tc.value
			source, rec := serveCredentialed(t, serveArchive(t), creds)
			_, err := source.Resolve(ctx, ports.Ref{Scheme: "https", Location: "downloads.vendor.example/b.tar.zst"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), "tok-abc")
			assert.Empty(t, rec.saw("downloads.vendor.example"), "the request went out anyway")
		})
	}
}

// A secret that is not set, or an installation that cannot be read, stops the
// fetch with that reason and is not retried.
func TestACredentialThatCannotBeReadStopsTheFetch(t *testing.T) {
	ctx := context.Background()
	creds := credentialsFor(vendorToken())
	source, rec := serveCredentialed(t, serveArchive(t), creds)
	_, err := source.Resolve(ctx, ports.Ref{Scheme: "https", Location: "downloads.vendor.example/b.tar.zst"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrNotFound), "got: %v", err)
	assert.Contains(t, err.Error(), "vendor_token")
	assert.Empty(t, rec.saw("downloads.vendor.example"), "the fetch went out without its credential")

	creds = credentialsFor()
	creds.fail = domain.InstallationError(nil, "the installation cannot be read")
	source, rec = serveCredentialed(t, serveArchive(t), creds)
	_, err = source.Resolve(ctx, ports.Ref{Scheme: "https", Location: "downloads.vendor.example/b.tar.zst"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInstallation), "got: %v", err)
	assert.Empty(t, rec.saw("downloads.vendor.example"), "the fetch went out without the credential it was meant to carry")
}
