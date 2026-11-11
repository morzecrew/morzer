package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// Facts going back to the authority (RFC 0057 P5): the release's task runs
// after an operation, its output is encrypted to the authority and signed by
// the machine, and `state facts` reads it only once it checks out.

const relayFacts = "public_key=RELAY-PUBLIC-HALF\naddress=relay.example\n"

// factsRig is an authority rig whose machine publishes facts: sync.recipient
// is set, and the release's facts task prints relayFacts.
type factsRig struct {
	*authorityRig
	identity string
}

func newFactsRig(t *testing.T) *factsRig {
	t.Helper()
	a := newAuthorityRig(t)
	identity := filepath.Join(t.TempDir(), "authority.age")
	recipient, err := sopsage.GenerateIdentity(identity)
	require.NoError(t, err)
	a.h.Runtime.OneShotResults["app"] = ports.ExitResult{Stdout: relayFacts}
	require.NoError(t, setSettings(t, a.h, map[string]string{"sync.recipient": recipient}))
	return &factsRig{authorityRig: a, identity: identity}
}

func (f *factsRig) facts(t *testing.T) (ops.StateFactsReport, error) {
	t.Helper()
	return ops.StateFacts(context.Background(), f.h.Deps, ops.StateFactsOptions{
		TargetOptions: ops.TargetOptions{URL: f.target}, Roster: f.roster,
		Product: f.inst.Product, InstallationID: f.inst.ID, Identity: f.identity,
	})
}

func (f *factsRig) publishRow(t *testing.T) domain.FleetRow {
	t.Helper()
	_, err := ops.FleetPublish(context.Background(), f.h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)
	row, _ := publishedRow(t, f.offsite, f.inst)
	return row
}

// The task's output reaches the authority, through a row the roster's key
// verifies, and the facts kept on the machine are only ciphertext.
func TestAMachinesFactsReachTheAuthority(t *testing.T) {
	f := newFactsRig(t)
	state := f.state(t)
	require.NotNil(t, state.Facts, "setting sync.recipient made no facts")
	assert.Empty(t, state.Facts.Problem)
	assert.True(t, strings.HasPrefix(state.Facts.Digest, "sha256:"))

	kept, err := os.ReadFile(f.h.Paths.FactsFile())
	require.NoError(t, err)
	assert.NotContains(t, string(kept), "RELAY-PUBLIC-HALF", "the facts were kept in the clear")

	row := f.publishRow(t)
	assert.Equal(t, state.Facts.Digest, row.FactsDigest)
	report, err := f.facts(t)
	require.NoError(t, err)
	assert.Equal(t, relayFacts, report.Facts)
	assert.Equal(t, state.Facts.Digest, report.Digest)
}

