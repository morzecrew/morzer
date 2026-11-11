package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/adapters/target"
	"github.com/morzecrew/morzer/internal/adapters/target/localdir"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// A machine following an authority's desired state (RFC 0057 P3). The
// authority here is a key and a directory: what `state publish` will write,
// written by hand, so each check in the cycle can be handed the one document
// that fails it.

// syncRig is a running machine that follows a test authority, on a release
// that lets log_level, db_password and installation.domains be managed.
type syncRig struct {
	h         *harness
	inst      domain.Installation
	offsite   string
	authority *signer.Signer
	key       string
	recipient string
	notified  *recordingNotifier

	// write puts one object where the machine reads: a file under offsite,
	// unless a test points the machine at another target.
	write func(t *testing.T, key string, data []byte)
}

func newSyncRig(t *testing.T) *syncRig {
	t.Helper()
	return newSyncRigFor(t, "demo")
}

func newSyncRigFor(t *testing.T, product string) *syncRig {
	t.Helper()
	ctx := context.Background()
	h := verifyHarnessFor(t, product)

	path := filepath.Join(h.Release.Root, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	floor := `min_manager_version: "0.0.0"`
	require.Contains(t, string(raw), floor)
	patched := strings.Replace(string(raw), floor, `min_manager_version: "0.16.0"`, 1) +
		"\nmanaged:\n  parameters: [log_level]\n  secrets: [db_password]\n  settings: [installation.domains]\n" +
		"\ntasks:\n  entry:\n    service: app\n    command: [relay-entry]\n" +
		"\npublished:\n  task: entry\n"
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))
	h.Release, err = release.Load(h.Release.Root)
	require.NoError(t, err)

	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	h.Deps.Targets, h.Deps.Objects = registry, registry

	signingInstallation(t, h)
	h.setHookEnv()
	applyBaseline(t, h)

	r := &syncRig{h: h, offsite: filepath.Join(t.TempDir(), "offsite"), notified: &recordingNotifier{}}
	r.write = func(t *testing.T, key string, data []byte) {
		t.Helper()
		path := filepath.Join(r.offsite, filepath.FromSlash(key))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, data, 0o644))
	}
	h.Deps.Notifier = r.notified
	r.recipient, err = sopsage.GenerateIdentity(h.Paths.AgeIdentityFile())
	require.NoError(t, err)
	r.authority = signer.New(filepath.Join(t.TempDir(), "authority.key"), "authority")
	pub, err := r.authority.EnsureKey(ctx)
	require.NoError(t, err)
	r.key = pub.Line

	r.inst = installation(t, h)
	r.inst.Sync = domain.SyncConfig{Target: "file://" + r.offsite, Authority: r.key, Enabled: true}
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, r.inst))
	return r
}

// envelope is a document's header, valid at the harness's clock.
func (r *syncRig) envelope(serial int64) string {
	return fmt.Sprintf("api_version: selfhost/v1alpha1\nkind: desired-state\nproduct: %s\ninstallation: %s\n"+
		"serial: %d\nissued_at: 2026-08-03T11:00:00Z\nexpires_at: 2026-08-10T11:00:00Z\n",
		r.inst.Product, r.inst.ID, serial)
}

// publish writes serial as the authority would: encrypted to this machine,
// the ciphertext signed with a comment naming it.
func (r *syncRig) publish(t *testing.T, serial int64, body string) {
	t.Helper()
	r.put(t, serial, r.seal(t, r.envelope(serial)+body, r.recipient), r.authority,
		domain.DesiredStateComment(r.inst.Product, r.inst.ID, serial))
}

func (r *syncRig) seal(t *testing.T, doc, recipient string) []byte {
	t.Helper()
	var sealed bytes.Buffer
	require.NoError(t, agecrypt.Encrypt(&sealed, strings.NewReader(doc), []string{recipient}))
	return sealed.Bytes()
}

// put writes a pair as given, signed by by with comment. A nil by writes the
// ciphertext alone, as a publish cut off before its signature does.
func (r *syncRig) put(t *testing.T, serial int64, sealed []byte, by *signer.Signer, comment string) {
	t.Helper()
	docKey, sigKey := domain.DesiredStateKeys(r.inst.Product, r.inst.ID, serial)
	r.write(t, docKey, sealed)
	if by != nil {
		sig, err := by.Sign(context.Background(), sealed, comment)
		require.NoError(t, err)
		r.write(t, sigKey, sig.Encoded)
	}
}

func (r *syncRig) sync(t *testing.T) (ops.SyncReport, error) {
	t.Helper()
	return ops.SyncNow(context.Background(), r.h.Deps, ops.SyncOptions{})
}

func (r *syncRig) syncOK(t *testing.T) ops.SyncReport {
	t.Helper()
	report, err := r.sync(t)
	require.NoError(t, err)
	return report
}

func (r *syncRig) state(t *testing.T) domain.SyncConfig {
	t.Helper()
	return installation(t, r.h).Sync
}

