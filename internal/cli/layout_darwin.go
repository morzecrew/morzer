package cli

import "github.com/morzecrew/morzer/internal/domain"

// A Mac has no user layout (logs/quadlet-adapter.md, U5): RFC 0055 decision 13
// runs the manager as root there, and macOS sets no $XDG_RUNTIME_DIR. Every
// command keeps the system layout.

func userTree() string { return "" }

func userPaths(product string) (domain.Paths, error) {
	return domain.Paths{}, domain.Internal(nil, "there is no user layout for %s on macOS", product)
}
