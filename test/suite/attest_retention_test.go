package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// Pruning the attestations a machine keeps (RFC 0049).

// retentionHarness is verifyHarness with a clock that moves. Statements are
// pruned oldest first by their operation ids, which order by time, and the
// harness's own clock is stopped.
func retentionHarness(t *testing.T) *harness {
	t.Helper()
	h := verifyHarness(t)
	h.Deps.Now = steppingClock()
	return h
}

func keepAttestations(t *testing.T, h *harness, inst domain.Installation, n int) domain.Installation {
	t.Helper()
	inst.Policy.RetainAttestations = n
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
	return inst
}

func failingApply(t *testing.T, h *harness) {
	t.Helper()
	h.Runtime.Fail["Up"] = domain.RuntimeError(errInjected, "daemon refused")
	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.Error(t, err)
	delete(h.Runtime.Fail, "Up")
}

// The oldest go, each with its signature, and what is left still verifies.
func TestRetentionRemovesTheOldestAttestationsWithTheirSignatures(t *testing.T) {
	h := retentionHarness(t)
	inst := signingInstallation(t, h)
	for range 3 {
		applyOnce(t, h)
	}
	before := statementsOnDisk(t, h)
	require.Len(t, before, 3, "unset keeps every statement")

	keepAttestations(t, h, inst, 2)
	var said string
	h.Deps.Bus.SubscribeFunc(func(e events.Event) {
		if e.Level == events.LevelInfo && strings.Contains(e.Message, "retention.attestations") {
			said = e.Message
		}
	})
	applyOnce(t, h)

	after := statementsOnDisk(t, h)
	assert.Equal(t, before[2], after[0], "not the oldest were removed")
	assert.Len(t, after, 2)
	for _, gone := range before[:2] {
		assert.NoFileExists(t, gone)
		assert.NoFileExists(t, gone+".minisig")
	}
	assert.FileExists(t, after[0]+".minisig")
	assert.Contains(t, said, "removed the 2 oldest")

	report, err := ops.AttestVerify(context.Background(), h.Deps, ops.VerifyOptions{})
	require.NoError(t, err)
	assert.Zero(t, report.Problems())
}

// A statement a target is missing is the only copy of itself: it stays, and so
// does everything newer, until `attest push` sends it. The target keeps all.
func TestRetentionKeepsWhatATargetIsMissing(t *testing.T) {
	h := retentionHarness(t)
	inst, offsite := h.withAttestationTarget(t)
	bare := inst
	bare.Backup.Targets = nil
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), bare))
	applyOnce(t, h)
	applyOnce(t, h)

	keepAttestations(t, h, inst, 1)
	applyOnce(t, h)
	assert.Len(t, statementsOnDisk(t, h), 3, "a statement the target is missing was removed")

	_, err := ops.AttestPush(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	applyOnce(t, h)
	assert.Len(t, statementsOnDisk(t, h), 1)
	assert.Len(t, pushedStatements(t, offsite), 8, "the target's copies were pruned")
}

// A target that cannot be listed says nothing about what it holds, so nothing
// is removed.
func TestRetentionKeepsEverythingWhenATargetCannotBeListed(t *testing.T) {
	requireNonRoot(t)
	h := retentionHarness(t)
	inst, offsite := h.withAttestationTarget(t)
	applyOnce(t, h)
	applyOnce(t, h)

	keepAttestations(t, h, inst, 1)
	listing := filepath.Join(offsite, "attestations")
	require.NoError(t, os.Chmod(listing, 0o300))
	t.Cleanup(func() { _ = os.Chmod(listing, 0o755) })
	applyOnce(t, h)
	assert.Len(t, statementsOnDisk(t, h), 3)
}

// `--against-live` compares with the newest successful statement, so a run of
// failures longer than the limit does not remove it.
func TestRetentionKeepsTheNewestSuccessfulStatement(t *testing.T) {
	h := retentionHarness(t)
	inst := signingInstallation(t, h)
	applyOnce(t, h)
	succeeded := statementsOnDisk(t, h)[0]

	keepAttestations(t, h, inst, 1)
	failingApply(t, h)
	failingApply(t, h)
	assert.FileExists(t, succeeded)
	assert.Len(t, statementsOnDisk(t, h), 3, "a statement newer than a kept one was removed")
	_, err := ops.AttestVerify(context.Background(), h.Deps, ops.VerifyOptions{AgainstLive: true})
	require.NoError(t, err)

	applyOnce(t, h)
	assert.Len(t, statementsOnDisk(t, h), 1)
	assert.NoFileExists(t, succeeded)
}

// A prune that cannot remove a statement warns, stops, and leaves the
// operation succeeded.
func TestAFailedPruneDoesNotFailTheOperation(t *testing.T) {
	h := retentionHarness(t)
	inst := signingInstallation(t, h)
	applyOnce(t, h)
	applyOnce(t, h)
	oldest := statementsOnDisk(t, h)[0]
	// A signature that is a directory with something in it cannot be removed.
	require.NoError(t, os.Remove(oldest+".minisig"))
	require.NoError(t, os.MkdirAll(filepath.Join(oldest+".minisig", "x"), 0o755))

	keepAttestations(t, h, inst, 1)
	var warned bool
	h.Deps.Bus.SubscribeFunc(func(e events.Event) {
		if e.Level == events.LevelWarn && strings.Contains(e.Message, "cannot remove") {
			warned = true
		}
	})
	applyOnce(t, h)
	assert.True(t, warned)
	assert.FileExists(t, oldest)
	assert.Len(t, statementsOnDisk(t, h), 3)
}

