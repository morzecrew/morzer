package suite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// RFC 0041: a file an operator passes to a task with --input.

const restoreTask = "  restore:\n" +
	"    service: app\n" +
	"    command: [restore, \"{inputs.snapshot}\"]\n" +
	"    inputs:\n" +
	"      snapshot:\n" +
	"        description: Catalogue snapshot (JSONL)\n" +
	"        max_size: 1KiB\n"

// inputsHarness is taskHarness at the manager version that reads `inputs`.
func inputsHarness(t *testing.T, tasks ...string) *harness {
	t.Helper()
	h := newHarness(t)

	path := filepath.Join(h.Release.Root, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	patched := strings.Replace(string(raw), `min_manager_version: "0.0.0"`,
		`min_manager_version: "`+domain.TaskInputsMinManagerVersion+`"`, 1)
	require.NotEqual(t, string(raw), patched, "the fixture no longer declares the manager version this rewrites")
	patched += "\ntasks:\n" + strings.Join(tasks, "")
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))
	h.Release, err = release.Load(h.Release.Root)
	require.NoError(t, err)

	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	return h
}

func writeInput(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog-snapshot.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// stagingLeft is what is under the inputs directory now.
func stagingLeft(t *testing.T, h *harness) []string {
	t.Helper()
	entries, err := os.ReadDir(h.Paths.InputsDir())
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestATaskReadsTheBytesItWasGivenAndTheJournalNamesThem(t *testing.T) {
	h := inputsHarness(t, restoreTask)
	const content = `{"sku":"a1"}` + "\n"
	path := writeInput(t, content)
	sum := sha256.Sum256([]byte(content))
	digest := hex.EncodeToString(sum[:])

	res, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{
		Name: "restore", Inputs: map[string]string{"snapshot": path},
	})
	require.NoError(t, err)

	run := h.Runtime.OneShots[len(h.Runtime.OneShots)-1]
	assert.Equal(t, []string{"restore", "/morzer/inputs/snapshot"}, run.Argv)
	assert.Equal(t, "/morzer/inputs/snapshot", run.Env["MORZER_INPUT_SNAPSHOT"])
	require.Len(t, run.Mounts, 1)
	assert.Equal(t, "/morzer/inputs/snapshot", run.Mounts[0].Target)
	assert.True(t, run.Mounts[0].ReadOnly, "the input was mounted writable")
	assert.Equal(t, content, string(h.Runtime.OneShotMounts["/morzer/inputs/snapshot"]),
		"the task did not see the bytes it was given")

	assert.Equal(t, fmt.Sprintf("%d sha256:%s", len(content), digest), res.Record.Flags["input.snapshot"])
	for _, v := range res.Record.Flags {
		assert.NotContains(t, v, path, "the journal names the operator's path")
	}
	assert.Equal(t, "stage-inputs", res.Record.Steps[0].ID)
	assert.Empty(t, stagingLeft(t, h), "the copy outlived the operation")

	// And the statement lists it, as v3.
	raw, err := os.ReadFile(filepath.Join(h.Paths.AttestationsDir(), res.Record.ID+".json"))
	require.NoError(t, err)
	var stmt domain.Statement
	require.NoError(t, json.Unmarshal(raw, &stmt))
	assert.Equal(t, domain.PredicateType, stmt.PredicateType)
	assert.Equal(t, []domain.AttestedInput{{Name: "snapshot", Size: int64(len(content)), SHA256: digest}},
		stmt.Predicate.Inputs)
}

func TestAFailedTaskStillRemovesItsInputs(t *testing.T) {
	h := inputsHarness(t, restoreTask)
	h.Runtime.OneShotResults["app"] = ports.ExitResult{ExitCode: 1}

	res, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{
		Name: "restore", Inputs: map[string]string{"snapshot": writeInput(t, "x")},
	})
	require.Error(t, err)
	assert.NotEmpty(t, res.Record.Flags["input.snapshot"], "a failed task's input was not journalled")
	assert.Empty(t, stagingLeft(t, h), "a failed task left its input behind")
}

// Every refusal of the request is exit 2 before the lock: nothing journalled,
// nothing run, nothing copied.
func TestWhatATaskCannotBeGivenIsRefusedBeforeTheLock(t *testing.T) {
	h := inputsHarness(t, restoreTask, reindexTask)
	ctx := context.Background()
	fifo := filepath.Join(t.TempDir(), "pipe")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	large := writeInput(t, strings.Repeat("x", 2*1024))
	before := len(taskRecords(t, h))

	for label, tc := range map[string]struct {
		opts ops.TaskOptions
		want string
	}{
		"an undeclared input": {ops.TaskOptions{Name: "restore", Inputs: map[string]string{
			"snapshot": writeInput(t, "x"), "extra": writeInput(t, "y")}}, `declares no input "extra"`},
		"an input to a task with none": {ops.TaskOptions{Name: "reindex",
			Inputs: map[string]string{"snapshot": writeInput(t, "x")}}, "takes no inputs"},
		"a declared input not given": {ops.TaskOptions{Name: "restore"}, "needs --input snapshot=<path>"},
		"a directory": {ops.TaskOptions{Name: "restore",
			Inputs: map[string]string{"snapshot": t.TempDir()}}, "is a directory, not a file"},
		"a named pipe": {ops.TaskOptions{Name: "restore",
			Inputs: map[string]string{"snapshot": fifo}}, "is a named pipe, not a file"},
		"a missing file": {ops.TaskOptions{Name: "restore",
			Inputs: map[string]string{"snapshot": filepath.Join(t.TempDir(), "nope")}}, "cannot open"},
		"a file over max_size": {ops.TaskOptions{Name: "restore",
			Inputs: map[string]string{"snapshot": large}}, "takes at most 1KiB"},
	} {
		_, err := ops.RunTask(ctx, h.Deps, tc.opts)
		require.Error(t, err, label)
		assert.Equal(t, domain.ExitUsage, domain.ExitCode(err), "%s: %v", label, err)
		assert.Contains(t, err.Error(), tc.want, label)
	}
	assert.Len(t, taskRecords(t, h), before, "a refused request was journalled")
	assert.Empty(t, stagingLeft(t, h))
}

