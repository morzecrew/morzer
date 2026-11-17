package cli

import (
	"context"

	"github.com/morzecrew/morzer/internal/adapters/dirservice"
	"github.com/morzecrew/morzer/internal/adapters/dockerdesktop"
	"github.com/morzecrew/morzer/internal/adapters/filevault"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// engineRunner runs every docker command with the CLI root may run, settled at
// the first one, and gives every process the manager starts its DOCKER_CONFIG,
// rebuilt from root's own before anything reaches a registry. Until it is, a
// registry is reached anonymously, so root's own config is never read
// unchecked (RFC 0055 decisions 18, 21 and 22).
//
// beforeHook is what a hook waits on: the settled CLI, and no plugin it would
// find that someone other than root could replace (RFC 0055 decision 24).
func engineRunner(runner exec.Runner, paths domain.Paths) (
	wrapped exec.Runner, settle func(context.Context) (ports.EngineCLI, error), beforeHook func(context.Context) error,
) {
	engine := dockerdesktop.NewEngine(runner, paths.ManagerDir())
	// A refusal here, a command run without root say, leaves the process
	// reaching registries anonymously, and surfaces at its first docker
	// command.
	_ = engine.Prepare()
	return dockerdesktop.Runner{Runner: runner, Engine: engine}, engine.Settle, engine.BeforeHook
}

// engineUser is who the engine reads the host's files as: on a Mac, the user
// who runs Docker Desktop, read at every call (RFC 0055 decision 19).
var engineUser = dockerdesktop.User

// diskEncryption reports whether FileVault encrypts the disk the secrets
// render to, since a Mac has no memory-backed filesystem (decision 5).
func diskEncryption(runner exec.Runner) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) { return filevault.On(ctx, runner) }
}

// runDirWriters lists who other than root can write a directory, read from
// Directory Services: RunDir's entry is in /private/var/run, which its group can
// write.
func runDirWriters(runner exec.Runner) func(context.Context, string) ([]string, error) {
	return func(ctx context.Context, dir string) ([]string, error) { return dirservice.Writers(ctx, runner, dir) }
}
