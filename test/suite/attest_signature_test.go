package suite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/verify"
	"github.com/morzecrew/morzer/internal/adapters/verify/checksum"
	"github.com/morzecrew/morzer/internal/adapters/verify/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// verificationOf returns the verification block of the statement an operation
// filed, as the JSON an auditor reads -- absence of a field is the claim under
// test, and a Go struct would hide it behind a zero value.
func verificationOf(t *testing.T, h *harness, opID string) map[string]any {
	t.Helper()
	for _, f := range statementsOnDisk(t, h) {
		body, err := os.ReadFile(f)
		require.NoError(t, err)
		var doc struct {
			Predicate struct {
				Operation    struct{ ID string }
				Verification map[string]any
			}
		}
		require.NoError(t, json.Unmarshal(body, &doc))
		if doc.Predicate.Operation.ID == opID {
			return doc.Predicate.Verification
		}
	}
	t.Fatalf("operation %s filed no statement", opID)
	return nil
}

// An update verifies the bundle's signature itself, so its statement says the
// signature verified and against which key. An apply afterwards verifies
// nothing, and its statement says the release was verified when staged, by the
// update -- never that the apply verified it (SECURITY.md, known gaps).
func TestAStatementSaysWhichOperationVerifiedTheSignature(t *testing.T) {
	ctx := context.Background()
	h := verifyHarness(t)
	signingInstallation(t, h)
	h.setHookEnv()
	applyBaseline(t, h)

	h.Deps.Verifier = verify.NewChain(checksum.New(), minisign.New())
	src := stageUpgradeSource(t, h)
	key := signBundle(t, src)
	requireSignatures(t, h, key, false)

	updated, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)
	applied, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)

	byUpdate := verificationOf(t, h, updated.Record.ID)
	assert.Equal(t, true, byUpdate["signature_verified"], "the update verified a signature and did not say so")
	keyID, _ := byUpdate["key_id"].(string)
	assert.Len(t, keyID, 16, "the key a signature verified against is not named")
	assert.NotContains(t, byUpdate, "signature_verified_when_staged",
		"the update's own check was reported as an earlier operation's")

	byApply := verificationOf(t, h, applied.Record.ID)
	assert.NotContains(t, byApply, "signature_verified",
		"an apply, which verifies nothing, claims to have verified a signature")
	staged, ok := byApply["signature_verified_when_staged"].(map[string]any)
	require.True(t, ok, "the apply's statement says nothing of the signature verified when the release was staged")
	assert.Equal(t, keyID, staged["key_id"])
	assert.Equal(t, updated.Record.ID, staged["operation_id"], "the check is not tied to the operation that made it")
	assert.NotEmpty(t, staged["at"])
}

// An unsigned release was never verified, so no statement about it claims a
// signature either way: the fields are absent, not false.
func TestAnUnsignedReleaseIsNeverReportedAsVerified(t *testing.T) {
	ctx := context.Background()
	h := verifyHarness(t)
	signingInstallation(t, h)
	h.setHookEnv()
	applyBaseline(t, h)

	updated, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.NoError(t, err)
	applied, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)

	for _, id := range []string{updated.Record.ID, applied.Record.ID} {
		v := verificationOf(t, h, id)
		assert.NotContains(t, v, "signature_verified")
		assert.NotContains(t, v, "signature_verified_when_staged")
	}
}

