package https

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// WithCredentials sends each server the credential configured for it
// (RFC 0038 §5.2).
func WithCredentials(c ports.SourceCredentials) Option {
	return func(s *Source) { s.creds = c }
}

// credentialTransport attaches a configured credential to each request that
// goes to its server over TLS.
//
// In the transport rather than on the request the client is given, because
// the client copies that request's headers onto the redirects it follows,
// to a subdomain among them. Here every hop is judged on its own authority,
// so a redirect to another host, or to the same host on another port, goes
// out without the credential (decision 7). It also replaces an
// Authorization the client built from a password in the URL, so one request
// never carries two (decision 8).
type credentialTransport struct {
	base  http.RoundTripper
	creds ports.SourceCredentials
}

func (t credentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != Scheme {
		return t.base.RoundTrip(req)
	}
	entry, found, err := t.creds.SourceFor(req.Context(), authorityOf(req.URL))
	if err != nil {
		return nil, err
	}
	if !found {
		return t.base.RoundTrip(req)
	}
	value, err := t.creds.RevealSource(req.Context(), entry)
	if err != nil {
		return nil, err
	}
	header, err := authorization(entry, value)
	if err != nil {
		return nil, err
	}
	if sent, ok := req.Context().Value(sentKey{}).(*sentCredentials); ok {
		sent.add(value, header)
	}
	// A RoundTripper must not change the request it is handed.
	out := req.Clone(req.Context())
	out.Header.Set("Authorization", header)
	return t.base.RoundTrip(out)
}

// sentCredentials is every credential one fetch sent, so that an error quoting
// what the server answered can be checked for it: a server may echo what it
// was sent in a Location the client cannot parse, and the client quotes that.
type sentCredentials struct {
	mu     sync.Mutex
	values []string
}

type sentKey struct{}

// add records a credential in each form an echo could take: as stored, as
// sent in the header, and for basic, the password alone. Each is kept as
// written, as the client's %q quoting escapes it, and URL-encoded, since a
// server may echo it any of those ways.
func (s *sentCredentials) add(value, header string) {
	value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	parts := []string{value, header}
	if _, encoded, ok := strings.Cut(header, " "); ok {
		parts = append(parts, encoded)
	}
	if _, password, ok := strings.Cut(value, ":"); ok && password != "" {
		parts = append(parts, password)
	}
	var forms []string
	for _, p := range parts {
		quoted := strconv.Quote(p)
		forms = append(forms, p, quoted[1:len(quoted)-1], url.PathEscape(p), url.QueryEscape(p))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = append(s.values, forms...)
}

// in reports whether text carries any credential this fetch sent.
func (s *sentCredentials) in(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.values {
		if v != "" && strings.Contains(text, v) {
			return true
		}
	}
	return false
}

// any reports whether this fetch sent a credential at all.
func (s *sentCredentials) any() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.values) > 0
}

// aboutALocation reports whether err quotes a Location the server sent. After
// a credential was sent, that text is the server's to fill with any encoding of
// it, so no match against known forms can be complete: the cause is left out.
func aboutALocation(err error) bool {
	return strings.Contains(err.Error(), "Location header")
}

// authorityOf is a URL's server in domain.SourceAuthority's form.
func authorityOf(u *url.URL) string { return domain.SourceAuthority(u.Hostname(), u.Port()) }

// authorization builds the header from a secret's value (decision 14): one
// trailing newline is what `secret set` from a file or a terminal leaves, and
// is not part of the credential.
func authorization(entry domain.HTTPSSource, value string) (string, error) {
	value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	switch {
	case value == "":
		return "", domain.ValidationError(nil, "the secret %q for %s is empty", entry.Secret, entry.Host).
			WithHint("set it with `morzer secret set %s`", entry.Secret)
	case strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		// Never quoted: the message would carry the credential.
		return "", domain.ValidationError(nil,
			"the secret %q for %s holds a line break or another control character", entry.Secret, entry.Host).
			WithHint("set it again with `morzer secret set %s`, as one line", entry.Secret)
	}
	if entry.Auth == domain.SourceAuthBearer {
		return "Bearer " + value, nil
	}
	user, password, ok := strings.Cut(value, ":")
	if !ok || user == "" {
		return "", domain.ValidationError(nil,
			"the secret %q for %s is not `user:password`, which basic authentication sends", entry.Secret, entry.Host).
			WithHint("set it with `morzer secret set %s`", entry.Secret)
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password)), nil
}

// refusedHint says what to do about a 401 or 403, naming the secret the
// server was sent and never its value.
func (s *Source) refusedHint(ctx context.Context, resp *http.Response) string {
	authority := ""
	if resp.Request != nil && resp.Request.URL != nil {
		authority = authorityOf(resp.Request.URL)
	}
	if s.creds != nil && authority != "" {
		if entry, found, err := s.creds.SourceFor(ctx, authority); err == nil && found {
			return "the server refused the " + string(entry.Auth) + " credential in the secret `" + entry.Secret +
				"`; replace it with `morzer secret set " + entry.Secret + "`"
		}
	}
	if authority == "" {
		authority = "this server"
	}
	return "no credential is configured for " + authority + "; on an installation, set a secret and add it with " +
		"`morzer release source add https://" + authority + " --bearer --secret <name>`, and for a first install " +
		"give `morzer init` the credential with `--source-secret-file`"
}
