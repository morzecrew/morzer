package clitest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/test/clitest"
)

// The `attest` commands at the binary, and above all their `--json` shape.
//
// `--json` is a monitoring contract: the operation computes a report, the view
// publishes the keys, and a mapping sits between them so the terminal output
// can change without moving what a cron job parses. Nothing else exercises that
// mapping -- a field dropped on the way through would ship silently and break
// somebody's `jq` rather than a test.

func TestAttestLogReadsBackWhatAnOperationRecorded(t *testing.T) {
	r := clitest.NewInstalled(t)

	// A config change is an attested operation that needs nothing running.
	r.Run("config", "set", "http_port=9000").ExitCode(0)

	r.Run("attest", "log").ExitCode(0).StdoutContains("config", "succeeded")

	out := r.Run("--json", "attest", "log").ExitCode(0)
	out.FieldEquals("ok", true)

	newest := firstOf(t, out.Field("data.entries"))
	assert.Equal(t, "config", newest["kind"])
	assert.Equal(t, "succeeded", newest["outcome"])
	assert.Equal(t, true, newest["signed"])
	assert.NotEmpty(t, newest["operation"])
	assert.NotEmpty(t, newest["started"])
}

// firstOf reads the first element of a JSON array field.
//
// The envelope helper walks objects only, and the keys worth pinning here are
// inside a list -- these are the fields somebody's `jq` reads.
func firstOf(t *testing.T, field any) map[string]any {
	t.Helper()
	list, ok := field.([]any)
	require.True(t, ok, "%v is not a list", field)
	require.NotEmpty(t, list)
	first, ok := list[0].(map[string]any)
	require.True(t, ok, "%v is not an object", list[0])
	return first
}

func TestAttestVerifyAcceptsWhatThisMachineJustSigned(t *testing.T) {
	r := clitest.NewInstalled(t)
	r.Run("config", "set", "http_port=9000").ExitCode(0)

	r.Run("attest", "verify").ExitCode(0).StdoutContains("statement(s)", "0 problem(s)")

	out := r.Run("--json", "attest", "verify").ExitCode(0)
	out.FieldEquals("ok", true)
	out.FieldEquals("data.problems", float64(0))
	out.FieldEquals("data.live_checked", false)

	statement := firstOf(t, out.Field("data.statements"))
	signature, ok := statement["signature"].(map[string]any)
	require.True(t, ok, "a statement's verdict carries no signature")
	assert.Equal(t, "signed-by-current-key", signature["outcome"])
}

// An installation that has recorded nothing says so, rather than reporting an
// empty list as though it had checked something.
func TestAttestOnAMachineThatHasRecordedNothing(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("attest", "log").ExitCode(domain.ExitInstallation).
		StderrContains("no attestations")
	r.Run("attest", "verify").ExitCode(domain.ExitInstallation).
		StderrContains("no attestations")
}

// `attest push` on an installation that keeps its backups nowhere is a usage
// error naming what to do, not a silent success.
func TestAttestPushWithNoTargetsSaysWhatToConfigure(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("attest", "push").ExitCode(domain.ExitUsage).
		StderrContains("backup target add")
}
