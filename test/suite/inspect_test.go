package suite

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// The four commands that reach into a running deployment, against the fake
// runtime.
//
// What the fakes can prove here is the half that is this project's: which
// options reach the port, what the redactor does to a stream, what the journal
// records about an exec, and which refusals fire. What they cannot prove --
// that `--tail` and `--since` are flags Docker accepts -- is the container
// lane's, and the runtime contract suite is where it lives.

// running brings the deployment up so the four inspection commands have
// something to inspect.
func (h *harness) running(t *testing.T) {
	t.Helper()
	h.install()
	h.setHookEnv()
	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
}

func TestLogsPassesWhatTheOperatorAskedForToTheRuntime(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	since := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	stream, err := ops.StreamLogs(context.Background(), h.Deps, ops.LogsOptions{
		Services: []string{"app"},
		Tail:     5,
		Since:    since,
		Follow:   true,
	})
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	// Asserted on the options the port received, not on the output.
	// Asserting on output passes just as well for a command that ignored
	// every flag and streamed everything, which is the failure this is
	// written to catch.
	require.Len(t, h.Runtime.LogRequests, 1)
	got := h.Runtime.LogRequests[0]
	assert.Equal(t, []string{"app"}, got.Services)
	assert.Equal(t, 5, got.Tail)
	assert.True(t, got.Since.Equal(since), "the --since instant did not reach the runtime")
	assert.True(t, got.Follow)
	assert.False(t, got.Timestamps,
		"the human stream must not ask for timestamps: the runtime's own layout "+
			"is what an operator reads")
}

func TestLogsScrubsThisInstallationsSecretsFromTheStream(t *testing.T) {
	const secret = "a-real-database-password"

	h := newHarness(t)
	h.running(t)
	h.Runtime.LogOutput = "demo-app-1  | connecting with " + secret + "\n"

	stream, err := ops.StreamLogs(context.Background(), h.Deps, ops.LogsOptions{Redact: true})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	out, err := io.ReadAll(stream)
	require.NoError(t, err)

	assert.True(t, stream.RedactionArmed,
		"the secret values were not loaded, so this test would pass against a "+
			"redactor that knows nothing")
	assert.NotContains(t, string(out), secret)
	// The line is still there. Without this, a filter that dropped the
	// whole line would pass the assertion above while losing the log.
	assert.Contains(t, string(out), "connecting with "+domain.Redacted)
}

func TestLogsWithoutRedactionStreamTheBytesTheContainerWrote(t *testing.T) {
	const secret = "a-real-database-password"

	h := newHarness(t)
	h.running(t)
	h.Runtime.LogOutput = "demo-app-1  | connecting with " + secret + "\n"

	stream, err := ops.StreamLogs(context.Background(), h.Deps, ops.LogsOptions{Redact: false})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	out, err := io.ReadAll(stream)
	require.NoError(t, err)

	// The other half of the pair. A redaction test that only tests the on
	// case cannot tell redaction from a stream that dropped the line, and a
	// --no-redact that quietly kept redacting would be a flag that lies.
	assert.Contains(t, string(out), secret)
	assert.False(t, stream.RedactionArmed)
}

func TestLogsRedactsASecretSplitAcrossTwoReads(t *testing.T) {
	const secret = "a-real-database-password"

	h := newHarness(t)
	h.running(t)
	// The boundary falls inside the value, which is where a filter that
	// scrubbed whatever each read happened to bring in leaks: neither half
	// matches, and both are written out.
	h.Runtime.LogWrites = []string{
		"demo-app-1  | connecting with a-real-data",
		"base-password now\n",
	}

	stream, err := ops.StreamLogs(context.Background(), h.Deps, ops.LogsOptions{Redact: true})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	out, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.NotContains(t, string(out), secret,
		"a secret straddling a read boundary reached the operator's terminal")
	assert.Contains(t, string(out), domain.Redacted)
}