// A rollback returns to a release verified when it was first staged, and the
// check travels with it: the rollback's statement reports it as that
// operation's, and the record keeps it for the statements after.
func TestARollbackKeepsTheCheckMadeWhenTheReleaseWasStaged(t *testing.T) {
	ctx := context.Background()
	h := verifyHarness(t)
	signingInstallation(t, h)
	h.setHookEnv()
	applyBaseline(t, h)

	// What `init` records when it verifies the release it installs.
	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	check := domain.SignatureCheck{KeyID: "E7620F1842B4E81F", OperationID: "op-init", At: domain.NewTime(time.Now().UTC())}
	current.SignatureVerified = &check
	require.NoError(t, h.Deps.State.SetCurrentRelease(ctx, current))
	applyBaseline(t, h)

	updated, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
	require.NoError(t, err)
	assert.NotContains(t, verificationOf(t, h, updated.Record.ID), "signature_verified_when_staged",
		"the update credited the release it installed with the check made on the one it replaced")
	rolledBack, err := ops.Rollback(ctx, h.Deps, ops.RollbackOptions{Options: ops.Options{Force: true}})
	require.NoError(t, err)

	staged, ok := verificationOf(t, h, rolledBack.Record.ID)["signature_verified_when_staged"].(map[string]any)
	require.True(t, ok, "the rollback's statement lost the check made when the release was staged")
	assert.Equal(t, "op-init", staged["operation_id"])

	after, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	require.NotNil(t, after.SignatureVerified, "the record the rollback wrote dropped the check")
	assert.Equal(t, "op-init", after.SignatureVerified.OperationID)
}

// `init` verifies the release it installs, and records the check with it, so
// the first apply's statement can say the release was verified when staged.
func TestInitRecordsTheSignatureItVerified(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.setHookEnv()
	realIdentity(t, h)
	h.Deps.Verifier = verify.NewChain(checksum.New(), minisign.New())

	src := filepath.Join(t.TempDir(), "bundle-1.2.0")
	copyBundle(t, testBundlePath(t), src)
	retargetManifest(t, src, h.Root)
	key := signBundle(t, src)

	result, err := ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, ReleasePath: src,
		RequireSignature: true, SigningKeys: []string{key},
	})
	require.NoError(t, err)

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	require.NotNil(t, current.SignatureVerified, "init verified a signature and recorded nothing")
	assert.Len(t, current.SignatureVerified.KeyID, 16)
	assert.Equal(t, result.Record.ID, current.SignatureVerified.OperationID)
}

// Statements written before the predicate moved to v2 are still read: v2 only
// adds a field. One of another predicate type is still refused by name.
func TestVerifyReadsAV1StatementAndRefusesAForeignOne(t *testing.T) {
	ctx := context.Background()
	h := verifyHarness(t)
	attested(t, h)

	files := statementsOnDisk(t, h)
	require.Len(t, files, 1)
	body, err := os.ReadFile(files[0])
	require.NoError(t, err)

	for predicate, readable := range map[string]bool{
		domain.PredicateTypeV1:           true,
		"https://slsa.dev/provenance/v1": false,
	} {
		rewritten := strings.Replace(string(body), domain.PredicateType, predicate, 1)
		require.NotEqual(t, string(body), rewritten)
		require.NoError(t, os.WriteFile(files[0], []byte(rewritten), 0o644))

		report, err := ops.AttestVerify(ctx, h.Deps, ops.VerifyOptions{})
		require.NoError(t, err)
		require.Len(t, report.Statements, 1)
		if readable {
			assert.Empty(t, report.Statements[0].Unreadable, "a v1 statement is no longer read")
		} else {
			assert.Contains(t, report.Statements[0].Unreadable, "predicate type")
		}
	}
}

// `init --repair` that stages the release already installed, with no signing
// keys to check it against, verified nothing -- and must not erase the check
// the original init recorded for that same release.
func TestARepairKeepsTheCheckRecordedForTheSameRelease(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.setHookEnv()
	realIdentity(t, h)
	h.Deps.Verifier = verify.NewChain(checksum.New(), minisign.New())

	src := filepath.Join(t.TempDir(), "bundle-1.2.0")
	copyBundle(t, testBundlePath(t), src)
	retargetManifest(t, src, h.Root)
	key := signBundle(t, src)

	first, err := ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, ReleasePath: src,
		RequireSignature: true, SigningKeys: []string{key},
	})
	require.NoError(t, err)

	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, ReleasePath: src, Repair: true,
	})
	require.NoError(t, err)

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	require.NotNil(t, current.SignatureVerified, "the repair erased the check the first init recorded")
	assert.Equal(t, first.Record.ID, current.SignatureVerified.OperationID)
}
