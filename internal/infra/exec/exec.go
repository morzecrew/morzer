// Package exec is the single place external tools are run.
//
// Everything the manager shells out to -- docker, compose, sops, hooks --
// goes through this runner, so process-group cancellation, timeouts, output
// streaming, and secret redaction are implemented once and cannot be
// forgotten by an adapter author.
package exec

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
)

// Stream identifies which pipe a line came from.
type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// Line is one line of subprocess output, already redacted.
type Line struct {
	Stream Stream
	Text   string
}

// Command describes an external process to run.
type Command struct {
	// Argv is the program and its arguments. Argv[0] is resolved through
	// PATH unless it contains a separator.
	//
	// No secret ever appears here: argv is world-readable through /proc on
	// most systems, so a credential passed as a flag is a credential
	// published to every local user.
	Argv []string

	Dir string

	// Env is the child's environment. When nil the parent's environment is
	// inherited; when non-nil it fully replaces it, so a caller that wants
	// PATH must include it. Use BaseEnv to build one.
	Env []string

	Stdin io.Reader

	// Stdout receives the child's standard output verbatim when set,
	// instead of it being scanned into lines.
	//
	// It exists for the output that is not text: a volume's contents arrive
	// as a tar stream on stdout, and the line scanner would both corrupt it
	// (splitting on 0x0a bytes that are data) and hold a hundred gigabytes
	// in memory to do it. Redaction is deliberately not applied -- a binary
	// stream cannot be searched for secrets line by line, and the callers
	// that use this pipe bytes to a file rather than to a log.
	//
	// Stderr is unaffected and is still scanned, so a failing command still
	// reports why.
	Stdout io.Writer

	// Timeout bounds the run independently of the context. Zero means only
	// the context governs.
	Timeout time.Duration

	// Redact lists values scrubbed from captured output, logs, and errors.
	// This is the last line of defence -- values should not be reaching
	// the process in the first place.
	Redact []string

	// OnLine receives each output line as it is produced, for the live
	// view. It is called from the reader goroutines, so it must not block.
	OnLine func(Line)

	// CaptureOutput retains stdout and stderr in the Result. Off for
	// commands whose output is large and only needed as a stream.
	CaptureOutput bool

	// MaxCapture bounds retained output. Beyond it, the head is kept and
	// the middle dropped: the beginning of an error is almost always the
	// informative part.
	MaxCapture int

	// GraceTimeout is how long a cancelled process has to exit on SIGTERM
	// before it is killed. Zero uses DefaultGrace.
	GraceTimeout time.Duration

	// ExtraFiles are handed to the child starting at file descriptor 3.
	// This is how the hook ABI's structured-result channel works: the hook
	// writes JSON to fd 3, keeping it separate from stdout, which is
	// reserved for human-readable output that the live view tails.
	ExtraFiles []*os.File
}

const (
	// DefaultMaxCapture bounds retained output at a size that comfortably
	// holds a stack trace but cannot exhaust memory on a runaway process.
	DefaultMaxCapture = 256 * 1024

	// DefaultGrace gives a cancelled tool time to clean up. `docker
	// compose` in particular needs a moment to stop containers properly;
	// killing it immediately leaves them running.
	DefaultGrace = 10 * time.Second

	// stderrExcerptLimit bounds what an error message quotes. Errors are
	// read by humans, and a 200-line excerpt is not read at all.
	stderrExcerptLimit = 2000
)

// Result is the outcome of a run.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration

	// Truncated reports that output exceeded MaxCapture.
	Truncated bool
}

func (r Result) OK() bool { return r.ExitCode == 0 }

// Runner runs external commands. It is an interface so the lifecycle layer
// and every adapter can be tested without spawning processes.
type Runner interface {
	Run(ctx context.Context, cmd Command) (Result, error)

	// Look resolves a binary in PATH, reporting a typed error naming what
	// to install when it is missing.
	Look(name string) (string, error)
}

// runner is the production implementation.
type runner struct {
	// lookPath is injectable so tests can simulate a missing tool.
	lookPath func(string) (string, error)
}

// New returns a Runner backed by os/exec.
func New() Runner {
	return &runner{lookPath: osexec.LookPath}
}

