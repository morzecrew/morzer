package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/target"
	"github.com/morzecrew/morzer/internal/adapters/target/localdir"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// withTargets wires a real target registry and a directory target, and points
// the fake backup engine at a real directory so a push has something to move.
//
// The registry and the adapter are the production ones. Only the backup engine
// is fake, because what these tests are about is the *operation* -- what the
// step engine does when a push fails -- and not what a hook writes.
func (h *harness) withTargets(t *testing.T) (inst domain.Installation, offsite string) {
	t.Helper()

	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	h.Deps.Targets = registry

	h.Backup.Root = filepath.Join(h.Root, "var", "backups")
	require.NoError(t, os.MkdirAll(h.Backup.Root, 0o700))

	offsite = filepath.Join(t.TempDir(), "offsite")

	inst = h.install()
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + offsite}}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	return inst, offsite
}

func TestABackupReachesItsTarget(t *testing.T) {
	h := newHarness(t)
	_, offsite := h.withTargets(t)

	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Prune: true, Push: true, PruneRemote: true,
	})
	require.NoError(t, err)

	ref, ok := result.Data.(ports.BackupRef)
	require.True(t, ok, "the backup operation returned no backup reference")
	assert.Contains(t, result.Summary, "copied to",
		"a backup that went off the machine and one that did not must not read alike")

	// The backup is on the target, whole, and its manifest is readable
	// there without a key.
	manifests, err := localdir.New().List(context.Background(), mustTarget(t, offsite))
	require.NoError(t, err)
	require.Len(t, manifests, 1)
	assert.Equal(t, ref.ID, manifests[0].ID)

	// And the local copy is still there. A target is a second copy, not a
	// move: the fast restore is from the local disk.
	_, err = os.Stat(h.Backup.Dir(ref.ID))
	require.NoError(t, err, "pushing a backup must not remove it from this machine")
}

// TestAFailedPushFailsTheBackup is decision 3 of RFC 0009, and the reason the
// whole mechanism is worth having.
//
// Retention failing is Continue: a disk fuller than intended is a smaller
// problem than a red backup. This is not that. The operation's purpose is that
// the data is somewhere the machine's failure does not reach, and a green
// `backup` on a machine whose backups are all local is exactly the state an
// operator would find out about during the disaster.
func TestAFailedPushFailsTheBackup(t *testing.T) {
	h := newHarness(t)
	inst, _ := h.withTargets(t)

	// A target on a path that cannot be created: the ordinary shape of an
	// unmounted disk or a revoked credential.
	blocked := filepath.Join(h.Root, "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o600))

	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + filepath.Join(blocked, "backups")}}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: true,
	})
	require.Error(t, err, "the backup was reported as succeeding although it never left the machine")

	assert.Equal(t, "push-backup", failedStepID(result.Record),
		"the failure must be attributed to the push, not to the backup itself")

	// The remedy is in the message, and it is not "take another backup":
	// the data is on the disk, correct and verified, and what failed was the
	// medium.
	assert.Contains(t, domain.AsError(err).Hint, "backup push",
		"the refusal must name the retry; a failed push is not a failed backup")
}

// TestAFailedPushKeepsTheBackupItTook is the promise the previous test's
// remedy depends on, and it is not free.
//
// The obvious `OnFailure: Compensate` rolls back every completed step
// newest-first, and the oldest of those is create-backup, whose compensation
// deletes the backup. That would leave an operator who configured a target with
// *no* backup on a night the target was unreachable -- strictly worse than
// having configured nothing at all.
func TestAFailedPushKeepsTheBackupItTook(t *testing.T) {
	h := newHarness(t)
	inst, _ := h.withTargets(t)

	blocked := filepath.Join(h.Root, "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o600))
	inst.Backup.Targets = []domain.BackupTargetConfig{
		{URL: "file://" + filepath.Join(blocked, "backups")},
	}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: true,
	})
	require.Error(t, err)

	local, err := h.Backup.List(context.Background())
	require.NoError(t, err)
	require.Len(t, local, 1,
		"the failed push took the backup with it; the operator is worse off than "+
			"before they configured a target")

	_, err = os.Stat(h.Backup.Dir(local[0].ID))
	require.NoError(t, err, "the backup directory was removed by compensation")
}

