package cli

import (
	"context"

	"github.com/morzecrew/morzer/internal/adapters/dockerdesktop"
	"github.com/morzecrew/morzer/internal/adapters/filevault"
	"github.com/morzecrew/morzer/internal/infra/exec"
)

// engineUser is who the engine reads the host's files as: on a Mac, the user
// who runs Docker Desktop, read at every call (RFC 0055 decision 19).
var engineUser = dockerdesktop.User

// diskEncryption reports whether FileVault encrypts the disk the secrets
// render to, since a Mac has no memory-backed filesystem (decision 5).
func diskEncryption(runner exec.Runner) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) { return filevault.On(ctx, runner) }
}
