package suite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// `attest verify`, and the case RFC 0025 decision 8 makes the design
// conditional on.
//
// The decision is unusually blunt: `--against-live` must have a fault-injection
// case that makes it fail before P3 can be called complete, and if none can be
// constructed the RFC closes as rejected. A verifier that can only pass proves
// nothing, and the RFC's own §2 calls that the theatre it is most at risk of.
//
// So the test below is not a nicety. It is the condition on which this feature
// exists, written as an assertion.

func verifyHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.Deps.Signer = signer.New(h.Paths.SigningKeyFile(), "demo")
	h.Deps.Checker = signer.NewChecker()

	// What the services run, as the real adapter would report after an
	// apply: the references the manifest pins. Without this the fake
	// reports no image at all, and every comparison reads as "nothing is
	// running" rather than as agreement.
	for name, spec := range h.Release.Manifest.Images {
		h.Runtime.Images[name] = spec.Ref
	}
	return h
}

// attested runs one apply so there is a statement to verify.
func attested(t *testing.T, h *harness) domain.Installation {
	t.Helper()
	inst := h.install()
	key, err := h.Deps.Signer.EnsureKey(context.Background())
	require.NoError(t, err)
	inst.Signing.PublicKey = key.Line
	inst.AttestationSalt = "0123456789abcdef0123456789abcdef"
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	_, err = ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	return inst
}

// statementsOnDisk lists the statements this installation has filed.
func statementsOnDisk(t *testing.T, h *harness) []string {
	t.Helper()
	entries, err := os.ReadDir(h.Paths.AttestationsDir())
	require.NoError(t, err)

	var out []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			out = append(out, filepath.Join(h.Paths.AttestationsDir(), e.Name()))
		}
	}
	return out
}

func TestVerifyAcceptsWhatThisMachineSigned(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	report, err := ops.AttestVerify(context.Background(), h.Deps, ops.VerifyOptions{})
	require.NoError(t, err)

	require.NotEmpty(t, report.Statements)
	for _, s := range report.Statements {
		assert.Equal(t, domain.SignedByCurrentKey, s.Signature.Outcome, s.File)
	}
	assert.Empty(t, report.Chain)
	assert.Zero(t, report.Problems())
}

// An edited statement is unverifiable. Without this the test above passes for a
// verifier that checks nothing.
func TestVerifyRefusesAnEditedStatement(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	files, err := os.ReadDir(h.Paths.AttestationsDir())
	require.NoError(t, err)
	var doc string
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".json" {
			doc = filepath.Join(h.Paths.AttestationsDir(), f.Name())
		}
	}
	require.NotEmpty(t, doc)

	body, err := os.ReadFile(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(doc, append(body, ' '), 0o644))

	report, err := ops.AttestVerify(context.Background(), h.Deps, ops.VerifyOptions{})
	require.NoError(t, err)
	require.Len(t, report.Statements, 1)
	assert.Equal(t, domain.Unverifiable, report.Statements[0].Signature.Outcome)
	assert.Positive(t, report.Problems())
}

// A signature from a key this installation has retired is its own outcome.
//
// RFC 0028 decision 10 written as a test: a verifier that reported this
// identically to a current-key signature would pass every other test in this
// file, and would also accept whatever a stolen key signs after a rotation --
// which is the one case rotation exists for.
func TestASignatureFromARetiredKeyIsItsOwnOutcome(t *testing.T) {
	h := verifyHarness(t)
	inst := attested(t, h)

	// The machine rotates: what signed the statement becomes a predecessor,
	// and a new key takes its place.
	rotated := inst.SucceedSigning(domain.NewTime(time.Now()), domain.RetiredByRotation)
	rotated.Signing.PublicKey = "RWTaKeyThisMachineNowSignsWithButDidNotThen0000000000000"
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), rotated))

	report, err := ops.AttestVerify(context.Background(), h.Deps, ops.VerifyOptions{})
	require.NoError(t, err)
	require.Len(t, report.Statements, 1)

	got := report.Statements[0].Signature
	assert.Equal(t, domain.SignedByPredecessor, got.Outcome,
		"a retired key's signature must not report as the current key's")
	assert.Equal(t, inst.Signing.PublicKey, got.Key)
	assert.Equal(t, domain.RetiredByRotation, got.Reason)

	// And it is not a problem: a rotated machine has a normal history.
	assert.Zero(t, report.Problems())
}

