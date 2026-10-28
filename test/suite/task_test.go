package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/logging"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// A release declares tasks an operator runs by name, and `task run` runs one as
// a one-shot container of a release service, as an operation (RFC 0035).

const seedTask = "  seed:\n" +
	"    description: Fill an empty database with demo data\n" +
	"    service: app\n" +
	"    command: [seed, --fast]\n" +
	"    args: true\n" +
	"    modes: [dev]\n" +
	"    timeout: 2m\n"

const reindexTask = "  reindex:\n    service: app\n    command: [reindex]\n"

// taskHarness is an applied installation whose release declares the given
// tasks. The manifest is patched before install, so the digest the
// installation records is the patched bundle's.
func taskHarness(t *testing.T, mode domain.Mode, tasks ...string) *harness {
	t.Helper()
	h := newHarness(t)

	path := filepath.Join(h.Release.Root, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	patched := strings.Replace(string(raw), `min_manager_version: "0.0.0"`,
		`min_manager_version: "`+domain.TasksMinManagerVersion+`"`, 1)
	require.NotEqual(t, string(raw), patched, "the fixture no longer declares the manager version this rewrites")
	patched += "\ntasks:\n" + strings.Join(tasks, "")
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))
	h.Release, err = release.Load(h.Release.Root)
	require.NoError(t, err)

	inst := h.install()
	if mode != "" {
		// A mode is fixed at creation, so the record is created again.
		require.NoError(t, os.Remove(h.Paths.InstallationState()))
		inst.Mode = mode
		require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
	}
	h.setHookEnv()
	applyBaseline(t, h)
	return h
}

// taskRecords is every journalled task, newest first.
func taskRecords(t *testing.T, h *harness) []domain.OperationRecord {
	t.Helper()
	records, err := h.Deps.State.Operations(context.Background(), ops.OperationFilterAll())
	require.NoError(t, err)
	var out []domain.OperationRecord
	for _, rec := range records {
		if rec.Type == domain.OpTypeTask {
			out = append(out, rec)
		}
	}
	return out
}

func TestATaskRunsItsCommandInAOneShotOfItsService(t *testing.T) {
	h := taskHarness(t, domain.ModeDev, seedTask)
	before := len(h.Runtime.OneShots)
	h.Runtime.OneShotResults["app"] = ports.ExitResult{Stdout: "seeded 40 rows"}

	result, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{
		Name: "seed", Args: []string{"--size", "s"},
	})
	require.NoError(t, err)
	assert.Equal(t, domain.OpTypeTask, result.Record.Type)
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)

	require.Len(t, h.Runtime.OneShots, before+1, "the task never ran")
	run := h.Runtime.OneShots[before]
	assert.Equal(t, []string{"seed", "--fast", "--size", "s"}, run.Argv,
		"the operator's arguments follow the task's own command")
	assert.Equal(t, 2*time.Minute, run.Timeout)
	assert.True(t, run.Remove, "a task's container outlived it")
	assert.Equal(t, "task", run.Env["DEMO_PHASE"])
	assert.Equal(t, "task", run.Env["DEMO_OPERATION_TYPE"])
	assert.NotContains(t, run.Env, "DEMO_DATA_DIR", "a container was handed a host path")

	assert.Equal(t, ops.TaskRun{Task: "seed", Service: "app", Stdout: "seeded 40 rows"}, result.Data,
		"the output never reached the caller")

	recs := taskRecords(t, h)
	require.Len(t, recs, 1)
	assert.Equal(t, "seed", recs[0].Flags["task"])
	assert.Equal(t, "app", recs[0].Flags["service"])
	assert.Equal(t, "--size s", recs[0].Flags["args"])
	assert.NotContains(t, journalText(t, &recs[0]), "seeded 40 rows",
		"the journal keeps a copy of what the task printed")
}

// Rendered as apply renders, so a task after a reboot finds the files its
// service mounts: secrets and generated configuration live on tmpfs.
func TestATaskRendersWhatItsServiceMounts(t *testing.T) {
	h := taskHarness(t, "", reindexTask)
	require.NoError(t, os.RemoveAll(h.Paths.SecretsRenderDir()), "the reboot this simulates")

	_, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{Name: "reindex"})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(h.Paths.SecretsRenderDir(), "db_password"))
}

