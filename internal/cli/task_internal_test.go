package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/jsonout"
)

// With --output, a task's output goes through as it printed it, and the
// invocation exits with the task's own status (RFC 0035 decisions 12 and 21).
func TestATasksOutputAndExitCodeAreItsOwn(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Stream: ui.Streams{Out: &out, Err: &errOut}}
	run := ops.TaskRun{Task: "report", Service: "app", ExitCode: 4, Stdout: "rows: 40", Stderr: "warning: slow"}
	failed := domain.RuntimeError(nil, "task report, the service \"app\", exited with code 4")

	err := app.finishTask(run, failed, true)

	if got := exitCodeFor(err); got != 4 {
		t.Errorf("exit %d, want the task's own 4", got)
	}
	if !silentFailure(err) {
		t.Error("the step's error, already on the screen, would be printed twice")
	}
	if out.String() != "rows: 40\n" {
		t.Errorf("stdout is %q, want the task's own, ended", out.String())
	}
	if errOut.String() != "warning: slow\n" {
		t.Errorf("stderr is %q, want the task's own, ended", errOut.String())
	}
}

// Output the runtime cut is followed by a warning, on stderr so a report piped
// to a file is not changed by it.
func TestATaskWhoseOutputWasCutIsFollowedByAWarning(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Stream: ui.Streams{Out: &out, Err: &errOut}}

	if err := app.finishTask(ops.TaskRun{Stdout: "row 1", Truncated: true}, nil, true); err != nil {
		t.Fatal(err)
	}
	if out.String() != "row 1\n" {
		t.Errorf("stdout is %q, want the task's own and nothing else", out.String())
	}
	if !strings.Contains(errOut.String(), "output is cut short") {
		t.Errorf("stderr says nothing of the cut: %q", errOut.String())
	}

	errOut.Reset()
	_ = app.finishTask(ops.TaskRun{Stdout: "row 1"}, nil, true)
	if errOut.Len() != 0 {
		t.Errorf("a whole output was warned about: %q", errOut.String())
	}

	// A failure's last lines are the last of what was kept, not of what the
	// task printed, so they carry the warning too.
	errOut.Reset()
	failed := domain.RuntimeError(nil, "task seed, the service \"app\", exited with code 3")
	_ = app.finishTask(ops.TaskRun{ExitCode: 3, Stderr: "early line", Truncated: true}, failed, false)
	if !strings.Contains(errOut.String(), "output is cut short") {
		t.Errorf("a failure's lines from cut output carried no warning: %q", errOut.String())
	}
}

// A refusal before the task ran carries no exit code of the task's, and keeps
// the manager's own.
func TestARefusedTaskKeepsTheManagersExitCode(t *testing.T) {
	app := &App{Stream: ui.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}}
	refusal := domain.Usage("task seed takes no arguments")

	err := app.finishTask(ops.TaskRun{}, refusal, false)
	if got := exitCodeFor(err); got != domain.ExitUsage {
		t.Errorf("exit %d, want usage", got)
	}
	if silentFailure(err) {
		t.Error("a refusal nothing has printed would be silent")
	}
}

// With --json, stdout is the envelope and nothing else.
func TestATasksOutputStaysInTheEnvelope(t *testing.T) {
	var out bytes.Buffer
	app := &App{json: jsonout.New(jsonout.Options{Out: &out}), Stream: ui.Streams{Out: &out, Err: &out}}

	for _, output := range []bool{false, true} {
		if err := app.finishTask(ops.TaskRun{Stdout: "rows: 40"}, nil, output); err != nil {
			t.Fatal(err)
		}
	}
	if out.Len() != 0 {
		t.Errorf("the task's output was written beside the envelope: %q", out.String())
	}
}

// Without --output, a task that succeeded prints nothing of its own: the
// operation's result is the answer, and its streams are for whoever asks.
func TestATaskPrintsNothingOfItsOwnUnlessAsked(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Stream: ui.Streams{Out: &out, Err: &errOut}}

	run := ops.TaskRun{Task: "seed", Service: "app", Stdout: "workspace: demo", Stderr: "[envfile] A <- A_FILE", Truncated: true}
	if err := app.finishTask(run, nil, false); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("a task nobody asked to hear printed:\n--- stdout ---\n%s--- stderr ---\n%s", out.String(), errOut.String())
	}
}

// A task that failed shows the end of its stderr, so the operator sees why
// without running it again, and nothing of its stdout.
func TestAFailedTaskShowsTheEndOfItsStderr(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Stream: ui.Streams{Out: &out, Err: &errOut}}

	var lines []string
	for i := 1; i <= taskFailureTailLines+5; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	run := ops.TaskRun{Task: "seed", Service: "app", ExitCode: 3, Stdout: "rows: 40", Stderr: strings.Join(lines, "\n") + "\n"}
	failed := domain.RuntimeError(nil, "task seed, the service \"app\", exited with code 3")

	err := app.finishTask(run, failed, false)

	if got := exitCodeFor(err); got != 3 {
		t.Errorf("exit %d, want the task's own 3", got)
	}
	if out.Len() != 0 {
		t.Errorf("a failed task's stdout was printed: %q", out.String())
	}
	want := strings.Join(lines[len(lines)-taskFailureTailLines:], "\n") + "\n"
	if errOut.String() != want {
		t.Errorf("stderr is %q, want the last %d lines of the task's: %q", errOut.String(), taskFailureTailLines, want)
	}
}

// --verbose printed the output as it arrived, so the end does not print it
// again; --output still does, because it was asked for.
func TestVerboseDoesNotPrintATasksOutputTwice(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Flags: globalFlags{verbose: true}, Stream: ui.Streams{Out: &out, Err: &errOut}}
	run := ops.TaskRun{Task: "seed", Service: "app", ExitCode: 3, Stdout: "rows: 40", Stderr: "boom"}
	failed := domain.RuntimeError(nil, "task seed, the service \"app\", exited with code 3")

	_ = app.finishTask(run, failed, false)
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("--verbose printed the output again:\n--- stdout ---\n%s--- stderr ---\n%s", out.String(), errOut.String())
	}

	_ = app.finishTask(run, failed, true)
	if out.String() != "rows: 40\n" || errOut.String() != "boom\n" {
		t.Errorf("--verbose --output did not print the output: stdout %q, stderr %q", out.String(), errOut.String())
	}
}
