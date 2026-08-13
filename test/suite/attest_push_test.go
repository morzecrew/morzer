package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/target"
	"github.com/morzecrew/morzer/internal/adapters/target/localdir"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// Statements leaving the machine (RFC 0025 §4.6, P4).
//
// The whole point of the feature is that the record does not die with the disk,
// so the tests that matter are the ones about the record actually arriving
// somewhere else -- and about the manager being honest when it did not.

// withAttestationTarget wires the production registry and a directory target,
// and returns where the statements should end up.
func (h *harness) withAttestationTarget(t *testing.T) (inst domain.Installation, offsite string) {
	t.Helper()

	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	h.Deps.Targets = registry
	h.Deps.Objects = registry

	offsite = filepath.Join(t.TempDir(), "offsite")

	inst = signingInstallation(t, h)
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + offsite}}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	return inst, offsite
}

// pushedStatements lists what reached a target.
func pushedStatements(t *testing.T, offsite string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(offsite, "attestations"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)

	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestAnAttestationReachesTheTargetAsItIsWritten(t *testing.T) {
	h := verifyHarness(t)
	_, offsite := h.withAttestationTarget(t)
	applyOnce(t, h)

	names := pushedStatements(t, offsite)
	require.NotEmpty(t, names, "the statement never left the machine")

	// The document and its signature, so a third party holding the target
	// can check it with `minisign -Vm` and nothing from this machine.
	var docs, sigs int
	for _, name := range names {
		if filepath.Ext(name) == ".minisig" {
			sigs++
			continue
		}
		docs++
	}
	assert.Positive(t, docs)
	assert.Equal(t, docs, sigs, "a statement reached the target without its signature")
}

// The asymmetry with RFC 0009, asserted rather than described: a target that
// cannot be reached must not fail the operation being recorded.
func TestAnUnreachableTargetDoesNotFailTheOperation(t *testing.T) {
	h := verifyHarness(t)
	inst, _ := h.withAttestationTarget(t)

	// A path under a file, so creating the directory cannot succeed.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + blocker + "/offsite"}}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	var warned bool
	h.Deps.Bus.SubscribeFunc(func(e events.Event) {
		if e.Level == events.LevelWarn && strings.Contains(e.Message, "attest push") {
			warned = true
		}
	})

	// The operation succeeds. An update refused because a log shipper was
	// down would be the notification anti-pattern RFC 0015 spent a section
	// avoiding.
	applyOnce(t, h)

	assert.True(t, warned,
		"a failed push must say so, and name the command that closes the gap")
	assert.NotEmpty(t, statementsOnDisk(t, h), "the local record must survive a failed push")
}

// And the gap closes afterwards. Without this, the warning above tells an
// operator to run a command that cannot help them.
func TestAttestPushSendsWhatAnEarlierFailureLeftBehind(t *testing.T) {
	h := verifyHarness(t)
	inst, offsite := h.withAttestationTarget(t)

	// Attested with no target configured, so nothing was pushed at the
	// time -- the state a machine is in after a target was added later, and
	// after any push that failed.
	bare := inst
	bare.Backup.Targets = nil
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), bare))
	applyOnce(t, h)
	require.Empty(t, pushedStatements(t, offsite))

	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	result, err := ops.AttestPush(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "pushed")

	require.NotEmpty(t, pushedStatements(t, offsite),
		"`attest push` did not close the gap `doctor` tells an operator to close with it")

	// And it is idempotent: a second run sends nothing, which is what makes
	// it safe from cron.
	again, err := ops.AttestPush(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Contains(t, again.Summary, "already on")
}