// A document is applied as one operation, its serial recorded with it, and its
// statement names it. The pair above it with no signature is not a document
// yet, and a second cycle finds nothing to do.
func TestASyncAppliesTheNewestCompleteDocument(t *testing.T) {
	r := newSyncRig(t)
	r.publish(t, 100, "parameters:\n  log_level: debug\nsecrets:\n  db_password: from-the-authority\n")
	r.put(t, 200, r.seal(t, r.envelope(200)+"parameters:\n  log_level: warn\n", r.recipient), nil, "")
	before := len(configRecords(t, r.h))

	report := r.syncOK(t)
	assert.Equal(t, domain.SyncOK, report.Result)
	assert.Equal(t, int64(100), report.Serial)
	assert.Equal(t, "applied desired-state document 100", report.Summary())

	assert.Equal(t, "debug", installation(t, r.h).Parameters["log_level"])
	secret, _ := storedSecret(t, r.h, "db_password")
	assert.Equal(t, "from-the-authority", secret)
	records := configRecords(t, r.h)
	require.Len(t, records, before+1, "a document is one operation")

	state := r.state(t)
	require.NotNil(t, state.Applied)
	assert.Equal(t, int64(100), state.Applied.Serial)
	assert.Equal(t, domain.MinisignKeyID(r.key), state.Applied.AuthorityKeyID)
	assert.Equal(t, domain.SyncOK, state.LastFetch.Result)

	desired := syncStatement(t, r.h, records[0].ID)
	require.NotNil(t, desired, "the sync's statement names no document")
	assert.Equal(t, int64(100), desired.Serial)
	assert.Equal(t, domain.MinisignKeyID(r.key), desired.AuthorityKeyID)
	assert.True(t, strings.HasPrefix(desired.Digest, "sha256:"), desired.Digest)

	again := r.syncOK(t)
	assert.Equal(t, domain.SyncCurrent, again.Result)
	assert.Len(t, configRecords(t, r.h), before+1)
}

// syncStatement is the desired_state block of the statement opID filed.
func syncStatement(t *testing.T, h *harness, opID string) *domain.AttestedDesiredState {
	t.Helper()
	for _, file := range statementsOnDisk(t, h) {
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		var stmt domain.Statement
		require.NoError(t, json.Unmarshal(body, &stmt))
		if stmt.Predicate.Operation.ID == opID {
			assert.Equal(t, domain.PredicateType, stmt.PredicateType)
			return stmt.Predicate.DesiredState
		}
	}
	t.Fatalf("operation %s filed no statement", opID)
	return nil
}

// A document holding only a secret still records its serial (decision 25), and
// one whose values are already in force records it without an operation.
func TestEveryAppliedDocumentRecordsItsSerial(t *testing.T) {
	r := newSyncRig(t)
	r.publish(t, 10, "secrets:\n  db_password: only-a-secret\n")
	assert.Equal(t, domain.SyncOK, r.syncOK(t).Result)
	assert.Equal(t, int64(10), r.state(t).Applied.Serial)

	before := len(configRecords(t, r.h))
	r.publish(t, 11, "secrets:\n  db_password: only-a-secret\n")
	report := r.syncOK(t)
	assert.True(t, report.NoChange)
	assert.Equal(t, int64(11), r.state(t).Applied.Serial)
	assert.Len(t, configRecords(t, r.h), before, "a document that changes nothing ran an operation")
}

// What fails its signature never moves the watermark: the serial in an object's
// name is anybody's who can write to the bucket.
func TestAPairThatIsNotTheAuthoritysLeavesTheWatermarkAlone(t *testing.T) {
	other := signer.New(filepath.Join(t.TempDir(), "other.key"), "other")
	_, err := other.EnsureKey(context.Background())
	require.NoError(t, err)
	for name, tamper := range map[string]func(t *testing.T, r *syncRig){
		"signed by another key": func(t *testing.T, r *syncRig) {
			r.put(t, 50, r.seal(t, r.envelope(50)+"parameters:\n  log_level: debug\n", r.recipient), other,
				domain.DesiredStateComment(r.inst.Product, r.inst.ID, 50))
		},
		"renamed to a higher serial": func(t *testing.T, r *syncRig) {
			r.put(t, 50, r.seal(t, r.envelope(5)+"parameters:\n  log_level: debug\n", r.recipient), r.authority,
				domain.DesiredStateComment(r.inst.Product, r.inst.ID, 5))
		},
		"another installation's": func(t *testing.T, r *syncRig) {
			r.put(t, 50, r.seal(t, r.envelope(50)+"parameters:\n  log_level: debug\n", r.recipient), r.authority,
				domain.DesiredStateComment(r.inst.Product, "inst_01OTHERINSTALLATION", 50))
		},
		"too large to be a document": func(t *testing.T, r *syncRig) {
			r.put(t, 50, bytes.Repeat([]byte("x"), ports.MaxObjectBytes+1), r.authority,
				domain.DesiredStateComment(r.inst.Product, r.inst.ID, 50))
		},
		"not signed over these bytes": func(t *testing.T, r *syncRig) {
			r.publish(t, 50, "parameters:\n  log_level: debug\n")
			docKey, _ := domain.DesiredStateKeys(r.inst.Product, r.inst.ID, 50)
			require.NoError(t, os.WriteFile(filepath.Join(r.offsite, filepath.FromSlash(docKey)),
				r.seal(t, r.envelope(50)+"parameters:\n  log_level: warn\n", r.recipient), 0o644))
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSyncRig(t)
			tamper(t, r)
			report := r.syncOK(t)
			assert.Equal(t, domain.SyncBadSignature, report.Result)
			state := r.state(t)
			assert.Nil(t, state.Refused, "an unauthenticated serial was recorded as refused")
			assert.Nil(t, state.Applied)
			assert.Equal(t, domain.SyncBadSignature, state.LastFetch.Result)
			assert.NotEqual(t, "debug", installation(t, r.h).Parameters["log_level"])
		})
	}
}

