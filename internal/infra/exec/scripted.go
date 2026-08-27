package exec

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Scripted is a Runner whose replies are written in advance.
//
// It exists because most of what an adapter does is decide what a tool's output
// *meant*, and the interesting answers are the ones a healthy machine will not
// produce: Compose reporting a container that exited 137, systemctl reporting a
// unit that failed to enable, a tool that prints a warning to stderr and exits
// zero anyway. Waiting for a real daemon to misbehave is not a test strategy.
//
// Deliberately not a general mock. It matches on the command line, replies, and
// records what it was asked — no expectation ordering, no verify phase, nothing
// that turns a test failure into a puzzle about the mock.
type Scripted struct {
	mu sync.Mutex

	replies []scriptedReply
	calls   []Command

	// Fallback answers a command no rule matched. The zero value is a
	// successful, silent run, which keeps a test to the commands it cares
	// about instead of scripting every incidental one.
	Fallback Result

	// LookErr, when set, makes every Look fail -- the "this tool is not
	// installed" case that preflight and doctor both report on.
	LookErr error
}

type scriptedReply struct {
	match  string
	result Result
	err    error

	// exit is returned alongside the result, for a command that ran and
	// failed. Distinct from err, which is a command that could not be run.
	exit *ExitError
}

// NewScripted returns a runner that succeeds silently until told otherwise.
func NewScripted() *Scripted { return &Scripted{} }

var _ Runner = (*Scripted)(nil)

// On makes a command whose joined argv contains match return result.
//
// Substring matching rather than exact argv: adapters build long command lines
// with paths and generated project names in them, and a test that had to
// reproduce one exactly would be asserting the argv rather than the behaviour.
// Rules are tried in the order they were added, so a specific one registered
// first wins over a general one after it.
func (s *Scripted) On(match string, result Result) *Scripted {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = append(s.replies, scriptedReply{match: match, result: result})
	return s
}

// OnError makes a matching command fail the way a runner fails when the process
// could not be run at all -- distinct from a process that ran and exited
// non-zero, which is On with a non-zero ExitCode.
func (s *Scripted) OnError(match string, err error) *Scripted {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = append(s.replies, scriptedReply{match: match, err: err})
	return s
}

// OnExit is the common case: a command that ran and failed.
//
// It returns an *ExitError alongside the result, because that is what the real
// runner does and what every caller keys on: an adapter that classifies a
// failure does it with errors.As, and a fake that set only the exit code would
// let a test pass while the adapter treated the failure as a success. That is
// exactly what happened the first time this was used against the secret store.
func (s *Scripted) OnExit(match string, code int, stderr string) *Scripted {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = append(s.replies, scriptedReply{
		match:  match,
		result: Result{ExitCode: code, Stderr: stderr},
		exit:   &ExitError{ExitCode: code, Stderr: stderr},
	})
	return s
}

// OnOutput is the other common case: a command that ran and printed something.
func (s *Scripted) OnOutput(match, stdout string) *Scripted {
	return s.On(match, Result{Stdout: stdout})
}

func (s *Scripted) Run(ctx context.Context, cmd Command) (Result, error) {
	s.mu.Lock()
	s.calls = append(s.calls, cmd)
	replies := make([]scriptedReply, len(s.replies))
	copy(replies, s.replies)
	fallback := s.Fallback
	s.mu.Unlock()

	// A cancelled context is honoured before any rule, because that is what
	// the real runner does and what the cancellation tests depend on.
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	line := strings.Join(cmd.Argv, " ")
	for _, r := range replies {
		if strings.Contains(line, r.match) {
			if r.err != nil {
				return Result{}, r.err
			}
			res := r.result
			if res.Duration == 0 {
				res.Duration = time.Millisecond
			}
			if r.exit != nil {
				exit := *r.exit
				exit.Argv = cmd.Argv
				exit.Duration = res.Duration
				return res, &exit
			}
			return res, nil
		}
	}
	return fallback, nil
}

func (s *Scripted) Look(name string) (string, error) {
	if s.LookErr != nil {
		return "", s.LookErr
	}
	return "/usr/bin/" + name, nil
}

// Calls returns every command the runner was asked to run, in order.
func (s *Scripted) Calls() []Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Command, len(s.calls))
	copy(out, s.calls)
	return out
}

// Ran reports whether any command's line contains match.
func (s *Scripted) Ran(match string) bool {
	return slices.ContainsFunc(s.Calls(), func(c Command) bool {
		return strings.Contains(strings.Join(c.Argv, " "), match)
	})
}

// CommandLines renders every call, for a failure message that says what the
// adapter actually did rather than only what it did not.
func (s *Scripted) CommandLines() string {
	var b strings.Builder
	for i, c := range s.Calls() {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, strings.Join(c.Argv, " "))
	}
	if b.Len() == 0 {
		return "  (no commands were run)\n"
	}
	return b.String()
}
