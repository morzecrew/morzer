package suite

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/verify"
	"github.com/morzecrew/morzer/internal/adapters/verify/checksum"
	"github.com/morzecrew/morzer/internal/adapters/verify/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// The trusted release keys change after `init` (RFC 0004 decision 24). Driven
// through the verifier production wires, because which keys are acceptable is
// the minisign parser's to say, and the suite's checksum-only verifier would
// accept anything.

func trustingMachine(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.install()
	h.Deps.Verifier = verify.NewChain(checksum.New(), minisign.New())
	return h
}

func trustedKeys(t *testing.T, h *harness) domain.Policy {
	t.Helper()
	inst, err := h.Deps.State.LoadInstallation(context.Background())
	require.NoError(t, err)
	return inst.Policy
}

// requireSignatures arms the machine as a production one following a channel
// would be: one trusted key, a signature required, and optionally auto-apply.
func requireSignatures(t *testing.T, h *harness, key string, autoApply bool) {
	t.Helper()
	ctx := context.Background()
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Policy.RequireSignature = true
	inst.Policy.SigningKeys = []string{key}
	inst.Update.AutoApply = autoApply
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
}

func TestAKeyIsTrustedAfterInit(t *testing.T) {
	h := trustingMachine(t)
	ctx := context.Background()
	_, key := minisignKeypair(t)

	plan, err := ops.TrustAdd(ctx, h.Deps, ops.Options{DryRun: true}, key)
	require.NoError(t, err)
	assert.Contains(t, plan.Summary, "would")
	assert.Empty(t, trustedKeys(t, h).SigningKeys, "a plan trusted a key")

	_, err = ops.TrustAdd(ctx, h.Deps, ops.Options{}, "  "+key+"\n")
	require.NoError(t, err)
	assert.Equal(t, []string{key}, trustedKeys(t, h).SigningKeys,
		"the key was not recorded, or recorded with the whitespace it was typed with")

	// A rotation is re-run; the step that already worked must not fail.
	again, err := ops.TrustAdd(ctx, h.Deps, ops.Options{}, key)
	require.NoError(t, err, "trusting a key twice is a no-op, not an error")
	assert.Contains(t, again.Summary, "already")
	assert.Len(t, trustedKeys(t, h).SigningKeys, 1, "the key was recorded twice")

	report, err := ops.TrustList(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, ops.TrustReport{Keys: []string{key}}, report)
}

// A key that does not parse is refused where it is typed, not at the first
// update that tries to verify a release with it.
func TestAKeyThatDoesNotParseIsNotTrusted(t *testing.T) {
	h := trustingMachine(t)
	_, key := minisignKeypair(t)
	typo := key[:len(key)-4]

	for _, bad := range []string{typo, "", "age1notaminisignkey"} {
		_, err := ops.TrustAdd(context.Background(), h.Deps, ops.Options{}, bad)
		require.Error(t, err, "%q was trusted", bad)
		assert.Equal(t, domain.ExitUsage, domain.ExitCode(err), "a malformed key is the operator's input, not an internal failure")
		assert.Contains(t, err.Error(), "not a valid minisign public key")
	}
	assert.Empty(t, trustedKeys(t, h).SigningKeys)
}

// The last key cannot go while a signature is required: nothing could be
// installed, and unattended updates require exactly that policy. Refused in a
// plan too, since a plan saying "would remove" would be a plan that lies.
func TestTheLastKeyStaysWhileASignatureIsRequired(t *testing.T) {
	for _, autoApply := range []bool{false, true} {
		h := trustingMachine(t)
		ctx := context.Background()
		_, old := minisignKeypair(t)
		requireSignatures(t, h, old, autoApply)

		for _, dryRun := range []bool{true, false} {
			_, err := ops.TrustRemove(ctx, h.Deps, ops.Options{DryRun: dryRun}, old)
			require.Error(t, err, "the only key was removed (auto_apply=%v, dry run=%v)", autoApply, dryRun)
			assert.Contains(t, err.Error(), "only trusted key")
			hint := domain.AsError(err).Hint
			assert.Contains(t, hint, "release trust add", "the refusal does not say what to do first")
			assert.Equal(t, autoApply, strings.Contains(hint, "update.auto_apply"),
				"the hint is wrong about what depends on the key")
		}
		assert.Equal(t, []string{old}, trustedKeys(t, h).SigningKeys)

		// The rotation the refusal points at.
		_, replacement := minisignKeypair(t)
		_, err := ops.TrustAdd(ctx, h.Deps, ops.Options{}, replacement)
		require.NoError(t, err)
		_, err = ops.TrustRemove(ctx, h.Deps, ops.Options{}, old)
		require.NoError(t, err)
		assert.Equal(t, []string{replacement}, trustedKeys(t, h).SigningKeys)
	}
}

// A machine that requires no signature may drop its last key; that turns
// signature checking off, which is the policy it already declared.
func TestTheLastKeyMayGoWhenNoSignatureIsRequired(t *testing.T) {
	h := trustingMachine(t)
	ctx := context.Background()
	_, key := minisignKeypair(t)
	_, err := ops.TrustAdd(ctx, h.Deps, ops.Options{}, key)
	require.NoError(t, err)

	plan, err := ops.TrustRemove(ctx, h.Deps, ops.Options{DryRun: true}, key)
	require.NoError(t, err)
	assert.Contains(t, plan.Summary, "would")
	assert.Equal(t, []string{key}, trustedKeys(t, h).SigningKeys, "a plan removed a key")

	_, err = ops.TrustRemove(ctx, h.Deps, ops.Options{}, key)
	require.NoError(t, err)
	assert.Empty(t, trustedKeys(t, h).SigningKeys)
}

// A typo in `remove` must not read as success while the old key stays trusted.
func TestRemovingAKeyThatIsNotTrustedNamesTheOnesThatAre(t *testing.T) {
	h := trustingMachine(t)
	ctx := context.Background()
	_, key := minisignKeypair(t)
	_, other := minisignKeypair(t)
	_, err := ops.TrustAdd(ctx, h.Deps, ops.Options{}, key)
	require.NoError(t, err)

	_, err = ops.TrustRemove(ctx, h.Deps, ops.Options{}, other)
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, key, "the refusal does not name the trusted key")
	assert.Equal(t, []string{key}, trustedKeys(t, h).SigningKeys)
}

// A key with whitespace around it -- a trailing newline from `$(cat key.pub)`,
// a pasted space -- still parses, so `init` stored it as typed, and `remove`,
// which trims what it is given, could never match it: the old key stayed
// trusted however often an operator dropped it, and `add` trusted the same key
// a second time. Keys are compared, and stored, as the parser reads them.
func TestAKeyStoredWithWhitespaceCanStillBeRemoved(t *testing.T) {
	ctx := context.Background()
	_, old := minisignKeypair(t)
	_, replacement := minisignKeypair(t)

	// As `init` wrote it until now, which an existing installation keeps.
	h := trustingMachine(t)
	requireSignatures(t, h, " "+old+"\n", false)

	_, err := ops.TrustAdd(ctx, h.Deps, ops.Options{}, old)
	require.NoError(t, err)
	assert.Len(t, trustedKeys(t, h).SigningKeys, 1, "the same key was trusted twice")

	_, err = ops.TrustAdd(ctx, h.Deps, ops.Options{}, replacement)
	require.NoError(t, err)
	_, err = ops.TrustRemove(ctx, h.Deps, ops.Options{}, old)
	require.NoError(t, err, "the key init stored could not be removed")
	assert.Equal(t, []string{replacement}, trustedKeys(t, h).SigningKeys)
}