// A document naming anything the release does not manage is refused whole, once,
// and a newer document is still tried (decision 22).
func TestADocumentReachingPastManagedIsRefusedWhole(t *testing.T) {
	r := newSyncRig(t)
	r.publish(t, 20, "parameters:\n  log_level: debug\n  http_port: \"9000\"\nsettings:\n  update.check: \"true\"\n")

	report := r.syncOK(t)
	assert.Equal(t, domain.SyncUnmanageable, report.Result)
	assert.Equal(t, []string{"update.check", "http_port"}, report.Names)
	assert.Contains(t, report.Summary(), "document 20 was refused at validate: unmanageable (update.check, http_port)")
	assert.NotEqual(t, "debug", installation(t, r.h).Parameters["log_level"], "a refused document was partly applied")

	state := r.state(t)
	require.NotNil(t, state.Refused)
	assert.Equal(t, int64(20), state.Refused.Serial)
	assert.Equal(t, "validate", state.Refused.Step)
	assert.Equal(t, "unmanageable", state.Refused.Class)
	assert.Equal(t, []events.Kind{events.KindSyncRefused}, r.notified.kinds())

	assert.Equal(t, domain.SyncCurrent, r.syncOK(t).Result, "a refused serial was tried again")
	assert.Len(t, r.notified.kinds(), 1)

	r.publish(t, 21, "parameters:\n  log_level: debug\n")
	assert.Equal(t, domain.SyncOK, r.syncOK(t).Result)
	assert.Equal(t, "debug", installation(t, r.h).Parameters["log_level"])
}

// An older document the authority signed is a replay once a newer one applied.
func TestAnOlderDocumentIsAReplay(t *testing.T) {
	r := newSyncRig(t)
	r.publish(t, 30, "parameters:\n  log_level: debug\n")
	r.syncOK(t)

	require.NoError(t, os.RemoveAll(filepath.Join(r.offsite, "state")))
	r.publish(t, 29, "parameters:\n  log_level: warn\n")
	assert.Equal(t, domain.SyncReplay, r.syncOK(t).Result)
	assert.Equal(t, "debug", installation(t, r.h).Parameters["log_level"])
}

// A document that cannot be read here, or that is no longer valid, is refused
// with the step and class that say so.
func TestADocumentThatCannotBeUsedIsRefusedWithItsClass(t *testing.T) {
	stranger, err := sopsage.GenerateIdentity(filepath.Join(t.TempDir(), "identity"))
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		doc         func(r *syncRig) []byte
		result      domain.SyncOutcome
		step, class string
	}{
		"encrypted to another machine": {
			doc: func(r *syncRig) []byte {
				return r.seal(t, r.envelope(40)+"parameters:\n  log_level: debug\n", stranger)
			},
			result: domain.SyncUndecryptable, step: "decrypt", class: "undecryptable",
		},
		"expired": {
			doc: func(r *syncRig) []byte {
				env := strings.Replace(r.envelope(40), "2026-08-10T11:00:00Z", "2026-08-03T11:30:00Z", 1)
				return r.seal(t, env+"parameters:\n  log_level: debug\n", r.recipient)
			},
			result: domain.SyncExpired, step: "check", class: "expired",
		},
		"for another installation inside": {
			doc: func(r *syncRig) []byte {
				env := strings.Replace(r.envelope(40), r.inst.ID, "inst_01OTHERINSTALLATION", 1)
				return r.seal(t, env+"parameters:\n  log_level: debug\n", r.recipient)
			},
			result: domain.SyncInvalid, step: "parse", class: "invalid",
		},
		"not a document": {
			doc:    func(r *syncRig) []byte { return r.seal(t, r.envelope(40)+"colour: blue\n", r.recipient) },
			result: domain.SyncInvalid, step: "parse", class: "invalid",
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSyncRig(t)
			r.put(t, 40, tc.doc(r), r.authority, domain.DesiredStateComment(r.inst.Product, r.inst.ID, 40))
			report := r.syncOK(t)
			assert.Equal(t, tc.result, report.Result)
			refused := r.state(t).Refused
			require.NotNil(t, refused)
			assert.Equal(t, []any{int64(40), tc.step, tc.class}, []any{refused.Serial, refused.Step, refused.Class})
			assert.NotEqual(t, "debug", installation(t, r.h).Parameters["log_level"])
		})
	}
}