func (r *runner) Look(name string) (string, error) {
	path, err := r.lookPath(name)
	if err != nil {
		return "", domain.Preflight(domain.ErrToolMissing, "required tool %q was not found in PATH", name).
			WithHint("install %s and make sure it is on the PATH of the user running the manager", name)
	}
	return path, nil
}

// ExitError is returned when a process runs to completion with a non-zero
// status. It carries the redacted command, the code, and a bounded stderr
// excerpt, so the operator sees what failed without reading a log file.
type ExitError struct {
	Argv     []string // already redacted
	ExitCode int
	Stderr   string // already redacted and bounded
	Duration time.Duration
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s exited with code %d", strings.Join(e.Argv, " "), e.ExitCode)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

// Run executes the command.
//
// Cancellation semantics are the reason this function exists: the child is
// started in its own process group and cancellation signals the *group*.
// `docker compose` spawns children that outlive a signal sent only to the
// direct child, so without this a cancelled operation would leave containers
// being started by an orphaned process.
func (r *runner) Run(ctx context.Context, cmd Command) (Result, error) {
	if len(cmd.Argv) == 0 {
		return Result{}, domain.Internal(nil, "exec: empty argv")
	}

	if cmd.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cmd.Timeout)
		defer cancel()
	}

	maxCapture := cmd.MaxCapture
	if maxCapture <= 0 {
		maxCapture = DefaultMaxCapture
	}
	grace := cmd.GraceTimeout
	if grace <= 0 {
		grace = DefaultGrace
	}

	redactor := newRedactor(cmd.Redact)
	safeArgv := redactor.strings(cmd.Argv)

	c := osexec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	c.Stdin = cmd.Stdin
	c.ExtraFiles = cmd.ExtraFiles

	// Setpgid puts the child in a new process group whose ID equals its
	// PID, so the whole tree can be signalled at once.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// signalGroup signals the group rather than the process. The negative
	// PID is the kernel's convention for "this process group".
	signalGroup := func(sig syscall.Signal) error {
		if c.Process == nil {
			return nil
		}
		if err := syscall.Kill(-c.Process.Pid, sig); err != nil {
			// The group may already be gone; fall back to the
			// process so the signal is not silently a no-op.
			return c.Process.Signal(sig)
		}
		return nil
	}

	c.Cancel = func() error { return signalGroup(syscall.SIGTERM) }

	// WaitDelay escalates to SIGKILL when the grace period expires. Go
	// kills only the direct child, so the group is killed here too.
	c.WaitDelay = grace

	stdoutPipe, err := c.StdoutPipe()
	if err != nil {
		return Result{}, domain.Internal(err, "exec: cannot open stdout pipe")
	}
	stderrPipe, err := c.StderrPipe()
	if err != nil {
		return Result{}, domain.Internal(err, "exec: cannot open stderr pipe")
	}

	started := time.Now()
	if err := c.Start(); err != nil {
		if errors.Is(err, osexec.ErrNotFound) {
			return Result{}, domain.Preflight(domain.ErrToolMissing,
				"required tool %q was not found in PATH", cmd.Argv[0]).
				WithHint("install %s and make sure it is on the PATH", cmd.Argv[0])
		}
		return Result{}, domain.RuntimeError(err, "cannot start %s", strings.Join(safeArgv, " "))
	}

	pgid := 0
	if c.Process != nil {
		pgid = c.Process.Pid
	}

	// waited is closed once Wait has returned, so the escalation below does
	// not outlive the process it was watching.
	waited := make(chan struct{})

	// terminate stops the group once a write failure has already decided the
	// outcome of the run.
	//
	// Closing the pipe is not enough on its own: it only makes the child's
	// next write fail, and a child that ignores SIGPIPE -- a shell pipeline
	// with a trap, anything that installs its own handler -- keeps running,
	// so Wait blocks until it finishes on its own. A volume capture that
	// filled the disk would hang there instead of returning the storage
	// error, which is the failure this exists to avoid.
	//
	// SIGTERM first, because the child is usually `docker run` and killing
	// it outright leaves the container behind; SIGKILL once the grace period
	// is up, for the child that ignores that too.
	terminate := func() {
		_ = signalGroup(syscall.SIGTERM)
		go func() {
			timer := time.NewTimer(grace)
			defer timer.Stop()
			select {
			case <-waited:
			case <-timer.C:
				_ = signalGroup(syscall.SIGKILL)
			}
		}()
	}

	var (
		wg        sync.WaitGroup
		stdoutBuf = newBoundedBuffer(maxCapture)
		// stderr is always retained regardless of CaptureOutput: it is
		// what ExitError quotes, and an error without its cause is a
		// support ticket.
		stderrBuf = newBoundedBuffer(maxCapture)
	)

	var streamErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		if cmd.Stdout != nil {
			// Raw, because the caller asked for bytes rather than
			// lines.
			_, streamErr = io.Copy(cmd.Stdout, stdoutPipe)
			if streamErr != nil {
				// Closed rather than drained. Something must
				// happen to the pipe -- an unread one blocks the
				// child forever and Wait with it -- and the
				// reason this write failed is usually a full
				// disk, at which point reading the remaining
				// hundred gigabytes into io.Discard is a long
				// wait for an outcome already decided. Closing
				// makes the child's next write fail instead.
				//
				// Wait closes it again and ignores the error, so
				// the double close is safe.
				_ = stdoutPipe.Close()

				// And the child is stopped, because a closed
				// pipe alone only asks.
				terminate()
			}
			return
		}
		scanLines(stdoutPipe, func(text string) {
			text = redactor.string(text)
			if cmd.CaptureOutput {
				stdoutBuf.WriteLine(text)
			}
			if cmd.OnLine != nil {
				cmd.OnLine(Line{Stream: StreamStdout, Text: text})
			}
		})
	}()
	go func() {
		defer wg.Done()
		scanLines(stderrPipe, func(text string) {
			text = redactor.string(text)
			{
				stderrBuf.WriteLine(text)
			}
			if cmd.OnLine != nil {
				cmd.OnLine(Line{Stream: StreamStderr, Text: text})
			}
		})
	}()

	wg.Wait()
	waitErr := c.Wait()
	close(waited)
	duration := time.Since(started)

	// A process that ignored SIGTERM and outlived WaitDelay would keep
	// holding the deployment's ports or its database connection, so the
	// group is swept -- but only on a run that was cancelled or timed out.
	//
	// The restriction is the PID-reuse hazard. Wait has just reaped the
	// leader, so its pid is free for the kernel to hand to something else;
	// a group whose members are all gone can therefore have its id reused
	// by an unrelated process, and this signal would land on that. On a run
	// that ended by itself the sweep buys nothing anyway: nothing was
	// signalled, the leader exited on its own terms. On a cancelled one it
	// is the whole point, and the group was signalled before the reap.
	if pgid > 0 && ctx.Err() != nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}

	result := Result{
		ExitCode:  c.ProcessState.ExitCode(),
		Stdout:    stdoutBuf.String(),
		Stderr:    stderrBuf.String(),
		Duration:  duration,
		Truncated: stdoutBuf.Truncated() || stderrBuf.Truncated(),
	}

	// Context cancellation outranks the exit status: a tool killed by our
	// own SIGTERM reports a signal death, and reporting that as a tool
	// failure would send the operator hunting for a bug that is not there.
	if ctxErr := ctx.Err(); ctxErr != nil {
		switch {
		case errors.Is(ctxErr, context.DeadlineExceeded):
			return result, domain.RuntimeError(ctxErr,
				"%s timed out after %s", strings.Join(safeArgv, " "), duration.Round(time.Second)).
				WithHint("raise the timeout in the manifest, or investigate why the tool is slow")
		default:
			return result, domain.Interrupted("%s was cancelled", strings.Join(safeArgv, " "))
		}
	}

	// Before the exit status, because when both are set the write failure
	// caused the exit: closing the pipe is what kills the child, so a full
	// disk during a volume capture surfaces as "exited with code 141"
	// unless this is checked first. That sends an operator looking for a
	// bug in tar instead of at their disk.
	//
	// Reporting success here at all is how a full disk produces a truncated
	// tarball that verifies against a checksum taken of the truncation.
	if streamErr != nil {
		return result, domain.RuntimeError(streamErr,
			"cannot store the output of %s", strings.Join(safeArgv, " "))
	}

	if waitErr != nil {
		var exitErr *osexec.ExitError
		if errors.As(waitErr, &exitErr) {
			return result, &ExitError{
				Argv:     safeArgv,
				ExitCode: result.ExitCode,
				Stderr:   excerpt(result.Stderr, stderrExcerptLimit),
				Duration: duration,
			}
		}
		return result, domain.RuntimeError(waitErr, "%s failed", strings.Join(safeArgv, " "))
	}

	return result, nil
}

