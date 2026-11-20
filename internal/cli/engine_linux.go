package cli

import (
	"context"

	"github.com/morzecrew/morzer/internal/adapters/runtime/compose"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// engineRunner is engine_darwin.go's counterpart: on Linux root runs the
// docker CLI on PATH, as it always has.
func engineRunner(runner exec.Runner, _ domain.Paths) (
	exec.Runner, func(context.Context) (ports.EngineCLI, error), func(context.Context) error,
) {
	return runner, nil, nil
}

// ingestRelay is engine_darwin.go's counterpart: a Linux daemon shares the
// manager's loopback, so an ingest needs no relay.
func ingestRelay(domain.Paths) compose.Option { return func(*compose.Runtime) {} }

// engineUser is engine_darwin.go's counterpart: on Linux the engine reads the
// host as root, so a rendered secret keeps the owner its release declares.
var engineUser func() (uid, gid int, err error)

// diskEncryption is nil on Linux, whose secrets render to a tmpfs.
func diskEncryption(exec.Runner) func(context.Context) (bool, error) { return nil }

// runDirWriters is nil on Linux, whose /run only root can write.
func runDirWriters(exec.Runner) func(context.Context, string) ([]string, error) { return nil }
