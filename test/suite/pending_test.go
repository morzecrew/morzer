package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// RFC 0058: a document gives values to the release the channel staged. The
// machine keeps them pending, apart from the values in force, and the next
// update to a release that declares them takes them as if given with --set.

// newPendingRig is a 1.2.0 machine that follows an authority managing
// log_level, and a channel whose 1.3.0 adds admin_email, with no default, and
// mail_port, and lets the authority manage both, http_port, which 1.2.0
// declares and does not let be managed, and two secrets: api_token, which only
// 1.3.0 declares, required and with no generator, and smtp_password, which
// 1.2.0 declares too.
func newPendingRig(t *testing.T) *syncRig {
	t.Helper()
	return newPendingRigWith(t, nil)
}

func newPendingRigWith(t *testing.T, patch func(t *testing.T, dir string)) *syncRig {
	t.Helper()
	h, _, _, _ := followingHarnessWith(t, func(t *testing.T, dir string) {
		t.Helper()
		migrateNoFurther(t, dir)
		requireAdminEmail(t, dir)
		patchManifest(t, dir, "parameters:\n",
			"parameters:\n  mail_port:\n    type: int\n    default: 25\n    description: Port the product mails on\n    services: [app]\n")
		manageInRelease(t, dir, "log_level, admin_email, mail_port, http_port", "db_password, api_token, smtp_password")
		appendTo(t, filepath.Join(dir, "secrets.schema.yaml"),
			"\n  - name: api_token\n    description: Token the product calls out with\n    required: true\n    services: [app]\n")
		if patch != nil {
			patch(t, dir)
		}
	})
	// The rig writes a whole installation, so the channel goes back after it.
	update := installation(t, h).Update
	r := newSyncRigOn(t, verifying(h))
	inst := installation(t, r.h)
	inst.Update = update
	require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), inst))
	r.inst = inst
	return r
}

// manageInRelease lets an authority manage the names in a bundle's manifest.
func manageInRelease(t *testing.T, dir, parameters, secrets string) {
	t.Helper()
	patchManifest(t, dir, `min_manager_version: "0.0.0"`, `min_manager_version: "0.16.0"`)
	appendTo(t, filepath.Join(dir, "manifest.yaml"), "\nmanaged:\n  parameters: ["+parameters+"]\n"+
		"  secrets: ["+secrets+"]\n  settings: [installation.domains]\n")
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(raw, text...), 0o644))
}

// stage has the channel stage its release, as the tick or `update --stage`
// does before anything installs it.
func (r *syncRig) stage(t *testing.T) {
	t.Helper()
	res, err := ops.FollowChannel(context.Background(), r.h.Deps, ops.FollowChannelOptions{})
	require.NoError(t, err)
	require.True(t, res.Candidate.IsStaged(), "the channel staged nothing: %s", res.Summary())
}

func (r *syncRig) secret(t *testing.T, name string) (string, bool) {
	t.Helper()
	stored, err := r.h.Deps.Secrets.Load(context.Background())
	require.NoError(t, err)
	value, ok := stored.Get(name)
	return value.Reveal(), ok
}

