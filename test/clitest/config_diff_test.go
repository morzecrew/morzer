package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// `config diff` prints the diffs on stdout and the summary on stderr, so what is
// piped onward is the diffs alone, and exits 0 with files differing: a
// difference is a finding, and `doctor` is where a finding becomes an exit code.
// Nothing has applied this installation, so its one configuration file is
// absent, which is drift.
func TestConfigDiffPrintsTheDiffsAndTheSummaryApart(t *testing.T) {
	r := clitest.NewInstalled(t)

	res := r.Run("config", "diff").ExitCode(0)
	res.StdoutContains("--- " + r.Path("etc", "demo", "application.yaml"))
	res.StderrContains("1 of 1 configuration file differs from what 1.2.0 renders")

	r.Run("config", "diff", "--json").ExitCode(0).
		FieldLen("data.drift", 1).
		FieldEquals("data.targets", float64(1)).
		FieldEquals("data.release", "1.2.0")
}
