// Package https implements ports.ReleaseSource for bundles published over
// HTTPS.
//
// It is transport only. What arrives is a `tar.zst`, and everything after the
// last byte lands -- extraction limits, the refusal of links and device nodes,
// the content digest -- is the local source's job, reached by handing it the
// downloaded file. Reimplementing any of that here would mean a bundle was
// safer or less safe depending on how it reached the machine, which is exactly
// the property the digest exists to deny.
//
// The transport's own rules are the ones a network adds:
//
//   - TLS is not optional and a redirect may not leave it. `ParseRef` already
//     refuses a plaintext `http://` reference; a server that redirects to one
//     would route around that refusal at the moment it matters most.
//   - A response body is bounded while it is being read. A `Content-Length` is
//     a claim by the same server that sends the body.
//   - A failed request is retried, because a mirror returning 503 during a
//     deploy is ordinary. A 404 is not retried: repeating a request the server
//     answered definitively only delays the error.
package https

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/morzecrew/morzer/internal/adapters/source"
	"github.com/morzecrew/morzer/internal/adapters/source/local"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// Scheme is the reference scheme this source handles.
const Scheme = "https"

// Defaults chosen to be generous for a release bundle and still far below what
// it takes to hurt a machine.
const (
	DefaultMaxBody   = 512 << 20 // 512 MiB on the wire
	DefaultAttempts  = 3
	DefaultTimeout   = 10 * time.Minute
	DefaultBackoff   = 500 * time.Millisecond
	maxRedirectDepth = 5
)

type Source struct {
	client   *http.Client
	local    *local.Source
	maxBody  int64
	attempts int
	backoff  time.Duration

	cache *source.TempCache
}

type Option func(*Source)

// WithTransport replaces the round tripper. Tests use it to trust an
// httptest server's certificate; the redirect and timeout policy stay this
// package's, which is the point of injecting a transport rather than a client.
func WithTransport(rt http.RoundTripper) Option {
	return func(s *Source) { s.client.Transport = rt }
}

// WithMaxBody bounds a response body.
func WithMaxBody(n int64) Option { return func(s *Source) { s.maxBody = n } }

// WithAttempts sets how many times a retryable failure is retried.
func WithAttempts(n int) Option { return func(s *Source) { s.attempts = n } }

// WithBackoff sets the base delay between attempts.
func WithBackoff(d time.Duration) Option { return func(s *Source) { s.backoff = d } }

// WithLimits overrides the extraction limits applied after download.
func WithLimits(l atomicfs.ExtractLimits) Option {
	return func(s *Source) { s.local = s.local.WithLimits(l) }
}

