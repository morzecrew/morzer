package cli

import (
	"context"
	"os"

	"github.com/morzecrew/morzer/internal/adapters/dirservice"
	"github.com/morzecrew/morzer/internal/adapters/dockerdesktop"
	"github.com/morzecrew/morzer/internal/adapters/filevault"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// engineRunner runs every docker command with the CLI root may run, settled at
// the first one, and gives every process the manager starts its DOCKER_CONFIG
// from now on, rebuilt from root's own before anything reaches a registry.
// Before it exists, a registry is reached anonymously, so root's own config is
// never read unchecked (RFC 0055 decisions 18, 21 and 22).
func engineRunner(runner exec.Runner, paths domain.Paths) (exec.Runner, func(context.Context) (ports.EngineCLI, error)) {
	engine := dockerdesktop.NewEngine(runner, paths.ManagerDir())
	_ = os.Setenv("DOCKER_CONFIG", engine.ConfigDir())
	// A refusal here, a command run without root say, leaves no config this
	// process can read, and surfaces at its first docker command.
	_ = engine.Prepare()
	return dockerdesktop.Runner{Runner: runner, Engine: engine}, engine.Settle
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