// scanLines reads r line by line, handling lines longer than the default
// scanner limit -- container logs and JSON output routinely exceed 64 KiB.
func scanLines(r io.Reader, fn func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		fn(sc.Text())
	}
	// A scan error (a too-long line, a closed pipe) is not reported: the
	// process's exit status is the authority on whether it worked, and
	// output is diagnostic. Losing a log line must not fail an operation.
	if err := sc.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
		fn(fmt.Sprintf("[output truncated: %v]", err))
	}
}

// boundedBuffer keeps at most max bytes, retaining the head.
type boundedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	max       int
	truncated bool
}

func newBoundedBuffer(max int) *boundedBuffer {
	return &boundedBuffer{max: max}
}

func (b *boundedBuffer) WriteLine(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() >= b.max {
		b.truncated = true
		return
	}
	if b.buf.Len() > 0 {
		b.buf.WriteByte('\n')
	}
	remaining := b.max - b.buf.Len()
	if len(s) > remaining {
		b.buf.WriteString(s[:remaining])
		b.truncated = true
		return
	}
	b.buf.WriteString(s)
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *boundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// excerpt bounds a string for an error message, keeping the tail -- the last
// thing a failing tool says is usually the reason it failed.
func excerpt(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return "..." + s[len(s)-limit:]
}

// BaseEnv builds a child environment from the parent's, with overrides
// applied. Variables holding secrets are never added here: secrets reach
// tools as files, not as environment.
func BaseEnv(overrides map[string]string) []string {
	env := os.Environ()
	return MergeEnv(env, overrides)
}

// PassthroughEnv is what a tool needs from the parent environment to run at
// all: where to find itself, where its own configuration lives, and how to
// reach the daemon.
//
// Everything outside this list is dropped. That is the point -- see
// FilteredEnv.
var PassthroughEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "LANG",
	"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
	// Docker's own client configuration: which daemon, which context,
	// which certificates, and the agent socket for an ssh:// host.
	"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
	"DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY", "DOCKER_API_VERSION",
	"SSH_AUTH_SOCK",
	// Proxies, for a machine that reaches a registry through one.
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy",
}