func New(opts ...Option) *Source {
	s := &Source{
		client: &http.Client{
			Timeout:       DefaultTimeout,
			CheckRedirect: checkRedirect,
		},
		local:    local.New(),
		maxBody:  DefaultMaxBody,
		attempts: DefaultAttempts,
		backoff:  DefaultBackoff,
		cache:    source.NewTempCache("download"),
	}
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

// Close removes anything downloaded. Callers that build a source per command
// defer this; a leaked temp directory holding a release bundle is untidy rather
// than dangerous, but there is no reason to leave one.
func (s *Source) Close() error { return s.cache.Close() }

// Resolve downloads the bundle and reads its manifest.
//
// There is no way to learn a remote bundle's version or digest without having
// its bytes, so "resolve without downloading the payload" is not achievable
// over a plain URL. What is achievable, and what matters, is that resolving
// changes nothing on this machine: the download lands in a temporary directory
// this source owns, and a digest mismatch is refused before anything reaches
// the release store.
func (s *Source) Resolve(ctx context.Context, ref ports.Ref) (ports.ResolvedRelease, error) {
	archive, err := s.download(ctx, ref)
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

	// The reference the caller gave, not the temp path we resolved through.
	resolved.Ref = ref
	return resolved, nil
}

// Fetch places the bundle at destDir, reusing what Resolve already downloaded.
func (s *Source) Fetch(ctx context.Context, ref ports.Ref, destDir string) (ports.BundlePath, error) {
	archive, err := s.download(ctx, ref)
	if err != nil {
		return "", err
	}
	return s.local.Fetch(ctx, ports.Ref{Scheme: local.Scheme, Location: archive}, destDir)
}

// List reports that a URL is a bundle, not an index.
//
// Enumerating versions over HTTPS would need an index format nobody has
// specified, and inventing one here would make every vendor implement it.
func (s *Source) List(ctx context.Context, ref ports.Ref) ([]domain.Version, error) {
	return nil, domain.ValidationError(domain.ErrUnsupported,
		"an https reference names one bundle, not a version index")
}

// download fetches the reference into a temporary file, once per source.
func (s *Source) download(ctx context.Context, ref ports.Ref) (string, error) {
	target, err := s.targetURL(ref)
	if err != nil {
		return "", err
	}

	if cached, ok := s.cache.Lookup(target); ok {
		return cached, nil
	}

	path, err := s.cache.Reserve()
	if err != nil {
		return "", err
	}
	if err := s.fetchInto(ctx, target, path); err != nil {
		_ = os.Remove(path)
		return "", err
	}

	s.cache.Store(target, path)
	return path, nil
}

// targetURL rebuilds and checks the URL.
//
// ParseRef strips the scheme, so it is put back here rather than assumed: a
// reference that reached this source claiming to be https must actually be one.
func (s *Source) targetURL(ref ports.Ref) (string, error) {
	raw := Scheme + "://" + ref.Location

	u, err := url.Parse(raw)
	if err != nil {
		return "", domain.Usage("invalid https reference %q: %v", ref.Location, err)
	}
	if u.Scheme != Scheme {
		return "", domain.Usage("refusing a non-https reference %q", raw)
	}
	if u.Host == "" {
		return "", domain.Usage("the https reference %q names no host", raw)
	}
	return u.String(), nil
}

// fetchInto downloads with retries, writing to path.
func (s *Source) fetchInto(ctx context.Context, target, path string) error {
	attempts := max(s.attempts, 1)

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			// Linear rather than exponential: three attempts a few
			// hundred milliseconds apart covers a restarting mirror,
			// and anything longer is an operator waiting on a
			// deployment that is not going to succeed.
			delay := time.Duration(attempt-1) * s.backoff
			select {
			case <-ctx.Done():
				return domain.Interrupted("download of %s was cancelled", target)
			case <-time.After(delay):
			}
		}

		err := s.attemptFetch(ctx, target, path)
		if err == nil {
			return nil
		}
		lastErr = err

		if !isRetryable(err) {
			return err
		}
	}

	return domain.ValidationError(lastErr,
		"cannot download %s after %d attempts", target, attempts).
		WithHint("check the URL and the network; the bundle can also be fetched " +
			"out of band and installed from a path")
}

// certificateProblem names a TLS failure instead of retrying it.
//
// A certificate the machine does not trust will not start being trusted on the
// second attempt, so retrying only makes the operator wait three times as long
// for a message that never mentions the certificate. There is deliberately no
// flag to skip verification: a bundle fetched over a connection nothing
// authenticated is a bundle from nobody in particular.
func certificateProblem(err error, target string) error {
	var certErr *tls.CertificateVerificationError
	var hostErr x509.HostnameError
	var authorityErr x509.UnknownAuthorityError

	switch {
	case errors.As(err, &certErr), errors.As(err, &authorityErr):
		return domain.ValidationError(err,
			"the TLS certificate for %s is not trusted by this machine", target).
			WithHint("install the issuing CA, or fetch the bundle out of band and " +
				"install it from a path. There is no option to skip verification.")
	case errors.As(err, &hostErr):
		return domain.ValidationError(err,
			"the TLS certificate for %s is issued for a different name", target).
			WithHint("check the URL; a certificate for another host proves nothing about this one")
	default:
		return nil
	}
}

// retryable marks a failure worth trying again.
type retryable struct{ error }

