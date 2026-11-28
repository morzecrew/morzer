package quadlet

import (
	"context"
	"errors"
	"fmt"
	osexec "os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// container finds the container a service's unit runs, from the bundle cfg
// names.
func (r *Runtime) container(cfg ports.RuntimeConfig, service string) (string, error) {
	b, err := checkBundle(cfg)
	if err != nil {
		return "", err
	}
	u, ok := b.service(service)
	if !ok {
		return "", domain.ValidationError(nil, "no Quadlet file defines the service %q", service)
	}
	return b.containerName(u), nil
}

// Exec runs a command in a running service's container. A non-zero exit is
// the command's, returned as data; an error is an exec that could not run.
func (r *Runtime) Exec(ctx context.Context, cfg ports.RuntimeConfig, service string, argv []string) (ports.ExitResult, error) {
	name, err := r.container(cfg, service)
	if err != nil {
		return ports.ExitResult{}, err
	}
	cmd, tail := withOutputTail(r.command(5*time.Minute, append([]string{r.podman, "exec", name}, argv...)...))
	res, err := r.runner.Run(ctx, cmd)
	out := ports.ExitResult{
		ExitCode: res.ExitCode, Stdout: res.Stdout, Stderr: res.Stderr,
		Output: tail.String(), Duration: res.Duration, Truncated: res.Truncated,
	}
	if err != nil {
		var exitErr *exec.ExitError
		// 125 is Podman's own failure, such as a container that is not
		// running; the command never ran, so it is not the command's exit.
		if asExit(err, &exitErr) && exitErr.ExitCode != 125 {
			out.ExitCode = exitErr.ExitCode
			return out, nil
		}
		return out, wrapExit(err, fmt.Sprintf("cannot exec in service %q: %s", service,
			firstLine(res.Stderr)), "")
	}
	return out, nil
}

const interactiveStopGrace = 10 * time.Second

// ExecInteractive runs `podman exec` with the caller's streams. Started here
// rather than through the runner, for the reason Compose's is: a process
// outside the terminal's foreground group is stopped when it reads the
// terminal and never receives Ctrl-C.
func (r *Runtime) ExecInteractive(
	ctx context.Context, cfg ports.RuntimeConfig, service string, argv []string, streams ports.ExecIO,
) (int, error) {
	name, err := r.container(cfg, service)
	if err != nil {
		return -1, err
	}
	args := []string{"exec", "--interactive"}
	if streams.TTY {
		args = append(args, "--tty")
	}
	args = append(append(args, name), argv...)

	cmd := osexec.CommandContext(ctx, r.podman, args...) //nolint:gosec // the operator's own command, by design
	cmd.Stdin, cmd.Stdout, cmd.Stderr = streams.Stdin, streams.Stdout, streams.Stderr
	cmd.Env = localEnv(nil)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = interactiveStopGrace

	err = cmd.Run()
	var exitErr *osexec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exitErr):
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return exitErr.ExitCode(), nil
	default:
		return -1, wrapExit(err, fmt.Sprintf("cannot exec in service %q", service), "")
	}
}

// outputTailLines bounds ExitResult.Output, as Compose's does.
const outputTailLines = 10

// outputTail keeps the last non-blank lines of a run's two streams, in the
// order they arrived.
type outputTail struct {
	mu    sync.Mutex
	lines []string
}

func withOutputTail(cmd exec.Command) (exec.Command, *outputTail) {
	tail := &outputTail{}
	sink := cmd.OnLine
	cmd.OnLine = func(l exec.Line) {
		tail.add(l.Text)
		if sink != nil {
			sink(l)
		}
	}
	return cmd, tail
}

func (t *outputTail) add(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, text)
	if len(t.lines) > outputTailLines {
		t.lines = t.lines[1:]
	}
}

func (t *outputTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}
