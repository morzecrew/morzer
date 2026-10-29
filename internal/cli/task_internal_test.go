package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/jsonout"
)

// A task's output goes through as it printed it, and the invocation exits with
// the task's own status (RFC 0035 decisions 11 and 12).
func TestATasksOutputAndExitCodeAreItsOwn(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Stream: ui.Streams{Out: &out, Err: &errOut}}
	run := ops.TaskRun{Task: "report", Service: "app", ExitCode: 4, Stdout: "rows: 40", Stderr: "warning: slow"}
	failed := domain.RuntimeError(nil, "task report, the service \"app\", exited with code 4")

	err := app.finishTask(run, failed)

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

	if err := app.finishTask(ops.TaskRun{Stdout: "row 1", Truncated: true}, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "row 1\n" {
		t.Errorf("stdout is %q, want the task's own and nothing else", out.String())
	}
	if !strings.Contains(errOut.String(), "output is cut short: only the start") {
		t.Errorf("stderr says nothing of the cut: %q", errOut.String())
	}

	errOut.Reset()
	_ = app.finishTask(ops.TaskRun{Stdout: "row 1"}, nil)
	if errOut.Len() != 0 {
		t.Errorf("a whole output was warned about: %q", errOut.String())
	}
}

// A refusal before the task ran carries no exit code of the task's, and keeps
// the manager's own.
func TestARefusedTaskKeepsTheManagersExitCode(t *testing.T) {
	app := &App{Stream: ui.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}}
	refusal := domain.Usage("task seed takes no arguments")

	err := app.finishTask(ops.TaskRun{}, refusal)
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

	if err := app.finishTask(ops.TaskRun{Stdout: "rows: 40"}, nil); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("the task's output was written beside the envelope: %q", out.String())
	}
}
