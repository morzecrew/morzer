package suite

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
)

// RFC 0034: `installation remove` takes an installation off the machine, keeps
// the data and what unlocks it unless told to purge, and leaves a record of
// itself.

// removable is an applied installation with everything a removal has to find:
// units, data, backups, a statement, secret state and a machine key.
func removable(t *testing.T) (*harness, domain.Installation) {
	t.Helper()
	h := verifyHarness(t)
	inst := signingInstallation(t, h)

	h.Deps.Supervisor = h.Supervisor
	for _, unit := range h.Supervisor.ManagedUnitNames("demo")[:3] {
		h.Supervisor.Installed[unit] = []byte("[Unit]\n")
	}
	applyOnce(t, h)

	for path, body := range map[string]string{
		filepath.Join(h.Paths.DataDir(), "uploads", "a.png"):       "png",
		filepath.Join(h.Paths.BackupsDir(), "bk_1", "backup.json"): "{}",
		h.Paths.InstallationFile():                                 "product: demo\n",
		h.Paths.SecretsFile():                                      "sops: {}\n",
		h.Paths.AgeIdentityFile():                                  "AGE-SECRET-KEY-1",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	return h, inst
}

func removeIt(t *testing.T, h *harness, opts ops.RemoveOptions) (ops.Result, ops.RemoveReport) {
	t.Helper()
	opts.Force = true
	res, err := ops.Remove(context.Background(), h.Deps, opts)
	require.NoError(t, err)
	report, ok := res.Data.(ops.RemoveReport)
	require.True(t, ok, "the result carries no report")
	return res, report
}

// journalIDs reads the operation ids a journal file holds, in order.
func journalIDs(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	var ids []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var rec domain.OperationRecord
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &rec))
		ids = append(ids, rec.ID)
	}
	require.NoError(t, scanner.Err())
	return ids
}

func TestARemovalKeepsTheDataAndWhatUnlocksIt(t *testing.T) {
	h, inst := removable(t)

	res, report := removeIt(t, h, ops.RemoveOptions{ConfirmedInstallationID: inst.ID})
	assert.Equal(t, domain.StatusSucceeded, res.Record.Status)
	assert.Equal(t, domain.OpTypeRemove, res.Record.Type)

	// Gone: what runs it, what it ran, and what made it an installation.
	assert.Empty(t, h.Supervisor.Installed, "a unit survived, and would start the product at boot")
	assert.Contains(t, h.Runtime.Calls, "Down")
	assert.NotContains(t, h.Runtime.Calls, "Down(volumes)", "the default must keep the named volumes")
	for _, gone := range []string{
		h.Paths.OptDir, h.Paths.RunDir, h.Paths.InstallationFile(), h.Paths.ApplicationFile(),
		h.Paths.SigningDir(), h.Paths.InstallationState(), h.Paths.CurrentReleaseFile(),
		h.Paths.JournalFile(),
	} {
		assert.NoFileExists(t, gone)
		assert.NoDirExists(t, gone)
	}
	exists, err := h.Deps.State.InstallationExists(context.Background())
	require.NoError(t, err)
	assert.False(t, exists, "a later `init` would refuse: an installation still exists")

	// Kept: the data, and what it takes to read it again.
	for _, kept := range []string{
		filepath.Join(h.Paths.DataDir(), "uploads", "a.png"),
		filepath.Join(h.Paths.BackupsDir(), "bk_1", "backup.json"),
		h.Paths.SecretsFile(), h.Paths.AgeIdentityFile(),
	} {
		assert.FileExists(t, kept)
	}
	assert.True(t, report.VolumesKept)
	assert.Contains(t, report.Kept, h.Paths.DataDir())
	assert.Contains(t, report.Kept, h.Paths.AgeDir())

	// The record: the journal under the removal's name, and a signed
	// statement of the removal itself, made with the key that is now gone.
	assert.Equal(t, h.Paths.RemovedJournalFile(res.Record.ID), report.Journal)
	assert.Contains(t, journalIDs(t, report.Journal), res.Record.ID)
	statement := filepath.Join(h.Paths.AttestationsDir(), res.Record.ID+".json")
	assert.FileExists(t, statement)
	assert.FileExists(t, statement+".minisig", "the removal's statement went unsigned")
}