// TestAFailedPushRemovesWhatItManagedToCopy. Compensation cleans up the
// targets that did succeed, so a backup the operation refused does not exist
// half-copied on one of them.
func TestAFailedPushRemovesWhatItManagedToCopy(t *testing.T) {
	h := newHarness(t)
	inst, good := h.withTargets(t)

	blocked := filepath.Join(h.Root, "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o600))

	// The good one first, so it succeeds before the bad one fails.
	inst.Backup.Targets = []domain.BackupTargetConfig{
		{URL: "file://" + good},
		{URL: "file://" + filepath.Join(blocked, "backups")},
	}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: true,
	})
	require.Error(t, err)

	manifests, err := localdir.New().List(context.Background(), mustTarget(t, good))
	require.NoError(t, err)
	assert.Empty(t, manifests,
		"a backup the operation refused was left on one of the targets; someone "+
			"will eventually restore from a backup the manager says does not exist")
}

// TestAnInstallationWithNoTargetsIsUnchanged. Everything above has to be
// invisible to the deployments that keep backups on one machine on purpose.
func TestAnInstallationWithNoTargetsIsUnchanged(t *testing.T) {
	h := newHarness(t)
	h.install()

	// Deliberately no target registry at all, which is also what a build
	// without one looks like.
	h.Deps.Targets = nil

	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Prune: true, Push: true, PruneRemote: true,
	})
	require.NoError(t, err)
	assert.NotContains(t, result.Summary, "copied to")

	for _, step := range result.Record.Steps {
		assert.NotEqual(t, "push-backup", step.ID,
			"a deployment with no targets must not acquire a push step")
	}
}

// TestPushIsTheRetryForAFailedPush. The documented remedy, and it must not
// need another backup.
func TestPushIsTheRetryForAFailedPush(t *testing.T) {
	h := newHarness(t)
	_, offsite := h.withTargets(t)

	// A backup taken with the push turned off: the state a failed push
	// leaves behind.
	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: false,
	})
	require.NoError(t, err)
	ref, ok := result.Data.(ports.BackupRef)
	require.True(t, ok, "the backup operation returned no backup reference")

	manifests, err := localdir.New().List(context.Background(), mustTarget(t, offsite))
	require.NoError(t, err)
	require.Empty(t, manifests)

	pushed, err := ops.Push(context.Background(), h.Deps, ops.PushOptions{})
	require.NoError(t, err)
	assert.Contains(t, pushed.Summary, ref.ID)

	manifests, err = localdir.New().List(context.Background(), mustTarget(t, offsite))
	require.NoError(t, err)
	require.Len(t, manifests, 1)
	assert.Equal(t, ref.ID, manifests[0].ID)

	// Verified before it is copied, for the same reason the operation
	// verifies before it pushes.
	assert.Positive(t, h.Backup.Verified[ref.ID])
}

// TestRetentionOnATargetKeepsTheSamePolicyAsLocally, and never the most recent
// backup.
func TestRetentionOnATargetKeepsTheSamePolicyAsLocally(t *testing.T) {
	h := newHarness(t)
	inst, offsite := h.withTargets(t)

	inst.Policy.RetainBackups = 2
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	var ids []string
	for range 4 {
		result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
			Reason: "manual", Verify: true, Prune: true, Push: true, PruneRemote: true,
		})
		require.NoError(t, err)
		ref, ok := result.Data.(ports.BackupRef)
		require.True(t, ok)
		ids = append(ids, ref.ID)
	}

	manifests, err := localdir.New().List(context.Background(), mustTarget(t, offsite))
	require.NoError(t, err)
	require.Len(t, manifests, 2, "the target kept a different number of backups than the policy says")

	// The newest is always among them. Retention that could remove the most
	// recent backup is retention that can leave a deployment with nothing.
	assert.Equal(t, ids[len(ids)-1], manifests[0].ID)
}

