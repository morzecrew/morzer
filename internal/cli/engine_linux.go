package cli

import (
	"context"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// engineRunner is engine_darwin.go's counterpart: on Linux root runs the
// docker CLI on PATH, as it always has.
func engineRunner(runner exec.Runner, _ domain.Paths) (exec.Runner, func(context.Context) (ports.EngineCLI, error)) {
	return runner, nil
}

// engineUser is engine_darwin.go's counterpart: on Linux the engine reads the
// host as root, so a rendered secret keeps the owner its release declares.
var engineUser func() (uid, gid int, err error)

// diskEncryption is nil on Linux, whose secrets render to a tmpfs.
func diskEncryption(exec.Runner) func(context.Context) (bool, error) { return nil }

// runDirWriters is nil on Linux, whose /run only root can write.
func runDirWriters(exec.Runner) func(context.Context, string) ([]string, error) { return nil }