// An unsigned statement is distinct from an unverifiable one, because the
// remedies are entirely different.
func TestAnUnsignedStatementIsNotAnUnverifiableOne(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	files, err := os.ReadDir(h.Paths.AttestationsDir())
	require.NoError(t, err)
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".minisig" {
			require.NoError(t, os.Remove(filepath.Join(h.Paths.AttestationsDir(), f.Name())))
		}
	}

	report, err := ops.AttestVerify(context.Background(), h.Deps, ops.VerifyOptions{})
	require.NoError(t, err)
	require.Len(t, report.Statements, 1)
	assert.Equal(t, domain.Unsigned, report.Statements[0].Signature.Outcome)
	assert.Zero(t, report.Problems(), "an unsigned record is a gap, not a finding")
}

// TestAgainstLiveFailsWhenAnImageWasSwappedByHand is decision 8.
//
// The whole RFC is conditional on this test existing and failing for a reason
// nobody planted in the verifier: the deployment is changed *behind* the
// manager, and the check notices.
func TestAgainstLiveFailsWhenAnImageWasSwappedByHand(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	// Clean first: the deployment as the manager left it must verify, or a
	// failure below would prove nothing about the swap.
	clean, err := ops.AttestVerify(context.Background(), h.Deps,
		ops.VerifyOptions{AgainstLive: true})
	require.NoError(t, err)
	require.True(t, clean.LiveChecked)
	require.Empty(t, clean.Live,
		"the deployment the manager just applied does not match its own statement")

	// Somebody pulls a different image and restarts the container by hand.
	// The manager is not involved and files no statement, which is exactly
	// the situation an audit is trying to detect.
	h.Runtime.SwapImage("app", "registry.example/demo/app@sha256:"+
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	report, err := ops.AttestVerify(context.Background(), h.Deps,
		ops.VerifyOptions{AgainstLive: true})
	require.NoError(t, err)

	require.NotEmpty(t, report.Live,
		"an image swapped by hand did not make --against-live fail; "+
			"RFC 0025 decision 8 closes the design as rejected if this cannot be constructed")
	assert.Equal(t, "image", report.Live[0].Kind)
	assert.Positive(t, report.Problems())
}

// And the other direction: a service the statement attested that is no longer
// running.
func TestAgainstLiveNoticesAnAttestedServiceThatIsGone(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	h.Runtime.RemoveService("app")

	report, err := ops.AttestVerify(context.Background(), h.Deps,
		ops.VerifyOptions{AgainstLive: true})
	require.NoError(t, err)

	require.NotEmpty(t, report.Live)
	assert.Equal(t, "missing", report.Live[0].Kind)
}

// An update files a statement that joins the chain.
//
// This is what makes `attest verify`'s chain check mean anything on a real
// machine. Only version-moving operations participate, and `apply` moves
// nothing -- so until `update` attested, the chain check was exercised by unit
// tests over synthetic statements and was inert in production. A check for data
// nothing produces is not a check.
func TestAnUpdateFilesAStatementThatJoinsTheChain(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	before := len(statementsOnDisk(t, h))

	// A real second version, retargeted the way every other update test
	// retargets it, so what is installed is a release this machine could
	// really run.
	next := filepath.Join(t.TempDir(), "bundle-1.3.0")
	copyBundle(t, filepath.Join(testBundlePath(t), "..", "bundle-1.3.0"), next)
	retargetManifest(t, next, h.Root)

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{Ref: next})
	require.NoError(t, err)

	files := statementsOnDisk(t, h)
	require.Greater(t, len(files), before, "the update filed no statement")

	report, err := ops.AttestVerify(context.Background(), h.Deps, ops.VerifyOptions{})
	require.NoError(t, err)

	// The update's statement carries a from_version, which is what the
	// chain is built out of -- an apply's never does.
	var moved int
	for _, f := range files {
		body, err := os.ReadFile(f)
		require.NoError(t, err)
		var s domain.Statement
		require.NoError(t, json.Unmarshal(body, &s))
		if s.Predicate.Release.FromVersion != "" {
			moved++
		}
	}
	assert.Positive(t, moved, "no statement records the version it moved from, so the chain check has nothing to follow")

	assert.Empty(t, report.Chain, "a straightforward update broke its own chain")
	assert.Zero(t, report.Problems())
}
