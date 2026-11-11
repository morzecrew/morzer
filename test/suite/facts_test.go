package suite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	kept, err := os.ReadFile(f.h.Paths.FactsFile(state.Facts.Digest))
	require.NoError(t, err)
	assert.NotContains(t, string(kept), "RELAY-PUBLIC-HALF", "the facts were kept in the clear")

	row := f.publishRow(t)
	assert.Equal(t, state.Facts.Digest, row.FactsDigest)
	report, err := f.facts(t)
	require.NoError(t, err)
	assert.Equal(t, relayFacts, report.Facts)
	assert.Equal(t, state.Facts.Digest, report.Digest)
	assert.Equal(t, row.PublishedAt, report.RowPublishedAt)
	assert.False(t, report.Stale)

	// A row put back from two days ago passes every check, and its age is
	// how it shows.
	f.h.Deps.Now = func() time.Time { return row.PublishedAt.Add(48 * time.Hour) }
	report, err = f.facts(t)
	require.NoError(t, err)
	assert.True(t, report.Stale, "a two-day-old row was not called stale")
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
	matches, err := filepath.Glob(a.h.Paths.FactsFilePattern())
	require.NoError(t, err)
	assert.Empty(t, matches)

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
	require.NoError(t, os.WriteFile(f.h.Paths.FactsFile(f.state(t).Facts.Digest), []byte("not these"), 0o600))
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

// Facts are published only to the recipient they were encrypted to: cleared,
// nothing is published; changed, the old facts are not offered to the new
// recipient, even when the next run fails.
func TestFactsGoOnlyToTheRecipientTheyWereMadeFor(t *testing.T) {
	f := newFactsRig(t)
	_, err := ops.SetSettings(context.Background(), f.h.Deps, ops.SetSettingsOptions{Unset: []string{"sync.recipient"}})
	require.NoError(t, err)
	assert.Empty(t, f.publishRow(t).FactsDigest, "facts were published after sync.recipient was cleared")

	f = newFactsRig(t)
	f.h.Runtime.OneShotResults["app"] = ports.ExitResult{ExitCode: 1}
	other, err := sopsage.GenerateIdentity(filepath.Join(t.TempDir(), "other.age"))
	require.NoError(t, err)
	require.NoError(t, setSettings(t, f.h, map[string]string{"sync.recipient": other}))
	assert.Empty(t, f.publishRow(t).FactsDigest, "facts made for the old recipient were offered to the new one")
}

// What a facts run made is recorded only if the recipient it encrypted to is
// still the one configured when it commits, and only one run commits at a
// time.
func TestAFactsRunCommitsOnlyWhatIsStillCurrent(t *testing.T) {
	f := newFactsRig(t)
	before := f.state(t).Facts.Digest
	base := f.h.Locker.Acquired("deployment")
	other, err := sopsage.GenerateIdentity(filepath.Join(t.TempDir(), "other.age"))
	require.NoError(t, err)
	// The change takes the lock, then the facts task, then the commit:
	// the recipient changes just before the commit.
	f.h.Locker.BeforeAcquire = func() {
		if f.h.Locker.Acquired("deployment") == base+2 {
			inst := installation(t, f.h)
			inst.Sync.Recipient = other
			require.NoError(t, f.h.Deps.State.SaveInstallation(context.Background(), inst))
		}
	}
	f.h.Runtime.OneShotResults["app"] = ports.ExitResult{Stdout: "public_key=FOR-THE-OLD-RECIPIENT\n"}
	_, err = ops.ConfigSet(context.Background(), f.h.Deps, ops.ConfigSetOptions{Set: map[string]string{"http_port": "9000"}})
	f.h.Locker.BeforeAcquire = nil
	require.NoError(t, err)
	if facts := f.state(t).Facts; facts != nil {
		assert.Equal(t, before, facts.Digest, "facts made for a recipient no longer configured were recorded")
	}

	// The same for a release replaced while the task ran.
	r := newFactsRig(t)
	beforeR := r.state(t).Facts.Digest
	baseR := r.h.Locker.Acquired("deployment")
	r.h.Locker.BeforeAcquire = func() {
		if r.h.Locker.Acquired("deployment") == baseR+2 {
			current, err := r.h.Deps.State.CurrentRelease(context.Background())
			require.NoError(t, err)
			current.Digest = "sha256:another-release"
			require.NoError(t, r.h.Deps.State.SetCurrentRelease(context.Background(), current))
		}
	}
	r.h.Runtime.OneShotResults["app"] = ports.ExitResult{Stdout: "public_key=FOR-THE-OLD-RELEASE\n"}
	_, err = ops.ConfigSet(context.Background(), r.h.Deps, ops.ConfigSetOptions{Set: map[string]string{"http_port": "9000"}})
	r.h.Locker.BeforeAcquire = nil
	require.NoError(t, err)
	assert.Equal(t, beforeR, r.state(t).Facts.Digest, "facts made under a release no longer installed were recorded")

	// A refresh already under way holds the facts lock, and a second one
	// gives way to it.
	g := newFactsRig(t)
	beforeG := g.state(t).Facts.Digest
	g.h.Locker.RefuseNth = map[string]int{"facts": g.h.Locker.Acquired("facts") + 1}
	g.h.Runtime.OneShotResults["app"] = ports.ExitResult{Stdout: "public_key=SECOND\n"}
	_, err = ops.Apply(context.Background(), g.h.Deps, ops.Options{})
	g.h.Locker.RefuseNth = nil
	require.NoError(t, err)
	assert.Equal(t, beforeG, g.state(t).Facts.Digest, "a facts run did not give way to one under way")
}