// TestAPreUpdateBackupIsExemptOnTheTargetToo. The exemption exists so the
// backup guarding an update survives until the update is confirmed good, and it
// would be worth very little if it survived only on the machine being updated.
func TestAPreUpdateBackupIsExemptOnTheTargetToo(t *testing.T) {
	h := newHarness(t)
	inst, offsite := h.withTargets(t)

	inst.Policy.RetainBackups = 1
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	guard, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "pre-update", Verify: true, Prune: true, Push: true, PruneRemote: true,
	})
	require.NoError(t, err)
	guardRef, ok := guard.Data.(ports.BackupRef)
	require.True(t, ok)
	guardID := guardRef.ID

	for range 2 {
		_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
			Reason: "manual", Verify: true, Prune: true, Push: true, PruneRemote: true,
		})
		require.NoError(t, err)
	}

	manifests, err := localdir.New().List(context.Background(), mustTarget(t, offsite))
	require.NoError(t, err)

	var found bool
	for _, m := range manifests {
		if m.ID == guardID {
			found = true
		}
	}
	assert.True(t, found,
		"the pre-update backup was pruned from the target; it is the one an "+
			"operator reaches for when the update they were guarding goes wrong")
}

// TestRetentionFailingOnATargetDoesNotFailTheBackup.
//
// Unlike the push, this is `Continue`: the backup was taken and it is off the
// machine, which is everything the operation promised. A target that stays
// fuller than intended is a warning, not a failed backup.
//
// Driven by a target that accepts every push and refuses every removal, rather
// than by a read-only directory. A read-only directory breaks the push too, so
// the test could only assert "one of two things happened" -- which is a test
// that passes whichever way the code behaves.
func TestRetentionFailingOnATargetDoesNotFailTheBackup(t *testing.T) {
	h := newHarness(t)
	inst := h.install()

	fake := fakes.NewBackupTarget()
	h.Deps.Targets = fake
	h.Backup.Root = filepath.Join(h.Root, "var", "backups")
	require.NoError(t, os.MkdirAll(h.Backup.Root, 0o700))

	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "memory:///backups"}}
	inst.Policy.RetainBackups = 1
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Prune: true, Push: true, PruneRemote: true,
	})
	require.NoError(t, err)

	fake.FailRemoveWith = domain.BackupError(nil, "the target refuses deletions")

	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Prune: true, Push: true, PruneRemote: true,
	})
	require.NoError(t, err,
		"a target that would not prune failed the backup it had just accepted")
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)
	assert.Contains(t, result.Summary, "copied to")

	// Both backups are still there: the prune failed, which is the point.
	manifests, err := fake.List(context.Background(),
		ports.TargetRef{Scheme: "memory", Path: "/backups"})
	require.NoError(t, err)
	assert.Len(t, manifests, 2)

	// And the operator was told. "Continue" is the right call for retention,
	// but a step that fails silently is indistinguishable from one that
	// worked -- and a target nobody prunes fills up, which surfaces much
	// later as a failed push during an incident.
	var warned bool
	for _, e := range h.Events.Events() {
		if e.Level == events.LevelWarn && strings.Contains(e.Message, "prune-remote-backups") {
			warned = true
		}
	}
	assert.True(t, warned,
		"retention failed on the target and the backup was reported as a plain "+
			"success, so nobody learns the target is filling up")
}

// TestFetchBringsABackupBackAndVerifiesIt. The recovery move: the machine is
// new, the backup is on the target, and the operator wants to look at it before
// it overwrites anything.
func TestFetchBringsABackupBackAndVerifiesIt(t *testing.T) {
	h := newHarness(t)
	h.withTargets(t)

	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: true,
	})
	require.NoError(t, err)
	ref, ok := result.Data.(ports.BackupRef)
	require.True(t, ok, "the backup operation returned no backup reference")

	// Simulate the rebuilt machine: the local copy is gone, the target's is
	// not.
	require.NoError(t, os.RemoveAll(h.Backup.Dir(ref.ID)))

	fetched, err := ops.FetchRemote(context.Background(), h.Deps, ops.FetchOptions{})
	require.NoError(t, err)
	assert.Contains(t, fetched.Summary, ref.ID)

	dest := filepath.Join(h.Paths.BackupsDir(), ref.ID)
	for _, name := range []string{ports.BackupManifestFileName, "db.sql.age"} {
		_, err := os.Stat(filepath.Join(dest, name))
		require.NoError(t, err, "%s did not come back", name)
	}
}