// A document whose operation fails is put back by the operation and recorded as
// refused at the step that failed. The error's text stays on this machine.
func TestADocumentThatFailsItsHealthChecksIsUndoneAndRefused(t *testing.T) {
	r := newSyncRig(t)
	r.publish(t, 60, "parameters:\n  log_level: debug\n")
	r.h.Health.FailingWaits = 1

	report, err := r.sync(t)
	require.Error(t, err)
	assert.Equal(t, domain.SyncFailed, report.Result)
	assert.Equal(t, "health-check", report.Class)
	assert.Equal(t, "health-checks", report.Step)
	assert.Equal(t, []string{"log_level"}, report.Names)

	state := r.state(t)
	assert.Nil(t, state.Applied, "a document undone by its operation was recorded as applied")
	require.NotNil(t, state.Refused)
	assert.Equal(t, int64(60), state.Refused.Serial)
	assert.Contains(t, state.Refused.Message, "health-checks")
	assert.Equal(t, report.OperationID, state.Refused.OperationID)
	assert.NotEqual(t, "debug", installation(t, r.h).Parameters["log_level"])
}

// The document in force past its expiry, with nothing newer, is reported once.
func TestAnAuthorityGoneQuietIsReportedOnce(t *testing.T) {
	r := newSyncRig(t)
	r.publish(t, 70, "parameters:\n  log_level: debug\n")
	r.syncOK(t)
	r.notified.events = nil

	r.h.Deps.Now = func() time.Time { return time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC) }
	assert.Equal(t, domain.SyncExpired, r.syncOK(t).Result)
	assert.Equal(t, domain.SyncExpired, r.syncOK(t).Result)
	assert.Equal(t, []events.Kind{events.KindSyncExpired}, r.notified.kinds())
	assert.Equal(t, "debug", installation(t, r.h).Parameters["log_level"], "an expired document in force was undone")
}

// Nothing published and a target that does not answer are both outcomes, not
// errors: a tick reports them and changes nothing.
func TestNothingToFetchIsAnOutcome(t *testing.T) {
	r := newSyncRig(t)
	assert.Equal(t, domain.SyncAbsent, r.syncOK(t).Result)
	assert.False(t, r.state(t).LastReached.IsZero())

	r.inst = installation(t, r.h)
	r.inst.Sync.Target = "file:///nonexistent/" + filepath.Base(t.TempDir())
	require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), r.inst))
	report := r.syncOK(t)
	assert.Contains(t, []domain.SyncOutcome{domain.SyncAbsent, domain.SyncUnreachable}, report.Result)
}

// A cycle needs sync switched on, and one that cannot take the lock says so
// rather than waiting.
func TestASyncNeedsTheSwitchAndTheLock(t *testing.T) {
	r := newSyncRig(t)
	r.inst = installation(t, r.h)
	r.inst.Sync.Enabled = false
	require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), r.inst))
	_, err := r.sync(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync is not enabled")

	r.inst.Sync.Enabled = true
	require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), r.inst))
	r.publish(t, 80, "parameters:\n  log_level: debug\n")
	r.h.Locker.FailAcquire = true
	_, err = r.sync(t)
	assert.ErrorIs(t, err, domain.ErrLocked)
	r.h.Locker.FailAcquire = false
	assert.Nil(t, r.state(t).Applied)
}

// A cycle that waited for the lock while another applied the same document
// applies nothing: the serial is checked again under the lock.
func TestADocumentAppliedWhileACycleWaitedIsNotAppliedTwice(t *testing.T) {
	r := newSyncRig(t)
	r.publish(t, 90, "parameters:\n  log_level: debug\n")
	before := len(configRecords(t, r.h))
	once := false
	r.h.Locker.BeforeAcquire = func() {
		if once {
			return
		}
		once = true
		inst := installation(t, r.h)
		inst.Sync.Applied = &domain.SyncDocument{Serial: 90, AuthorityKeyID: domain.MinisignKeyID(r.key)}
		require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), inst))
	}
	statements := len(statementsOnDisk(t, r.h))
	assert.Equal(t, domain.SyncCurrent, r.syncOK(t).Result)
	assert.Len(t, configRecords(t, r.h), before)
	assert.NotEqual(t, "debug", installation(t, r.h).Parameters["log_level"])
	assert.Empty(t, r.notified.kinds(), "a cycle that applied nothing reported an outcome")
	assert.Len(t, statementsOnDisk(t, r.h), statements, "a cycle that applied nothing filed a statement")
}

