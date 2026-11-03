package ports

import (
	"context"
	"fmt"
	"runtime"

	"github.com/morzecrew/morzer/internal/domain"
)

// ManagerFeed says which releases of the manager itself are published (RFC
// 0046). It is about this binary, never about a product's releases, which
// come from the vendor's ReleaseSource.
type ManagerFeed interface {
	// Newest is the highest published release that is neither a draft nor
	// a prerelease.
	Newest(ctx context.Context) (ManagerRelease, error)

	// Download writes the three files of release v that a self-update
	// reads into dir: this platform's archive, SumsFileName and
	// SignatureFileName. It proves nothing about them; ManagerUnpacker
	// does.
	Download(ctx context.Context, v domain.Version, dir string) error
}

// ManagerRelease is one published release of the manager.
type ManagerRelease struct {
	Version domain.Version

	// URL is the release's page, where its notes are.
	URL string
}

// ManagerArchive is the name the release pipeline gives release v's archive
// for the platform this binary was built for.
func ManagerArchive(v domain.Version) string {
	return fmt.Sprintf("morzer_%s_linux_%s.tar.zst", v, runtime.GOARCH)
}

// ManagerUnpacker turns a manager release's files into a binary this machine
// may run (RFC 0046 §5.3). Everything that decides whether a binary is
// trusted lives behind it, so the one key it checks against cannot be
// replaced by anything an operator or an installation configures (decision 6).
type ManagerUnpacker interface {
	// Target is the version of this platform's archive that dir's
	// SumsFileName names, which is what `self-update --from` installs
	// (decision 14).
	Target(dir string) (domain.Version, error)

	// Unpack verifies dir's SignatureFileName over SumsFileName with the
	// key this binary embeds, then release v's archive against its own
	// line, and writes the binary the archive carries to dst, executable.
	Unpack(ctx context.Context, dir string, v domain.Version, dst string) error

	// Version runs the binary at path and returns the version it reports.
	Version(ctx context.Context, path string) (domain.Version, error)
}
