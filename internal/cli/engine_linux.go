package cli

import (
	"context"

	"github.com/morzecrew/morzer/internal/infra/exec"
)

// engineUser is engine_darwin.go's counterpart: on Linux the engine reads the
// host as root, so a rendered secret keeps the owner its release declares.
var engineUser func() (uid, gid int, err error)

// diskEncryption is nil on Linux, whose secrets render to a tmpfs.
func diskEncryption(exec.Runner) func(context.Context) (bool, error) { return nil }
