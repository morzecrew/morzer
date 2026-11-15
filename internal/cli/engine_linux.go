package cli

import (
	"context"

	"github.com/morzecrew/morzer/internal/infra/exec"
)

// engineUser is engine_darwin.go's counterpart: on Linux the engine reads the
// host as root, so a rendered secret keeps the owner its release declares.
var engineUser func() (uid, gid int, err error)

// runDirWriters is nil on Linux, whose /run only root can write.
func runDirWriters(exec.Runner) func(context.Context, string) ([]string, error) { return nil }