// While sync is on, a local change to a name the authority manages is refused
// unless forced (decision 6): the authority's next document would set it back.
// Names it does not manage change as before.
func TestALocalChangeToAManagedNameNeedsForce(t *testing.T) {
	r := newSyncRig(t)
	ctx := context.Background()

	_, err := ops.ConfigSet(ctx, r.h.Deps, ops.ConfigSetOptions{Set: map[string]string{"log_level": "debug"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "log_level managed by the authority this installation follows (key "+
		domain.MinisignKeyID(r.key)+")")
	_, err = configApply(t, r.h, "secrets:\n  db_password: changed-here\n", ops.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret db_password managed by the authority")
	_, err = ops.SetSettings(ctx, r.h.Deps, ops.SetSettingsOptions{Set: map[string]string{"installation.domains": "other.example"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "installation.domains managed by the authority")

	_, err = ops.ConfigSet(ctx, r.h.Deps, ops.ConfigSetOptions{Set: map[string]string{"http_port": "9000"}})
	require.NoError(t, err, "a name the authority does not manage was refused")
	_, err = ops.ConfigSet(ctx, r.h.Deps, ops.ConfigSetOptions{
		Options: ops.Options{Force: true}, Set: map[string]string{"log_level": "debug"},
	})
	require.NoError(t, err)
	assert.Equal(t, "debug", installation(t, r.h).Parameters["log_level"])

	inst := installation(t, r.h)
	inst.Sync.Enabled = false
	require.NoError(t, r.h.Deps.State.SaveInstallation(ctx, inst))
	_, err = ops.ConfigSet(ctx, r.h.Deps, ops.ConfigSetOptions{Set: map[string]string{"log_level": "warn"}})
	require.NoError(t, err, "a managed name was refused with sync off")
}

// The row reports a refusal by serial, step, class and names, and its text,
// which quotes the value refused, stays on the machine (decision 23). The row
// also carries the recipient an authority encrypts to.
func TestARowReportsSyncWithoutTheRefusalsText(t *testing.T) {
	r := newSyncRig(t)
	ctx := context.Background()
	recipients, err := r.h.Secrets.Recipients(ctx)
	require.NoError(t, err)
	var machine string
	for _, rc := range recipients {
		if rc.Kind == ports.RecipientMachine {
			machine = rc.PublicKey
		}
	}
	require.NotEmpty(t, machine)
	r.publish(t, 110, "parameters:\n  log_level: PARAM-VALUE-MUST-NOT-TRAVEL\n")
	report, err := r.sync(t)
	require.NoError(t, err)
	assert.Equal(t, domain.SyncInvalid, report.Result)
	require.Contains(t, r.state(t).Refused.Message, "PARAM-VALUE-MUST-NOT-TRAVEL",
		"the refusal does not quote the value, so this test proves nothing")

	inst := installation(t, r.h)
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + r.offsite}}
	require.NoError(t, r.h.Deps.State.SaveInstallation(ctx, inst))
	_, err = ops.FleetPublish(ctx, r.h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	row, body := publishedRow(t, r.offsite, inst)
	assert.NotContains(t, string(body), "PARAM-VALUE-MUST-NOT-TRAVEL", "a refusal's text reached the row")
	assert.Equal(t, 2, row.Schema)
	assert.Equal(t, machine, row.AgeRecipient)
	require.NotNil(t, row.Sync)
	assert.Equal(t, domain.MinisignKeyID(r.key), row.Sync.AuthorityKeyID)
	require.NotNil(t, row.Sync.Refused)
	assert.Equal(t, int64(110), row.Sync.Refused.Serial)
	assert.Equal(t, "invalid", row.Sync.Refused.Class)
	assert.Equal(t, []string{"log_level"}, row.Sync.Refused.Names)
	assert.Equal(t, domain.SyncInvalid, row.Sync.LastFetch.Result)
}

// `doctor` warns when the authority goes quiet: the document in force near or
// past its expiry, or a target that has not answered for three intervals.
func TestDoctorWarnsWhenTheAuthorityGoesQuiet(t *testing.T) {
	r := newSyncRig(t)
	r.publish(t, 120, "parameters:\n  log_level: debug\n")
	r.syncOK(t)
	doctor := func(at time.Time) checkResult {
		t.Helper()
		r.h.Deps.Now = func() time.Time { return at }
		report, err := ops.Doctor(context.Background(), r.h.Deps)
		require.NoError(t, err)
		return findCheck(t, report, "sync.authority")
	}
	reached := func(at time.Time) {
		inst := installation(t, r.h)
		inst.Sync.LastReached = domain.NewTime(at)
		require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), inst))
	}

	assert.Equal(t, "ok", doctor(time.Date(2026, 8, 3, 12, 30, 0, 0, time.UTC)).Status)

	silent := doctor(time.Date(2026, 8, 3, 12, 46, 0, 0, time.UTC))
	assert.Equal(t, "warn", silent.Status)
	assert.Contains(t, silent.Message, "has not answered since")

	reached(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	soon := doctor(time.Date(2026, 8, 9, 12, 5, 0, 0, time.UTC))
	assert.Equal(t, "warn", soon.Status)
	assert.Contains(t, soon.Message, "within a day")

	reached(time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC))
	expired := doctor(time.Date(2026, 8, 11, 12, 5, 0, 0, time.UTC))
	assert.Equal(t, "warn", expired.Status)
	assert.Contains(t, expired.Message, "document 120 expired")
}

// A failure of this machine's own before the operation runs is not the
// document's doing: the cycle fails, nothing is refused, and the next tick
// applies the document once the machine recovers. A value the document gets
// wrong is the document's, and is refused like any other refusal.
func TestALocalFailureBeforeTheOperationRefusesNothing(t *testing.T) {
	r := newSyncRig(t)
	r.publish(t, 130, "secrets:\n  db_password: from-the-authority\n")
	r.h.Secrets.Fail = map[string]error{"Load": domain.SecretsError(nil, "the secret store cannot be read")}

	_, err := r.sync(t)
	require.Error(t, err)
	assert.Nil(t, r.state(t).Refused, "a document was refused for this machine's own failure")
	assert.Empty(t, r.notified.kinds())

	r.h.Secrets.Fail = nil
	assert.Equal(t, domain.SyncOK, r.syncOK(t).Result)

	r.publish(t, 131, "parameters:\n  log_level: loud\n")
	report, err := r.sync(t)
	require.NoError(t, err, "a refused document exited as a failure")
	assert.Equal(t, domain.SyncInvalid, report.Result)
	assert.Equal(t, "plan", report.Step)
	assert.Equal(t, int64(131), r.state(t).Refused.Serial)
}

// A target whose credential cannot be resolved is a target this machine cannot
// reach: recorded so, and `doctor` says so, rather than every tick failing
// where nobody reads it.
func TestATargetWithoutItsCredentialIsReportedUnreachable(t *testing.T) {
	r := newSyncRig(t)
	inst := installation(t, r.h)
	inst.Sync.Credentials = "sync_s3_never_set"
	require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), inst))

	assert.Equal(t, domain.SyncUnreachable, r.syncOK(t).Result)
	report, err := ops.Doctor(context.Background(), r.h.Deps)
	require.NoError(t, err)
	check := findCheck(t, report, "sync.authority")
	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Message, "never answered")
}

