package suite

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// RFC 0028 P2: `installation rotate-signing-key`.

// keyOnDisk is the public key the machine signs with now.
func keyOnDisk(t *testing.T, h *harness) string {
	t.Helper()
	key, err := h.Deps.Signer.PublicKey(context.Background())
	require.NoError(t, err)
	return key.Line
}

// A rotation retires the key into previous_keys, signs from then on with a new
// one, and leaves everything the old key signed verifiable as a predecessor's
// signature (decision 10) rather than as the current key's.
func TestARotationRetiresTheKeyAndKeepsItsSignaturesVerifiable(t *testing.T) {
	h := verifyHarness(t)
	inst := attested(t, h)
	old := inst.Signing.PublicKey

	_, err := ops.RotateSigningKey(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	after, err := h.Deps.State.LoadInstallation(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, old, after.Signing.PublicKey, "the key did not change")
	assert.Equal(t, keyOnDisk(t, h), after.Signing.PublicKey, "state records a key the machine does not sign with")
	require.NotEmpty(t, after.Signing.PreviousKeys)
	assert.Equal(t, old, after.Signing.PreviousKeys[0].Key, "the old key is not the newest predecessor")
	assert.Equal(t, domain.RetiredByRotation, after.Signing.PreviousKeys[0].Reason)
	assert.NoFileExists(t, h.Paths.SigningKeyFile()+".next", "the staged key was left behind")
	ok, detail, err := atomicfs.CheckMode(h.Paths.SigningKeyFile(), 0o400)
	require.NoError(t, err)
	assert.True(t, ok, detail)

	report, err := ops.AttestVerify(context.Background(), h.Deps, ops.VerifyOptions{})
	require.NoError(t, err)
	require.Len(t, report.Statements, 2, "the rotation filed no statement of its own")
	outcomes := map[domain.SignatureOutcome]int{}
	for _, s := range report.Statements {
		outcomes[s.Signature.Outcome]++
	}
	assert.Equal(t, 1, outcomes[domain.SignedByPredecessor], "the apply's statement is not a predecessor's: %v", outcomes)
	assert.Equal(t, 1, outcomes[domain.SignedByCurrentKey], "the rotation's statement is not the new key's: %v", outcomes)
	assert.Zero(t, report.Problems())
}

// stuckActivation is a signer whose new key cannot be moved into place.
type stuckActivation struct{ *signer.Signer }

func (stuckActivation) ActivateStagedKey(context.Context) error {
	return domain.SecretsError(nil, "injected: cannot move the new signing key into place")
}

// A rotation that fails at the last step leaves the machine signing with the
// old key and state saying so: nothing half-rotated, and no staged key left.
func TestARotationThatCannotFinishLeavesTheOldKey(t *testing.T) {
	h := verifyHarness(t)
	inst := signingInstallation(t, h)
	h.Deps.Signer = stuckActivation{signer.New(h.Paths.SigningKeyFile(), "demo")}

	_, err := ops.RotateSigningKey(context.Background(), h.Deps, ops.Options{})
	require.Error(t, err)

	after, err := h.Deps.State.LoadInstallation(context.Background())
	require.NoError(t, err)
	assert.Equal(t, inst.Signing.PublicKey, after.Signing.PublicKey, "state names a key that was never put in place")
	assert.Empty(t, after.Signing.PreviousKeys, "the current key was recorded as retired")
	assert.Equal(t, inst.Signing.PublicKey, keyOnDisk(t, h))
	assert.NoFileExists(t, h.Paths.SigningKeyFile()+".next", "the staged key was left behind")
}

// interruptedActivation cancels the operation as the new key is moved, the
// way a SIGTERM would, and leaves the key staged.
type interruptedActivation struct {
	*signer.Signer
	cancel context.CancelFunc
}

func (s interruptedActivation) ActivateStagedKey(ctx context.Context) error {
	s.cancel()
	return ctx.Err()
}

// A rotation interrupted at the move is finished by `--resume`, which mints no
// second key and records the old one as retired exactly once.
func TestAnInterruptedRotationIsFinishedByResume(t *testing.T) {
	h := verifyHarness(t)
	inst := signingInstallation(t, h)
	real := signer.New(h.Paths.SigningKeyFile(), "demo")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Deps.Signer = interruptedActivation{Signer: real, cancel: cancel}
	_, err := ops.RotateSigningKey(ctx, h.Deps, ops.Options{})
	require.Error(t, err)

	h.Deps.Signer = real
	_, err = ops.RotateSigningKey(context.Background(), h.Deps, ops.Options{Resume: true})
	require.NoError(t, err)

	after, err := h.Deps.State.LoadInstallation(context.Background())
	require.NoError(t, err)
	assert.Equal(t, keyOnDisk(t, h), after.Signing.PublicKey)
	assert.NotEqual(t, inst.Signing.PublicKey, after.Signing.PublicKey)
	require.Len(t, after.Signing.PreviousKeys, 1, "the old key was retired more than once, or not at all")
	assert.Equal(t, inst.Signing.PublicKey, after.Signing.PreviousKeys[0].Key)
	assert.NoFileExists(t, h.Paths.SigningKeyFile()+".next")
}

func TestARotationIsRefusedWhenThereIsNothingClearToRetire(t *testing.T) {
	t.Run("no key on disk", func(t *testing.T) {
		h := verifyHarness(t)
		h.install()
		_, err := ops.RotateSigningKey(context.Background(), h.Deps, ops.Options{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nothing to rotate")
		assert.NoFileExists(t, h.Paths.SigningKeyFile(), "a refused rotation minted a key")
	})
	t.Run("a key file state does not record", func(t *testing.T) {
		h := verifyHarness(t)
		inst := signingInstallation(t, h)
		inst.Signing.PublicKey = "RWTaKeyStateRecordsButTheMachineDoesNotHold000000000000"
		require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

		_, err := ops.RotateSigningKey(context.Background(), h.Deps, ops.Options{})
		require.Error(t, err)
		assert.Contains(t, domain.AsError(err).Hint, "init --repair")
	})
}

// A planned rotation changes neither the key file nor state, and stages nothing.
func TestAPlannedRotationChangesNothing(t *testing.T) {
	h := verifyHarness(t)
	inst := signingInstallation(t, h)

	result, err := ops.RotateSigningKey(context.Background(), h.Deps, ops.Options{DryRun: true})
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "would rotate")

	after, err := h.Deps.State.LoadInstallation(context.Background())
	require.NoError(t, err)
	assert.Equal(t, inst.Signing, after.Signing)
	assert.Equal(t, inst.Signing.PublicKey, keyOnDisk(t, h))
	_, err = os.Stat(h.Paths.SigningKeyFile() + ".next")
	assert.ErrorIs(t, err, os.ErrNotExist, "a plan staged a key")
}

// A rotation whose last step finished but whose record was never closed -- a
// process killed between the move and the journal write -- is resumed without
// rotating again: the key to retire is the one the interrupted run retired,
// not whatever is on disk now.
func TestResumingARotationThatAlreadyMovedItsKeyRotatesNothing(t *testing.T) {
	h := verifyHarness(t)
	signingInstallation(t, h)
	ctx := context.Background()

	done, err := ops.RotateSigningKey(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	rotated, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)

	crashed := done.Record
	crashed.ID = "op-killed-after-the-move"
	crashed.Status = domain.StatusRunning
	crashed.FinishedAt = domain.Time{}
	crashed.StartedAt = domain.NewTime(time.Now().Add(time.Second))
	crashed.Steps = append([]domain.StepRecord(nil), done.Record.Steps...)
	crashed.Steps[len(crashed.Steps)-1].Status = domain.StepRunning
	require.NoError(t, h.Deps.State.AppendOperation(ctx, crashed))

	_, err = ops.RotateSigningKey(ctx, h.Deps, ops.Options{Resume: true})
	require.NoError(t, err)

	after, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Equal(t, rotated.Signing.PublicKey, after.Signing.PublicKey, "the resume rotated a second time")
	assert.Len(t, after.Signing.PreviousKeys, 1, "the resume retired the new key as well")
	assert.Equal(t, rotated.Signing.PublicKey, keyOnDisk(t, h))
}

// Decision 11 as a test: a statement the retired key signs *after* the
// rotation, dated after retired_at, is still only "signed by a predecessor" --
// not rejected for its date. The date comes from the statement, which is what
// whoever kept a copy of the key writes, so a verifier that rejected it would
// stop only a forger who forgot to back-date.
func TestARetiredKeySigningAfterTheRotationIsStillOnlyAPredecessor(t *testing.T) {
	h := verifyHarness(t)
	signingInstallation(t, h)
	keyFile := h.Paths.SigningKeyFile()
	stolen, err := os.ReadFile(keyFile)
	require.NoError(t, err)

	_, err = ops.RotateSigningKey(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	current, err := os.ReadFile(keyFile)
	require.NoError(t, err)

	// The copy signs a statement after the rotation.
	putKey := func(body []byte) {
		require.NoError(t, os.Remove(keyFile))
		require.NoError(t, os.WriteFile(keyFile, body, 0o400))
	}
	putKey(stolen)
	applyOnce(t, h)
	putKey(current)

	report, err := ops.AttestVerify(context.Background(), h.Deps, ops.VerifyOptions{})
	require.NoError(t, err)
	outcomes := map[domain.SignatureOutcome]int{}
	for _, s := range report.Statements {
		outcomes[s.Signature.Outcome]++
	}
	assert.Equal(t, 1, outcomes[domain.SignedByPredecessor],
		"the retired key's later signature was not reported as a predecessor's: %v", outcomes)
	assert.Equal(t, 1, outcomes[domain.SignedByCurrentKey], "%v", outcomes)
}
