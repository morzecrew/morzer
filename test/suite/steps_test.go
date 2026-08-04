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

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// The step bodies each operation only reaches when something specific has gone
// wrong. Three ports the harness never failed before -- the supervisor, the
// renderer, and the state store on a read-only filesystem -- plus the fixtures
// nothing had built: an unfinished operation for `--resume`, a host with a
// supervisor, and a machine that has already been through a recovery.

func TestConfigurationRenderingFailuresStopTheApply(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	h.Renderer.Err = domain.ValidationError(errInjected,
		"template references an undefined value")
	h.Deps.Renderer = h.Renderer

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.Error(t, err, "a configuration file that could not be rendered was "+
		"reported as written, so the product starts against whatever was there before")
	assert.Contains(t, err.Error(), "undefined value")
}

// TestConfigurationIsWrittenWhereTheManifestSaysAndNowhereElse.
func TestConfigurationRenderingWritesEveryDeclaredFile(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	target := filepath.Join(h.Root, "etc", "demo", "application.yaml")
	info, err := os.Stat(target)
	require.NoError(t, err, "the declared configuration file was not written")

	// The manifest declares 0640: a configuration file naming secret paths
	// should not be world-readable.
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}

// TestAStateStoreThatCannotBeWrittenFailsTheOperation is the read-only /var
// case, provoked for real rather than through a fake.
func TestAStateStoreThatCannotBeWrittenFailsTheOperation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	h := newHarness(t)
	h.install()
	h.setHookEnv()

	etc := h.Paths.EtcDir
	require.NoError(t, os.Chmod(etc, 0o500))
	t.Cleanup(func() { _ = os.Chmod(etc, 0o700) })

	_, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"log_level": "debug"},
	})
	require.Error(t, err, "a parameter change was reported recorded on a filesystem "+
		"that could not be written")
}

// TestSupervisorFailuresDuringInit. Installing units is the last thing `init`
// does, and a failure there must not leave an installation that believes it is
// managed by systemd when it is not.
// realIdentity writes the machine key `init` verifies on disk.
//
// The fake secret store keeps its values in memory, so nothing creates the age
// identity that `init` then checks is 0400. Writing it with the real generator
// keeps the check meaningful rather than stubbing it out.
func realIdentity(t *testing.T, h *harness) {
	t.Helper()
	_, err := sopsage.GenerateIdentity(h.Paths.AgeIdentityFile())
	require.NoError(t, err)
}

func TestSupervisorFailuresDuringInit(t *testing.T) {
	cases := map[string]string{
		"the units cannot be written": "InstallUnits",
		"the daemon will not reload":  "InstallUnits",
	}

	for name, method := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.setHookEnv()
			realIdentity(t, h)
			h.Supervisor.Present = true
			h.Supervisor.Fail[method] = domain.Internal(errInjected,
				"systemctl daemon-reload failed")
			h.Deps.Supervisor = h.Supervisor

			_, err := ops.Init(context.Background(), h.Deps, ops.InitOptions{
				Product: "demo", NoRecoveryKey: true, InstallUnits: true,
			})
			require.Error(t, err, "init reported success after the unit step failed")
		})
	}
}

// TestInitInstallsUnitsWhenASupervisorIsPresent covers the step nothing
// reached before, because no test had a host with a supervisor.
func TestInitInstallsUnitsWhenASupervisorIsPresent(t *testing.T) {
	h := newHarness(t)
	h.setHookEnv()
	realIdentity(t, h)
	h.Supervisor.Present = true
	h.Deps.Supervisor = h.Supervisor

	_, err := ops.Init(context.Background(), h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, InstallUnits: true,
		BackupSchedule: "daily",
	})
	require.NoError(t, err)

	assert.NotEmpty(t, h.Supervisor.Installed,
		"a host with systemd got no units, so nothing starts it at boot")

	// The schedule was asked for, so a timer has to exist alongside the
	// service: a backup schedule with no timer is a promise nothing keeps.
	var sawTimer bool
	for name := range h.Supervisor.Installed {
		if strings.Contains(name, "timer") {
			sawTimer = true
		}
	}
	assert.True(t, sawTimer, "a backup schedule was configured but no timer was "+
		"installed: %v", keysOf(h.Supervisor.Installed))
}