func TestATaskKeepsTheOperatorsPasswordOutOfTheJournal(t *testing.T) {
	const secret = "a-real-database-password"
	h := taskHarness(t, domain.ModeDev, seedTask)
	// Unarmed, as a fresh `morzer task run` starts: the harness's apply
	// armed the shared one, and the test would pass on its registration.
	h.Deps.Redactor = logging.NewRedactor()

	_, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{
		Name: "seed", Args: []string{"--password", secret},
	})
	require.NoError(t, err)

	recs := taskRecords(t, h)
	require.Len(t, recs, 1)
	assert.NotContains(t, journalText(t, &recs[0]), secret)
	assert.Contains(t, recs[0].Flags["args"], domain.Redacted)
}

// Secret state that will not decrypt teaches the redactor nothing, so the
// arguments are left out rather than written down unscrubbed, and the record
// says why. The task then fails at load-secrets, as apply would.
func TestATaskOmitsTheArgumentsItCouldNotScrub(t *testing.T) {
	const secret = "a-real-database-password"
	h := taskHarness(t, domain.ModeDev, seedTask)
	h.Deps.Redactor = logging.NewRedactor()
	h.Secrets.Fail["Load"] = domain.SecretsError(nil, "no age identity on this machine")
	before := len(h.Runtime.OneShots)

	_, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{
		Name: "seed", Args: []string{"--password", secret},
	})
	require.Error(t, err)
	assert.Len(t, h.Runtime.OneShots, before, "a task ran without the secrets its service mounts")

	recs := taskRecords(t, h)
	require.Len(t, recs, 1)
	assert.NotContains(t, journalText(t, &recs[0]), secret)
	assert.Empty(t, recs[0].Flags["args"])
	assert.Contains(t, recs[0].Flags["args_omitted"], "could not be")
}