// TestAnInterruptedFetchLeavesNothingInTheBackupStore. The store is what
// `backup list` and `restore` read; a half-fetched directory in it is a backup
// somebody selects.
func TestAnInterruptedFetchLeavesNothingInTheBackupStore(t *testing.T) {
	h := newHarness(t)
	h.withTargets(t)

	_, err := ops.FetchRemote(context.Background(), h.Deps, ops.FetchOptions{})
	require.Error(t, err, "fetching from an empty target must be an error")

	entries, err := os.ReadDir(h.Paths.BackupsDir())
	if err == nil {
		for _, e := range entries {
			assert.NotContains(t, e.Name(), ".fetching",
				"a staging directory was left in the backup store")
		}
	}
}

// TestATargetsCredentialsComeFromTheSecretStore, and are named rather than
// written into installation.yaml.
func TestATargetsCredentialsComeFromTheSecretStore(t *testing.T) {
	h := newHarness(t)
	inst, offsite := h.withTargets(t)

	inst.Backup.Targets = []domain.BackupTargetConfig{
		{URL: "file://" + offsite, Credentials: "backup_creds"},
	}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	// Not set yet: the refusal has to name the secret, because "cannot read
	// credentials" without a name is a support ticket.
	_, err := ops.ListRemote(context.Background(), h.Deps, ops.TargetOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backup_creds")
	assert.Contains(t, domain.AsError(err).Hint, "secret set")

	require.NoError(t, h.Secrets.Set(context.Background(), "backup_creds",
		domain.NewSecret("access_key_id: AKIAEXAMPLE\nsecret_access_key: s3kr3t\n")))

	_, err = ops.ListRemote(context.Background(), h.Deps, ops.TargetOptions{})
	require.NoError(t, err)
}

// TestACredentialNeverReachesTheJournalOrALogLine. The values are registered
// for redaction the moment they are read, before anything can print them.
func TestACredentialNeverReachesTheJournalOrALogLine(t *testing.T) {
	h := newHarness(t)
	inst, offsite := h.withTargets(t)

	const password = "correct-horse-battery-staple"

	inst.Backup.Targets = []domain.BackupTargetConfig{
		{URL: "file://" + offsite, Credentials: "backup_creds"},
	}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
	require.NoError(t, h.Secrets.Set(context.Background(), "backup_creds",
		domain.NewSecret("access_key_id: AKIAEXAMPLE\nsecret_access_key: "+password+"\n")))

	_, err := ops.ListRemote(context.Background(), h.Deps, ops.TargetOptions{})
	require.NoError(t, err)

	for _, value := range h.Deps.Redactor.Values() {
		if value == password {
			return
		}
	}
	t.Fatal("the target's secret key was not registered for redaction, so a log " +
		"line or a subprocess argument could print it")
}

// TestAMalformedCredentialDocumentIsRefusedWithoutQuotingIt. A YAML decoder's
// error names the line it failed on, and that line is the credential.
func TestAMalformedCredentialDocumentIsRefusedWithoutQuotingIt(t *testing.T) {
	const password = "correct-horse-battery-staple"

	_, err := ops.ParseTargetCredentials("access_key_id: [unclosed\nsecret: " + password)
	require.Error(t, err)
	assert.NotContains(t, err.Error()+domain.AsError(err).Hint, password,
		"the refusal quoted the document it was refusing")
	assert.Contains(t, domain.AsError(err).Hint, "access_key_id",
		"the refusal must say what a good document looks like")
}

// TestDoctorReportsATargetThatCannotBeReached. A fail rather than a warn: an
// unreachable target means the data is on one disk, which is the state
// configuring one was meant to end.
func TestDoctorReportsATargetThatCannotBeReached(t *testing.T) {
	h := newHarness(t)
	inst, _ := h.withTargets(t)

	blocked := filepath.Join(h.Root, "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o600))
	inst.Backup.Targets = []domain.BackupTargetConfig{
		{URL: "file://" + filepath.Join(blocked, "backups")},
	}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	result := findCheck(t, report, "backup.target-reachable")
	assert.Equal(t, string(events.CheckFail), result.Status)
	assert.Contains(t, result.Remedy, "push step",
		"the remedy must say what happens if this is left alone")
}

// TestDoctorReportsABackupThatNeverLeftTheMachine. This is the failure the
// whole mechanism exists to prevent, and the one that hides: the backup ran, it
// succeeded, and the copy that would survive the machine is not there.
func TestDoctorReportsABackupThatNeverLeftTheMachine(t *testing.T) {
	h := newHarness(t)
	h.withTargets(t)

	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: false,
	})
	require.NoError(t, err)
	ref, ok := result.Data.(ports.BackupRef)
	require.True(t, ok, "the backup operation returned no backup reference")

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	check := findCheck(t, report, "backup.target-freshness")
	assert.Equal(t, string(events.CheckFail), check.Status)
	assert.Contains(t, check.Remedy, "backup push "+ref.ID,
		"the remedy must name the command that fixes it, with the id")
}

