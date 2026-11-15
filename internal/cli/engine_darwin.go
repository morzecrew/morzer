package cli

import (
	"context"

	"github.com/morzecrew/morzer/internal/adapters/dirservice"
	"github.com/morzecrew/morzer/internal/adapters/dockerdesktop"
	"github.com/morzecrew/morzer/internal/infra/exec"
)

// engineUser is who the engine reads the host's files as: on a Mac, the user
// who runs Docker Desktop, read at every call (RFC 0055 decision 19).
var engineUser = dockerdesktop.User

// runDirWriters lists who other than root can write a directory, read from
// Directory Services: RunDir's entry is in /private/var/run, which its group can
// write.
func runDirWriters(runner exec.Runner) func(context.Context, string) ([]string, error) {
	return func(ctx context.Context, dir string) ([]string, error) { return dirservice.Writers(ctx, runner, dir) }
}
