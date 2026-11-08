package suite

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	gominisign "github.com/jedisct1/go-minisign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/verify"
	"github.com/morzecrew/morzer/internal/adapters/verify/checksum"
	"github.com/morzecrew/morzer/internal/adapters/verify/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// A release may ship its vendor's key as minisign.pub. An `init` that creates
// an installation, given no key, trusts it once the release's signature
// verifies against it, and requires a signature from then on. After that the
// key a release ships is never trusted again (RFC 0052).

// vendor is a release signing key and its public half.
type vendor struct {
	sk  gominisign.PrivateKey
	pub string
}

func newVendor(t *testing.T) vendor {
	t.Helper()
	sk, pub := minisignKeypair(t)
	return vendor{sk: sk, pub: pub}
}

func (v vendor) id() string { return minisignID(v.sk) }

// What a release carries, the columns of RFC 0052's §5.2 table and the case
// it adds.
type shipped int

const (
	signedShippingItsKey shipped = iota
	signedShippingNoKey
	unsignedRelease
	shippingAKeyUnsigned
)

// firstUseMachine has nothing installed, and the verifier production wires.
func firstUseMachine(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.setHookEnv()
	realIdentity(t, h)
	h.Deps.Verifier = verify.NewChain(checksum.New(), minisign.New())
	return h
}

// releaseFrom makes a copy of a fixture bundle for h, signed by v or not, and
// shipping v's key or not.
func releaseFrom(t *testing.T, h *harness, fixture string, v vendor, what shipped) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), fixture)
	copyBundle(t, filepath.Join(testBundlePath(t), "..", fixture), src)
	retargetManifest(t, src, h.Root)
	switch what {
	case signedShippingItsKey:
		shipKey(t, src, v.pub)
		signWith(t, src, v.sk)
	case signedShippingNoKey:
		signWith(t, src, v.sk)
	case shippingAKeyUnsigned:
		shipKey(t, src, v.pub)
		writeSumsFile(t, src)
	}
	return src
}

func firstRelease(t *testing.T, h *harness, v vendor, what shipped) string {
	t.Helper()
	return releaseFrom(t, h, "bundle", v, what)
}

func initOptions(release string) ops.InitOptions {
	return ops.InitOptions{Product: "demo", NoRecoveryKey: true, ReleasePath: release}
}

// refusal is what an operator reads: the message and its hint.
func refusal(err error) string {
	return err.Error() + "\n" + domain.AsError(err).Hint
}

func messagesOf(h *harness) string {
	var out []string
	for _, e := range h.Events.OfKind(events.KindMessage) {
		out = append(out, e.Message)
	}
	return strings.Join(out, "\n")
}

