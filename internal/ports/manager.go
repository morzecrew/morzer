package ports

import (
	"context"

	"github.com/morzecrew/morzer/internal/domain"
)

// ManagerFeed says which releases of the manager itself are published (RFC
// 0046). It is about this binary, never about a product's releases, which
// come from the vendor's ReleaseSource.
type ManagerFeed interface {
	// Newest is the highest published release that is neither a draft nor
	// a prerelease.
	Newest(ctx context.Context) (ManagerRelease, error)
}

// ManagerRelease is one published release of the manager.
type ManagerRelease struct {
	Version domain.Version

	// URL is the release's page, where its notes are.
	URL string
}