func TestAFailedTaskFailsTheOperationAndKeepsItsOutputOutOfTheJournal(t *testing.T) {
	h := taskHarness(t, "", reindexTask)
	h.Runtime.OneShotResults["app"] = ports.ExitResult{
		ExitCode: 3, Stdout: "indexed 12 of 40", Output: "indexed 12 of 40\nindex locked by another writer",
	}

	result, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{Name: "reindex"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 3")

	// Not quoted, as a migration's is: the error is journalled, and the
	// operator has the whole output anyway (RFC 0035 decision 20).
	recs := taskRecords(t, h)
	require.Len(t, recs, 1)
	assert.NotContains(t, journalText(t, &recs[0]), "index locked by another writer",
		"the task's output reached the journal through its error")

	// Failed, not compensated and not flagged: nothing the manager did can
	// be undone and nothing a person must clear (RFC 0035 decision 9).
	assert.Equal(t, domain.StatusFailed, result.Record.Status)
	assert.Equal(t, ops.TaskRun{Task: "reindex", Service: "app", ExitCode: 3, Stdout: "indexed 12 of 40"},
		result.Data, "a failed task's output and exit code were lost")
}

// "Nothing to do" is the migration ABI's, and nothing reads it from a task.
func TestExitTwoFailsATask(t *testing.T) {
	h := taskHarness(t, "", reindexTask)
	h.Runtime.OneShotResults["app"] = ports.ExitResult{ExitCode: 2}

	_, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{Name: "reindex"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 2")
}

// Every refusal comes before the lock, so none of them runs a container or
// writes a record: a refused request is not something that happened.
func TestATaskIsRefusedBeforeAnythingRuns(t *testing.T) {
	cases := map[string]struct {
		opts ops.TaskOptions
		want string
		code int
	}{
		"an unknown task": {
			ops.TaskOptions{Name: "sed"}, `declares no task named "sed"`, domain.ExitUsage,
		},
		"arguments to a task without args": {
			ops.TaskOptions{Name: "reindex", Args: []string{"--all"}}, "takes no arguments", domain.ExitUsage,
		},
		"a dev-only task on production": {
			ops.TaskOptions{Name: "seed"}, "may run only on dev machines, and this one is production", domain.ExitPreflight,
		},
		"a forced dev-only task on production": {
			ops.TaskOptions{Name: "seed", Options: ops.Options{Force: true}}, "may run only on dev", domain.ExitPreflight,
		},
		"a resume": {
			ops.TaskOptions{Name: "reindex", Options: ops.Options{Resume: true}}, "cannot continue a task", domain.ExitUsage,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := taskHarness(t, "", seedTask, reindexTask)
			before := len(h.Runtime.OneShots)

			_, err := ops.RunTask(context.Background(), h.Deps, tc.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Equal(t, tc.code, domain.ExitCode(err))
			assert.Len(t, h.Runtime.OneShots, before, "a refused task ran")
			assert.Empty(t, taskRecords(t, h), "a refused task was journalled")
		})
	}
}

func TestATaskOnAServiceTheReleaseDoesNotDefineIsRefused(t *testing.T) {
	h := taskHarness(t, "", "  audit:\n    service: auditor\n    command: [audit]\n")
	before := len(h.Runtime.OneShots)

	_, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{Name: "audit"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `runs the service "auditor", which the release's runtime configuration does not define`)
	assert.Contains(t, domain.AsError(err).Hint, "app, db", "the refusal did not say which services there are")
	assert.Len(t, h.Runtime.OneShots, before,
		"the runtime's own failure for a missing service would have passed for the task's exit code")
}

// The validated list leaves out a service behind a profile that is not active,
// which is where a one-off tool usually lives and which the one-shot starts
// when named. Refusing it would refuse the common case.
func TestATaskOnAServiceBehindAProfileRuns(t *testing.T) {
	h := taskHarness(t, "", "  audit:\n    service: tools\n    command: [audit]\n")
	h.Runtime.Images["tools"] = "registry.example/demo/tools@sha256:" + strings.Repeat("3", 64)
	before := len(h.Runtime.OneShots)

	_, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{Name: "audit"})
	require.NoError(t, err)
	assert.Len(t, h.Runtime.OneShots, before+1)
}

func TestAPlannedTaskRunsNothing(t *testing.T) {
	h := taskHarness(t, "", reindexTask)
	before := len(h.Runtime.OneShots)

	result, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{
		Name: "reindex", Options: ops.Options{DryRun: true},
	})
	require.NoError(t, err)
	assert.Equal(t, "would run task reindex in app", result.Summary)
	assert.Len(t, h.Runtime.OneShots, before, "a plan ran the task")
	assert.Empty(t, taskRecords(t, h), "a plan was journalled")
}

func TestTaskListSaysWhichTasksThisMachineMayRun(t *testing.T) {
	production := taskHarness(t, "", seedTask, reindexTask)
	tasks, err := ops.ListTasks(context.Background(), production.Deps)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "reindex", tasks[0].Name, "the listing is not in a stable order")
	assert.True(t, tasks[0].Allowed, "a task listing no modes runs everywhere")
	assert.False(t, tasks[1].Allowed, "a dev-only task was offered on production")
	assert.True(t, tasks[1].Args)
	assert.Equal(t, []string{"dev"}, tasks[1].Modes)
	assert.Equal(t, "Fill an empty database with demo data", tasks[1].Description)

	sandbox := taskHarness(t, domain.ModeDev, seedTask)
	tasks, err = ops.ListTasks(context.Background(), sandbox.Deps)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.True(t, tasks[0].Allowed)
}

// A plan prints the step it would run, and the operator's arguments are as
// sensitive there as in the journal: unscrubbed, they are left out of it too.
func TestAPlanShowsNoArgumentItCouldNotScrub(t *testing.T) {
	const secret = "a-real-database-password"
	h := taskHarness(t, domain.ModeDev, seedTask)
	h.Deps.Redactor = logging.NewRedactor()
	h.Secrets.Fail["Load"] = domain.SecretsError(nil, "no age identity on this machine")

	_, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{
		Name: "seed", Args: []string{"--password", secret}, Options: ops.Options{DryRun: true},
	})
	require.NoError(t, err)

	plans := h.Events.OfKind(events.KindPlan)
	require.NotEmpty(t, plans)
	for _, step := range plans[len(plans)-1].Plan {
		assert.NotContains(t, step.Description, secret, "the plan printed an argument it could not scrub")
	}
}
