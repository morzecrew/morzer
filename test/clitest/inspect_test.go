package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/test/clitest"
)

// The refusals the four inspection commands make before they ever reach the
// runtime, driven the way an operator's shell drives them.
//
// Everything that needs a daemon -- the stream itself, a sample, a command
// inside a container -- is the acceptance run's and the container lane's. What
// is here is the half that has to work on a machine where Docker is down, which
// is most of when somebody types these.

func TestLogsRefusesASinceItCannotResolve(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("logs", "--since", "yesterday").
		ExitCode(domain.ExitUsage).
		SaysAll("neither a duration nor a timestamp")

	// The zone gets its own message: the operator wrote a time, and "invalid
	// value" would send them looking at the date rather than at what is
	// missing.
	r.Run("logs", "--since", "2026-08-10T09:12:33").
		ExitCode(domain.ExitUsage).
		SaysAll("names no time zone")
}

func TestExecRequiresTheTerminator(t *testing.T) {
	r := clitest.NewInstalled(t)

	// Without it, `morzer exec app --json` is ambiguous: the flag belongs to
	// the manager and the operator meant it for the container. Refusing
	// makes which one they got a decision rather than a discovery.
	r.Run("exec", "app", "ls").
		ExitCode(domain.ExitUsage).
		SaysAll("goes after `--`")

	// And a service on its own is not a command: there is no TTY, so a
	// bare `morzer exec app` would open a shell nobody could type into.
	r.Run("exec", "app").Failed()
}

func TestExecRefusesToPlanACommandItCannotSee(t *testing.T) {
	r := clitest.NewInstalled(t)

	// `--dry-run` is a global flag, so an operator can type it here — and
	// the manager cannot say what somebody's command would do. Refusing
	// beats ignoring: a `--dry-run` that ran the command anyway is how a
	// flag typed to prevent something becomes the thing that does it.
	r.Run("--dry-run", "exec", "app", "--", "rm", "-rf", "/tmp/nothing").
		ExitCode(domain.ExitUsage).
		SaysAll("cannot plan a command inside a container")
}

func TestStatsRefusesToStreamIntoTheSingleEnvelopeContract(t *testing.T) {
	r := clitest.NewInstalled(t)

	// Decision 10: the streaming exception is one this design carries once,
	// and `logs` has it. A second one for a table that redraws buys nothing
	// `sleep` does not.
	r.Run("--json", "stats", "--watch").
		ExitCode(domain.ExitUsage).
		SaysAll("loop around")
}

func TestStatsRefusesAnIntervalBelowTheFloor(t *testing.T) {
	r := clitest.NewInstalled(t)

	// Below a second the reading is mostly the sampler: `docker stats`
	// walks every container's cgroup, so the number would be about the
	// manager rather than about the deployment.
	r.Run("stats", "--watch", "--interval", "200ms").
		ExitCode(domain.ExitUsage).
		SaysAll("measures the sampler")
}

// withoutARelease is an installation `init` created and nothing was staged
// into: the state right after an operator runs `morzer init --product demo`.
//
// It is what makes the refusals below deterministic on any machine. An
// installation *with* a release resolves far enough to ask the daemon, so the
// same assertions would depend on whether the runner has Docker -- and these
// commands' whole promise is that they answer when it is down.
func withoutARelease(t *testing.T) *clitest.Runner {
	t.Helper()

	r := clitest.New(t)
	r.Run("init", "--product", "demo", "--no-recovery-recipient",
		"--install-units=false", "--generate-secrets=false").ExitCode(0)
	return r
}

func TestInspectionSaysThereIsNothingRunningBeforeTheFirstRelease(t *testing.T) {
	r := withoutARelease(t)

	// Every one of the four says the same thing, and says it rather than
	// showing an empty table -- which would read as a deployment with
	// nothing running rather than one that has nothing to run.
	for _, args := range [][]string{
		{"ps"},
		{"stats"},
		{"logs", "--tail", "5"},
		{"exec", "app", "--", "true"},
	} {
		r.Run(args...).
			ExitCode(domain.ExitInstallation).
			SaysAll("no release is installed")
	}
}

func TestAFailureBeforeTheStreamStartsStillGetsItsEnvelope(t *testing.T) {
	r := withoutARelease(t)

	// `logs --json` is the one command that writes its own machine-readable
	// output and no envelope. That exception must not swallow a failure
	// that happened before a single record went out: a consumer whose
	// command was refused would otherwise get empty input rather than
	// something it can read.
	out := r.Run("--json", "logs").ExitCode(domain.ExitInstallation)
	out.FieldEquals("ok", false)
	if code, _ := out.Field("error.code").(string); code == "" {
		t.Errorf("the error envelope carries no code:\n%s", out.Stdout)
	}
}

func TestPsAndStatsAppearInTheOperatingGroup(t *testing.T) {
	r := clitest.New(t)

	// The four are in `--help` where an operator meets them: after the
	// commands that change the deployment, since these are what they run
	// when the answer was not there.
	r.Run("--help").ExitCode(0).OutputContains("logs", "ps", "stats", "exec")
}
