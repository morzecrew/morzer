package compose

import (
	"context"
	"errors"
	"io"
	osexec "os/exec"
	"sync"
	"sync/atomic"
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

	// ctx is what tells a stream morzer stopped from one the runtime ended:
	// both reach EOF, and only the second is a failure worth reporting.
	ctx    context.Context
	stderr stderrTail
	redact []string

	waitOnce sync.Once
	waitErr  error
	reaped   atomic.Bool

	mu        sync.Mutex
	killTimer *time.Timer
}

// maxStderrTail bounds what is kept of compose's stderr. A follow can run for
// days, and the reason a stream failed is the last thing compose said.
const maxStderrTail = 4 << 10

// stderrTail keeps the end of what was written to it. Read only after Wait,
// which waits for os/exec's copy into it, so it needs no lock.
type stderrTail struct{ buf []byte }

func (t *stderrTail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - maxStderrTail; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

// Read ends the stream with the runtime's failure, rather than EOF, when
// compose exited non-zero on its own: a daemon that is down and a service
// that does not exist both close stdout with nothing on it.
func (s *streamCloser) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if !errors.Is(err, io.EOF) || s.cmd == nil {
		return n, err
	}
	// An exit status only. Wait also reports ErrWaitDelay for a compose that
	// exited 0 while a child held stderr open, and that stream did end.
	var exit *osexec.ExitError
	if waitErr := s.wait(); errors.As(waitErr, &exit) && s.ctx.Err() == nil {
		reason := exec.Redact(s.redact, domain.LastLines(string(s.stderr.buf), 3))
		if reason == "" {
			reason = waitErr.Error()
		}
		return n, domain.RuntimeError(waitErr, "the runtime could not stream the logs: %s", reason).
			WithHint("`docker info` checks the daemon; `morzer ps` lists the services")
	}
	return n, err
}

// wait reaps the process once, for whichever of Read and Close gets there
// first.
func (s *streamCloser) wait() error {
	s.waitOnce.Do(func() {
		s.waitErr = s.cmd.Wait()
		s.reaped.Store(true)
	})
	return s.waitErr
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
	// Only ESRCH proves the group is gone; a nil probe means members
	// survive, and any other error (EPERM above all) means the group
	// exists but cannot be probed -- either way the timer stays.
	if s.pgid > 0 && !errors.Is(syscall.Kill(-s.pgid, 0), syscall.ESRCH) {
		return
	}
	s.killTimer.Stop()
}

func (s *streamCloser) Close() error {
	// Once Read has reaped the leader at the stream's own end, neither half
	// is left to do. Wait has closed the pipe, and closing it again reports
	// os.ErrClosed. The leader's id is free for the kernel to reuse, so a
	// group signal could land on an unrelated group; the shared runner's
	// post-Wait sweep keeps the same rule.
	if s.reaped.Load() {
		s.disarmKill()
		return nil
	}
	closeErr := s.ReadCloser.Close()
	if s.pgid > 0 {
		_ = syscall.Kill(-s.pgid, syscall.SIGTERM)
	}
	if s.cmd != nil {
		_ = s.wait()
	}
	s.disarmKill()
	return closeErr
}

func startStream(ctx context.Context, argv []string, cfg ports.RuntimeConfig, redact []string) (io.ReadCloser, error) {
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
	sc := &streamCloser{ctx: ctx, redact: redact}
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
	// Compose writes its own progress to stderr; keeping it off stdout keeps
	// the caller's stream to log lines only. Its tail is kept for Read, which
	// quotes it when compose fails.
	cmd.Stderr = &sc.stderr

	if err := cmd.Start(); err != nil {
		return nil, domain.RuntimeError(err, "cannot start the log stream")
	}

	sc.ReadCloser, sc.cmd = stdout, cmd
	if cmd.Process != nil {
		sc.pgid = cmd.Process.Pid
	}
	return sc, nil
}