func TestRetentionAttestationsIsASetting(t *testing.T) {
	h := newHarness(t)
	h.install()

	_, err := setSetting(t, h, false, map[string]string{"retention.attestations": "0"})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "keeps every statement")

	_, err = setSetting(t, h, false, map[string]string{"retention.attestations": "500"})
	require.NoError(t, err)
	assert.Equal(t, 500, installation(t, h).Policy.RetainAttestations)

	_, err = ops.SetSettings(context.Background(), h.Deps,
		ops.SetSettingsOptions{Unset: []string{"retention.attestations"}})
	require.NoError(t, err)
	assert.Zero(t, installation(t, h).Policy.RetainAttestations)
}

// lockedSigner notes, when it is asked to sign, whether the attestation lock
// was free to take.
type lockedSigner struct {
	ports.Signer
	locker ports.Locker
	free   bool
}

func (s *lockedSigner) Sign(ctx context.Context, data []byte, comment string) (ports.Signature, error) {
	if release, err := s.locker.Acquire(ctx, "attestations", ports.LockOptions{}); err == nil {
		s.free = true
		_ = release()
	}
	return s.Signer.Sign(ctx, data, comment)
}

// A statement is written and signed under the lock the prune and `attest
// push` take, so neither sees it before its signature exists: the prune would
// count a target holding the document as holding all of it and remove it, and
// the push would send the document alone.
func TestAStatementIsSignedUnderTheAttestationLock(t *testing.T) {
	h := retentionHarness(t)
	signingInstallation(t, h)
	locked := &lockedSigner{Signer: h.Deps.Signer, locker: h.Deps.Locker}
	h.Deps.Signer = locked
	applyOnce(t, h)
	assert.False(t, locked.free, "the statement was signed with the attestation lock free")
}

// While something holds the lock, the prune removes nothing and `attest push`
// does not start.
func TestThePruneAndAttestPushTakeTheAttestationLock(t *testing.T) {
	h := retentionHarness(t)
	inst, _ := h.withAttestationTarget(t)
	applyOnce(t, h)
	applyOnce(t, h)
	keepAttestations(t, h, inst, 1)

	release, err := h.Deps.Locker.Acquire(context.Background(), "attestations", ports.LockOptions{})
	require.NoError(t, err)
	applyOnce(t, h)
	assert.Len(t, statementsOnDisk(t, h), 3, "the prune ran without the lock")
	_, err = ops.AttestPush(context.Background(), h.Deps, ops.Options{})
	require.Error(t, err, "attest push ran without the lock")
	require.NoError(t, release())
}

// resumeApply resumes an apply journaled as killed mid-run, after prepare has
// set up the statement path its resume files under: a resume keeps the
// interrupted run's id.
func resumeApply(t *testing.T, h *harness, prepare func(statement string)) string {
	t.Helper()
	ctx := context.Background()
	clean, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	killed := clean.Record
	killed.ID = "op-killed-mid-apply"
	killed.Status = domain.StatusRunning
	killed.FinishedAt = domain.Time{}
	// After the clean run, which the engine stamps with the wall clock.
	killed.StartedAt = domain.NewTime(time.Now().Add(time.Second))
	killed.Steps = append([]domain.StepRecord(nil), clean.Record.Steps...)
	for i := range killed.Steps {
		killed.Steps[i].Status = domain.StepPending
	}
	require.NoError(t, h.Deps.State.AppendOperation(ctx, killed))

	statement := filepath.Join(h.Paths.AttestationsDir(), killed.ID+".json")
	prepare(statement)
	resumed, err := ops.Apply(ctx, h.Deps, ops.Options{Resume: true})
	require.NoError(t, err)
	require.Equal(t, killed.ID, resumed.Record.ID)
	return statement
}

type refusingSigner struct{ ports.Signer }

func (refusingSigner) Sign(context.Context, []byte, string) (ports.Signature, error) {
	return ports.Signature{}, domain.NoSigningKey(nil, "no key")
}

// A resume that cannot sign leaves its statement unsigned, not beside the
// interrupted run's signature, which signs a document that is gone.
func TestAResumeThatCannotSignLeavesNoEarlierSignature(t *testing.T) {
	h := retentionHarness(t)
	signingInstallation(t, h)
	statement := resumeApply(t, h, func(statement string) {
		require.NoError(t, os.WriteFile(statement, []byte("{}\n"), 0o644))
		require.NoError(t, os.WriteFile(statement+".minisig", []byte("the earlier signature\n"), 0o644))
		h.Deps.Signer = refusingSigner{h.Deps.Signer}
	})
	assert.NotEqual(t, "{}\n", readFile(t, statement))
	assert.NoFileExists(t, statement+".minisig")
}

// A resume whose statement cannot be written leaves the interrupted run's
// signed pair as it was.
func TestAResumeThatCannotWriteItsStatementKeepsTheEarlierPair(t *testing.T) {
	h := retentionHarness(t)
	signingInstallation(t, h)
	statement := resumeApply(t, h, func(statement string) {
		// A directory with something in it cannot be replaced by a file.
		require.NoError(t, os.MkdirAll(filepath.Join(statement, "x"), 0o755))
		require.NoError(t, os.WriteFile(statement+".minisig", []byte("the earlier signature\n"), 0o644))
	})
	assert.Equal(t, "the earlier signature\n", readFile(t, statement+".minisig"))
}
