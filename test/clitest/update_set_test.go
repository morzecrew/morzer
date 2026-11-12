package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/test/clitest"
)

// `update --set` and `--set-file` go with an install: a check, a stage or a
// tick records nothing they could go with, so the pair is refused, and a value
// that is not name=value is refused before anything is fetched.
func TestUpdateSetGoesWithAnInstall(t *testing.T) {
	r := clitest.NewInstalled(t)

	for _, mode := range [][]string{{"--check"}, {"--stage"}, {"--unattended"}} {
		r.Run(append([]string{"update", "--set", "admin_email=ops@example.test"}, mode...)...).ExitCode(domain.ExitUsage).
			SaysAll("--set and --set-file apply to an install")
		r.Run(append([]string{"update", "--set-file", "admin_email=/dev/null"}, mode...)...).ExitCode(domain.ExitUsage).
			SaysAll("--set and --set-file apply to an install")
	}

	r.Run("update", r.BundleAt("1.3.0"), "--set", "admin_email").ExitCode(domain.ExitUsage).OutputContains("admin_email")
}
