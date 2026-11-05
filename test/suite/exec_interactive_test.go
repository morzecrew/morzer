package suite

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// `morzer exec -i` (RFC 0050 P2), against the fake runtime. What needs a real
// terminal and a real daemon is the acceptance run's.

func interactiveExec(h *harness, stdin string, argv ...string) (ops.ExecResult, string, error) {
	var out bytes.Buffer
	res, err := ops.ExecInteractive(context.Background(), h.Deps,
		ops.ExecOptions{Service: "app", Argv: argv},
		ports.ExecIO{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &out}, nil)
	return res, out.String(), err
}

func execRecords(t *testing.T, h *harness) []domain.OperationRecord {
	t.Helper()
	records, err := h.Deps.State.Operations(context.Background(), ops.OperationFilterAll())
	require.NoError(t, err)
	var out []domain.OperationRecord
	for _, rec := range records {
		if rec.Type == domain.OpTypeExec {
			out = append(out, rec)
		}
	}
	return out
}

// The command gets the caller's streams, and the session is journalled as an
// exec with the argv and never what passed through it.
func TestAnInteractiveExecHandsTheCommandTheCallersStreams(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	res, out, err := interactiveExec(h, "select 1;\n", "psql", "-U", "demo")
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "select 1;\n", out, "the command did not read the caller's stdin")
	require.Len(t, h.Runtime.Sessions, 1)
	assert.False(t, h.Runtime.Sessions[0].TTY, "a TTY was asked for with no terminal")

	records := execRecords(t, h)
	require.Len(t, records, 1)
	assert.Equal(t, "true", records[0].Flags["interactive"])
	assert.Equal(t, "psql -U demo", records[0].Flags["argv"])
	assert.NotContains(t, journalText(t, &records[0]), "select 1", "what the session read reached the journal")
}

// A command that exits non-zero is a result, as for `exec` without -i.
func TestAnInteractiveExecPropagatesTheCommandsExitCode(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	h.Runtime.SessionCode = 3

	res, _, err := interactiveExec(h, "", "sh", "-c", "exit 3")
	require.NoError(t, err)
	assert.Equal(t, 3, res.ExitCode)
	assert.Equal(t, "3", execRecords(t, h)[0].Flags["exit_code"])
}

// A service that is not running is refused before anything starts, and
// nothing is journalled: a refused command is not something a human did.
func TestAnInteractiveExecRefusesAServiceThatIsNotRunning(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	h.Runtime.RemoveService("app")

	_, _, err := interactiveExec(h, "", "sh")
	require.Error(t, err)
	assert.Empty(t, h.Runtime.Sessions)
	assert.Empty(t, execRecords(t, h))
}

// A runtime that cannot hold a session is refused by name, and nothing ran, so
// nothing is journalled (RFC 0021 decision 4).
func TestAnInteractiveExecIsRefusedByARuntimeThatCannotHoldOne(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	h.Runtime.Fail["ExecInteractive"] = fmt.Errorf("compose: %w", domain.ErrUnsupported)

	_, _, err := interactiveExec(h, "", "sh")
	require.ErrorIs(t, err, domain.ErrUnsupported)
	assert.Empty(t, execRecords(t, h), "an exec that never ran was journalled")
}

// The command is still named: everything after `--` is the container's argv
// (RFC 0050 decision 2).
func TestAnInteractiveExecStillNeedsACommand(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	_, _, err := interactiveExec(h, "")
	require.Error(t, err)
	assert.Empty(t, h.Runtime.Sessions)
}

// The CLI's hold on the process's signals spans the command and nothing
// before it: a Ctrl-C while the deployment is resolved or the service checked
// still stops the command from starting. So the guard is taken after those
// checks, never for a refused session, and given back when the command ends.
func TestTheSessionGuardHoldsOnlyTheCommand(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	var taken, released int
	var checkedFirst bool
	since := len(h.Runtime.Calls)
	guard := func(ctx context.Context) (context.Context, func()) {
		taken++
		checkedFirst = slices.Contains(h.Runtime.Calls[since:], "Status") && len(h.Runtime.Sessions) == 0
		return ctx, func() { released++ }
	}
	streams := ports.ExecIO{Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}}

	_, err := ops.ExecInteractive(context.Background(), h.Deps,
		ops.ExecOptions{Service: "app", Argv: []string{"sh"}}, streams, guard)
	require.NoError(t, err)
	assert.Equal(t, 1, taken)
	assert.Equal(t, 1, released, "the signals were not given back when the command ended")
	assert.True(t, checkedFirst, "the guard was taken before the service was checked")

	h.Runtime.RemoveService("app")
	_, err = ops.ExecInteractive(context.Background(), h.Deps,
		ops.ExecOptions{Service: "app", Argv: []string{"sh"}}, streams, guard)
	require.Error(t, err)
	assert.Equal(t, 1, taken, "a refused session took the signals over")
}
