package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/test/clitest"
)

// Going back past what a machine has seen is a decision for one install (RFC
// 0037 decision 4): a check, a stage and a tick install nothing older, so
// --allow-older beside them is a usage error rather than a flag ignored.
func TestAllowOlderOnlyGoesWithAnInstall(t *testing.T) {
	r := clitest.NewInstalled(t)
	for _, mode := range []string{"--check", "--stage", "--unattended"} {
		r.Run("update", mode, "--allow-older").ExitCode(domain.ExitUsage).
			SaysAll("--allow-older applies to an install")
	}
}
