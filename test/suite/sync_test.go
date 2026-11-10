package suite

import (
	"bytes"
	"context"
	"encoding/json"
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
}

func newSyncRig(t *testing.T) *syncRig {
	t.Helper()
	ctx := context.Background()
	h := verifyHarness(t)

	path := filepath.Join(h.Release.Root, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	floor := `min_manager_version: "0.0.0"`
	require.Contains(t, string(raw), floor)
	patched := strings.Replace(string(raw), floor, `min_manager_version: "0.16.0"`, 1) +
		"\nmanaged:\n  parameters: [log_level]\n  secrets: [db_password]\n  settings: [installation.domains]\n"
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
	write := func(key string, data []byte) {
		path := filepath.Join(r.offsite, filepath.FromSlash(key))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, data, 0o644))
	}
	write(docKey, sealed)
	if by != nil {
		sig, err := by.Sign(context.Background(), sealed, comment)
		require.NoError(t, err)
		write(sigKey, sig.Encoded)
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
	assert.Equal(t, domain.SyncCurrent, r.syncOK(t).Result)
	assert.Len(t, configRecords(t, r.h), before)
	assert.NotEqual(t, "debug", installation(t, r.h).Parameters["log_level"])
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
	require.Error(t, err)
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