// Every cell of the table, by the policy it records or the refusal it meets.
func TestTheFirstUseTable(t *testing.T) {
	type want struct {
		refused  string // a refusal containing this, or "" to install
		trusted  bool   // the vendor's key is trusted
		required bool
		warns    string // a message containing this
	}
	rows := map[string]func(o *ops.InitOptions, v vendor){
		"no flags":                    func(o *ops.InitOptions, v vendor) {},
		"--signing-key":               func(o *ops.InitOptions, v vendor) { o.SigningKeys = []string{v.pub} },
		"--signing-key, not required": func(o *ops.InitOptions, v vendor) { o.SigningKeys, o.SignatureNotRequired = []string{v.pub}, true },
		"--require-signature":         func(o *ops.InitOptions, v vendor) { o.RequireSignature = true },
		"--require-signature=false":   func(o *ops.InitOptions, v vendor) { o.SignatureNotRequired = true },
	}
	table := map[string]map[shipped]want{
		"no flags": {
			signedShippingItsKey: {trusted: true, required: true},
			signedShippingNoKey:  {warns: "ships no key, so its signature was not checked"},
			unsignedRelease:      {warns: "will not check the releases after it"},
			shippingAKeyUnsigned: {refused: "not signed"},
		},
		"--signing-key": {
			signedShippingItsKey: {trusted: true, required: true},
			signedShippingNoKey:  {trusted: true, required: true},
			unsignedRelease:      {refused: "--require-signature=false"},
			shippingAKeyUnsigned: {refused: "--require-signature=false"},
		},
		"--signing-key, not required": {
			signedShippingItsKey: {trusted: true},
			signedShippingNoKey:  {trusted: true},
			unsignedRelease:      {trusted: true},
			shippingAKeyUnsigned: {trusted: true},
		},
		"--require-signature": {
			signedShippingItsKey: {trusted: true, required: true},
			signedShippingNoKey:  {refused: "ships no key"},
			unsignedRelease:      {refused: "ships no key"},
			shippingAKeyUnsigned: {refused: "not signed"},
		},
		"--require-signature=false": {
			signedShippingItsKey: {},
			signedShippingNoKey:  {},
			unsignedRelease:      {},
			shippingAKeyUnsigned: {},
		},
	}
	columns := map[shipped]string{
		signedShippingItsKey: "signed, ships its key",
		signedShippingNoKey:  "signed, ships no key",
		unsignedRelease:      "unsigned",
		shippingAKeyUnsigned: "ships a key, unsigned",
	}

	for row, flags := range rows {
		for column, w := range table[row] {
			t.Run(row+"/"+columns[column], func(t *testing.T) {
				ctx := context.Background()
				h := firstUseMachine(t)
				v := newVendor(t)
				opts := initOptions(firstRelease(t, h, v, column))
				flags(&opts, v)

				_, err := ops.Init(ctx, h.Deps, opts)
				if w.refused != "" {
					require.Error(t, err)
					assert.Contains(t, refusal(err), w.refused)
					exists, err := h.Deps.State.InstallationExists(ctx)
					require.NoError(t, err)
					assert.False(t, exists, "a refused init recorded an installation")
					return
				}
				require.NoError(t, err)

				policy := trustedKeys(t, h)
				if w.trusted {
					assert.Equal(t, []string{v.pub}, policy.SigningKeys)
				} else {
					assert.Empty(t, policy.SigningKeys)
				}
				assert.Equal(t, w.required, policy.RequireSignature)
				if w.warns != "" {
					assert.Contains(t, messagesOf(h), w.warns)
				}
			})
		}
	}
}

// What a first use records beside the policy: the journal names the key's id
// and where it came from, JSON carries the key, and the release record says
// which key verified the release.
func TestAFirstUseSaysWhichKeyItTrustedAndWhereFrom(t *testing.T) {
	ctx := context.Background()
	h := firstUseMachine(t)
	v := newVendor(t)

	result, err := ops.Init(ctx, h.Deps, initOptions(firstRelease(t, h, v, signedShippingItsKey)))
	require.NoError(t, err)

	data, ok := result.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, v.pub, data["trusted_key"])
	assert.Equal(t, v.id(), data["trusted_key_id"])
	assert.Equal(t, "release", data["trusted_key_source"])
	assert.Equal(t, v.id(), result.Record.Flags["trusted_key_id"])
	assert.Equal(t, "release", result.Record.Flags["trusted_key_source"])

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	require.NotNil(t, current.SignatureVerified)
	assert.Equal(t, v.id(), current.SignatureVerified.KeyID)
}

