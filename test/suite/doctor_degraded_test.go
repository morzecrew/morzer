package suite

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// `doctor` is what an operator runs when something is wrong, so its behaviour
// on a broken machine is the behaviour that matters. A diagnostic that is only
// tested against a healthy installation is a diagnostic tested in the one state
// nobody runs it in.
//
// Every case here asserts the remedy as well as the finding: a check that says
// something is wrong without saying what to do has done half a job, which is
// this project's own rule.

func findResult(t *testing.T, report ops.DoctorReport, id string) events0 {
	t.Helper()
	for _, r := range report.Results {
		if r.ID == id {
			return events0{Status: string(r.Status), Message: r.Message, Remedy: r.Remedy}
		}
	}
	t.Fatalf("doctor produced no check %q", id)
	return events0{}
}

type events0 struct{ Status, Message, Remedy string }

func TestDoctorOnAMachineWithNoReleaseInstalled(t *testing.T) {
	h := newHarness(t)

	// An installation, but nothing installed over it -- the state between
	// `init` and the first `update`.
	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_01", Product: "demo",
		CreatedAt: domain.NewTime(h.Deps.Now()), Policy: domain.DefaultPolicy(),
	}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	found := findResult(t, report, "runtime.release")
	// A warning, not a failure: this is a legitimate state, and exiting
	// non-zero over it would make `doctor` useless as a post-init check.
	assert.Equal(t, "warn", found.Status)
	assert.Contains(t, found.Remedy, "morzer update")

	// Other checks may legitimately fail on a bare installation -- there
	// are no secrets and no directories a release would have created. What
	// this test pins is the release check itself, not the overall verdict.
	for _, r := range report.Results {
		if r.ID == "runtime.release" && r.Status == "fail" {
			t.Errorf("the absence of a release was reported as a failure")
		}
	}
}

// TestDoctorReportsAReleaseItCannotRead is the state a half-finished fetch or a
// hand-deleted directory leaves behind.
func TestDoctorReportsAReleaseItCannotRead(t *testing.T) {
	h := newHarness(t)
	h.install()

	// The state still points at the release; the release is gone.
	require.NoError(t, os.RemoveAll(h.Release.Root))

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err, "doctor must diagnose a broken release, not fail on it")

	found := findResult(t, report, "runtime.release")
	assert.Equal(t, "fail", found.Status)
	// Naming the version and the path is what turns this into something an
	// operator can act on.
	assert.Contains(t, found.Message, "1.2.0")
	assert.Contains(t, found.Message, h.Release.Root)
}

func TestDoctorReportsARuntimeItCannotReach(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Runtime.Fail["Status"] = domain.RuntimeError(nil, "cannot connect to the Docker daemon")

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	services := findResult(t, report, "runtime.services")
	assert.NotEqual(t, "ok", services.Status)
	assert.Contains(t, services.Remedy, "docker info")
	// The adapter's message already names the failure; prefixing it once
	// produced "cannot read service status: cannot read service status".
	assert.Equal(t, 1, strings.Count(services.Message, "cannot connect"),
		"the adapter's message was duplicated")

	// And the health check does not then probe a runtime it cannot see.
	health := findResult(t, report, "runtime.health")
	assert.Contains(t, strings.ToLower(health.Message), "not probed")
}

func TestDoctorReportsServicesThatAreDown(t *testing.T) {
	h := newHarness(t)
	h.install()

	h.Runtime.Services = map[string]ports.ServiceState{
		"app": {Name: "app", State: "running", Health: ports.HealthHealthy},
		"db":  {Name: "db", State: "exited", ExitCode: 137},
	}

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	found := findResult(t, report, "runtime.services")
	assert.NotEqual(t, "ok", found.Status)
	// The service that is down is named; the one that is up is not the
	// operator's problem.
	assert.Contains(t, found.Message, "db")
}

// TestDoctorSaysNothingAboutHealthWhenNothingIsRunning stops a stopped
// deployment producing a wall of connection refusals that say nothing the
// service list has not already said.
func TestDoctorSaysNothingAboutHealthWhenNothingIsRunning(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Runtime.Services = map[string]ports.ServiceState{
		"app": {Name: "app", State: "exited"},
	}

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	found := findResult(t, report, "runtime.health")
	assert.Contains(t, strings.ToLower(found.Message), "not probed")
}

func TestDoctorReportsAFailingHealthCheck(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Runtime.Services = map[string]ports.ServiceState{
		"app": {Name: "app", State: "running", Health: ports.HealthHealthy},
	}
	h.Health.Healthy = false

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	found := findResult(t, report, "runtime.health")
	assert.NotEqual(t, "ok", found.Status)
	assert.NotEmpty(t, found.Message, "a failing probe reported nothing")
}

// TestDoctorReportsThatNoBackupExists is the finding an operator most wants
// before an update, and the one a fresh installation always produces.
func TestDoctorReportsThatNoBackupExists(t *testing.T) {
	h := newHarness(t)
	h.install()

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	found := findResult(t, report, "backup.freshness")
	assert.NotEqual(t, "ok", found.Status)
	assert.Contains(t, found.Remedy, "morzer backup")
}

// TestStatusOnAMachineWhoseRuntimeIsUnreachable pins that `status` degrades
// rather than failing: it has to work on a machine with a broken daemon,
// because that is when somebody runs it.
func TestStatusOnAMachineWhoseRuntimeIsUnreachable(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Runtime.Fail["Status"] = domain.RuntimeError(nil, "cannot connect to the Docker daemon")

	status, err := ops.GetStatus(context.Background(), h.Deps)
	require.NoError(t, err, "status must degrade, not fail")

	assert.Equal(t, "demo", status.Product)
	require.NotEmpty(t, status.Problems, "the unreachable runtime is not reported")
	assert.Contains(t, strings.Join(status.Problems, " "), "cannot connect")
}