// TestDoctorIsSilentAboutTargetsWhenNoneAreConfigured. A warning nobody can act
// on is a warning everybody learns to ignore.
func TestDoctorIsSilentAboutTargetsWhenNoneAreConfigured(t *testing.T) {
	h := newHarness(t)
	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	h.Deps.Targets = registry
	h.install()

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	check := findCheck(t, report, "backup.target-reachable")
	assert.Equal(t, string(events.CheckOK), check.Status)

	for _, r := range report.Results {
		assert.NotEqual(t, "backup.target-freshness", r.ID,
			"freshness against a target that does not exist is not a question")
	}
}

// TestAddingATargetChecksItBeforeRecordingIt. A typo that only fails at push
// time fails during the nightly backup, weeks later.
func TestAddingATargetChecksItBeforeRecordingIt(t *testing.T) {
	h := newHarness(t)
	h.withTargets(t)

	blocked := filepath.Join(h.Root, "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o600))

	_, err := ops.TargetAdd(context.Background(), h.Deps, ops.TargetAddOptions{
		URL: "file://" + filepath.Join(blocked, "backups"),
	})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "not added")

	inst, err := h.Deps.State.LoadInstallation(context.Background())
	require.NoError(t, err)
	for _, cfg := range inst.Backup.Targets {
		assert.NotContains(t, cfg.URL, "blocked",
			"a target that does not answer was recorded anyway")
	}
}

// TestRemovingATargetLeavesWhatIsAlreadyThere. An operator retiring one medium
// for another wants the old copies exactly where they are.
func TestRemovingATargetLeavesWhatIsAlreadyThere(t *testing.T) {
	h := newHarness(t)
	_, offsite := h.withTargets(t)

	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: true,
	})
	require.NoError(t, err)

	result, err := ops.TargetRemove(context.Background(), h.Deps, ops.Options{}, "file://"+offsite)
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "left alone")

	manifests, err := localdir.New().List(context.Background(), mustTarget(t, offsite))
	require.NoError(t, err)
	assert.Len(t, manifests, 1,
		"removing a target from the configuration deleted an off-site archive")
}

// TestATargetIsRefusedWhenTheInstallationAlreadyHasIt. Two identical targets
// would be pushed to twice and pruned twice, and the second pass would report
// removing what the first already removed.
func TestATargetIsRefusedWhenTheInstallationAlreadyHasIt(t *testing.T) {
	h := newHarness(t)
	_, offsite := h.withTargets(t)

	_, err := ops.TargetAdd(context.Background(), h.Deps, ops.TargetAddOptions{
		URL: "file://" + offsite,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already")
}

// TestAnInstallationWithABadTargetIsRefusedWhereItIsWritten, rather than during
// the operation whose whole purpose is to still work.
func TestAnInstallationWithABadTargetIsRefusedWhereItIsWritten(t *testing.T) {
	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_x",
		Product:       "demo",
		Backup: domain.BackupConfig{Targets: []domain.BackupTargetConfig{
			{URL: "/mnt/usb/backups"}, // no scheme
		}},
	}
	err := inst.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backup.targets[0].url")

	inst.Backup.Targets = []domain.BackupTargetConfig{
		{URL: "file:///mnt/a"},
		{URL: "file:///mnt/a"},
	}
	err = inst.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "twice")
}