// A statement whose signature did not make it is not pushed.
//
// This command's own failure mode: the document goes first, so a transfer that
// died between the two leaves a statement on the target and no signature. If
// only the document decided, the document would be there, nothing would be
// missing, `doctor` would say all clear, and the record an auditor holds could
// never be checked -- a remedy that cannot repair the damage its own
// interruption causes.
func TestAStatementWhoseSignatureDidNotArriveIsPushedAgain(t *testing.T) {
	h := verifyHarness(t)
	_, offsite := h.withAttestationTarget(t)
	applyOnce(t, h)

	require.NotEmpty(t, pushedStatements(t, offsite))

	// The interrupted push, reconstructed: the document arrived, the
	// signature did not.
	remote := filepath.Join(offsite, "attestations")
	for _, name := range pushedStatements(t, offsite) {
		if filepath.Ext(name) == ".minisig" {
			require.NoError(t, os.Remove(filepath.Join(remote, name)))
		}
	}

	// doctor must be saying so in the meantime, or nobody would run the
	// command that repairs it.
	before, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)
	for _, res := range before.Results {
		if res.ID == "backup.attestations-pushed" {
			assert.Equal(t, events.CheckWarn, res.Status,
				"a statement the target cannot verify was reported as pushed")
		}
	}

	report, err := ops.AttestPush(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Contains(t, report.Summary, "pushed",
		"a statement missing its signature on the target was reported as already there")

	var sigs int
	for _, name := range pushedStatements(t, offsite) {
		if filepath.Ext(name) == ".minisig" {
			sigs++
		}
	}
	assert.Positive(t, sigs, "the signature was never sent again")
}

// A dry run says what it would send and sends nothing.
func TestAttestPushDryRunSendsNothing(t *testing.T) {
	h := verifyHarness(t)
	inst, offsite := h.withAttestationTarget(t)

	bare := inst
	bare.Backup.Targets = nil
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), bare))
	applyOnce(t, h)
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	result, err := ops.AttestPush(context.Background(), h.Deps, ops.Options{DryRun: true})
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "would push")
	assert.Empty(t, pushedStatements(t, offsite),
		"a dry run sent an attestation to the target")
}

// A target that does not answer is reported, and reported as unreachable
// rather than as done.
//
// The distinction is what the command's exit code is built on: a statement
// still missing is not a failure of `attest push` -- it pushed what it could,
// and `doctor` keeps saying so -- but a target that could not be reached at all
// is what a cron job needs to hear about.
func TestAttestPushReportsATargetThatDoesNotAnswer(t *testing.T) {
	h := verifyHarness(t)
	inst, _ := h.withAttestationTarget(t)

	bare := inst
	bare.Backup.Targets = nil
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), bare))
	applyOnce(t, h)

	// A target path under a regular file: nothing can create the directory.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + blocker + "/offsite"}}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	result, err := ops.AttestPush(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err, "an unreachable target is a finding to report, not an error to raise")

	report, ok := result.Data.(ops.AttestPushReport)
	require.True(t, ok)
	assert.True(t, report.Unreachable(),
		"a target that did not answer was reported as though the push had worked")
	assert.Contains(t, result.Summary, "did not answer")
	require.Len(t, report.Targets, 1)
	assert.NotEmpty(t, report.Targets[0].Error)
}

// `doctor` keeps saying it after the warning has scrolled past.
func TestDoctorReportsStatementsThatAreOnlyOnThisMachine(t *testing.T) {
	h := verifyHarness(t)
	inst, _ := h.withAttestationTarget(t)

	bare := inst
	bare.Backup.Targets = nil
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), bare))
	applyOnce(t, h)
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	var found bool
	for _, res := range report.Results {
		if res.ID != "backup.attestations-pushed" {
			continue
		}
		found = true
		assert.Equal(t, events.CheckWarn, res.Status)
		assert.Contains(t, res.Message, "only on this machine")
		assert.Contains(t, res.Remedy, "attest push")
	}
	require.True(t, found, "doctor must report attestations that have not been pushed")

	// And pushing them must clear it -- a check whose remedy does not
	// resolve it is a check that trains an operator to ignore it.
	_, err = ops.AttestPush(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	after, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)
	for _, res := range after.Results {
		if res.ID == "backup.attestations-pushed" {
			assert.Equal(t, events.CheckOK, res.Status, res.Message)
		}
	}
}