// The whole of it: the document is taken with its values pending and nothing
// converged, the row tells the authority what is staged and waiting, and the
// tick installs the release with the value and the secret it needed.
func TestADocumentGivesTheStagedReleaseItsValueAndTheTickInstallsIt(t *testing.T) {
	r := newPendingRig(t)
	ctx := context.Background()
	r.stage(t)

	before := len(r.h.Runtime.Calls)
	r.publish(t, 40, "parameters:\n  admin_email: "+adminEmail+"\n  http_port: \"9000\"\n"+
		"secrets:\n  api_token: token-from-the-authority\n")
	report := r.syncOK(t)
	require.Equal(t, domain.SyncOK, report.Result, report.Summary())

	inst := installation(t, r.h)
	require.NotNil(t, inst.Pending)
	assert.Equal(t, "1.3.0", inst.Pending.Release.String())
	assert.Equal(t, map[string]string{"admin_email": adminEmail, "http_port": "9000"}, inst.Pending.Parameters)
	assert.NotContains(t, inst.Parameters, "admin_email", "a pending value was recorded for the installed release")
	assert.NotContains(t, inst.Parameters, "http_port",
		"a name the installed release does not let be managed was applied to it")
	assert.Equal(t, int64(40), inst.Sync.Applied.Serial)
	assert.NotContains(t, r.h.Runtime.Calls[before:], "Up", "a document for the staged release converged the installed one")
	token, ok := r.secret(t, "api_token")
	require.True(t, ok, "the staged release's secret was not stored")
	assert.Equal(t, "token-from-the-authority", token)

	report2, err := ops.ConfigList(ctx, r.h.Deps)
	require.NoError(t, err)
	require.NotNil(t, report2.Pending)
	assert.Equal(t, "1.3.0", report2.Pending.Release)
	status, err := ops.GetStatus(ctx, r.h.Deps)
	require.NoError(t, err)
	assert.Equal(t, []string{"admin_email", "http_port"}, status.Pending)

	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + r.offsite}}
	require.NoError(t, r.h.Deps.State.SaveInstallation(ctx, inst))
	_, err = ops.FleetPublish(ctx, r.h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)
	row, body := publishedRow(t, r.offsite, inst)
	assert.Equal(t, "1.3.0", row.Staged)
	require.NotNil(t, row.Sync)
	assert.Equal(t, []string{"admin_email", "http_port"}, row.Sync.Pending)
	assert.NotContains(t, string(body), adminEmail, "a pending value reached the row")

	armAutoApply(t, r.h)
	res, err := ops.RunUnattended(ctx, r.h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)
	require.True(t, res.Applied, "refused: %s", res.Assessment.Why())
	assert.Equal(t, "1.3.0", currentVersion(t, r.h))

	inst = installation(t, r.h)
	assert.Equal(t, adminEmail, inst.Parameters["admin_email"])
	assert.Equal(t, "9000", inst.Parameters["http_port"])
	assert.Nil(t, inst.Pending, "pending outlived the update that took it")
	rec := res.Update.Record
	assert.Equal(t, "admin_email,http_port", rec.Flags["pending"])
	assert.Empty(t, rec.Flags["set"], "a pending value was journalled as given with --set")
	assert.Contains(t, rec.Flags["set_digest"], "admin_email=")
	cfg, _ := lastUp(t, r.h)
	assert.Equal(t, adminEmail, cfg.Env["DEMO_PARAM_ADMIN_EMAIL"], "the converge did not deliver the value")
	token, _ = r.secret(t, "api_token")
	assert.Equal(t, "token-from-the-authority", token, "the update replaced the secret the document gave")

	journal, err := os.ReadFile(r.h.Paths.JournalFile())
	require.NoError(t, err)
	assert.NotContains(t, string(journal), adminEmail, "a value reached the journal")
}

// With nothing staged, a name only the next release manages is refused, as it
// was before (decision 3), and so is the whole document.
func TestWithNothingStagedADocumentForTheNextReleaseIsRefusedWhole(t *testing.T) {
	r := newPendingRig(t)
	r.publish(t, 40, "parameters:\n  log_level: debug\n  admin_email: "+adminEmail+"\n")

	report := r.syncOK(t)
	assert.Equal(t, domain.SyncUnmanageable, report.Result)
	assert.Equal(t, []string{"admin_email"}, report.Names)
	inst := installation(t, r.h)
	assert.Nil(t, inst.Pending)
	assert.NotEqual(t, "debug", inst.Parameters["log_level"], "a refused document was partly applied")
}

// A name neither release manages still refuses the document whole, which is
// what catches a typo; so does a secret the installed release declares, which
// storing would change under it.
func TestANameNeitherReleaseTakesIsRefusedWhole(t *testing.T) {
	for name, doc := range map[string]string{
		"admin_emial":          "parameters:\n  admin_emial: " + adminEmail + "\n",
		"secret smtp_password": "secrets:\n  smtp_password: from-the-authority\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := newPendingRig(t)
			r.stage(t)
			r.publish(t, 40, "parameters:\n  admin_email: "+adminEmail+"\n"+strings.TrimPrefix(doc, "parameters:\n"))

			report := r.syncOK(t)
			assert.Equal(t, domain.SyncUnmanageable, report.Result)
			assert.Equal(t, []string{name}, report.Names)
			assert.Nil(t, installation(t, r.h).Pending, "a refused document left something pending")
		})
	}
}

// A value the staged release would refuse refuses the whole document, checked
// as `--set` is against the release being installed.
func TestAPendingValueOfTheWrongTypeIsRefusedWhole(t *testing.T) {
	r := newPendingRig(t)
	r.stage(t)
	r.publish(t, 40, "parameters:\n  log_level: debug\n  mail_port: not-a-number\n")

	report := r.syncOK(t)
	assert.Equal(t, domain.SyncInvalid, report.Result)
	inst := installation(t, r.h)
	assert.Nil(t, inst.Pending)
	assert.NotEqual(t, "debug", inst.Parameters["log_level"], "a refused document was partly applied")
}