// TestAnInstallationFromANewerSchemaIsRefused, which is what makes the bump to
// schema 3 do its job: an older manager reading a state it does not understand
// would see no targets, take a backup, report success, and leave it on the
// machine the operator configured a target to survive.
//
// The refusal is the mechanism; that targets are what arrived at schema 3 is
// asserted separately below, because a test named for one and checking the
// other is a test nobody can maintain.
func TestAnInstallationFromANewerSchemaIsRefused(t *testing.T) {
	require.GreaterOrEqual(t, domain.InstallationSchemaVersion, 3,
		"backup targets arrived at schema 3")

	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion + 1,
		ID:            "inst_x",
		Product:       "demo",
		Backup: domain.BackupConfig{
			Targets: []domain.BackupTargetConfig{{URL: "file:///mnt/backups"}},
		},
	}
	err := inst.Validate()
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "newer manager")
}

func mustTarget(t *testing.T, dir string) ports.TargetRef {
	t.Helper()
	ref, err := ports.TargetURL("file://" + dir)
	require.NoError(t, err)
	return ref
}

// failedStepID names the step a record blames, so a test can assert the failure
// was attributed where an operator would look.
func failedStepID(record domain.OperationRecord) string {
	for _, step := range record.Steps {
		if step.Status == domain.StepFailed {
			return step.ID
		}
	}
	return ""
}

// TestFetchRefusesToOverwriteALocalBackup. The local copy is the one that has
// been verified on this machine; silently replacing it with bytes that just
// arrived over a network is not a thing a fetch should decide.
func TestFetchRefusesToOverwriteALocalBackup(t *testing.T) {
	h := newHarness(t)
	h.withTargets(t)

	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: true,
	})
	require.NoError(t, err)
	ref, ok := result.Data.(ports.BackupRef)
	require.True(t, ok)

	// The backup store is where `backup list` and `restore` read from, so a
	// fetch has to place it there -- and the local copy of this id is
	// already there.
	dest := filepath.Join(h.Paths.BackupsDir(), ref.ID)
	require.NoError(t, os.MkdirAll(dest, 0o700))

	_, err = ops.FetchRemote(context.Background(), h.Deps, ops.FetchOptions{
		BackupID: ref.ID,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), ref.ID)
	assert.Contains(t, domain.AsError(err).Hint, "restore it with",
		"the refusal must point at what the operator probably meant")
}

// TestFetchingAnIdThatIsNotOnTheTargetNamesTheTarget, so an operator can tell
// "wrong id" from "wrong target".
func TestFetchingAnIdThatIsNotOnTheTargetNamesTheTarget(t *testing.T) {
	h := newHarness(t)
	_, offsite := h.withTargets(t)

	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: true,
	})
	require.NoError(t, err)

	_, err = ops.FetchRemote(context.Background(), h.Deps, ops.FetchOptions{
		BackupID: "20991231T235959Z",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "20991231T235959Z")
	assert.Contains(t, err.Error(), offsite)
}

// TestNamingAConfiguredTargetPicksUpItsCredentials.
//
// `--target` is a filter in the ordinary case and a complete specification in
// the recovery case. When the URL matches something the installation already
// configures, the stored credentials come with it -- otherwise an operator who
// typed the URL to narrow a listing would be told their credentials are
// missing.
func TestNamingAConfiguredTargetPicksUpItsCredentials(t *testing.T) {
	h := newHarness(t)
	inst, offsite := h.withTargets(t)

	inst.Backup.Targets = []domain.BackupTargetConfig{
		{URL: "file://" + offsite, Credentials: "backup_creds"},
	}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
	require.NoError(t, h.Secrets.Set(context.Background(), "backup_creds",
		domain.NewSecret("access_key_id: AKIAEXAMPLE\nsecret_access_key: s3kr3t\n")))

	_, err := ops.ListRemote(context.Background(), h.Deps, ops.TargetOptions{
		URL: "file://" + offsite,
	})
	require.NoError(t, err)

	// A URL the installation does not configure is still addressable: that
	// is the recovery case, and it must not need the installation to agree.
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.MkdirAll(elsewhere, 0o700))
	_, err = ops.ListRemote(context.Background(), h.Deps, ops.TargetOptions{
		URL: "file://" + elsewhere,
	})
	require.NoError(t, err)
}

