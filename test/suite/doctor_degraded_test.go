package suite

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/clitest"
	"github.com/morzecrew/morzer/test/fakes"
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

// backupsOfSize is the backup engine the growth check reads, with sizes the
// test chooses.
//
// The shared fake reports every backup as a kilobyte, which cannot express the
// only case backup.growth exists for: a backup large enough that the retention
// count stops fitting on the disk.
type backupsOfSize struct {
	*fakes.Backup
	refs []ports.BackupRef
}

func (b backupsOfSize) List(context.Context) ([]ports.BackupRef, error) { return b.refs, nil }

// seedBackups points the deps at backups of the given sizes, newest first.
func seedBackups(t *testing.T, h *harness, keep int, sizes ...int64) {
	t.Helper()
	ctx := context.Background()

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Policy.RetainBackups = keep
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	refs := make([]ports.BackupRef, 0, len(sizes))
	for i, size := range sizes {
		refs = append(refs, ports.BackupRef{
			ID:   "backup-" + string(rune('a'+i)),
			At:   domain.NewTime(h.Deps.Now()),
			Size: size,
		})
	}
	h.Deps.Backup = backupsOfSize{Backup: h.Backup, refs: refs}
}

// TestDoctorDoesNotReportARetentionPolicyThatCannotFitAsSatisfied is the
// overflow case.
//
// Four backups of four exbibytes is a requirement no disk can meet, and it is
// also the multiplication that wraps int64 back to exactly zero -- at which
// point the shortfall came out negative, compared as satisfied, and the check
// reported ok about a policy that can never be met.
func TestDoctorDoesNotReportARetentionPolicyThatCannotFitAsSatisfied(t *testing.T) {
	h := newHarness(t)
	h.install()

	const huge = int64(1) << 62 // four of these is 2^64
	seedBackups(t, h, 4, huge)

	// Driven, so the verdict is about the arithmetic rather than about how
	// much room the machine running the suite happens to have.
	h.Deps.FreeSpace = func(string) (int64, error) { return 64 << 30, nil }

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	found := findResult(t, report, "backup.growth")
	assert.Equal(t, "warn", found.Status,
		"a retention policy no disk can satisfy was reported as fitting")
	assert.Contains(t, found.Message, "keeping 4 backups")
	assert.NotEmpty(t, found.Remedy)
}

// TestDoctorWarnsWhenTheNextBackupWillNotFitEvenThoughRetentionIsFull is the
// steady state, which is where an installation spends its whole life.
//
// Retention is full, so there is no further growth to make room for -- and the
// next backup still has to be written in full before the oldest one is pruned.
// Comparing only against the remaining growth reported ok the night before
// ENOSPC.
func TestDoctorWarnsWhenTheNextBackupWillNotFitEvenThoughRetentionIsFull(t *testing.T) {
	h := newHarness(t)
	h.install()

	const petabyte = int64(1) << 50
	seedBackups(t, h, 2, petabyte, petabyte)

	// Driven rather than measured. This check compares against the real
	// filesystem, so without a seam the verdict depends on how much room
	// the machine running the suite happens to have -- and a host with more
	// free space than the seeded backup silently flips the branch under
	// test. Half a backup: retention is already full, and the next one
	// still does not fit.
	h.Deps.FreeSpace = func(string) (int64, error) { return petabyte / 2, nil }

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	found := findResult(t, report, "backup.growth")
	assert.Equal(t, "warn", found.Status,
		"a disk with less free space than one backup was reported as fitting")
	// The message says which of the two conditions tripped, because the
	// remedies differ: this one is not fixed by lowering retention.
	assert.Contains(t, found.Message, "the next backup")
	assert.NotEmpty(t, found.Remedy)
}

// The backup engine is wired in the CLI layer, and what it should do with a
// project configuration that will not assemble depends on which command asked
// for it -- so the two tests below drive `morzer` itself.

// breakTheProjectConfiguration leaves the installation holding a parameter the
// release does not declare, and returns its name.
//
// What a release that dropped a parameter leaves behind on a machine whose
// operator had set it. Nothing else is disturbed: the release on disk is still
// exactly the one that was installed.
func breakTheProjectConfiguration(t *testing.T, r *clitest.Runner) string {
	t.Helper()
	const name = "a_parameter_a_newer_release_dropped"

	ctx := context.Background()
	store := state.New(domain.PathsUnder(r.Root, "demo"))

	inst, err := store.LoadInstallation(ctx)
	require.NoError(t, err)
	// Added to whatever the installation already holds rather than
	// replacing it: the scenario is one parameter the release dropped
	// *alongside* the declared ones, and wiping the rest would also remove
	// the values that make the deployment resolvable in the first place.
	if inst.Parameters == nil {
		inst.Parameters = map[string]string{}
	}
	inst.Parameters[name] = "1"
	require.NoError(t, store.SaveInstallation(ctx, inst))

	return name
}

// TestTakingABackupRefusesWhenTheProjectConfigurationWillNotResolve pins the
// refusal, because the alternative is silent and unrecoverable.
//
// Without the project configuration the engine cannot read the project's named
// volumes, so a release with a backup hook produces a backup holding that
// hook's database dump, none of the volumes, and a success message. The
// operator finds out during a restore.
func TestTakingABackupRefusesWhenTheProjectConfigurationWillNotResolve(t *testing.T) {
	r := clitest.NewInstalled(t)
	name := breakTheProjectConfiguration(t, r)

	// The parameter is named, because "the configuration is broken" is not
	// something anybody can act on.
	r.Run("backup").Failed().OutputContains(name)

	// And a restore, for the same reason in the other direction: it would
	// put the database back and silently leave every volume as it is.
	r.Run("restore").Failed().OutputContains(name)
}

// TestReadOnlyBackupCommandsStillWorkWhenTheProjectConfigurationWillNotResolve
// is the other half of the same decision.
//
// A configuration that will not resolve is exactly when an operator runs these,
// and a `backup list` that refuses would hide the backups they are trying to
// restore from.
func TestReadOnlyBackupCommandsStillWorkWhenTheProjectConfigurationWillNotResolve(t *testing.T) {
	r := clitest.NewInstalled(t)
	breakTheProjectConfiguration(t, r)

	r.Run("backup", "list").ExitCode(0).OutputContains("no backups")

	// `doctor` most of all. Its backup checks exist only while an engine is
	// attached, so the freshness check appearing at all is the evidence that
	// the attach stayed tolerant -- and the coverage check is where the
	// configuration failure is reported by name.
	r.Run("doctor").OutputContains(
		"a recent backup exists",
		"cannot resolve the project")
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