// A document's merge: a later one that unsets a pending name removes it, and
// leaves the rest.
func TestUnsettingAPendingNameRemovesIt(t *testing.T) {
	r := newPendingRig(t)
	r.stage(t)
	r.publish(t, 40, "parameters:\n  admin_email: "+adminEmail+"\n  mail_port: \"2525\"\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)

	r.publish(t, 41, "unset:\n  parameters: [mail_port]\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)
	assert.Equal(t, map[string]string{"admin_email": adminEmail}, installation(t, r.h).Pending.Parameters)

	r.publish(t, 42, "unset:\n  parameters: [admin_email]\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)
	assert.Nil(t, installation(t, r.h).Pending, "an emptied pending section was kept")
}

// An update that fails keeps what was pending, and the next one, the tick's
// here, takes it.
func TestAFailedUpdateKeepsPendingForTheNextOne(t *testing.T) {
	r := newPendingRig(t)
	ctx := context.Background()
	r.stage(t)
	r.publish(t, 40, "parameters:\n  admin_email: "+adminEmail+"\nsecrets:\n  api_token: token\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)

	r.h.Health.FailingWaits = 1
	result, err := ops.Update(ctx, r.h.Deps, ops.UpdateOptions{To: "1.3.0"})
	require.Error(t, err)
	require.Equal(t, domain.StatusCompensated, result.Record.Status, "%v", err)
	assert.Equal(t, "1.2.0", currentVersion(t, r.h))
	inst := installation(t, r.h)
	require.NotNil(t, inst.Pending, "a rolled-back update cleared pending")
	assert.NotContains(t, inst.Parameters, "admin_email")

	armAutoApply(t, r.h)
	res, err := ops.RunUnattended(ctx, r.h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)
	require.True(t, res.Applied, "refused: %s", res.Assessment.Why())
	assert.Equal(t, adminEmail, installation(t, r.h).Parameters["admin_email"])
	assert.Nil(t, installation(t, r.h).Pending)
}

// otherRelease is the 1.3.0 fixture as 1.4.0, migrating no further than 1.2.0
// reads, and managing log_level only, with patch applied.
func otherRelease(t *testing.T, r *syncRig, patch func(t *testing.T, dir string)) string {
	t.Helper()
	src := stageRecoverableUpgradeSource(t, r.h)
	patchManifest(t, src, "version: 1.3.0", "version: 1.4.0")
	require.NoError(t, os.WriteFile(filepath.Join(src, "VERSION"), []byte("1.4.0\n"), 0o644))
	manageInRelease(t, src, "log_level", "db_password")
	if patch != nil {
		patch(t, src)
	}
	return src
}

// An update to another release than the one the values were checked against
// checks them again. One it refuses stops the update before anything runs; a
// name it does not declare is dropped, and the journal names it.
func TestAnUpdateToAnotherReleaseChecksPendingAgain(t *testing.T) {
	r := newPendingRig(t)
	ctx := context.Background()
	r.stage(t)
	r.publish(t, 40, "parameters:\n  admin_email: "+adminEmail+"\n  mail_port: \"2525\"\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)

	refusing := otherRelease(t, r, func(t *testing.T, dir string) {
		t.Helper()
		patchManifest(t, dir, "parameters:\n",
			"parameters:\n  mail_port:\n    type: enum\n    values: [\"25\", \"587\"]\n    default: \"25\"\n    services: [app]\n")
	})
	updates := countUpdates(t, r.h)
	_, err := ops.Update(ctx, r.h.Deps, ops.UpdateOptions{Ref: refusing})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mail_port")
	assert.Equal(t, updates, countUpdates(t, r.h), "an update refusing a pending value was journalled")
	assert.Equal(t, "1.2.0", currentVersion(t, r.h))

	result, err := ops.Update(ctx, r.h.Deps, ops.UpdateOptions{Ref: otherRelease(t, r, nil)})
	require.NoError(t, err)
	assert.Equal(t, "1.4.0", currentVersion(t, r.h))
	inst := installation(t, r.h)
	assert.Nil(t, inst.Pending)
	assert.NotContains(t, inst.Parameters, "admin_email")
	assert.NotContains(t, inst.Parameters, "mail_port")
	assert.Equal(t, "admin_email,mail_port", result.Record.Flags["pending_dropped"])
}

// While sync follows the authority, `update --set` of a name it manages, or of
// one it left pending, needs --force (decision 6), and the value given then
// wins over the pending one.
func TestUpdateSetOfAManagedOrPendingNameNeedsForce(t *testing.T) {
	r := newPendingRig(t)
	ctx := context.Background()
	r.stage(t)
	r.publish(t, 40, "parameters:\n  admin_email: "+adminEmail+"\nsecrets:\n  api_token: token\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)

	for _, name := range []string{"admin_email", "log_level"} {
		_, err := ops.Update(ctx, r.h.Deps, ops.UpdateOptions{To: "1.3.0", Parameters: map[string]string{name: "x@example.test"}})
		require.Error(t, err, "--set %s went through without --force", name)
		e := domain.AsError(err)
		assert.Contains(t, e.Message, name+" managed by the authority")
		assert.Contains(t, e.Message, domain.MinisignKeyID(r.key))
	}
	assert.Equal(t, "1.2.0", currentVersion(t, r.h))

	_, err := ops.Update(ctx, r.h.Deps, ops.UpdateOptions{
		Options: ops.Options{Force: true}, To: "1.3.0",
		Parameters: map[string]string{"admin_email": "local@example.test"},
	})
	require.NoError(t, err)
	inst := installation(t, r.h)
	assert.Equal(t, "local@example.test", inst.Parameters["admin_email"])
	assert.Nil(t, inst.Pending)
}

// A resume after a sync changed a pending value is refused by name, and a
// fresh update takes the new value (decision 13). Stopped before it converged,
// by a backup that failed: 1.2.0 still runs, so the new value is pending too.
func TestAResumeAfterASyncChangedAPendingValueIsRefused(t *testing.T) {
	r := newPendingRig(t)
	ctx := context.Background()
	r.stage(t)
	r.publish(t, 40, "parameters:\n  admin_email: "+adminEmail+"\nsecrets:\n  api_token: token\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)

	r.h.Backup.Fail["Create"] = domain.BackupError(errInjected, "no space left on device")
	_, err := ops.Update(ctx, r.h.Deps, ops.UpdateOptions{To: "1.3.0"})
	require.Error(t, err)
	delete(r.h.Backup.Fail, "Create")
	require.Equal(t, "1.2.0", currentVersion(t, r.h))

	r.publish(t, 41, "parameters:\n  admin_email: changed@example.test\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)
	require.Equal(t, "changed@example.test", installation(t, r.h).Pending.Parameters["admin_email"])

	_, err = ops.Update(ctx, r.h.Deps, ops.UpdateOptions{Options: ops.Options{Resume: true}, To: "1.3.0"})
	require.Error(t, err, "a resume took a pending value its run was not given")
	e := domain.AsError(err)
	assert.Contains(t, e.Message, "values pending for admin_email changed")
	assert.Contains(t, e.Hint, "without --resume")

	_, err = ops.Update(ctx, r.h.Deps, ops.UpdateOptions{To: "1.3.0"})
	require.NoError(t, err)
	assert.Equal(t, "changed@example.test", installation(t, r.h).Parameters["admin_email"])
}

// An update that installed its release and stopped in its record step keeps
// pending (decision 7). A document that then applies a pending name to the
// release now installed removes it from pending: an older value must not be
// recorded over the authority's later one.
func TestAValueAppliedNowSupersedesWhatWasPending(t *testing.T) {
	r := newPendingRig(t)
	ctx := context.Background()
	r.stage(t)
	r.publish(t, 40, "parameters:\n  admin_email: "+adminEmail+"\nsecrets:\n  api_token: token\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)

	armed := true
	store := r.h.Deps.State
	r.h.Deps.State = saveFailingWhen{StateStore: store, when: recording("admin_email", adminEmail), armed: &armed}
	_, err := ops.Update(ctx, r.h.Deps, ops.UpdateOptions{To: "1.3.0"})
	require.Error(t, err)
	r.h.Deps.State = store
	require.Equal(t, "1.3.0", currentVersion(t, r.h))
	require.NotNil(t, installation(t, r.h).Pending, "the stopped update cleared pending")

	r.publish(t, 41, "parameters:\n  admin_email: changed@example.test\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)
	inst := installation(t, r.h)
	assert.Equal(t, "changed@example.test", inst.Parameters["admin_email"])
	assert.Nil(t, inst.Pending, "a value applied now left the older one pending")

	_, err = ops.Update(ctx, r.h.Deps, ops.UpdateOptions{Options: ops.Options{Resume: true}, To: "1.3.0"})
	require.Error(t, err, "a resume recorded a value the authority has since replaced")
	assert.Equal(t, "changed@example.test", installation(t, r.h).Parameters["admin_email"])
}

// The rename: 1.2.0 manages log_level, and its successor renames it verbosity,
// with no default. The document sets both; the first applies now, the second
// waits, and the tick installs the release and retires the old name.
func TestARenamedParameterCrossesTheRelease(t *testing.T) {
	r := newPendingRigWith(t, func(t *testing.T, dir string) {
		t.Helper()
		patchManifest(t, dir, "  log_level:\n    type: enum\n    values: [debug, info, warn, error]\n    default: info\n",
			"  verbosity:\n    type: enum\n    values: [debug, info, warn, error]\n")
		patchManifest(t, dir, "parameters: [log_level, ", "parameters: [verbosity, ")
	})
	ctx := context.Background()
	r.stage(t)
	r.publish(t, 40, "parameters:\n  log_level: warn\n  verbosity: warn\n  admin_email: "+adminEmail+"\n"+
		"secrets:\n  api_token: token\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)
	inst := installation(t, r.h)
	assert.Equal(t, "warn", inst.Parameters["log_level"])
	assert.Equal(t, "warn", inst.Pending.Parameters["verbosity"])

	armAutoApply(t, r.h)
	res, err := ops.RunUnattended(ctx, r.h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)
	require.True(t, res.Applied, "refused: %s", res.Assessment.Why())
	inst = installation(t, r.h)
	assert.Equal(t, "warn", inst.Parameters["verbosity"])
	assert.NotContains(t, inst.Parameters, "log_level", "the old name was not retired")
	assert.Nil(t, inst.Pending)
}

// A sync that changes what is pending while an update waits for the lock
// refuses the update, as a parameter that changed meanwhile does: it planned
// with the values it read.
func TestPendingChangedWhileAnUpdateWaitedRefusesIt(t *testing.T) {
	r := newPendingRig(t)
	ctx := context.Background()
	r.stage(t)
	r.publish(t, 40, "parameters:\n  admin_email: "+adminEmail+"\nsecrets:\n  api_token: token\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)

	r.h.Locker.BeforeAcquire = func() {
		r.h.Locker.BeforeAcquire = nil
		inst := installation(t, r.h)
		inst.Pending.Parameters = map[string]string{"admin_email": "late@example.test"}
		require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), inst))
	}
	_, err := ops.Update(ctx, r.h.Deps, ops.UpdateOptions{To: "1.3.0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "values pending for admin_email changed while waiting for the deployment lock")
	assert.Equal(t, "1.2.0", currentVersion(t, r.h))
}

// On a machine that follows an authority, the tick and `update` point a
// missing managed value at the authority first: a --set of it needs --force.
func TestAMissingManagedValueIsTheAuthoritys(t *testing.T) {
	r := newPendingRig(t)
	ctx := context.Background()
	r.stage(t)

	_, err := ops.Update(ctx, r.h.Deps, ops.UpdateOptions{To: "1.3.0"})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "the authority's next document, or with the update: --force --set admin_email=")

	armAutoApply(t, r.h)
	res, err := ops.RunUnattended(ctx, r.h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)
	assert.False(t, res.Applied)
	assert.Contains(t, res.Assessment.Why(),
		"so it waits for the authority's next document, or `morzer update --to 1.3.0 --force --set admin_email=<value>`")
}

// A document may not unset a secret the staged release requires, as it may
// not unset one the installed release requires: the update would then refuse
// for want of it.
func TestADocumentCannotRemoveASecretTheStagedReleaseRequires(t *testing.T) {
	r := newPendingRig(t)
	r.stage(t)
	r.publish(t, 40, "secrets:\n  api_token: token\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)

	r.publish(t, 41, "unset:\n  secrets: [api_token]\n")
	report := r.syncOK(t)
	assert.Equal(t, domain.SyncInvalid, report.Result)
	_, ok := r.secret(t, "api_token")
	assert.True(t, ok, "a secret the staged release requires was removed")
}

// Values a document gives a newer staged release are checked against it, and
// so are the ones kept from an earlier document, since the section then names
// that release as what they were all checked against.
func TestAValueKeptFromAnEarlierReleaseIsCheckedAgain(t *testing.T) {
	r := newPendingRig(t)
	ctx := context.Background()
	r.stage(t)
	inst := installation(t, r.h)
	inst.Pending = &domain.PendingValues{
		Release:    domain.MustParseVersion("1.2.9"),
		Parameters: map[string]string{"mail_port": "not-a-number"},
	}
	require.NoError(t, r.h.Deps.State.SaveInstallation(ctx, inst))

	r.publish(t, 40, "parameters:\n  admin_email: "+adminEmail+"\n")
	assert.Equal(t, domain.SyncInvalid, r.syncOK(t).Result)
	assert.Equal(t, "1.2.9", installation(t, r.h).Pending.Release.String(), "a refused document relabelled pending")

	inst = installation(t, r.h)
	inst.Pending.Parameters = map[string]string{"mail_port": "2525"}
	require.NoError(t, r.h.Deps.State.SaveInstallation(ctx, inst))
	r.publish(t, 41, "parameters:\n  admin_email: "+adminEmail+"\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)
	assert.Equal(t, "1.3.0", installation(t, r.h).Pending.Release.String())
}

// A pending name the release does not declare, added while an update waits,
// refuses it too: its record step would drop a value its journal never named.
func TestAnUndeclaredPendingNameAddedWhileAnUpdateWaitedRefusesIt(t *testing.T) {
	r := newPendingRig(t)
	ctx := context.Background()
	r.stage(t)
	r.publish(t, 40, "parameters:\n  admin_email: "+adminEmail+"\nsecrets:\n  api_token: token\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)

	r.h.Locker.BeforeAcquire = func() {
		r.h.Locker.BeforeAcquire = nil
		inst := installation(t, r.h)
		inst.Pending.Parameters = map[string]string{"admin_email": adminEmail, "retired_name": "x"}
		require.NoError(t, r.h.Deps.State.SaveInstallation(context.Background(), inst))
	}
	_, err := ops.Update(ctx, r.h.Deps, ops.UpdateOptions{To: "1.3.0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retired_name")
}

// saveThenFail saves the installation when picks out, then reports a failure,
// as a run killed after its write and before the journal heard of it would.
type saveThenFail struct {
	saveFailingWhen
}

func (s saveThenFail) SaveInstallation(ctx context.Context, inst domain.Installation) error {
	if *s.armed && s.when(inst) {
		*s.armed = false
		if err := s.StateStore.SaveInstallation(ctx, inst); err != nil {
			return err
		}
		return assert.AnError
	}
	return s.StateStore.SaveInstallation(ctx, inst)
}

// A run whose record step wrote the values, and cleared pending, but stopped
// before its journal said so is resumed, not refused: what it took from
// pending is recorded, with the digests it journalled.
func TestAResumeAfterTheRecordLandedIsNotRefused(t *testing.T) {
	r := newPendingRig(t)
	ctx := context.Background()
	r.stage(t)
	r.publish(t, 40, "parameters:\n  admin_email: "+adminEmail+"\nsecrets:\n  api_token: token\n")
	require.Equal(t, domain.SyncOK, r.syncOK(t).Result)

	armed := true
	store := r.h.Deps.State
	r.h.Deps.State = saveThenFail{saveFailingWhen{StateStore: store, when: recording("admin_email", adminEmail), armed: &armed}}
	_, err := ops.Update(ctx, r.h.Deps, ops.UpdateOptions{To: "1.3.0"})
	require.Error(t, err)
	r.h.Deps.State = store
	require.Nil(t, installation(t, r.h).Pending, "the record step's write did not land")

	_, err = ops.Update(ctx, r.h.Deps, ops.UpdateOptions{Options: ops.Options{Resume: true}, To: "1.3.0"})
	require.NoError(t, err, "a resume was refused over values its own run recorded")
	assert.Equal(t, adminEmail, installation(t, r.h).Parameters["admin_email"])
}

// A staged release whose secret schema cannot be read is this machine's
// problem, not the document's: the cycle fails and refuses nothing, so the
// serial is tried again once the release is staged whole.
func TestAStagedSchemaThatWillNotLoadRefusesNothing(t *testing.T) {
	r := newPendingRig(t)
	ctx := context.Background()
	r.stage(t)
	candidate, err := r.h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(candidate.Root, "secrets.schema.yaml"), []byte("secrets: [\n"), 0o644))

	r.publish(t, 40, "secrets:\n  api_token: token\n")
	report, err := r.sync(t)
	require.Error(t, err)
	assert.Equal(t, domain.SyncFailed, report.Result)
	assert.Nil(t, r.state(t).Refused, "a local read failure refused the document")
}