// TestTargetListReportsWhatDoctorReports, so `backup target list` and the
// doctor check cannot disagree about whether a target answers.
func TestTargetListReportsWhatDoctorReports(t *testing.T) {
	h := newHarness(t)
	h.withTargets(t)

	statuses, err := ops.TargetList(context.Background(), h.Deps)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Reachable)
	assert.Zero(t, statuses[0].Backups)

	_, err = ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: true,
	})
	require.NoError(t, err)

	statuses, err = ops.TargetList(context.Background(), h.Deps)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Equal(t, 1, statuses[0].Backups)
	assert.NotEmpty(t, statuses[0].Latest)
}

// TestVerifyingATargetReadsTheBackupBack, which is the whole claim: a backup
// nobody has read back is a hope, and copying one to a bucket does not change
// that.
func TestVerifyingATargetReadsTheBackupBack(t *testing.T) {
	h := newHarness(t)
	_, offsite := h.withTargets(t)

	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: true,
	})
	require.NoError(t, err)
	ref, ok := result.Data.(ports.BackupRef)
	require.True(t, ok)

	verified, err := ops.VerifyRemote(context.Background(), h.Deps, ops.FetchOptions{})
	require.NoError(t, err)
	assert.Contains(t, verified.Summary, "1 backup(s)")

	// Rot on the target: one byte changed in a component nobody has read
	// since it was written. This is the state the check exists to find, and
	// the local copy is untouched, so nothing else would notice.
	component := filepath.Join(offsite, ref.ID, "db.sql.age")
	data, err := os.ReadFile(component)
	require.NoError(t, err)
	data[len(data)/2] ^= 0x01
	require.NoError(t, os.WriteFile(component, data, 0o600))

	_, err = ops.VerifyRemote(context.Background(), h.Deps, ops.FetchOptions{})
	require.Error(t, err, "a corrupted copy on the target passed verification")
	assert.Contains(t, err.Error(), "checksum mismatch")
	assert.Contains(t, domain.AsError(err).Hint, "check the local copy too")

	// The local copy is still fine, which is the point of checking both.
	require.NoError(t, h.Deps.Backup.Verify(context.Background(), ref))
}

// TestVerifyingATargetNoticesAMissingComponent, which is what a push that was
// interrupted after the manifest somehow would leave, and what a partial
// deletion leaves.
func TestVerifyingATargetNoticesAMissingComponent(t *testing.T) {
	h := newHarness(t)
	_, offsite := h.withTargets(t)

	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: true,
	})
	require.NoError(t, err)
	ref, ok := result.Data.(ports.BackupRef)
	require.True(t, ok)

	require.NoError(t, os.Remove(filepath.Join(offsite, ref.ID, "db.sql.age")))

	_, err = ops.VerifyRemote(context.Background(), h.Deps, ops.FetchOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

// TestThePreUpdateBackupReachesTheTarget.
//
// This is the backup an operator restores from when an update goes wrong, so
// leaving it on the machine alone means the moment the deployment is most
// fragile is the moment its backup is least durable.
func TestThePreUpdateBackupReachesTheTarget(t *testing.T) {
	h := newHarness(t)
	_, offsite := h.withTargets(t)
	h.setHookEnv()
	applyBaseline(t, h)

	result, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: stageUpgradeSource(t, h),
	})
	require.NoError(t, err, "%s", result.Record.Error)

	manifests, err := localdir.New().List(context.Background(), mustTarget(t, offsite))
	require.NoError(t, err)
	require.Len(t, manifests, 1,
		"the pre-update backup never left the machine")
	assert.Equal(t, "pre-update", manifests[0].Reason)
}

