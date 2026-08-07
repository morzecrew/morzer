package compose

import (
	"context"
	"errors"
	"io"
	osexec "os/exec"
	"sync"
	"syscall"
	"time"

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

	mu        sync.Mutex
	killTimer *time.Timer
}

// armKill schedules a group-wide SIGKILL one grace period after cancellation.
//
// WaitDelay's escalation is os.Process.Kill -- the leader alone -- so a
// compose child that ignores the group TERM would survive it. The timer is
// disarmed once the leader is reaped; until then it carries the same bounded
// PID-reuse tradeoff the shared runner's own post-Wait kill accepts.
func (s *streamCloser) armKill(pgid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.killTimer != nil || pgid <= 0 {
		return
	}
	s.killTimer = time.AfterFunc(exec.DefaultGrace, func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	})
}

// disarmKill stops the escalation once the whole group is gone. Stopping it
// on leader reap alone would spare a child that ignored the group TERM --
// the exact process the escalation exists for. While any member survives,
// the bounded timer stays armed, the same tradeoff as arming it.
func (s *streamCloser) disarmKill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.killTimer == nil {
		return
	}
	if s.pgid > 0 && syscall.Kill(-s.pgid, 0) == nil {
		return
	}
	s.killTimer.Stop()
}

func (s *streamCloser) Close() error {
	closeErr := s.ReadCloser.Close()
	if s.pgid > 0 {
		_ = syscall.Kill(-s.pgid, syscall.SIGTERM)
	}
	if s.cmd != nil {
		_ = s.cmd.Wait()
	}
	s.disarmKill()
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

	// Group-wide TERM with a group-wide KILL escalation, mirroring the
	// shared runner's signalGroup: the TERM falls back to the leader when
	// the group is already gone (ESRCH), so it is never silently a no-op,
	// and the KILL is armed on a timer because WaitDelay's own escalation
	// is os.Process.Kill -- the leader alone -- which a compose child that
	// ignores TERM would survive. CommandContext's default did neither.
	sc := &streamCloser{}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		sc.armKill(cmd.Process.Pid)
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		return nil
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

	sc.ReadCloser, sc.cmd = stdout, cmd
	if cmd.Process != nil {
		sc.pgid = cmd.Process.Pid
	}
	return sc, nil
}