// The facts run is not what the row reports as the machine's last operation.
func TestTheFactsRunIsNotTheRowsLastOperation(t *testing.T) {
	f := newFactsRig(t)
	// Distinct times, so the journal's order is the order things ran.
	f.h.Deps.Now = steppingClock()
	_, err := ops.Apply(context.Background(), f.h.Deps, ops.Options{})
	require.NoError(t, err)
	row := f.publishRow(t)
	require.NotNil(t, row.LastOperation)
	assert.Equal(t, string(domain.OpTypeApply), row.LastOperation.Kind)
}

// A target that refuses the facts gets no row naming them: the row it already
// has stays, consistent with the facts beside it, and the failure is named as
// the facts'.
func TestARowIsNotPublishedBesideFactsThatFailedToWrite(t *testing.T) {
	f := newFactsRig(t)
	objects := f.h.Deps.Objects
	f.h.Deps.Objects = putFailing{ObjectStore: objects, suffix: "facts.age"}
	result, err := ops.FleetPublish(context.Background(), f.h.Deps, ops.FleetPublishOptions{})
	f.h.Deps.Objects = objects
	require.NoError(t, err)
	report, ok := result.Data.(ops.FleetPublishReport)
	require.True(t, ok)
	require.Len(t, report.Targets, 1)
	assert.False(t, report.Targets[0].Published, "a row naming facts that did not reach the target was published")
	assert.NotEmpty(t, report.Targets[0].FactsError)
	assert.Contains(t, result.Summary, "facts")
}

// saveFailing is a state store whose next installation save fails, once armed.
type saveFailing struct {
	ports.StateStore
	armed *bool
}

func (s saveFailing) SaveInstallation(ctx context.Context, inst domain.Installation) error {
	if *s.armed {
		*s.armed = false
		return assert.AnError
	}
	return s.StateStore.SaveInstallation(ctx, inst)
}

// The previous facts survive a run whose record cannot be saved: the new pair
// is kept beside the old one until the record naming it commits.
func TestThePreviousFactsSurviveARecordThatCannotBeSaved(t *testing.T) {
	f := newFactsRig(t)
	before := f.state(t).Facts.Digest
	armed := false
	store := f.h.Deps.State
	f.h.Deps.State = saveFailing{StateStore: store, armed: &armed}
	base := f.h.Locker.Acquired("deployment")
	// The change takes the lock, then the facts task, then the commit,
	// whose save is the one that fails.
	f.h.Locker.BeforeAcquire = func() {
		if f.h.Locker.Acquired("deployment") == base+2 {
			armed = true
		}
	}
	f.h.Runtime.OneShotResults["app"] = ports.ExitResult{Stdout: "public_key=NEVER-RECORDED\n"}
	_, err := ops.ConfigSet(context.Background(), f.h.Deps, ops.ConfigSetOptions{Set: map[string]string{"http_port": "9000"}})
	f.h.Locker.BeforeAcquire, f.h.Deps.State = nil, store
	require.NoError(t, err)

	assert.Equal(t, before, f.state(t).Facts.Digest)
	assert.Equal(t, before, f.publishRow(t).FactsDigest, "the previous facts were lost to a record that did not save")
	report, err := f.facts(t)
	require.NoError(t, err)
	assert.Equal(t, relayFacts, report.Facts)
}

// The row's last operation is found past any number of facts runs, such as
// those after settings that run no operation of their own.
func TestTheLastOperationIsFoundPastManyFactsRuns(t *testing.T) {
	f := newFactsRig(t)
	f.h.Deps.Now = steppingClock()
	for i := 0; i < 10; i++ {
		require.NoError(t, setSettings(t, f.h, map[string]string{"sync.interval": fmt.Sprintf("%dm", 20+i)}))
	}
	row := f.publishRow(t)
	require.NotNil(t, row.LastOperation, "ten facts runs hid the last operation")
	assert.NotEqual(t, string(domain.OpTypeTask), row.LastOperation.Kind)
}

// A target that takes the new ciphertext and refuses its signature holds facts
// the row does not vouch for: `state facts` refuses them rather than misread
// them, and the next publish writes both.
func TestHalfWrittenFactsAreRefusedUntilTheNextPublish(t *testing.T) {
	f := newFactsRig(t)
	f.publishRow(t)
	f.h.Runtime.OneShotResults["app"] = ports.ExitResult{Stdout: "public_key=NEWER\n"}
	_, err := ops.Apply(context.Background(), f.h.Deps, ops.Options{})
	require.NoError(t, err)

	objects := f.h.Deps.Objects
	f.h.Deps.Objects = putFailing{ObjectStore: objects, suffix: "facts.age.minisig"}
	_, err = ops.FleetPublish(context.Background(), f.h.Deps, ops.FleetPublishOptions{})
	f.h.Deps.Objects = objects
	require.NoError(t, err)
	report, err := f.facts(t)
	require.Error(t, err, "half-written facts were read")
	assert.Empty(t, report.Facts)

	f.publishRow(t)
	report, err = f.facts(t)
	require.NoError(t, err)
	assert.Equal(t, "public_key=NEWER\n", report.Facts)
}