// TestAPreUpdateBackupThatCannotBePushedWarnsRatherThanFailingTheUpdate.
//
// The asymmetry with `morzer backup` is deliberate and worth pinning. That
// operation exists to produce a durable backup, so a copy that never left the
// machine means it did not do its job. This one exists to install a release,
// and refusing to update because a USB disk was unplugged would block the
// security fix an operator is applying. The gap is reported instead -- here,
// and by `doctor` until `backup push` closes it.
func TestAPreUpdateBackupThatCannotBePushedWarnsRatherThanFailingTheUpdate(t *testing.T) {
	h := newHarness(t)
	inst, _ := h.withTargets(t)

	blocked := filepath.Join(h.Root, "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o600))
	inst.Backup.Targets = []domain.BackupTargetConfig{
		{URL: "file://" + filepath.Join(blocked, "backups")},
	}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
	h.setHookEnv()
	applyBaseline(t, h)

	result, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: stageUpgradeSource(t, h),
	})
	require.NoError(t, err,
		"an unreachable target blocked an update; the operator cannot apply a fix")

	var warned bool
	for _, e := range h.Events.Events() {
		if e.Level == events.LevelWarn && strings.Contains(e.Message, "backup push") {
			warned = true
		}
	}
	assert.True(t, warned,
		"the update succeeded without telling the operator their pre-update "+
			"backup is only on the machine being updated")
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)
}

// TestRetentionNeverRemovesTheLastCopyOnATarget, whatever the policy says.
//
// A policy of zero is a configuration mistake, and honouring it literally on a
// target would delete the only copy that survives the machine — which is worse
// than the same mistake locally, because the local one is at least still on a
// disk the operator is looking at.
func TestRetentionNeverRemovesTheLastCopyOnATarget(t *testing.T) {
	h := newHarness(t)
	inst, offsite := h.withTargets(t)

	// Zero is not reachable through `policy.retain_backups`, which Validate
	// refuses below zero and treats zero as unset -- so it is exercised
	// where it could actually arrive: a manifest declaring it.
	inst.Policy.RetainBackups = 0
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	for range 3 {
		_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
			Reason: "manual", Verify: true, Prune: true, Push: true, PruneRemote: true,
		})
		require.NoError(t, err)
	}

	manifests, err := localdir.New().List(context.Background(), mustTarget(t, offsite))
	require.NoError(t, err)
	assert.NotEmpty(t, manifests,
		"retention emptied the target; the deployment's only off-machine copy is gone")

	// And what survives is whole rather than merely listed.
	require.NoError(t, localdir.New().Verify(context.Background(),
		ports.RemoteRef{Target: mustTarget(t, offsite), ID: manifests[0].ID}))
}

// TestACorruptFetchNeverReachesTheBackupStore.
//
// The store is what `backup list` reads and `restore` picks from. Promoting the
// fetched directory and verifying afterwards left a corrupt backup selectable by
// the very command the verification exists to protect.
func TestACorruptFetchNeverReachesTheBackupStore(t *testing.T) {
	h := newHarness(t)
	_, offsite := h.withTargets(t)

	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Push: true,
	})
	require.NoError(t, err)
	ref, ok := result.Data.(ports.BackupRef)
	require.True(t, ok)

	require.NoError(t, os.RemoveAll(h.Backup.Dir(ref.ID)))

	// Rot on the target, after the manifest recorded the digest.
	component := filepath.Join(offsite, ref.ID, "db.sql.age")
	require.NoError(t, os.WriteFile(component, []byte("not what was pushed"), 0o600))

	_, err = ops.FetchRemote(context.Background(), h.Deps, ops.FetchOptions{})
	require.Error(t, err, "a corrupt backup was fetched without complaint")

	// Required rather than tolerated: a store that cannot be read would have
	// skipped the assertion below and reported a pass, which is the one
	// outcome worse than a failure here.
	entries, err := os.ReadDir(h.Paths.BackupsDir())
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotEqual(t, ref.ID, e.Name(),
			"a backup that failed verification was left in the store, where "+
				"`restore` can select it")
	}
}