// A cycle publishes the fleet row when its outcome is news (§5.8 step 5): a
// document applied or refused, or a result unlike the last. A cycle that found
// what the last one found writes nothing.
func TestACyclePublishesTheRowWhenItsOutcomeIsNews(t *testing.T) {
	r := newSyncRig(t)
	ctx := context.Background()
	fleet := filepath.Join(t.TempDir(), "fleet")
	inst := installation(t, r.h)
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + fleet}}
	require.NoError(t, r.h.Deps.State.SaveInstallation(ctx, inst))
	key, err := domain.FleetKey(inst.Product, inst.ID)
	require.NoError(t, err)
	rowPath := filepath.Join(fleet, filepath.FromSlash(key))

	r.publish(t, 140, "parameters:\n  log_level: debug\n")
	r.syncOK(t)
	row, _ := publishedRow(t, fleet, inst)
	require.NotNil(t, row.Sync)
	require.NotNil(t, row.Sync.Applied, "the row does not report the document applied")
	assert.Equal(t, int64(140), row.Sync.Applied.Serial)

	// A newer document that changes nothing still moves the applied serial,
	// which the row reports.
	require.NoError(t, os.Remove(rowPath))
	r.publish(t, 141, "parameters:\n  log_level: debug\n")
	assert.True(t, r.syncOK(t).NoChange)
	row, _ = publishedRow(t, fleet, inst)
	assert.Equal(t, int64(141), row.Sync.Applied.Serial)

	// The first `current` changes the row's last fetch; the second is the
	// cycle with nothing new.
	assert.Equal(t, domain.SyncCurrent, r.syncOK(t).Result)
	require.NoError(t, os.Remove(rowPath))
	assert.Equal(t, domain.SyncCurrent, r.syncOK(t).Result)
	assert.NoFileExists(t, rowPath, "a cycle with nothing new published a row")
}

// failingObjects is a target that answers the listing or the fetch with err.
type failingObjects struct {
	ports.ObjectStore
	list, get error
}

func (f failingObjects) ObjectKeys(ctx context.Context, ref ports.TargetRef, prefix string) ([]string, error) {
	if f.list != nil {
		return nil, f.list
	}
	return f.ObjectStore.ObjectKeys(ctx, ref, prefix)
}

func (f failingObjects) GetObject(ctx context.Context, ref ports.TargetRef, key string) ([]byte, error) {
	if f.get != nil {
		return nil, f.get
	}
	return f.ObjectStore.GetObject(ctx, ref, key)
}

// A target that does not answer, or loses a pair between the listing and the
// fetch, changes nothing and refuses nothing.
func TestATargetThatFailsPartWayChangesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		objects func(ports.ObjectStore) ports.ObjectStore
		want    domain.SyncOutcome
		reached bool
	}{
		"the listing fails": {
			objects: func(o ports.ObjectStore) ports.ObjectStore {
				return failingObjects{ObjectStore: o, list: assert.AnError}
			},
			want: domain.SyncUnreachable,
		},
		"the fetch fails": {
			objects: func(o ports.ObjectStore) ports.ObjectStore {
				return failingObjects{ObjectStore: o, get: assert.AnError}
			},
			want: domain.SyncUnreachable, reached: true,
		},
		"the pair is gone by the fetch": {
			objects: func(o ports.ObjectStore) ports.ObjectStore {
				return failingObjects{ObjectStore: o, get: fs.ErrNotExist}
			},
			want: domain.SyncAbsent, reached: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSyncRig(t)
			r.publish(t, 170, "parameters:\n  log_level: debug\n")
			r.h.Deps.Objects = tc.objects(r.h.Deps.Objects)
			assert.Equal(t, tc.want, r.syncOK(t).Result)
			state := r.state(t)
			assert.Nil(t, state.Refused)
			assert.Nil(t, state.Applied)
			assert.Equal(t, tc.want, state.LastFetch.Result)
			assert.NotEqual(t, "debug", installation(t, r.h).Parameters["log_level"])
		})
	}
}