// FilteredEnv builds a child environment from an allow-list plus explicit
// overrides, instead of inheriting whatever the operator's shell happened to
// hold.
//
// This is what makes a declared parameter the only way an operator value
// reaches Compose. Inheriting the whole environment meant any `<PRODUCT>_*`
// variable silently interpolated into a Compose file -- undocumented,
// unvalidated, unrecorded, and not visible to the manifest. The result was a
// deployment published on one port while preflight checked, and the health
// probe asked for, another.
//
// Case-sensitive, and deliberately: HTTP_PROXY and http_proxy are both real
// conventions and tools read them differently, so both are listed rather than
// folded together.
func FilteredEnv(passthrough []string, overrides map[string]string) []string {
	allowed := make(map[string]bool, len(passthrough))
	for _, key := range passthrough {
		allowed[key] = true
	}

	env := make([]string, 0, len(passthrough)+len(overrides))
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && allowed[k] {
			env = append(env, kv)
		}
	}
	return MergeEnv(env, overrides)
}

// MergeEnv applies overrides to an environment slice, replacing existing keys
// rather than appending duplicates.
func MergeEnv(env []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return env
	}
	index := make(map[string]int, len(env))
	out := make([]string, len(env))
	copy(out, env)
	for i, kv := range out {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			index[kv[:eq]] = i
		}
	}
	keys := sortedKeys(overrides)
	for _, k := range keys {
		entry := k + "=" + overrides[k]
		if i, ok := index[k]; ok {
			out[i] = entry
		} else {
			out = append(out, entry)
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Insertion sort: these maps hold a couple of dozen entries at most,
	// and this keeps the package free of a sort import for one call.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