// A release that ships one key and is signed by another is refused, whether
// the key is stale or somebody else signed it.
func TestAFirstUseRefusesAReleaseNotSignedByTheKeyItShips(t *testing.T) {
	ctx := context.Background()
	h := firstUseMachine(t)
	signer, stale := newVendor(t), newVendor(t)
	src := firstRelease(t, h, signer, signedShippingNoKey)
	shipKey(t, src, stale.pub)
	signWith(t, src, signer.sk)

	_, err := ops.Init(ctx, h.Deps, initOptions(src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not verify against the key it ships")
	assert.Contains(t, err.Error(), stale.id())
}

// The wizard reads a local bundle where it is, the stage step reads its own
// copy, and a key that changed in between is not the one the operator
// approved.
func TestAFirstUseRefusesAKeyOtherThanTheOneApproved(t *testing.T) {
	ctx := context.Background()
	h := firstUseMachine(t)
	shown, staged := newVendor(t), newVendor(t)
	opts := initOptions(firstRelease(t, h, staged, signedShippingItsKey))
	opts.ApprovedKey = &ports.ShippedKey{Key: shown.pub, ID: shown.id()}

	_, err := ops.Init(ctx, h.Deps, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not ship the key you approved, "+shown.id())
}

// And a key that appeared, where the wizard found none.
func TestAFirstUseRefusesAKeyWhereTheWizardFoundNone(t *testing.T) {
	ctx := context.Background()
	h := firstUseMachine(t)
	v := newVendor(t)
	opts := initOptions(firstRelease(t, h, v, signedShippingItsKey))
	opts.ApprovedKey = &ports.ShippedKey{}

	_, err := ops.Init(ctx, h.Deps, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ships a key, "+v.id()+", and shipped none")
}

// `--require-signature` with no release has no stage step to find a key in,
// and is still refused before anything is created.
func TestRequireSignatureWithNoReleaseIsRefusedUpFront(t *testing.T) {
	ctx := context.Background()
	h := firstUseMachine(t)

	_, err := ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, RequireSignature: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--require-signature needs at least one --signing-key")
	assert.Empty(t, h.Events.OfKind(events.KindStepStarted), "a step ran before the refusal")
}

// A repair of an installation that exists is not a first use: it verifies
// against the installation's policy, and never reads the key a release ships.
func TestARepairVerifiesAgainstThePolicyItFinds(t *testing.T) {
	repair := func(release string) ops.InitOptions {
		opts := initOptions(release)
		opts.Repair = true
		return opts
	}

	t.Run("a release signed by another key, shipping it, is refused", func(t *testing.T) {
		ctx := context.Background()
		h := firstUseMachine(t)
		trusted, other := newVendor(t), newVendor(t)
		_, err := ops.Init(ctx, h.Deps, initOptions(firstRelease(t, h, trusted, signedShippingItsKey)))
		require.NoError(t, err)

		_, err = ops.Init(ctx, h.Deps, repair(releaseFrom(t, h, "bundle-1.3.0", other, signedShippingItsKey)))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not verify against any configured signing key")
		assert.Equal(t, []string{trusted.pub}, trustedKeys(t, h).SigningKeys)
	})

	t.Run("an unsigned release is refused where the policy requires a signature", func(t *testing.T) {
		ctx := context.Background()
		h := firstUseMachine(t)
		v := newVendor(t)
		_, err := ops.Init(ctx, h.Deps, initOptions(firstRelease(t, h, v, signedShippingItsKey)))
		require.NoError(t, err)

		_, err = ops.Init(ctx, h.Deps, repair(releaseFrom(t, h, "bundle-1.3.0", v, unsignedRelease)))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires a signature")
	})

	t.Run("an installation without keys stays without them", func(t *testing.T) {
		ctx := context.Background()
		h := firstUseMachine(t)
		v := newVendor(t)
		opts := initOptions(firstRelease(t, h, v, unsignedRelease))
		_, err := ops.Init(ctx, h.Deps, opts)
		require.NoError(t, err)

		_, err = ops.Init(ctx, h.Deps, repair(releaseFrom(t, h, "bundle-1.3.0", v, signedShippingItsKey)))
		require.NoError(t, err)
		assert.Empty(t, trustedKeys(t, h).SigningKeys, "a repair trusted the key the release ships")
		assert.False(t, trustedKeys(t, h).RequireSignature)
	})

	t.Run("with nothing installed, it trusts the shipped key as init does", func(t *testing.T) {
		ctx := context.Background()
		h := firstUseMachine(t)
		v := newVendor(t)

		_, err := ops.Init(ctx, h.Deps, repair(firstRelease(t, h, v, signedShippingItsKey)))
		require.NoError(t, err)
		assert.Equal(t, []string{v.pub}, trustedKeys(t, h).SigningKeys)
		assert.True(t, trustedKeys(t, h).RequireSignature)
	})
}

// updateMachine runs 1.2.0, trusting no key, with the verifier production
// wires. A first use leaves the policy requireSignatures sets.
func updateMachine(t *testing.T) *harness {
	t.Helper()
	h := trustingMachine(t)
	h.setHookEnv()
	applyBaseline(t, h)
	return h
}

// After the first install the key a release ships is never trusted: an update
// signed by another key it ships is refused by that key's id, and an
// installation without keys is told about a shipped key and stays without.
func TestAnUpdateNeverTrustsTheKeyAReleaseShips(t *testing.T) {
	t.Run("signed by an untrusted key it ships, refused by its id", func(t *testing.T) {
		ctx := context.Background()
		h := updateMachine(t)
		trusted, other := newVendor(t), newVendor(t)
		requireSignatures(t, h, trusted.pub, false)

		_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{
			Ref: releaseFrom(t, h, "bundle-1.3.0", other, signedShippingItsKey),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "signed by the key it ships, "+other.id())
		assert.Contains(t, refusal(err), "release trust add")
		assert.NotContains(t, refusal(err), other.pub, "the refusal printed the key it refused")
		assert.Equal(t, []string{trusted.pub}, trustedKeys(t, h).SigningKeys)
	})

	t.Run("an installation without keys gets a notice and keeps none", func(t *testing.T) {
		ctx := context.Background()
		h := updateMachine(t)
		v := newVendor(t)

		_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{
			Ref: releaseFrom(t, h, "bundle-1.3.0", v, signedShippingItsKey),
		})
		require.NoError(t, err)
		said := messagesOf(h)
		assert.Contains(t, said, "ships a signing key, "+v.id())
		assert.Contains(t, said, "config set release.require_signature=true")
		assert.NotContains(t, said, v.pub, "the notice printed the key")
		assert.Empty(t, trustedKeys(t, h).SigningKeys, "an update trusted the key the release ships")
		assert.False(t, trustedKeys(t, h).RequireSignature)
	})
}

// A plan says what the stage step would trust, and only where it would trust
// one: the key of a local bundle, a sentence for a remote one, nothing after
// --signing-key or on a repair.
func TestAPlanSaysWhichKeyAFirstUseWouldTrust(t *testing.T) {
	stageLine := func(t *testing.T, h *harness) string {
		t.Helper()
		plans := h.Events.OfKind(events.KindPlan)
		require.Len(t, plans, 1)
		for _, step := range plans[0].Plan {
			if step.ID == "stage-release" {
				return step.Description
			}
		}
		t.Fatal("the plan has no stage-release step")
		return ""
	}
	plan := func(opts ops.InitOptions) ops.InitOptions {
		opts.DryRun = true
		return opts
	}

	t.Run("a local bundle's key", func(t *testing.T) {
		h := firstUseMachine(t)
		v := newVendor(t)
		result, err := ops.Init(context.Background(), h.Deps, plan(initOptions(firstRelease(t, h, v, signedShippingItsKey))))
		require.NoError(t, err)

		line := stageLine(t, h)
		assert.Contains(t, line, v.id())
		assert.Contains(t, line, v.pub)
		data, _ := result.Data.(map[string]any)
		assert.Equal(t, v.id(), data["trusted_key_id"])
		exists, err := h.Deps.State.InstallationExists(context.Background())
		require.NoError(t, err)
		assert.False(t, exists, "a plan recorded an installation")
	})

	t.Run("a remote bundle's, not fetched", func(t *testing.T) {
		h := firstUseMachine(t)
		_, err := ops.Init(context.Background(), h.Deps, plan(initOptions("oci://registry.invalid/demo:1.2.0")))
		require.NoError(t, err)

		assert.Contains(t, stageLine(t, h), "if it ships one")
	})

	t.Run("nothing after --signing-key", func(t *testing.T) {
		h := firstUseMachine(t)
		v := newVendor(t)
		opts := plan(initOptions(firstRelease(t, h, v, signedShippingItsKey)))
		opts.SigningKeys = []string{v.pub}
		result, err := ops.Init(context.Background(), h.Deps, opts)
		require.NoError(t, err)

		assert.Equal(t, "stage release bundle", stageLine(t, h))
		data, _ := result.Data.(map[string]any)
		assert.NotContains(t, data, "trusted_key")
	})

	t.Run("nothing on a repair of an installation that exists", func(t *testing.T) {
		ctx := context.Background()
		h := firstUseMachine(t)
		v := newVendor(t)
		_, err := ops.Init(ctx, h.Deps, initOptions(firstRelease(t, h, v, unsignedRelease)))
		require.NoError(t, err)
		h.Events.Reset()

		opts := plan(initOptions(firstRelease(t, h, v, signedShippingItsKey)))
		opts.Repair = true
		_, err = ops.Init(ctx, h.Deps, opts)
		require.NoError(t, err)
		assert.Equal(t, "stage release bundle", stageLine(t, h))
	})

	t.Run("refused where the stage step would refuse", func(t *testing.T) {
		h := firstUseMachine(t)
		v := newVendor(t)
		_, err := ops.Init(context.Background(), h.Deps, plan(initOptions(firstRelease(t, h, v, shippingAKeyUnsigned))))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not signed")
	})
}

// impostorOf is another key carrying v's id. A minisign key id is eight bytes
// whoever makes the key chooses, so it names a key without proving one.
func impostorOf(t *testing.T, v vendor) vendor {
	t.Helper()
	other := newVendor(t)
	other.sk.KeyId = v.sk.KeyId
	pk := other.sk.PublicKey()
	blob := append(append(append([]byte{}, pk.SignatureAlgorithm[:]...), pk.KeyId[:]...), pk.PublicKey[:]...)
	other.pub = base64.StdEncoding.EncodeToString(blob)
	require.NotEqual(t, v.pub, other.pub)
	require.Equal(t, v.id(), other.id())
	return other
}

func TestAFirstUseRefusesAnotherKeyWithTheApprovedID(t *testing.T) {
	ctx := context.Background()
	h := firstUseMachine(t)
	shown := newVendor(t)
	impostor := impostorOf(t, shown)
	opts := initOptions(firstRelease(t, h, impostor, signedShippingItsKey))
	opts.ApprovedKey = &ports.ShippedKey{Key: shown.pub, ID: shown.id()}

	_, err := ops.Init(ctx, h.Deps, opts)
	require.Error(t, err, "a key with the approved id, and not the approved key, was trusted")
	assert.Contains(t, err.Error(), "does not ship the key you approved")
}

// lockedAfter runs then while the lock is being taken, as another operation
// holding it would have before letting go.
type lockedAfter struct {
	ports.Locker
	then func()
}

func (l lockedAfter) Acquire(ctx context.Context, name string, opts ports.LockOptions) (func() error, error) {
	l.then()
	return l.Locker.Acquire(ctx, name, opts)
}

func TestARepairVerifiesAgainstThePolicyAsItIsUnderTheLock(t *testing.T) {
	ctx := context.Background()
	h := firstUseMachine(t)
	old, current := newVendor(t), newVendor(t)
	opts := initOptions(firstRelease(t, h, old, signedShippingItsKey))
	opts.SigningKeys = []string{old.pub, current.pub}
	_, err := ops.Init(ctx, h.Deps, opts)
	require.NoError(t, err)

	// `release trust remove <old>` finishes while the repair waits.
	h.Deps.Locker = lockedAfter{Locker: h.Deps.Locker, then: func() {
		inst, err := h.Deps.State.LoadInstallation(ctx)
		require.NoError(t, err)
		inst.Policy.SigningKeys = []string{current.pub}
		require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	}}
	repair := initOptions(releaseFrom(t, h, "bundle-1.3.0", old, signedShippingItsKey))
	repair.Repair = true

	_, err = ops.Init(ctx, h.Deps, repair)
	require.Error(t, err, "the repair staged a release signed by a key removed before it took the lock")
	assert.Contains(t, err.Error(), "does not verify against any configured signing key")
}