// An authority re-pinned while a cycle waited for the lock wins: the old key's
// document is not applied, and its refusal is neither recorded nor notified.
func TestAnAuthorityRepinnedMidCycleWins(t *testing.T) {
	repin := func(r *syncRig) func() {
		once := false
		return func() {
			if once {
				return
			}
			once = true
			inst := installation(t, r.h)
			inst.Sync.Authority = authorityKey2
			require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), inst))
		}
	}

	r := newSyncRig(t)
	r.publish(t, 180, "parameters:\n  log_level: debug\n")
	r.h.Locker.BeforeAcquire = repin(r)
	assert.Equal(t, domain.SyncCurrent, r.syncOK(t).Result)
	assert.Nil(t, r.state(t).Applied)
	assert.NotEqual(t, "debug", installation(t, r.h).Parameters["log_level"])

	r = newSyncRig(t)
	r.publish(t, 181, "parameters:\n  http_port: \"9000\"\n")
	r.h.Locker.BeforeAcquire = repin(r)
	assert.Equal(t, domain.SyncUnmanageable, r.syncOK(t).Result)
	assert.Nil(t, r.state(t).Refused, "the old key's refusal was recorded under the new key")
	assert.Empty(t, r.notified.kinds())
}

// A dry run fetches, verifies and plans, and changes and records nothing.
func TestADryRunAppliesAndRecordsNothing(t *testing.T) {
	r := newSyncRig(t)
	dry := ops.SyncOptions{Options: ops.Options{DryRun: true}}

	r.publish(t, 190, "parameters:\n  log_level: debug\n")
	report, err := ops.SyncNow(context.Background(), r.h.Deps, dry)
	require.NoError(t, err)
	assert.Equal(t, domain.SyncOK, report.Result)
	assert.True(t, report.DryRun)
	assert.Equal(t, "would apply desired-state document 190", report.Summary())
	assert.NotEqual(t, "debug", installation(t, r.h).Parameters["log_level"])
	assert.Nil(t, r.state(t).Applied)

	r.publish(t, 191, "parameters:\n  http_port: \"9000\"\n")
	report, err = ops.SyncNow(context.Background(), r.h.Deps, dry)
	require.NoError(t, err)
	assert.Equal(t, domain.SyncUnmanageable, report.Result)
	state := r.state(t)
	assert.Nil(t, state.Refused)
	assert.Nil(t, state.LastFetch)
	assert.Empty(t, r.notified.kinds())
}

// Only a serial newer than the last refused is considered (§5.7). With the
// refused pair withdrawn, an older document the authority signed is not
// applied in its place, and neither is an older refused one.
func TestNothingAtOrBelowTheLastRefusedIsConsidered(t *testing.T) {
	r := newSyncRig(t)
	r.publish(t, 200, "parameters:\n  log_level: debug\n")
	r.publish(t, 210, "parameters:\n  http_port: \"9000\"\n")
	assert.Equal(t, domain.SyncUnmanageable, r.syncOK(t).Result)

	require.NoError(t, os.RemoveAll(filepath.Join(r.offsite, "state")))
	r.publish(t, 200, "parameters:\n  log_level: debug\n")
	assert.Equal(t, domain.SyncReplay, r.syncOK(t).Result)
	assert.NotEqual(t, "debug", installation(t, r.h).Parameters["log_level"],
		"a document older than the one refused was applied")
}

// Three intervals of a very long interval is not a negative duration: a target
// that answered a moment ago is heard from, whatever the interval.
func TestDoctorIsNotFooledByAVeryLongInterval(t *testing.T) {
	r := newSyncRig(t)
	inst := installation(t, r.h)
	inst.Sync.Interval = "900000h0m0s"
	inst.Sync.LastReached = domain.NewTime(r.h.Deps.Now())
	inst.Sync.LastFetch = &domain.SyncFetch{At: inst.Sync.LastReached, Result: domain.SyncAbsent}
	require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), inst))
	report, err := ops.Doctor(context.Background(), r.h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "ok", findCheck(t, report, "sync.authority").Status)
}

// The local refusal of a managed name holds under the lock: sync turned on
// while a change waited for it is sync on for that change.
func TestALocalChangeIsRefusedWhenSyncTurnsOnWhileItWaits(t *testing.T) {
	for name, change := range map[string]func(r *syncRig) error{
		"config set": func(r *syncRig) error {
			_, err := ops.ConfigSet(context.Background(), r.h.Deps, ops.ConfigSetOptions{Set: map[string]string{"log_level": "debug"}})
			return err
		},
		"a setting": func(r *syncRig) error {
			_, err := ops.SetSettings(context.Background(), r.h.Deps, ops.SetSettingsOptions{
				Set: map[string]string{"installation.domains": "other.example"},
			})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSyncRig(t)
			inst := installation(t, r.h)
			inst.Sync.Enabled = false
			require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), inst))
			once := false
			r.h.Locker.BeforeAcquire = func() {
				if once {
					return
				}
				once = true
				on := installation(t, r.h)
				on.Sync.Enabled = true
				require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), on))
			}
			err := change(r)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "managed by the authority")
		})
	}
}

// An identity this machine cannot read is this machine's failure, not the
// document's: nothing is refused, and the document applies once the identity
// is back.
func TestAnUnreadableIdentityRefusesNothing(t *testing.T) {
	r := newSyncRig(t)
	r.publish(t, 220, "parameters:\n  log_level: debug\n")
	identity := r.h.Paths.AgeIdentityFile()
	require.NoError(t, os.Rename(identity, identity+".away"))

	_, err := r.sync(t)
	require.Error(t, err)
	assert.Nil(t, r.state(t).Refused, "a document was refused because this machine could not read its own key")

	require.NoError(t, os.Rename(identity+".away", identity))
	assert.Equal(t, domain.SyncOK, r.syncOK(t).Result)
}

