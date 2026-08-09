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
	// Schemes are the reference schemes this source handles: "file",
	// "https", "oci".
	//
	// Plural because a registry of sources is itself a source -- it
	// dispatches on the ref and answers for everything registered in it, so
	// one scheme per implementation would have made the composite lie about
	// what it can do. It is also what builds the "this build supports: ..."
	// half of a refusal, which is the only reason a caller ever asks.
	Schemes() []string

	// Resolve turns a reference into concrete facts -- what version it is,
	// what it hashes to -- without installing anything.
	//
	// It is not necessarily cheap. This doc comment used to say "without
	// downloading the payload", which is true of a directory and false of a
	// registry: the OCI source computes a *content* digest, and a content
	// digest is a property of the bytes, so it pulls the layer to get one.
	// A poll loop that called Resolve every tick would download the whole
	// bundle every tick. Anything watching a reference for change wants
	// ChannelPeeker instead.
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
		// file://relative/path parses with its first segment as a URL
		// host. Dropping it silently -- resolving to a different path
		// than the operator wrote -- is worse than refusing, so this
		// mirrors the backup-target parser's rule. URL hosts are
		// case-insensitive, so LOCALHOST is localhost; localhost:8080
		// carries a port and is not.
		if host := strings.ToLower(u.Host); host != "" && host != "localhost" {
			return Ref{}, domain.Usage(
				"file references are local paths, but %q names the host %q", s, u.Host).
				WithHint("write file:///absolute/path -- three slashes")
		}
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

// ChannelPeeker asks what a mutable reference points at, without fetching what
// it points at.
//
// An optional capability rather than a method on ReleaseSource, because only a
// transport with a server-side identity for its content can answer: a registry
// has a manifest digest, a directory has nothing that changes when a file does
// not. A source that does not implement it is not following channels, and the
// caller says so rather than falling back to Resolve -- which would silently
// turn a five-minute poll into a five-minute download.
//
// This is what makes channel following affordable. RFC 0016 §5.2 priced a tick
// at "one Resolve", which was wrong by the size of the bundle.
type ChannelPeeker interface {
	// Peek reports what the reference currently addresses. The cost is one
	// manifest request; nothing the manifest points at is fetched.
	Peek(ctx context.Context, ref Ref) (ChannelState, error)
}

// ChannelState is what a mutable reference points at right now.
type ChannelState struct {
	// UpstreamDigest identifies the artefact *at the registry*: the
	// manifest digest, not the bundle's content digest.
	//
	// The two are different values for the same thing, and only this one is
	// knowable without a download. It is an opaque change token: compare it
	// with the one recorded last time, never with a content digest.
	UpstreamDigest string

	// Pinned addresses exactly what was seen, immutably.
	//
	// A channel is a tag that exists to move, so the tag may point
	// somewhere else by the time a decision made from this peek is acted
	// on. Fetching Pinned rather than the tag closes that window, and it is
	// built here because reference syntax belongs to the transport.
	Pinned Ref
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

// Names a bundle may ship its own integrity evidence under. They are part of
// the release contract rather than an adapter's private detail: a vendor's
// publishing pipeline writes them, a third party checks them with `sha256sum
// -c` and `minisign -Vm` without the manager, and two adapters read them.
const (
	// SumsFileName lists a per-file checksum for everything else in the
	// bundle.
	SumsFileName = "SHA256SUMS"

	// SignatureFileName is a detached minisign signature over SumsFileName.
	//
	// The signature covers the sums file rather than each file or the whole
	// tree, which is what makes the chain checkable by hand: the signature
	// proves who wrote the list, and the list proves what the files are.
	// It travels inside the bundle so it survives being packed into an
	// archive and unpacked again -- a sibling file would not.
	SignatureFileName = SumsFileName + ".minisig"
)

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

// RedactRefCredentials strips anything secret from a reference before it is
// written to state or shown to anyone.
//
// A reference is operator input and may carry a credential in two shapes:
// userinfo, as in https://user:token@host/bundle.tar.zst, and a registry
// reference whose path segment is effectively a secret. Only the first is
// mechanically recognisable, so only the first is removed -- the second is a
// judgement no parser can make.
//
// This runs before ReleaseRecord.SourceRef is persisted. That field exists so
// `update --check` has somewhere to look, and it is read back into `status`
// output, `doctor` output and the JSON envelope; a password stored there would
// surface in all three.
func RedactRefCredentials(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.Contains(trimmed, "://") {
		return trimmed
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.User == nil {
		return trimmed
	}
	u.User = url.User(u.User.Username())
	return u.String()
}