func isRetryable(err error) bool {
	var r retryable
	return errors.As(err, &r)
}

func (s *Source) attemptFetch(ctx context.Context, target, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return domain.Usage("cannot build a request for %s: %v", target, err)
	}
	req.Header.Set("Accept", "application/octet-stream")

	resp, err := s.client.Do(req)
	if err != nil {
		// A refusal this package raised itself -- a downgrade redirect,
		// too many hops -- travels back through the client unchanged.
		var typed *domain.Error
		if errors.As(err, &typed) {
			return typed
		}
		if certErr := certificateProblem(err, target); certErr != nil {
			return certErr
		}
		// A refused connection or a reset mid-handshake is what retrying
		// is for.
		return retryable{domain.RuntimeError(err, "cannot reach %s", target)}
	}
	defer func() { _ = resp.Body.Close() }()

	if err := checkStatus(target, resp); err != nil {
		return err
	}
	if err := s.checkDeclaredSize(target, resp); err != nil {
		return err
	}
	return s.copyBody(target, resp, path)
}

func checkStatus(target string, resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil

	case resp.StatusCode == http.StatusNotFound:
		return domain.ValidationError(domain.ErrReleaseNotFound,
			"no bundle at %s (404)", target).
			WithHint("check the URL; a release may have been renamed or withdrawn")

	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		// Retrying will not produce credentials.
		return domain.ValidationError(nil,
			"access to %s was refused (%d)", target, resp.StatusCode).
			WithHint("this build sends no credentials; fetch the bundle out of band " +
				"and install it from a path")

	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return retryable{domain.RuntimeError(nil,
			"%s returned %d", target, resp.StatusCode)}

	default:
		return domain.ValidationError(nil,
			"%s returned %d", target, resp.StatusCode)
	}
}

// checkDeclaredSize refuses an oversized download before reading it.
//
// The header is a claim by the server that is about to send the body, so it is
// checked *and* the body is bounded while it streams. This one only saves the
// transfer.
func (s *Source) checkDeclaredSize(target string, resp *http.Response) error {
	if s.maxBody <= 0 || resp.ContentLength < 0 {
		return nil
	}
	if resp.ContentLength > s.maxBody {
		return domain.ValidationError(nil,
			"%s declares %d bytes, over the limit of %d", target, resp.ContentLength, s.maxBody).
			WithHint("no release bundle should be this large; check the URL")
	}
	return nil
}

// copyBody streams the response to disk, bounded.
func (s *Source) copyBody(target string, resp *http.Response, path string) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return domain.Internal(err, "cannot create the download file")
	}
	defer func() { _ = out.Close() }()

	var body io.Reader = resp.Body
	if s.maxBody > 0 {
		// One byte past the limit, so exceeding it is detectable rather
		// than merely reached.
		body = io.LimitReader(resp.Body, s.maxBody+1)
	}

	written, err := io.Copy(out, body)
	if err != nil {
		return retryable{domain.RuntimeError(err, "the download of %s failed", target)}
	}
	if s.maxBody > 0 && written > s.maxBody {
		// A server that lied about Content-Length, or sent none at all.
		// Refused here rather than after, which is why the copy is
		// bounded instead of measured.
		return domain.ValidationError(nil,
			"%s sent more than the limit of %d bytes", target, s.maxBody).
			WithHint("the response does not look like a release bundle")
	}
	return nil
}

// checkRedirect refuses a redirect that leaves TLS, and bounds the chain.
//
// This is the rule that makes refusing `http://` at parse time mean something.
// Without it, an operator who typed an https URL could still be served over
// plaintext by a server that asked politely.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirectDepth {
		return domain.ValidationError(nil,
			"too many redirects fetching %s", via[0].URL.Redacted())
	}
	if req.URL.Scheme != Scheme {
		return domain.ValidationError(nil,
			"refusing a redirect from https to %s", req.URL.Scheme).
			WithHint("the bundle would arrive over a connection nothing authenticated; " +
				"fetch it out of band and install it from a path")
	}
	return nil
}
