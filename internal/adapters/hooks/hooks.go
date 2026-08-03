// Package hooks implements the hook ABI: the contract between the manager and
// the executables a release ships.
//
// Hooks are the only way to add product-specific logic without changing the
// manager, so this ABI is a public, versioned contract. Everything here is
// therefore deliberately conservative: a documented environment, a structured
// result channel that is not stdout, three exit-code meanings, and a timeout
// that reaches the whole process group.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// ResultFD and the exit-code meanings are defined by the ABI in ports; these
// keep the names short inside this package.
const (
	ResultFD    = ports.HookResultFD
	ExitSuccess = ports.HookExitSuccess
	ExitSkipped = ports.HookExitSkipped
)

// Type aliases keep call sites inside this package readable while the ABI
// vocabulary itself lives in ports, where the lifecycle layer can reach it
// without importing an adapter.
type (
	Env      = ports.HookEnv
	Phase    = ports.HookPhase
	Result   = ports.HookResult
	Artifact = ports.HookArtifact
	Outcome  = ports.HookOutcome
)

// Phase values, re-exported for brevity at call sites in this package.
const (
	PhasePreflight   = ports.PhasePreflight
	PhasePreUpdate   = ports.PhasePreUpdate
	PhasePostUpdate  = ports.PhasePostUpdate
	PhaseMigrate     = ports.PhaseMigrate
	PhaseSmokeTest   = ports.PhaseSmokeTest
	PhaseBackup      = ports.PhaseBackup
	PhaseRestore     = ports.PhaseRestore
	PhaseHealthCheck = ports.PhaseHealthCheck
)

// Runner executes hooks.
type Runner struct {
	runner exec.Runner
	redact []string
	onLine func(exec.Line)
}

var _ ports.HookRunner = (*Runner)(nil)

func NewRunner(runner exec.Runner, opts ...Option) *Runner {
	r := &Runner{runner: runner}
	for _, o := range opts {
		o(r)
	}
	return r
}

type Option func(*Runner)

func WithRedaction(values []string) Option {
	return func(r *Runner) { r.redact = values }
}

func WithOutputSink(fn func(exec.Line)) Option {
	return func(r *Runner) { r.onLine = fn }
}

// Run executes a hook from a release.
//
// The command is resolved against the release root, so a hook named `backup`
// runs the bundle's `hooks/backup` and never something on PATH that shares its
// name. Hooks run only from a release that has already been verified.
func (r *Runner) Run(ctx context.Context, rel domain.Release, command []string, env Env, timeout time.Duration) (Outcome, error) {
	if len(command) == 0 {
		return Outcome{}, domain.Internal(nil, "hook invoked with no command")
	}

	argv := append([]string(nil), command...)
	resolved, err := rel.Path(argv[0])
	if err != nil {
		return Outcome{}, err
	}
	argv[0] = resolved

	info, err := os.Stat(resolved)
	if err != nil {
		return Outcome{}, domain.ValidationError(err,
			"the release declares hook %q but it is missing", command[0]).
			WithHint("a declared-but-missing hook is a broken bundle")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return Outcome{}, domain.ValidationError(nil,
			"hook %q is not executable (mode %04o)", command[0], info.Mode().Perm()).
			WithHint("run `chmod +x %s` in the bundle source and rebuild it", command[0])
	}

	// The result pipe. The read end stays here; the write end becomes fd 3
	// in the child and is closed locally straight after start, so the read
	// returns EOF when the hook exits rather than blocking forever.
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		return Outcome{}, domain.Internal(err, "cannot create the hook result pipe")
	}

	resultCh := make(chan []byte, 1)
	go func() {
		// Bounded: a hook that streams gigabytes into fd 3 is broken,
		// and must not take the manager's memory with it.
		data, _ := io.ReadAll(io.LimitReader(readEnd, 1<<20))
		_ = readEnd.Close()
		resultCh <- data
	}()

	res, runErr := r.runner.Run(ctx, exec.Command{
		Argv: argv,
		// The working directory is the release root, so a hook can use
		// relative paths to its own files.
		Dir:           rel.Root,
		Env:           exec.BaseEnv(ports.HookEnvVars(env)),
		Timeout:       timeout,
		Redact:        r.redact,
		OnLine:        r.onLine,
		CaptureOutput: true,
		ExtraFiles:    []*os.File{writeEnd},
	})

	// Closing our copy of the write end is what lets the reader see EOF.
	_ = writeEnd.Close()
	resultData := <-resultCh

	outcome := Outcome{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		Duration: res.Duration,
	}

	if parsed, ok := parseResult(resultData); ok {
		outcome.Result = parsed
		outcome.Skipped = parsed.Skipped
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			outcome.ExitCode = exitErr.ExitCode
			if exitErr.ExitCode == ExitSkipped {
				// Exit 2 is "nothing to do", not a failure.
				outcome.Skipped = true
				return outcome, nil
			}
			return outcome, hookFailure(command[0], exitErr, outcome)
		}
		return outcome, runErr
	}

	return outcome, nil
}

// parseResult decodes the hook's structured output, tolerating silence.
//
// A hook that writes nothing to fd 3 is the common case -- most hooks just do
// work and exit -- so failing to parse an empty buffer would make every simple
// hook look broken.
func parseResult(data []byte) (Result, bool) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return Result{}, false
	}
	var out Result
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return Result{}, false
	}
	return out, true
}

// hookFailure builds an error that quotes what the hook actually said.
func hookFailure(name string, exitErr *exec.ExitError, outcome Outcome) error {
	detail := strings.TrimSpace(outcome.Result.Message)
	if detail == "" {
		detail = lastLines(outcome.Stderr, 3)
	}
	if detail == "" {
		detail = lastLines(outcome.Stdout, 3)
	}

	msg := fmt.Sprintf("hook %q failed with exit code %d", name, exitErr.ExitCode)
	if detail != "" {
		msg += ": " + detail
	}

	return domain.RuntimeError(exitErr, "%s", msg).
		WithHint("the hook ships with the release; its full output is in the log")
}

// lastLines keeps the tail of a hook's output. The last thing a failing script
// prints is almost always the reason it failed.
func lastLines(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "; "))
}
