package compose

import (
	"context"
	"errors"
	"io"
	osexec "os/exec"
	"syscall"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// asExit is errors.As specialised to the runner's exit error, kept here so the
// adapter does not import errors in three files.
func asExit(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}

// startStream runs a command and returns its stdout as a stream the caller
// closes.
//
// Log following is the one case the shared runner cannot serve: it is built
// around run-to-completion, and a follow reads until the operator stops
// watching. The process-group handling is repeated here rather than skipped,
// because a `compose logs --follow` left running after the reader closes would
// leak a process for the life of the session.
type streamCloser struct {
	io.ReadCloser
	cmd  *osexec.Cmd
	pgid int
}

func (s *streamCloser) Close() error {
	closeErr := s.ReadCloser.Close()
	if s.pgid > 0 {
		_ = syscall.Kill(-s.pgid, syscall.SIGTERM)
	}
	if s.cmd != nil {
		_ = s.cmd.Wait()
	}
	return closeErr
}

func startStream(ctx context.Context, argv []string, cfg ports.RuntimeConfig) (io.ReadCloser, error) {
	if len(argv) == 0 {
		return nil, domain.Internal(nil, "compose: empty argv for log stream")
	}

	cmd := osexec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cfg.WorkingDir
	cmd.Env = exec.BaseEnv(cfg.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Group-wide TERM with a KILL escalation, exactly as the shared runner
	// does it. CommandContext's default Cancel SIGKILLs only the leader --
	// despite Setpgid -- so a cancelled `logs --follow` orphaned compose's
	// own children; WaitDelay bounds a leader that ignores the TERM.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = exec.DefaultGrace

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, domain.RuntimeError(err, "cannot open a log stream")
	}
	// Compose writes its own progress to stderr; discarding it keeps the
	// caller's stream to log lines only.
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, domain.RuntimeError(err, "cannot start the log stream")
	}

	pgid := 0
	if cmd.Process != nil {
		pgid = cmd.Process.Pid
	}
	return &streamCloser{ReadCloser: stdout, cmd: cmd, pgid: pgid}, nil
}