// TestInitWithoutASupervisorSaysSoRatherThanFailing. A container host has no
// systemd, and refusing to install there would be the manager overreaching.
func TestInitWithoutASupervisorSaysSoRatherThanFailing(t *testing.T) {
	h := newHarness(t)
	h.setHookEnv()
	realIdentity(t, h)
	h.Supervisor.Present = false
	h.Deps.Supervisor = h.Supervisor

	_, err := ops.Init(context.Background(), h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, InstallUnits: true,
	})
	require.NoError(t, err, "a host with no supervisor could not be initialised")
	assert.Empty(t, h.Supervisor.Installed)
}

// TestDoctorReportsUnitsAndBackups covers the two checks that need a
// supervisor and a backup history to say anything at all.
func TestDoctorReportsUnitsAndBackups(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	h.Supervisor.Present = true
	h.Deps.Supervisor = h.Supervisor

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	ids := map[string]events.CheckStatus{}
	for _, r := range report.Results {
		ids[r.ID] = r.Status
	}

	// A host with systemd and no units installed is a host that will not
	// come back after a reboot, and doctor is where that is noticed.
	assert.Contains(t, ids, "system.units")
	assert.Contains(t, ids, "backup.freshness")

	// A machine that has never taken a backup is a finding, not a failure
	// of doctor.
	assert.NotEqual(t, events.CheckFail, ids["backup.freshness"],
		"never having taken a backup made doctor itself fail")
}

// TestResumeStartsFromTheStepThatDidNotFinish is the whole point of the
// journal: after a crash, `--resume` picks up rather than starting over.
func TestResumeStartsFromTheStepThatDidNotFinish(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	// A first apply that fails partway, leaving a record.
	h.Runtime.Fail["Up"] = domain.RuntimeError(errInjected, "the daemon refused")
	first, _ := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NotEqual(t, domain.StatusSucceeded, first.Record.Status)

	// The journal now holds something an operator could resume.
	unfinished, err := h.Deps.State.UnfinishedOperations(context.Background())
	require.NoError(t, err)
	if len(unfinished) == 0 {
		t.Skip("the failed apply compensated cleanly, so there is nothing to resume")
	}

	// With the fault cleared, a resume completes rather than refusing.
	delete(h.Runtime.Fail, "Up")
	second, err := ops.Apply(context.Background(), h.Deps, ops.Options{Resume: true})
	require.NoError(t, err, "an operation that could be resumed was refused")
	assert.Equal(t, domain.StatusSucceeded, second.Record.Status)
}

// TestAnUnfinishedOperationDoesNotBlockANewOne records a gap, not a design.
//
// `preflight.NoUnfinishedOperation` exists, is documented as "refuses to start
// while a previous operation is still flagged", and explains why -- "proceeding
// over an unfinished operation would layer new changes on a state nobody has
// confirmed, which is exactly how a recoverable failure becomes an
// unrecoverable one". **Nothing calls it.** No operation's preflight includes
// it, so an `apply` runs straight over an operation that asked for a human.
//
// Not fixed here: wiring a new refusal into every mutating operation is a
// behaviour change and belongs in its own pull request. This test fails the
// day it is wired, which is the point.
func TestAnUnfinishedOperationDoesNotBlockANewOne(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	require.NoError(t, h.Deps.State.AppendOperation(context.Background(),
		domain.OperationRecord{
			ID: "op-stuck", Type: domain.OpTypeUpdate,
			Status: domain.StatusManualIntervention, StartedAt: domain.NewTime(time.Now()),
		}))

	// It is genuinely recorded as needing attention...
	unfinished, err := h.Deps.State.UnfinishedOperations(context.Background())
	require.NoError(t, err)
	require.Len(t, unfinished, 1)
	require.True(t, unfinished[0].Status.NeedsAttention())

	// ...and `apply` proceeds anyway.
	_, err = ops.Apply(context.Background(), h.Deps, ops.Options{})
	if err != nil {
		t.Fatalf("the check is now wired in, which is an improvement: delete this "+
			"test and assert the refusal instead. Got: %v", err)
	}
}