func TestLogsAttributesEachLineToItsService(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	h.Runtime.LogOutput = "" +
		"demo-app-1  | 2026-08-11T09:12:33Z started on :8080\n" +
		"demo-db-1  | 2026-08-11T09:12:34.5Z ready to accept connections\n" +
		"demo-app-1 exited with code 0\n"

	stream, err := ops.StreamLogs(context.Background(), h.Deps, ops.LogsOptions{Structured: true})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	var lines []ports.LogLine
	require.NoError(t, stream.Lines(func(l ports.LogLine) error {
		lines = append(lines, l)
		return nil
	}))
	require.Len(t, lines, 3)

	assert.Equal(t, "demo-app-1", lines[0].Container)
	assert.Equal(t, "app", lines[0].Service, "the container was not attributed to its service")
	assert.Equal(t, "started on :8080", lines[0].Text)
	assert.True(t, lines[0].At.Equal(time.Date(2026, 8, 11, 9, 12, 33, 0, time.UTC)),
		"the container's own instant is what a record carries, not the moment "+
			"the manager read it")

	assert.Equal(t, "db", lines[1].Service)

	// The runtime's own narration has no frame, and it is the line that
	// explains why the rest stopped. Dropping it would hide exactly that.
	assert.Empty(t, lines[2].Container)
	assert.Equal(t, "demo-app-1 exited with code 0", lines[2].Text)
}

func TestStructuredLogsAskTheRuntimeForTimestamps(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	stream, err := ops.StreamLogs(context.Background(), h.Deps, ops.LogsOptions{Structured: true})
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	require.Len(t, h.Runtime.LogRequests, 1)
	assert.True(t, h.Runtime.LogRequests[0].Timestamps,
		"a record carrying a `ts` field has to get that instant from the container")
}

func TestPsReportsWhatIsRunning(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	services, err := ops.ListServices(context.Background(), h.Deps)
	require.NoError(t, err)
	require.NotEmpty(t, services)
	for _, s := range services {
		assert.NotEmpty(t, s.Name)
		assert.NotEmpty(t, s.Container,
			"%s reports no container, so nothing can tell two replicas apart", s.Name)
	}
}

func TestStatsRefusesByNameWhenTheRuntimeCannotReport(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	h.Runtime.Fail["Stats"] = domain.RuntimeError(domain.ErrUnsupported,
		"this runtime does not report resource statistics")

	_, err := ops.SampleStats(context.Background(), h.Deps)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrUnsupported),
		"an unsupported capability must be refused by name: an empty table is "+
			"indistinguishable from an idle deployment, which is the wrong thing "+
			"to show somebody diagnosing load")
}

func TestStatsSamplesOnceAndReturnsWhatTheRuntimeReported(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	rx, tx := int64(1024), int64(2048)
	h.Runtime.StatsResult = []ports.ServiceStats{{
		Service: "app", Container: "demo-app-1", Replica: 1,
		CPUPercent: 12.5, MemoryBytes: 64 << 20, MemoryLimit: 512 << 20,
		NetRxBytes: &rx, NetTxBytes: &tx,
	}}

	stats, err := ops.SampleStats(context.Background(), h.Deps)
	require.NoError(t, err)
	require.Len(t, stats, 1)
	assert.Equal(t, 1, h.Runtime.StatsCalls, "one invocation must be one sample")
	assert.Nil(t, stats[0].BlockRead,
		"a counter the host does not account for must stay unreported rather "+
			"than becoming a zero")
}

func TestExecPropagatesTheCommandsExitCode(t *testing.T) {
	for _, code := range []int{0, 1, 127} {
		h := newHarness(t)
		h.running(t)
		h.Runtime.ExecResults["app"] = ports.ExitResult{ExitCode: code, Stdout: "out"}

		res, err := ops.ExecInService(context.Background(), h.Deps, ops.ExecOptions{
			Service: "app", Argv: []string{"true"},
		})
		require.NoError(t, err, "a command that ran and exited is a result, not a failure")
		assert.Equal(t, code, res.ExitCode,
			"a manager that flattened the exit code would make `morzer exec` "+
				"unusable in a script")
	}
}

func TestExecIsJournalledWithItsArgvAndWithoutItsOutput(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	h.Runtime.ExecResults["app"] = ports.ExitResult{
		ExitCode: 0,
		Stdout:   "row-one\nrow-two\n",
	}

	_, err := ops.ExecInService(context.Background(), h.Deps, ops.ExecOptions{
		Service: "app", Argv: []string{"psql", "-c", "select 1"},
	})
	require.NoError(t, err)

	records, err := h.Deps.State.Operations(context.Background(), ops.OperationFilterAll())
	require.NoError(t, err)

	var record *domain.OperationRecord
	for i := range records {
		if records[i].Type == domain.OpTypeExec {
			record = &records[i]
		}
	}
	require.NotNil(t, record, "nothing recorded that a human was inside the deployment")

	assert.Equal(t, "psql -c select 1", record.Flags["argv"])
	assert.Equal(t, "app", record.Flags["service"])
	assert.Equal(t, "0", record.Flags["exit_code"])

	// The output is arbitrary vendor data plus whatever the operator's
	// command printed, and a journal holding it would be a second copy of
	// the product's data in a file nobody thinks of as one.
	assert.NotContains(t, journalText(t, record), "row-one")
}