func TestAnInputThatDoesNotFitIsRefusedBeforeAnythingIsCopied(t *testing.T) {
	h := inputsHarness(t, restoreTask)
	h.Deps.FreeSpace = func(string) (int64, error) { return 1024, nil }
	before := len(taskRecords(t, h))

	_, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{
		Name: "restore", Inputs: map[string]string{"snapshot": writeInput(t, "x")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is free under")
	assert.Len(t, taskRecords(t, h), before)
	assert.Empty(t, stagingLeft(t, h))
}

// The size checked before the lock is a first answer: a file that grows past
// its bound while it waits for the lock is refused at the copy, and no partial
// copy is left.
func TestAFileThatGrowsPastItsBoundIsRefused(t *testing.T) {
	h := inputsHarness(t, restoreTask)
	path := writeInput(t, "small")
	h.Locker.BeforeAcquire = func() {
		require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", 2*1024)), 0o600))
	}

	_, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{
		Name: "restore", Inputs: map[string]string{"snapshot": path},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "grew past its max_size")
	assert.Empty(t, stagingLeft(t, h), "a partial copy was left behind")
}

func TestADryRunCopiesNothing(t *testing.T) {
	h := inputsHarness(t, restoreTask)
	before := len(h.Runtime.OneShots)

	_, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{
		Options: ops.Options{DryRun: true},
		Name:    "restore", Inputs: map[string]string{"snapshot": writeInput(t, "x")},
	})
	require.NoError(t, err)
	assert.Empty(t, stagingLeft(t, h))
	assert.Len(t, h.Runtime.OneShots, before)
}

// A task killed mid-run leaves its copy; doctor names it, and the next task
// sweeps it.
func TestALeftoverCopyIsReportedAndSwept(t *testing.T) {
	h := inputsHarness(t, restoreTask)
	left := filepath.Join(h.Paths.InputsDir(), "op_killed")
	require.NoError(t, os.MkdirAll(left, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(left, "snapshot"), []byte("old"), 0o444))
	require.NoError(t, os.Chmod(left, 0o555))

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)
	found := false
	for _, res := range report.Results {
		if res.ID == "tasks.staged-inputs" {
			found = true
			assert.Equal(t, events.CheckWarn, res.Status)
			assert.Contains(t, res.Message, left)
		}
	}
	assert.True(t, found, "doctor has no tasks.staged-inputs check")

	_, err = ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{
		Name: "restore", Inputs: map[string]string{"snapshot": writeInput(t, "x")},
	})
	require.NoError(t, err)
	assert.Empty(t, stagingLeft(t, h), "the leftover was not swept")
}

// The sweep is every task's, not only one that takes inputs: a killed task's
// copy must not wait for the next task that happens to declare one.
func TestATaskWithoutInputsSweepsALeftoverToo(t *testing.T) {
	h := inputsHarness(t, restoreTask, reindexTask)
	left := filepath.Join(h.Paths.InputsDir(), "op_killed")
	require.NoError(t, os.MkdirAll(left, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(left, "snapshot"), []byte("old"), 0o444))
	require.NoError(t, os.Chmod(left, 0o555))

	_, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{Name: "reindex"})
	require.NoError(t, err)
	assert.Empty(t, stagingLeft(t, h), "a task without inputs left a leftover in place")
}

// Doctor takes no lock, so the operation holding it may be copying its inputs
// right now. Its directory is not a leftover, and doctor must not tell an
// operator to remove it.
func TestDoctorDoesNotNameTheRunningTasksInputs(t *testing.T) {
	h := inputsHarness(t, restoreTask)
	for _, op := range []string{"op_live", "op_dead"} {
		require.NoError(t, os.MkdirAll(filepath.Join(h.Paths.InputsDir(), op), 0o700))
	}
	release, err := h.Locker.Acquire(context.Background(), "deployment",
		ports.LockOptions{Owner: ports.LockOwner{OperationID: "op_live", Type: string(domain.OpTypeTask)}})
	require.NoError(t, err)
	defer func() { _ = release() }()

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)
	for _, res := range report.Results {
		if res.ID == "tasks.staged-inputs" {
			assert.Contains(t, res.Message, "op_dead")
			assert.NotContains(t, res.Message, "op_live", "doctor named the running task's inputs")
		}
	}
}
