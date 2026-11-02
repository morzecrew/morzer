package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/test/clitest"
)

// A release's own check refuses `apply` with the preflight exit code, in the
// vendor's words, and `doctor` reports it in a group of its own (RFC 0045).
func TestAVendorCheckRefusesApplyAndShowsInDoctor(t *testing.T) {
	r := clitest.New(t)
	r.Bundle = r.BundleWithAFailingCheck()
	r.Run("init",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
	).ExitCode(0)

	r.Run("apply").ExitCode(domain.ExitPreflight).
		SaysAll("the kernel allows enough memory maps", "vm.max_map_count is 65530",
			"raise vm.max_map_count to 262144")

	r.Run("doctor", "--verbose").ExitCode(domain.ExitPreflight).
		SaysAll("vendor x the kernel allows enough memory maps vm.max_map_count is 65530",
			"raise vm.max_map_count to 262144")
}
