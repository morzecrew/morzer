package clitest_test

import (
	"strings"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// `config apply` reads its document from stdin and prints no secret value it
// was given, whatever happens to the document.

const applyNeedle = "a-smtp-password-that-must-not-print"

func TestConfigApplyTakesTheDocumentFromStdinAndPrintsNoSecret(t *testing.T) {
	r := clitest.NewInstalled(t)

	out := r.RunWithInput("parameters:\n  log_level: debug\nsecrets:\n  smtp_password: "+applyNeedle+"\n",
		"config", "apply")
	out.ExitCode(0)
	out.NoOutputContains(applyNeedle)
	out.OutputContains("applied log_level, secret smtp_password")

	r.Run("config", "get", "log_level").ExitCode(0).StdoutContains("debug")
	r.Run("secret", "list").ExitCode(0).StdoutContains("smtp_password").NoOutputContains(applyNeedle)
}

func TestConfigApplyQuotesNothingOfABrokenDocument(t *testing.T) {
	r := clitest.NewInstalled(t)

	out := r.RunWithInput("secrets:\n  smtp_password: \""+applyNeedle+"\n  other: [\n", "config", "apply")
	out.ExitCode(2)
	out.OutputContains("not valid YAML at line")
	out.NoOutputContains(applyNeedle)
}

func TestConfigApplyRefusesADocumentOverOneMebibyte(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.RunWithInput("secrets:\n  smtp_password: "+strings.Repeat("x", 1<<20)+"\n", "config", "apply").
		ExitCode(2).
		OutputContains("unreasonably large")
}