func TestPurgeLeavesOnlyTheRemovalsRecord(t *testing.T) {
	h, inst := removable(t)
	earlier := statementsOnDisk(t, h)
	require.NotEmpty(t, earlier)

	res, report := removeIt(t, h, ops.RemoveOptions{Purge: true, ConfirmedInstallationID: inst.ID})

	assert.Contains(t, h.Runtime.Calls, "Down(volumes)")
	assert.False(t, report.VolumesKept)
	for _, gone := range []string{h.Paths.DataDir(), h.Paths.BackupsDir(), h.Paths.EtcDir} {
		assert.NoDirExists(t, gone)
	}

	assert.Equal(t, []string{filepath.Join(h.Paths.AttestationsDir(), res.Record.ID+".json")},
		statementsOnDisk(t, h), "only the removal's own statement may be left")
	for _, id := range journalIDs(t, report.Journal) {
		assert.Equal(t, res.Record.ID, id, "the purged journal still holds another operation")
	}

	entries, err := os.ReadDir(h.Paths.VarDir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"attestations", "manager"}, names)
}

func TestARemovalIsConfirmedBeforeAnythingIsJournaled(t *testing.T) {
	h, inst := removable(t)
	ctx := context.Background()
	before := journalIDs(t, h.Paths.JournalFile())

	_, err := ops.Remove(ctx, h.Deps, ops.RemoveOptions{ConfirmedInstallationID: inst.ID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")

	_, err = ops.Remove(ctx, h.Deps, ops.RemoveOptions{Options: ops.Options{Force: true}})
	require.Error(t, err, "production was removed without its id")
	assert.Contains(t, domain.AsError(err).Hint, "--confirm "+inst.ID)

	assert.Equal(t, before, journalIDs(t, h.Paths.JournalFile()),
		"a refusal journaled a removal that never ran")
	assert.FileExists(t, h.Paths.InstallationState())
}

func TestADevInstallationNeedsOnlyForce(t *testing.T) {
	h, inst := removable(t)
	inst.Mode = domain.ModeDev
	require.NoError(t, os.Remove(h.Paths.InstallationState()))
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	// A typed id naming some other installation is aimed at the wrong
	// machine, sandbox or not.
	_, err := ops.Remove(context.Background(), h.Deps, ops.RemoveOptions{
		Options: ops.Options{Force: true}, ConfirmedInstallationID: "inst_SOMEONEELSE",
	})
	require.Error(t, err)

	res, _ := removeIt(t, h, ops.RemoveOptions{})
	assert.Equal(t, domain.StatusSucceeded, res.Record.Status)
}

// A failed first install leaves directories and an age identity, and no record
// to read a mode or an id from (§5.5).
func TestWhatAFailedFirstInstallLeftIsRemovedWithForce(t *testing.T) {
	h := newHarness(t)
	require.NoError(t, os.MkdirAll(h.Paths.AgeDir(), 0o700))
	require.NoError(t, os.WriteFile(h.Paths.AgeIdentityFile(), []byte("AGE-SECRET-KEY-1"), 0o400))

	res, report := removeIt(t, h, ops.RemoveOptions{Purge: true})

	assert.Equal(t, domain.StatusSucceeded, res.Record.Status)
	assert.NotContains(t, h.Runtime.Calls, "Down", "nothing of a failed init was ever started")
	assert.NoDirExists(t, h.Paths.EtcDir)
	assert.NoDirExists(t, h.Paths.OptDir)
	assert.Empty(t, res.Record.InstallationID)
	assert.NotEmpty(t, report.Journal)
}

func TestRemovingWhatIsNotThereIsRefused(t *testing.T) {
	h := newHarness(t)
	for _, dir := range []string{h.Paths.EtcDir, h.Paths.VarDir, h.Paths.RunDir, h.Paths.OptDir} {
		require.NoError(t, os.RemoveAll(dir))
	}

	_, err := ops.Remove(context.Background(), h.Deps, ops.RemoveOptions{Options: ops.Options{Force: true}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing of demo")
	assert.NoDirExists(t, h.Paths.VarDir, "a removal of nothing created the journal it would write")
}

func TestARemovalPlanChangesNothing(t *testing.T) {
	h, inst := removable(t)
	before := journalIDs(t, h.Paths.JournalFile())

	res, report := removeIt(t, h, ops.RemoveOptions{
		Options: ops.Options{DryRun: true}, ConfirmedInstallationID: inst.ID,
	})

	assert.True(t, res.Record.DryRun)
	assert.True(t, report.DryRun)
	assert.Contains(t, res.Summary, "would remove")
	assert.NotContains(t, h.Runtime.Calls, "Down")
	assert.NotEmpty(t, h.Supervisor.Installed)
	assert.DirExists(t, h.Paths.OptDir)
	assert.FileExists(t, h.Paths.InstallationState())
	assert.Equal(t, before, journalIDs(t, h.Paths.JournalFile()))
}

// A wreck is one of the things a removal is for, so it is not gated on one
// (decision 4) -- and the journal it leaves behind is renamed, so the next
// installation does not inherit the gate either (decision 8).
func TestARemovalIsNotHeldByAWreckAndDoesNotPassItOn(t *testing.T) {
	h, inst := removable(t)
	ctx := context.Background()
	require.NoError(t, h.Deps.State.AppendOperation(ctx, domain.OperationRecord{
		ID: "op_WRECK", Type: domain.OpTypeUpdate, Status: domain.StatusManualIntervention,
		StartedAt: domain.NewTime(h.Deps.Now()),
	}))

	removeIt(t, h, ops.RemoveOptions{ConfirmedInstallationID: inst.ID})

	unfinished, err := h.Deps.State.UnfinishedOperations(ctx)
	require.NoError(t, err)
	assert.Empty(t, unfinished, "a reinstall would refuse to apply over the old installation's wreck")
}

// Images go by RFC 0031's rule (decision 6): a name another installation's
// release still uses stays.
func TestARemovalRemovesOnlyImagesNoOtherReleaseNames(t *testing.T) {
	h, inst := removable(t)
	storeRelease(t, h.otherInstallation(t), "9.0.0", pulled(appRepo, 1), pulled(dbRepo, 9))

	_, report := removeIt(t, h, ops.RemoveOptions{ConfirmedInstallationID: inst.ID})

	assert.Equal(t, []string{pulled(dbRepo, 2)}, h.Runtime.RemovedImages,
		"app@1 is named by the other installation's release")
	assert.Equal(t, h.Runtime.RemovedImages, report.Images.Removed)
}

// The project is taken down even when its release cannot be read, by name
// (decision 10).
func TestAnUnreadableReleaseIsTakenDownByName(t *testing.T) {
	h, inst := removable(t)
	require.NoError(t, os.RemoveAll(h.Release.Root))

	removeIt(t, h, ops.RemoveOptions{ConfirmedInstallationID: inst.ID})

	require.Len(t, h.Runtime.DownConfigs, 1)
	cfg := h.Runtime.DownConfigs[0]
	assert.Empty(t, cfg.Files, "a release that cannot be read has no files to name")
	assert.Equal(t, "demo", cfg.Product)
}

// A removal that fails part way is finished by running it again, or resuming
// it, and the record it has not yet forgotten still asks for the same
// confirmation (decision 5).
func TestAFailedRemovalIsFinishedByResumingIt(t *testing.T) {
	h, inst := removable(t)
	ctx := context.Background()
	h.Supervisor.Fail["RemoveUnits"] = errors.New("systemctl is not answering")

	_, err := ops.Remove(ctx, h.Deps, ops.RemoveOptions{
		Options: ops.Options{Force: true}, ConfirmedInstallationID: inst.ID,
	})
	require.Error(t, err)
	assert.FileExists(t, h.Paths.InstallationState(), "a failed removal forgot the installation")

	_, err = ops.Remove(ctx, h.Deps, ops.RemoveOptions{
		Options: ops.Options{Force: true, Resume: true},
	})
	require.Error(t, err, "a resumed removal skipped the confirmation")

	delete(h.Supervisor.Fail, "RemoveUnits")
	res, report := removeIt(t, h, ops.RemoveOptions{
		Options: ops.Options{Resume: true}, ConfirmedInstallationID: inst.ID,
	})
	assert.Equal(t, domain.StatusSucceeded, res.Record.Status)
	assert.Empty(t, h.Supervisor.Installed)
	// The journal is named after the record the resume continued, which is
	// the id its statement and every line in it carry.
	assert.Equal(t, h.Paths.RemovedJournalFile(res.Record.ID), report.Journal)
	assert.FileExists(t, filepath.Join(h.Paths.AttestationsDir(), res.Record.ID+".json"))
}

// The statement of a removal is pushed with credentials resolved before the
// secret state they are read from went (§5.4). Played by taking the
// credentials away as the project comes down, which is before
// remove-configuration deletes the file they live in.
func TestTheRemovalsStatementLeavesWithCredentialsReadBeforeTheSecretsWent(t *testing.T) {
	h, inst := removable(t)
	ctx := context.Background()

	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	h.Deps.Targets, h.Deps.Objects = registry, registry
	offsite := filepath.Join(t.TempDir(), "offsite")
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + offsite, Credentials: "offsite_creds"}}
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	require.NoError(t, h.Secrets.Set(ctx, "offsite_creds", domain.NewSecret("access_key_id: AKIA\n")))

	h.Runtime.OnCall = func(method string) {
		if method == "Down" {
			_ = h.Secrets.Remove(ctx, "offsite_creds")
		}
	}

	res, _ := removeIt(t, h, ops.RemoveOptions{ConfirmedInstallationID: inst.ID})

	assert.Contains(t, pushedStatements(t, offsite), res.Record.ID+".json",
		"the removal's statement never left the machine")
	assert.Contains(t, pushedStatements(t, offsite), res.Record.ID+".json.minisig")
}

// Units left behind after the directories were deleted by hand are still
// something of the product's, and still started at boot.
func TestUnitsLeftWithoutDirectoriesAreStillRemoved(t *testing.T) {
	h := newHarness(t)
	for _, dir := range []string{h.Paths.EtcDir, h.Paths.VarDir, h.Paths.RunDir, h.Paths.OptDir} {
		require.NoError(t, os.RemoveAll(dir))
	}
	h.Deps.Supervisor = h.Supervisor
	h.Supervisor.Installed["demo.service"] = []byte("[Unit]\n")

	res, _ := removeIt(t, h, ops.RemoveOptions{})

	assert.Equal(t, domain.StatusSucceeded, res.Record.Status)
	assert.Empty(t, h.Supervisor.Installed)
}

// A host where systemd is not running cannot stop or disable a unit, and the
// files left behind start the product at the next boot that has it -- so the
// removal says so rather than reporting the step done.
func TestUnitsAHostCannotStopAreReportedNotIgnored(t *testing.T) {
	h, inst := removable(t)
	h.Supervisor.Present = false

	var warned string
	h.Deps.Bus.SubscribeFunc(func(e events.Event) {
		if e.Level == events.LevelWarn && strings.Contains(e.Message, "demo.service") {
			warned = e.Message
		}
	})

	removeIt(t, h, ops.RemoveOptions{ConfirmedInstallationID: inst.ID})
	assert.NotEmpty(t, warned, "unit files were left in place without a word")
}

// forget-installation forgets the record last, so a failure part way through
// it still leaves the confirmation a retry has to ask for.
func TestAFailedForgetKeepsTheRecordForTheRetry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes what a 0500 directory holds, so this arrangement proves nothing")
	}
	h, inst := removable(t)
	stuck := filepath.Join(h.Paths.StagingDir(), "stuck")
	require.NoError(t, os.MkdirAll(stuck, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stuck, "f"), nil, 0o644))
	require.NoError(t, os.Chmod(stuck, 0o500))
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o755) })

	_, err := ops.Remove(context.Background(), h.Deps, ops.RemoveOptions{
		Options: ops.Options{Force: true}, ConfirmedInstallationID: inst.ID,
	})
	require.Error(t, err)
	assert.FileExists(t, h.Paths.InstallationState(),
		"a retry would no longer ask for the production installation's id")
}
