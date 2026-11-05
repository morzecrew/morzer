package compose

import (
	"context"
	"errors"
	"fmt"
	osexec "os/exec"
	"syscall"
	"time"

	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// interactiveStopGrace is how long a session's `compose` has to end after
// SIGTERM before it is killed.
const interactiveStopGrace = 10 * time.Second

// ExecInteractive runs `compose exec` with the caller's streams (RFC 0050 P2).
// With a TTY, `compose` is handed the terminal itself: it sets raw mode and
// follows resizes, and nothing here forwards a byte.
//
// Started here rather than through the runner, which puts every command in a
// process group of its own. A process outside the terminal's foreground group
// is stopped when it reads the terminal and never receives Ctrl-C, so this one
// stays in the manager's group, and a cancellation signals `compose` alone:
// the group holds the manager too.
func (r *Runtime) ExecInteractive(
	ctx context.Context, cfg ports.RuntimeConfig, service string, argv []string, streams ports.ExecIO,
) (int, error) {
	rest := []string{"exec"}
	if !streams.TTY {
		rest = append(rest, "--no-TTY")
	}
	full := append(r.args(cfg, append(rest, service)...), argv...)

	cmd := osexec.CommandContext(ctx, full[0], full[1:]...) //nolint:gosec // the operator's own command, by design
	cmd.Dir = cfg.WorkingDir
	cmd.Env = exec.FilteredEnv(exec.PassthroughEnv, cfg.Env)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = streams.Stdin, streams.Stdout, streams.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = interactiveStopGrace

	err := cmd.Run()
	var exitErr *osexec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exitErr):
		// Ended by a signal: reported as a shell reports it, 128 plus
		// the signal, so the status still says which.
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return exitErr.ExitCode(), nil
	default:
		return -1, wrapExit(err, fmt.Sprintf("cannot exec in service %q", service), "")
	}
}
