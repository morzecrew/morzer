package domain

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// SourcesConfig is how the manager authenticates to the servers it fetches
// release bundles from (RFC 0038). Only HTTPS: an OCI registry is reached
// with the ambient Docker credentials, and two mechanisms for one registry
// would disagree.
type SourcesConfig struct {
	// HTTPS is one entry per server that wants a credential.
	HTTPS []HTTPSSource `yaml:"https,omitempty" json:"https,omitempty"`
}

// IsZero reports whether no source is configured.
func (c SourcesConfig) IsZero() bool { return len(c.HTTPS) == 0 }

// For returns the entry for an authority in SourceAuthority's form.
func (c SourcesConfig) For(authority string) (HTTPSSource, bool) {
	for _, s := range c.HTTPS {
		if s.Host == authority {
			return s, true
		}
	}
	return HTTPSSource{}, false
}

// HTTPSSource names the credential sent to one HTTPS server. The value is a
// secret in the installation's store, named here and never copied here
// (RFC 0038 decision 6).
type HTTPSSource struct {
	// Host is the server's authority as SourceAuthority writes it: the host
	// lower-cased, with its port only when that is not 443 (decision 7).
	Host string `yaml:"host" json:"host"`

	// Auth is how the value is sent.
	Auth SourceAuth `yaml:"auth" json:"auth"`

	// Secret names the secret holding the value: a token for bearer, and
	// `user:password` for basic (decision 14).
	Secret string `yaml:"secret" json:"secret"`
}

// SourceAuth is an HTTP authentication scheme the manager speaks.
type SourceAuth string

// The schemes RFC 0038 decision 5 settles on.
const (
	SourceAuthBearer SourceAuth = "bearer"
	SourceAuthBasic  SourceAuth = "basic"
)

// Valid reports whether a is a scheme the manager sends.
func (a SourceAuth) Valid() bool { return a == SourceAuthBearer || a == SourceAuthBasic }

// Validate checks an entry read from state, which an operator may have edited
// by hand.
func (s HTTPSSource) Validate() error {
	switch {
	case s.Host == "":
		return ValidationError(nil, "names no host")
	case !isSourceAuthority(s.Host):
		return ValidationError(nil, "host %q is not written as an authority: a lower-case host, "+
			"with a port only when it is not 443", s.Host)
	case !s.Auth.Valid():
		return ValidationError(nil, "auth %q is neither %s nor %s", s.Auth, SourceAuthBearer, SourceAuthBasic)
	case !ValidSecretName(s.Secret):
		return ValidationError(nil, "secret %q is not a usable secret name", s.Secret)
	}
	return nil
}

// SourceAuthority writes an HTTPS authority the way an entry stores it, so
// that two spellings of one server are one entry: the host lower-cased, and
// the port kept only when it is not 443, which both sides default to
// (decision 7).
func SourceAuthority(host, port string) string {
	host = strings.ToLower(host)
	if port == "" || port == "443" {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, port)
}

// isSourceAuthority reports whether s is an authority already in
// SourceAuthority's form, and nothing more.
func isSourceAuthority(s string) bool {
	u, err := url.Parse("https://" + s)
	if err != nil || u.Hostname() == "" {
		return false
	}
	// A path, a user or a default port makes the rewrite differ from s.
	return SourceAuthority(u.Hostname(), u.Port()) == s
}

// ParseSourceURL reads the server `release source add` names: an `https://`
// URL carrying an authority and nothing else. A path, a query or a password
// would suggest that the entry is narrower than its server, or carries what
// the secret is for, and neither is so.
func ParseSourceURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", Usage("%q is not a URL", RedactURLCredentials(raw))
	}
	switch {
	case u.Scheme != "https":
		return "", Usage("%q is not an https:// URL: credentials are sent over TLS only", RedactURLCredentials(raw))
	case u.User != nil:
		return "", Usage("%q carries a user: the credential belongs in the secret, not the URL", RedactURLCredentials(raw))
	case u.Hostname() == "":
		return "", Usage("%q names no host", raw)
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "", u.Opaque != "":
		return "", Usage("%q names more than a server: a credential is configured per host and port, "+
			"for every path on it", raw)
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return "", Usage("%q names no usable port", raw)
		}
	}
	return SourceAuthority(u.Hostname(), u.Port()), nil
}

// DefaultSourceSecret is the secret `init` stores a source's credential under
// when it is not given one: `source_` and the authority, with everything but
// letters and digits turned into `_`.
func DefaultSourceSecret(authority string) string {
	var b strings.Builder
	b.WriteString("source_")
	for _, r := range authority {
		if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return b.String()
}

// validateSources checks every entry and refuses two for one server.
func validateSources(c SourcesConfig, v *validationErrors) {
	seen := map[string]bool{}
	for idx, s := range c.HTTPS {
		field := fmt.Sprintf("sources.https[%d]", idx)
		if err := s.Validate(); err != nil {
			v.add(field, "%s", AsError(err).Message)
			continue
		}
		if seen[s.Host] {
			v.add(field, "is a second entry for %s", s.Host)
		}
		seen[s.Host] = true
	}
}