// A run that fails keeps the previous facts, and the row says why with a
// class, never the task's output.
func TestAFailedFactsRunKeepsThePreviousFacts(t *testing.T) {
	f := newFactsRig(t)
	before := f.state(t).Facts.Digest

	for name, tc := range map[string]struct {
		result  ports.ExitResult
		problem string
	}{
		"exits non-zero":  {ports.ExitResult{ExitCode: 3, Stdout: "LEAKED-OUTPUT"}, "the facts task exited 3"},
		"prints too much": {ports.ExitResult{Stdout: strings.Repeat("x", domain.FactsLimit+1)}, "the facts task printed more than 64 KiB"},
	} {
		t.Run(name, func(t *testing.T) {
			f.h.Runtime.OneShotResults["app"] = tc.result
			_, err := ops.Apply(context.Background(), f.h.Deps, ops.Options{})
			require.NoError(t, err, "a failed facts run failed the operation before it")
			facts := f.state(t).Facts
			assert.Equal(t, before, facts.Digest, "the previous facts were replaced")
			assert.Equal(t, tc.problem, facts.Problem)

			row := f.publishRow(t)
			assert.Equal(t, before, row.FactsDigest)
			assert.Equal(t, facts.Problem, row.FactsProblem)
			_, body := publishedRow(t, f.offsite, f.inst)
			assert.NotContains(t, string(body), "LEAKED-OUTPUT")
			report, err := f.facts(t)
			require.NoError(t, err)
			assert.Equal(t, relayFacts, report.Facts)
		})
	}
	f.h.Runtime.OneShotResults["app"] = ports.ExitResult{Stdout: "public_key=NEW\n"}
	_, err := ops.Apply(context.Background(), f.h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Empty(t, f.state(t).Facts.Problem, "a run that succeeded left the old problem")
	assert.NotEqual(t, before, f.state(t).Facts.Digest)
}

// Without sync.recipient nothing runs and nothing is published, and the facts
// run is not notified as an operation of its own.
func TestFactsNeedARecipientAndAreNotNotified(t *testing.T) {
	a := newAuthorityRig(t)
	a.h.Runtime.OneShotResults["app"] = ports.ExitResult{Stdout: relayFacts}
	_, err := ops.Apply(context.Background(), a.h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Nil(t, a.state(t).Facts)
	_, err = os.Stat(a.h.Paths.FactsFile())
	assert.True(t, os.IsNotExist(err))

	f := newFactsRig(t)
	f.notified.events = nil
	_, err = ops.Apply(context.Background(), f.h.Deps, ops.Options{})
	require.NoError(t, err)
	var finished []domain.OperationType
	for _, ev := range f.notified.events {
		if ev.Kind == events.KindOperationFinished {
			finished = append(finished, ev.OpType)
		}
	}
	assert.Equal(t, []domain.OperationType{domain.OpTypeApply}, finished)
}

// Facts are used only when the machine's key signed them for this installation
// and they are the ones the newest verified row names (decision 28): an older
// pair put back, and facts a third party encrypted to the authority, are
// refused, and nothing is decrypted.
func TestStateFactsRefusesFactsTheRowDoesNotVouchFor(t *testing.T) {
	f := newFactsRig(t)
	f.publishRow(t)
	docKey, sigKey, err := domain.FactsKeys(f.inst.Product, f.inst.ID)
	require.NoError(t, err)
	docPath := filepath.Join(f.offsite, filepath.FromSlash(docKey))
	sigPath := filepath.Join(f.offsite, filepath.FromSlash(sigKey))
	oldDoc, err := os.ReadFile(docPath)
	require.NoError(t, err)
	oldSig, err := os.ReadFile(sigPath)
	require.NoError(t, err)

	f.h.Runtime.OneShotResults["app"] = ports.ExitResult{Stdout: "public_key=NEWER\n"}
	_, err = ops.Apply(context.Background(), f.h.Deps, ops.Options{})
	require.NoError(t, err)
	f.publishRow(t)

	// An older pair, correctly signed, put back.
	require.NoError(t, os.WriteFile(docPath, oldDoc, 0o644))
	require.NoError(t, os.WriteFile(sigPath, oldSig, 0o644))
	_, err = f.facts(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not the ones its row names")

	// Facts anybody could encrypt to the authority, signed with a key
	// that is not the machine's.
	other := signer.New(filepath.Join(t.TempDir(), "other.key"), "other")
	_, err = other.EnsureKey(context.Background())
	require.NoError(t, err)
	recipient, err := sopsage.PublicKeyFromIdentityFile(f.identity)
	require.NoError(t, err)
	var forged strings.Builder
	require.NoError(t, agecrypt.Encrypt(&forged, strings.NewReader("public_key=ATTACKER\n"), []string{recipient}))
	sig, err := other.Sign(context.Background(), []byte(forged.String()),
		domain.FactsComment(f.inst.Product, f.inst.ID))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(docPath, []byte(forged.String()), 0o644))
	require.NoError(t, os.WriteFile(sigPath, sig.Encoded, 0o644))
	report, err := f.facts(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not signed by the key the roster lists")
	assert.Empty(t, report.Facts)

	// The row's own facts, signed by the machine's key with a comment
	// naming another installation: the digest and the key both check out,
	// and only the comment says these are not this installation's.
	f.publishRow(t)
	genuine, err := os.ReadFile(docPath)
	require.NoError(t, err)
	elsewhere, err := f.h.Deps.Signer.Sign(context.Background(), genuine,
		domain.FactsComment(f.inst.Product, "inst_01OTHERINSTALLATION"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sigPath, elsewhere.Encoded, 0o644))
	_, err = f.facts(t)
	require.Error(t, err, "facts signed for another installation were read as this one's")
	assert.Contains(t, err.Error(), "not signed by the key the roster lists")
}

// No facts run follows an operation that failed: it changed nothing the facts
// describe.
func TestNoFactsRunFollowsAFailedOperation(t *testing.T) {
	f := newFactsRig(t)
	runs := len(f.h.Runtime.OneShots)
	f.h.Health.FailingWaits = 1
	_, err := ops.ConfigSet(context.Background(), f.h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"http_port": "9000"},
	})
	require.Error(t, err)
	assert.Len(t, f.h.Runtime.OneShots, runs, "the facts task ran after a failed operation")
}

// Facts kept on the machine that are not the ones its record names are not
// vouched for: the row names no digest until the next run replaces them.
func TestARowVouchesOnlyForTheFactsItsRecordNames(t *testing.T) {
	f := newFactsRig(t)
	require.NoError(t, os.WriteFile(f.h.Paths.FactsFile(), []byte("not these"), 0o600))
	assert.Empty(t, f.publishRow(t).FactsDigest)
}

// A facts run that finds another operation holding the lock does not wait for
// it, and records no problem: the next operation runs the task again.
func TestAFactsRunGivesWayToAHeldLock(t *testing.T) {
	f := newFactsRig(t)
	before := *f.state(t).Facts
	runs := len(f.h.Runtime.OneShots)
	// The configuration change takes the deployment lock first; the facts
	// task's is the second, after the setup above took it once.
	f.h.Locker.RefuseNth = map[string]int{"deployment": f.h.Locker.Acquired("deployment") + 2}
	_, err := ops.ConfigSet(context.Background(), f.h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"http_port": "9000"},
	})
	f.h.Locker.RefuseNth = nil
	require.NoError(t, err)
	assert.Len(t, f.h.Runtime.OneShots, runs, "the facts task ran without the lock")
	assert.Equal(t, before, *f.state(t).Facts, "a facts run that could not take the lock recorded a problem")
}

// `init` starts nothing, so it runs no facts task, which could start the
// services the task needs: the first apply or start makes the facts.
func TestInitRunsNoFactsTask(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.setHookEnv()
	realIdentity(t, h)
	src := filepath.Join(t.TempDir(), "bundle-1.2.0")
	copyBundle(t, testBundlePath(t), src)
	retargetManifest(t, src, h.Root)
	path := filepath.Join(src, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	patched := strings.Replace(string(raw), `min_manager_version: "0.0.0"`, `min_manager_version: "0.16.0"`, 1) +
		"\ntasks:\n  entry:\n    service: app\n    command: [relay-entry]\n\npublished:\n  task: entry\n"
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))
	h.Deps.Signer = signer.New(h.Paths.SigningKeyFile(), "demo")

	recipient, err := sopsage.GenerateIdentity(filepath.Join(t.TempDir(), "authority.age"))
	require.NoError(t, err)
	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, ReleasePath: src, SyncRecipient: recipient,
	})
	require.NoError(t, err)
	assert.Empty(t, h.Runtime.OneShots, "init ran the facts task")
	assert.Nil(t, installation(t, h).Sync.Facts)
}