// A document whose operation succeeded is applied, even when reconciling the
// units afterwards fails: the change is in force and its serial committed with
// it, so recording it refused would contradict the state.
func TestADocumentAppliedBeforeAUnitFailureIsNotRefused(t *testing.T) {
	r := newSyncRig(t)
	ctx := context.Background()
	r.h.Deps.Supervisor = r.h.Supervisor
	r.h.Supervisor.Present = true
	units, err := r.h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, r.h.Supervisor.InstallUnits(ctx, units, ports.EnableAll))
	r.h.Supervisor.Fail = map[string]error{"InstallUnits": assert.AnError}

	r.publish(t, 230, "settings:\n  installation.domains: other.example\n")
	report, err := r.sync(t)
	require.Error(t, err, "a unit failure was not reported")
	state := r.state(t)
	require.NotNil(t, state.Applied)
	assert.Equal(t, int64(230), state.Applied.Serial)
	assert.Nil(t, state.Refused, "an applied document was recorded as refused")
	assert.Equal(t, domain.SyncOK, report.Result)
}

// An expiry another cycle recorded while this one waited for the lock is not
// notified twice.
func TestAnExpiryRecordedMeanwhileIsNotNotifiedAgain(t *testing.T) {
	r := newSyncRig(t)
	r.publish(t, 240, "parameters:\n  log_level: debug\n")
	r.syncOK(t)
	r.notified.events = nil
	r.h.Deps.Now = func() time.Time { return time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC) }
	once := false
	r.h.Locker.BeforeAcquire = func() {
		if once {
			return
		}
		once = true
		inst := installation(t, r.h)
		inst.Sync.LastFetch = &domain.SyncFetch{At: domain.NewTime(r.h.Deps.Now()), Result: domain.SyncExpired}
		require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), inst))
	}
	assert.Equal(t, domain.SyncExpired, r.syncOK(t).Result)
	assert.Empty(t, r.notified.kinds(), "an expiry another cycle recorded was notified again")
}

// No secret value leaves the machine in a sync's notifications or statements
// (RFC 0057 §6), nor reaches its journal or state: not from a document
// applied, one refused before its operation by an error quoting a value beside
// the secret, or one its operation undid. The quoted value stays in state.
func TestASyncLeaksNoSecretIntoANotificationOrAStatement(t *testing.T) {
	r := newSyncRig(t)
	secrets := []string{"APPLIED-SECRET-VALUE", "REFUSED-SECRET-VALUE", "UNDONE-SECRET-VALUE"}

	r.publish(t, 10, "secrets:\n  db_password: "+secrets[0]+"\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)
	set, err := r.h.Secrets.Load(context.Background())
	require.NoError(t, err)
	stored, _ := set.Get("db_password")
	require.Equal(t, secrets[0], stored.Reveal(), "the document did not set the secret, so the scan proves nothing")

	r.publish(t, 20, "parameters:\n  log_level: PARAM-VALUE-MUST-NOT-TRAVEL\nsecrets:\n  db_password: "+secrets[1]+"\n")
	require.Equal(t, domain.SyncInvalid, r.syncOK(t).Result)
	require.Contains(t, r.state(t).Refused.Message, "PARAM-VALUE-MUST-NOT-TRAVEL",
		"the refusal does not quote the value beside the secret, so this test proves less")

	r.publish(t, 30, "parameters:\n  log_level: warn\nsecrets:\n  db_password: "+secrets[2]+"\n")
	r.h.Health.FailingWaits = 1
	report, err := r.sync(t)
	require.Error(t, err)
	require.Equal(t, domain.SyncFailed, report.Result)

	surfaces := map[string]string{}
	r.notified.mu.Lock()
	for i, ev := range r.notified.events {
		body, err := json.Marshal(ev)
		require.NoError(t, err)
		surfaces[fmt.Sprintf("notification %d (%s)", i, ev.Kind)] = string(body)
	}
	r.notified.mu.Unlock()
	require.Contains(t, r.notified.kinds(), events.KindSyncRefused, "no refusal was notified, so the scan proves less")
	ran := 0
	for _, file := range statementsOnDisk(t, r.h) {
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		if strings.Contains(string(body), `"desired_state"`) {
			ran++
		}
		surfaces["statement "+filepath.Base(file)] = withoutDigests(string(body))
	}
	assert.Equal(t, 2, ran, "the applied and the undone document should each have a statement")
	for name, path := range map[string]string{
		"journal": r.h.Paths.JournalFile(), "state": r.h.Paths.InstallationState(),
	} {
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		surfaces[name] = string(body)
	}

	for name, body := range surfaces {
		for _, secret := range secrets {
			assert.NotContains(t, body, secret, "a secret value reached the %s", name)
		}
		// The refusal's text quotes the parameter value, and stays in this
		// machine's state (decision 23): nothing that leaves carries it.
		if name != "state" {
			assert.NotContains(t, body, "PARAM-VALUE-MUST-NOT-TRAVEL", "a refused value reached the %s", name)
		}
	}
}
