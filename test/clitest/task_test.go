package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/test/clitest"
)

// The refusals `task` makes before it reaches the runtime, driven the way an
// operator's shell drives them. Running a task needs a daemon, and is the
// suite's on a fake and the container lane's on a real one.

func TestTaskArgumentsGoAfterTheTerminator(t *testing.T) {
	r := clitest.NewInstalled(t)

	// Without one, `morzer task run seed --json` would hand the manager's
	// flag to the vendor's command or the other way round.
	r.Run("task", "run", "seed", "small").
		ExitCode(domain.ExitUsage).
		SaysAll("go after `--`")

	r.Run("task", "run", "--", "seed").
		ExitCode(domain.ExitUsage).
		SaysAll("name goes before `--`")
}

func TestTaskSaysWhatTheReleaseDeclares(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("task", "list").ExitCode(0).SaysAll("declares no tasks")
	r.Run("--json", "task", "list").ExitCode(0).FieldLen("data", 0)

	r.Run("task", "run", "seed").
		ExitCode(domain.ExitUsage).
		SaysAll("declares no tasks")

	r.Run("--resume", "task", "run", "seed").
		ExitCode(domain.ExitUsage).
		SaysAll("cannot continue a task")
}

func TestTaskSaysThereIsNothingToRunBeforeTheFirstRelease(t *testing.T) {
	r := withoutARelease(t)

	for _, args := range [][]string{{"task", "list"}, {"task", "run", "seed"}} {
		r.Run(args...).
			ExitCode(domain.ExitInstallation).
			SaysAll("no release is installed")
	}
}