// TestClearingTheInterventionFlagMarksItResolved. The flag is what `doctor` and
// `status` keep surfacing, so clearing it has to actually clear it -- even
// though nothing currently blocks on it.
func TestClearingTheInterventionFlagMarksItResolved(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	require.NoError(t, h.Deps.State.AppendOperation(context.Background(),
		domain.OperationRecord{
			ID: "op-stuck", Type: domain.OpTypeUpdate,
			Status: domain.StatusManualIntervention, StartedAt: domain.NewTime(time.Now()),
		}))

	_, err := ops.ClearIntervention(context.Background(), h.Deps, "op-stuck")
	require.NoError(t, err)

	unfinished, err := h.Deps.State.UnfinishedOperations(context.Background())
	require.NoError(t, err)
	for _, rec := range unfinished {
		if rec.ID == "op-stuck" && rec.Status.NeedsAttention() {
			t.Error("the operation still asks for a human after being cleared, so " +
				"doctor keeps reporting a problem the operator has already fixed")
		}
	}
}

func TestClearingWhenNothingNeedsAttention(t *testing.T) {
	h := newHarness(t)
	h.install()

	_, err := ops.ClearIntervention(context.Background(), h.Deps, "op-nobody-recorded")
	require.Error(t, err, "an operation id nobody recorded was cleared")
	assert.Contains(t, err.Error(), "op-nobody-recorded")
}

// TestStatusReportsEveryPartOfTheMachine, including the parts that are absent.
func TestStatusReportsEveryPartOfTheMachine(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	report, err := ops.GetStatus(context.Background(), h.Deps)
	require.NoError(t, err)

	assert.Equal(t, "demo", report.Product)
	assert.NotEmpty(t, report.InstallationID)
	// Pointers, so "absent" is nil rather than a zero value: a first
	// install has no previous release, and null is what says so in the
	// JSON envelope.
	require.NotNil(t, report.CurrentRelease, "status reports no current release "+
		"on a machine that has one installed")
	assert.Nil(t, report.PreviousRelease,
		"a first install reported a previous release to roll back to")

	// After an apply the services are known.
	_, err = ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	after, err := ops.GetStatus(context.Background(), h.Deps)
	require.NoError(t, err)
	assert.NotEmpty(t, after.Services, "status reports no services on a machine "+
		"that has just been applied")
}

// TestStatusSurvivesARuntimeThatCannotAnswer. `status` is what an operator runs
// when Docker is broken.
func TestStatusSurvivesARuntimeThatCannotAnswer(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Runtime.Fail["Status"] = domain.RuntimeError(errInjected, "cannot connect to the daemon")

	report, err := ops.GetStatus(context.Background(), h.Deps)
	require.NoError(t, err, "status failed because the runtime did, which is the "+
		"one situation it exists for")
	assert.Equal(t, "demo", report.Product)
}

// TestExportNeedsAStoreThatCanExport. The fake secret store does not implement
// the recovery capability, and the refusal for that is a usage error naming the
// provider rather than a nil dereference deep inside the operation.
//
// The capability is optional on purpose: a provider that cannot hand out its
// encrypted state is a legitimate provider, it just cannot be recovered from.
// The real store's refusals are covered in test/suite/recovery_test.go and in
// the sopsage key tests.
func TestExportNeedsAStoreThatCanExport(t *testing.T) {
	h := newHarness(t)
	h.install()

	_, err := ops.Export(context.Background(), h.Deps, ops.ExportOptions{
		Path: filepath.Join(t.TempDir(), "demo.export"),
	})
	require.Error(t, err)
	assert.Equal(t, domain.CodeUsage, domain.AsError(err).Code)
	assert.Contains(t, err.Error(), "export or import",
		"the refusal does not say which capability is missing")
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
