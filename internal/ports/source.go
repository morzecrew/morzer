package ports

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
)

// ReleaseSource fetches release bundles. One adapter per scheme; the core
// never learns a new transport.
//
// v1: local directory. Later: tar.zst, HTTPS, OCI artifact, GitHub Releases.
// Container images always come from an OCI registry regardless of the bundle
// source -- the bundle carries digests, not layers.
type ReleaseSource interface {
	// Scheme is the reference scheme this source handles: "file", "https",
	// "oci".
	Scheme() string

	// Resolve turns a reference into concrete facts without downloading the
	// payload -- what version it is, what it will hash to.
	Resolve(ctx context.Context, ref Ref) (ResolvedRelease, error)

	// Fetch places the bundle at destDir and returns the resulting path.
	// Implementations extract into destDir through a traversal-proof root.
	Fetch(ctx context.Context, ref Ref, destDir string) (BundlePath, error)

	// List enumerates available versions. Returning domain.ErrUnsupported
	// is acceptable: a plain directory has nothing to enumerate.
	List(ctx context.Context, ref Ref) ([]domain.Version, error)
}

// BundlePath is the filesystem location of a fetched bundle.
type BundlePath string

func (b BundlePath) String() string { return string(b) }

// Ref is a normalized release reference. Every source consumes this one type,
// so adding a scheme never changes a call site.
type Ref struct {
	// Scheme selects the adapter: file, https, oci.
	Scheme string

	// Location is the scheme-specific body: a path, a URL, a registry
	// reference.
	Location string

	// Version is the requested version when the reference does not itself
	// pin one. Zero means "whatever the reference resolves to".
	Version domain.Version

	// Digest, when set, is the expected content digest. A mismatch is a
	// hard failure -- that is the whole point of pinning.
	Digest string
}

func (r Ref) String() string {
	if r.Scheme == "file" {
		return r.Location
	}
	return r.Scheme + "://" + strings.TrimPrefix(r.Location, "//")
}

// ParseRef normalizes the reference forms the CLI accepts:
//
//	./path            file
//	/abs/path         file
//	file:///abs/path  file
//	https://host/x    https
//	oci://registry/x  oci
//
// A bare relative path is the common case at the terminal, so it is the
// default rather than an error.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}, domain.Usage("release reference is empty").
			WithHint("pass a path, an https:// URL, or an oci:// reference")
	}

	if !strings.Contains(s, "://") {
		return Ref{Scheme: "file", Location: s}, nil
	}

	u, err := url.Parse(s)
	if err != nil {
		return Ref{}, domain.Usage("invalid release reference %q: %v", s, err)
	}

	switch u.Scheme {
	case "file":
		return Ref{Scheme: "file", Location: u.Path}, nil
	case "https", "oci":
		return Ref{Scheme: u.Scheme, Location: strings.TrimPrefix(s, u.Scheme+"://")}, nil
	case "http":
		return Ref{}, domain.Usage("refusing plaintext http reference %q", s).
			WithHint("use https, or fetch the bundle out of band and pass a path")
	default:
		return Ref{}, domain.Usage("unsupported release reference scheme %q", u.Scheme).
			WithHint("supported schemes: file, https, oci")
	}
}

// ResolvedRelease is what a source can say about a reference before fetching.
type ResolvedRelease struct {
	Ref     Ref
	Version domain.Version
	Digest  string
	Size    int64
}

// Verifier checks a fetched bundle against an expectation before anything in
// it is read as configuration or executed as a hook.
//
// v1: SHA-256. Later: minisign, cosign.
type Verifier interface {
	// Name identifies the verifier in journal records and doctor output.
	Name() string

	Verify(ctx context.Context, bundle BundlePath, expect Expectation) error
}

// Expectation is what a bundle must satisfy.
type Expectation struct {
	// Digest is the expected content digest, "sha256:...". Empty means the
	// digest is computed and recorded but not compared.
	Digest string

	// SignaturePath is the detached signature, when one accompanies the
	// bundle.
	SignaturePath string

	// PublicKeys are the keys any signature must verify against.
	PublicKeys []string

	// Required makes a missing signature an error. It comes from
	// installation policy (require_signature), never from the bundle --
	// a bundle asserting it needs no signature would defeat the check.
	Required bool
}

// DigestString formats a raw hash as a prefixed digest.
func DigestString(algo string, sum []byte) string {
	return fmt.Sprintf("%s:%x", algo, sum)
}