func TestExecKeepsTheOperatorsPasswordOutOfTheJournal(t *testing.T) {
	const secret = "a-real-database-password"

	h := newHarness(t)
	h.running(t)

	_, err := ops.ExecInService(context.Background(), h.Deps, ops.ExecOptions{
		Service: "app",
		Argv:    []string{"psql", "postgresql://demo:" + secret + "@localhost/demo"},
	})
	require.NoError(t, err)

	records, err := h.Deps.State.Operations(context.Background(), ops.OperationFilterAll())
	require.NoError(t, err)

	for _, rec := range records {
		if rec.Type != domain.OpTypeExec {
			continue
		}
		assert.NotContains(t, rec.Flags["argv"], secret,
			"the journal is a file this manager writes and keeps, and a password "+
				"in an argv is the ordinary case")
		assert.Contains(t, rec.Flags["argv"], domain.Redacted)
	}
}

func TestExecRefusesAServiceThatIsNotRunning(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	h.Runtime.Services["app"] = ports.ServiceState{
		Name: "app", Container: "demo-app-1", State: "exited", ExitCode: 137,
	}

	_, err := ops.ExecInService(context.Background(), h.Deps, ops.ExecOptions{
		Service: "app", Argv: []string{"true"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited",
		"the refusal must name the state: `exited` and `restarting` send an "+
			"operator to different places")

	// And nothing was journalled: a refused command is not something a
	// human did inside the deployment.
	records, err := h.Deps.State.Operations(context.Background(), ops.OperationFilterAll())
	require.NoError(t, err)
	for _, rec := range records {
		assert.NotEqual(t, domain.OpTypeExec, rec.Type,
			"a command that never ran was recorded as one that did")
	}
}

func TestExecNamesTheServicesWhenAskedForOneThatDoesNotExist(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	_, err := ops.ExecInService(context.Background(), h.Deps, ops.ExecOptions{
		Service: "worker", Argv: []string{"true"},
	})
	require.Error(t, err)
	assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
	assert.Contains(t, domain.AsError(err).Hint, "app")
}

func TestTheFourInspectionCommandsTakeNoLock(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	ctx := context.Background()

	// Held by something else, exactly as during an update. All four have to
	// answer anyway: they are what an operator runs *while* something is
	// happening, which is the case a lock would break.
	release, err := h.Locker.Acquire(ctx, "deployment", ports.LockOptions{
		Owner: ports.LockOwner{PID: 4321, Type: "update"},
	})
	require.NoError(t, err)
	defer func() { _ = release() }()

	stream, err := ops.StreamLogs(ctx, h.Deps, ops.LogsOptions{})
	require.NoError(t, err, "`logs` queued behind the lock")
	require.NoError(t, stream.Close())

	_, err = ops.ListServices(ctx, h.Deps)
	require.NoError(t, err, "`ps` queued behind the lock")

	_, err = ops.SampleStats(ctx, h.Deps)
	require.NoError(t, err, "`stats` queued behind the lock")

	_, err = ops.ExecInService(ctx, h.Deps, ops.ExecOptions{Service: "app", Argv: []string{"true"}})
	require.NoError(t, err, "`exec` queued behind the lock")
}

func TestInspectionRefusesBeforeAReleaseIsInstalled(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	require.NoError(t, h.Deps.State.SaveInstallation(ctx, domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_fresh", Product: "demo",
		CreatedAt: domain.NewTime(h.Deps.Now()), Policy: domain.DefaultPolicy(),
	}))

	// Right after `init`. There is no project to look into, and saying so
	// beats an empty table that reads as a deployment with nothing running.
	_, err := ops.ListServices(ctx, h.Deps)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrReleaseNotFound))
	assert.Contains(t, domain.AsError(err).Message, "nothing running")
}

// journalText renders a record the way the journal stores it, so an assertion
// about what is *not* in it covers every field rather than the one the test
// remembered to look at.
func journalText(t *testing.T, rec *domain.OperationRecord) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(string(rec.Type) + " " + string(rec.Status))
	for k, v := range rec.Flags {
		b.WriteString(" " + k + "=" + v)
	}
	for _, step := range rec.Steps {
		b.WriteString(" " + step.ID + " " + step.Message + " " + step.Error)
	}
	return b.String()
}
