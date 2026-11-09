package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// `--profile` takes several names, as `--domain` does: a comma list or the flag
// repeated, each name trimmed (RFC 0053 decision 8). Both spellings reach the
// same check, which refuses two alternatives together.
func TestProfileTakesSeveralNames(t *testing.T) {
	for name, args := range map[string][]string{
		"a comma list":      {"--profile", "embedded, external-db"},
		"the flag repeated": {"--profile", "embedded", "--profile", "external-db"},
	} {
		t.Run(name, func(t *testing.T) {
			r := clitest.New(t)
			r.Run(append([]string{"init", "--release", r.Bundle, "--no-recovery-recipient",
				"--install-units=false"}, args...)...).
				Failed().
				StderrContains("profiles embedded and external-db cannot be deployed together")
		})
	}

	r := clitest.NewInstalled(t)
	r.Run("config", "get", "installation.profile").ExitCode(0).StdoutContains("embedded")
}
