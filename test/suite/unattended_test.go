package suite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// The auto-apply gate decides whether a release may install itself on a machine
// nobody is watching. What it promises is narrower than it first reads, and the
// tests are written to that promise: not "no human will be needed", but "the
// failure will not be the unrecoverable kind".

func compat(produces int, rollbackSafe bool) domain.Compatibility {
	return domain.Compatibility{
		DatabaseSchemaMin:      1,
		DatabaseSchemaMax:      produces,
		DatabaseSchemaProduces: produces,
		RollbackSafe:           rollbackSafe,
	}
}

// signedInstallation is a machine that could auto-apply if the release let it.
func signedInstallation() domain.Installation {
	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_01",
		Product:       "demo",
		Policy:        domain.DefaultPolicy(),
	}
	inst.Policy.RequireSignature = true
	inst.Policy.SigningKeys = []string{"RWQfakekey"}
	return inst
}

func TestTheAutoApplyGate(t *testing.T) {
	// The current release reads up to schema 14 and produced 14.
	current := compat(14, true)

	cases := []struct {
		name    string
		current domain.Compatibility
		target  domain.Compatibility
		inst    func() domain.Installation
		ok      bool
		because string
	}{
		{
			name:    "a release that declares what it produces and stays readable",
			current: current,
			target:  compat(14, true),
			inst:    signedInstallation,
			ok:      true,
		},
		{
			name:    "a release that declares nothing about its migrations",
			current: current,
			target:  domain.Compatibility{RollbackSafe: true, DatabaseSchemaMax: 14},
			inst:    signedInstallation,
			// Without this half, the field could default to something
			// permissive and no test would notice.
			because: "database_schema_produces",
		},
		{
			name:    "a release declaring a schema number that cannot be true",
			current: current,
			// Every comparison downstream is guarded by `> 0`, so a
			// negative prediction is no prediction at all -- it would
			// pass the schema half of the gate by not being looked
			// at, while a release declaring *nothing* is refused.
			// Worse than absent must not be treated as better.
			target:  domain.Compatibility{DatabaseSchemaMax: 14, DatabaseSchemaProduces: -1, RollbackSafe: true},
			inst:    signedInstallation,
			because: "database_schema_produces",
		},
		{
			name:    "a release whose migrations cannot be undone",
			current: current,
			target:  compat(14, false),
			inst:    signedInstallation,
			because: "rollback_safe",
		},
		{
			name:    "a release that leaves a schema the previous one cannot read",
			current: compat(14, true),
			// Produces 15, and the installed release reads at most 14:
			// rolling the containers back would leave the application
			// reading a schema it does not understand.
			target:  domain.Compatibility{DatabaseSchemaMax: 15, DatabaseSchemaProduces: 15, RollbackSafe: true},
			inst:    signedInstallation,
			because: "schema",
		},
		{
			name:    "a machine that does not require signatures",
			current: current,
			target:  compat(14, true),
			inst: func() domain.Installation {
				inst := signedInstallation()
				inst.Policy.RequireSignature = false
				return inst
			},
			because: "require_signature",
		},
		{
			name:    "a machine with the pre-update backup disabled",
			current: current,
			target:  compat(14, true),
			inst: func() domain.Installation {
				inst := signedInstallation()
				inst.Policy.SkipBackupBeforeUpdate = true
				return inst
			},
			because: "skip_backup_before_update",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.AssessUnattended(tc.current, tc.target, tc.inst())

			if tc.ok {
				assert.True(t, got.OK, "refused: %s", got.Why())
				return
			}
			require.False(t, got.OK, "this should not install itself")
			assert.Contains(t, got.Why(), tc.because,
				"the refusal does not name what has to change")
		})
	}
}

// TestASandboxSkipsTheRecoveryGateAndKeepsTheSignatureOne.
//
// Both halves in one test, because either alone would pass on a gate that had
// been deleted. A sandbox's data is disposable, so nothing about recovering it
// applies; its *fidelity* is the whole point, so relaxing verification would
// mean the rehearsal is not of the thing being shipped.
func TestASandboxSkipsTheRecoveryGateAndKeepsTheSignatureOne(t *testing.T) {
	sandbox := signedInstallation()
	sandbox.Mode = domain.ModeDev

	// A release declaring none of what production requires.
	target := domain.Compatibility{RollbackSafe: false}

	got := domain.AssessUnattended(compat(14, true), target, sandbox)
	assert.True(t, got.OK, "a sandbox refused an update it has nothing to lose to: %s", got.Why())

	unsigned := sandbox
	unsigned.Policy.RequireSignature = false
	refused := domain.AssessUnattended(compat(14, true), target, unsigned)
	assert.False(t, refused.OK,
		"a sandbox skipped signature verification, so the rehearsal is not of the release being shipped")
}

// TestAutoApplyIsRefusedAtConfigurationTime.
//
// At the moment the setting is written rather than at the tick that would have
// acted on it, in the same shape as `--skip-backup` requiring `--force`. A
// machine that accepts the setting and then refuses to act every night is worse
// than one that refuses the setting: the operator believes it is armed.
func TestAutoApplyIsRefusedAtConfigurationTime(t *testing.T) {
	inst := signedInstallation()
	inst.Policy.RequireSignature = false
	inst.Update.AutoApply = true

	err := inst.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "require_signature")

	inst.Policy.RequireSignature = true
	assert.NoError(t, inst.Validate())
}

// TestModeCannotChangeThroughAnyManagerSurface.
//
// Asserted at the state store rather than per command, because that is where
// the rule lives: every surface that changes an installation writes through
// here, so a command added next year cannot forget it.
//
// Not a test that a hand-edited state file is detected -- `mode` is a field in a
// JSON file and root can edit it. Defending one boolean against an operator who
// can equally edit the recipient list would be defending the wrong thing.
func TestModeCannotChangeThroughAnyManagerSurface(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	production := h.install()
	require.False(t, production.IsDev())

	// Demotion: real data would immediately be under relaxed rules.
	demoted := production
	demoted.Mode = domain.ModeDev
	err := h.Deps.State.SaveInstallation(ctx, demoted)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mode is fixed")

	// And the reverse, which the first draft of this design allowed. It is
	// the quieter of the two and lands during an incident: you find out at
	// rollback time that `previous` was pruned and no pre-update backup was
	// ever taken.
	sandbox := newHarness(t)
	created := sandbox.install()

	// A machine with no installation is *creating* one, which is the only
	// moment a mode may be chosen. Removing the state is how this test gets
	// back to that moment rather than by writing a mode change it is here
	// to prove impossible.
	require.NoError(t, os.Remove(sandbox.Paths.InstallationState()))
	created.Mode = domain.ModeDev
	require.NoError(t, sandbox.Deps.State.SaveInstallation(ctx, created))

	promoted := created
	promoted.Mode = ""
	err = sandbox.Deps.State.SaveInstallation(ctx, promoted)
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "backup, fresh `init`, restore")
}

// TestAStateFileThatWillNotParseCannotLaunderAModeChange.
//
// The guard read the existing installation and treated *any* failure as a
// creation, on the reasoning that an unreadable state fails on its own terms at
// the next read. It does not: the write it was about to allow replaces the file
// with valid content, so the next read succeeds and reports whatever mode was
// written over it. A production machine whose state was momentarily corrupt --
// including one the new unknown-mode rule rejects -- could be silently rewritten
// as a sandbox, and a sandbox promoted.
//
// The assertion is on the file, not only on the error: a refusal that still
// wrote would be the defect with a message attached.
func TestAStateFileThatWillNotParseCannotLaunderAModeChange(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	production := h.install()
	require.False(t, production.IsDev())

	path := h.Paths.InstallationState()
	corrupt := []byte("{\"schema_version\": 5, \"product\":\n")
	require.NoError(t, os.WriteFile(path, corrupt, 0o640))

	demoted := production
	demoted.Mode = domain.ModeDev
	err := h.Deps.State.SaveInstallation(ctx, demoted)
	require.Error(t, err, "an unreadable state file admitted a mode change")
	assert.Contains(t, err.Error(), "mode")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, corrupt, after,
		"the refusal wrote the installation anyway, which is what it was refusing")
}

// TestImportingAsASandboxDropsTheBackupTargets is the assertion that stops a
// sandbox writing into production's bucket.
//
// The setup is the dangerous one, deliberately: an export from a machine that
// backs up to a real target, carrying the credentials, imported as a sandbox.
// Import keeps the original installation id -- which is the point of importing
// rather than re-initialising -- so a sandbox that kept the targets would push
// throwaway backups into the customer's bucket under a matching id.
//
// It must fail if the drop is ever removed for convenience, which is why the
// assertion is on the rebuilt installation rather than on the summary.
func TestImportingAsASandboxDropsTheBackupTargets(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)

	inst, err := origin.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Backup.Targets = []domain.BackupTargetConfig{{
		URL:         "s3://backups.example/demo",
		Credentials: "backup_credentials",
	}}
	// The second thing on the drop list, and the one nobody had noticed. A
	// notify target's endpoint may *be* its credential -- a Slack webhook
	// URL is a bearer token spelled as a path -- so a sandbox that kept it
	// would page the customer's on-call about a machine that exists in
	// order to be broken.
	inst.Notify.Targets = []domain.NotifyTargetConfig{{
		Name:      "oncall",
		URLSecret: "slack_webhook",
	}}
	require.NoError(t, origin.Deps.State.SaveInstallation(ctx, inst))

	exportPath := filepath.Join(t.TempDir(), "demo.export.yaml")
	_, err = ops.Export(ctx, origin.Deps, ops.ExportOptions{Path: exportPath})
	require.NoError(t, err)

	export, err := ops.LoadExport(exportPath)
	require.NoError(t, err)
	require.NotEmpty(t, export.Installation.Backup.Targets,
		"the export must carry the targets, or this test proves nothing")
	require.NotEmpty(t, export.Installation.Notify.Targets,
		"the export must carry the notify targets, or this test proves nothing")

	sandbox := newMachine(t, t.TempDir())
	result, err := ops.Import(ctx, sandbox.Deps, ops.ImportOptions{
		SourcePath:   exportPath,
		Export:       export,
		IdentityFile: recoveryPath,
		Mode:         domain.ModeDev,
		ModeSet:      true,
	})
	require.NoError(t, err)

	rebuilt, err := sandbox.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.True(t, rebuilt.IsDev())
	assert.Empty(t, rebuilt.Backup.Targets,
		"the sandbox kept production's backup target, so it can write into it")
	assert.Empty(t, rebuilt.Notify.Targets,
		"the sandbox kept production's alerting, so it can page the customer's on-call")

	// Reported, not silent: an operator who believes their sandbox is still
	// backing up -- or still paging them -- finds out during a recovery that
	// it was not.
	assert.Contains(t, result.Summary, "backup target")
	assert.Contains(t, result.Summary, "notify target")

	// And the outcome, for the thing that rides on this list without being
	// named by it.
	//
	// RFC 0026 §3.5 names the same hazard for fleet rows, and decision 3
	// reuses this exact field -- so fleet publishing is covered by the
	// backup-target drop rather than by anything written for it. The list
	// exists now, which is what decision 7 asked for, and this stays an
	// assertion about the *outcome*: it fails the day fleet targets move
	// off that field, whatever the list says at the time.
	if store, ok := sandbox.Deps.Targets.(ports.ObjectStore); ok {
		sandbox.Deps.Objects = store
	}
	_, err = ops.FleetPublish(ctx, sandbox.Deps, ops.FleetPublishOptions{})
	require.Error(t, err,
		"a sandbox rebuilt from a production export published a fleet row, "+
			"which goes into the customer's bucket under a matching installation id")
	assert.Contains(t, domain.AsError(err).Message, "no backup targets")
}

// TestASandboxCannotBeImportedAsProduction.
//
// The deferred-risk transition wearing a different hat. Refused by name, so an
// operator reads why rather than discovering at rollback time that `previous`
// was pruned and no pre-update backup was ever taken.
func TestASandboxCannotBeImportedAsProduction(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)

	inst, err := origin.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	require.NoError(t, os.Remove(origin.Paths.InstallationState()))
	inst.Mode = domain.ModeDev
	require.NoError(t, origin.Deps.State.SaveInstallation(ctx, inst))

	exportPath := filepath.Join(t.TempDir(), "demo.export.yaml")
	_, err = ops.Export(ctx, origin.Deps, ops.ExportOptions{Path: exportPath})
	require.NoError(t, err)
	export, err := ops.LoadExport(exportPath)
	require.NoError(t, err)
	require.True(t, export.Installation.IsDev())

	rebuilt := newMachine(t, t.TempDir())
	_, err = ops.Import(ctx, rebuilt.Deps, ops.ImportOptions{
		SourcePath:   exportPath,
		Export:       export,
		IdentityFile: recoveryPath,
		ModeSet:      true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be imported as production")

	// And with no flag it rebuilds as what it was, which is the safe
	// reading: a lost sandbox comes back a sandbox.
	_, err = ops.Import(ctx, rebuilt.Deps, ops.ImportOptions{
		SourcePath:   exportPath,
		Export:       export,
		IdentityFile: recoveryPath,
	})
	require.NoError(t, err)
	got, err := rebuilt.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.True(t, got.IsDev())
}

// armedSandbox is a machine configured to install what its channel offers:
// signatures required, auto-apply on.
//
// Signature *policy*, not a real signature: what is being tested is the gate's
// arithmetic, and the verification chain has its own suite. A machine that
// requires signatures with a key configured is what the gate asks about.
func armAutoApply(t *testing.T, h *harness) {
	t.Helper()
	ctx := context.Background()

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Policy.RequireSignature = true
	inst.Policy.SigningKeys = []string{"RWQfakekey"}
	inst.Update.AutoApply = true
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
}

// TestAnUnattendedTickStagesWhatItMayNotInstall.
//
// The 1.3.0 fixture declares that its migrations leave the database at 14, and
// the installed 1.2.0 reads at most 12 -- so rolling back after this update
// would need a restore, which is precisely the failure the gate refuses to risk
// unattended.
//
// It is still fetched, verified and staged. That is the whole design: the
// network, the credentials and the verification move off the human's critical
// path, and what waits for them is only the decision that costs downtime.
func TestAnUnattendedTickStagesWhatItMayNotInstall(t *testing.T) {
	h, _, _, _ := followingHarness(t)
	h.setHookEnv()
	applyBaseline(t, h)
	armAutoApply(t, h)
	ctx := context.Background()

	res, err := ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err, "a release left staged is a successful tick, not a failure")

	assert.False(t, res.Applied)
	assert.False(t, res.Assessment.OK)
	assert.Contains(t, res.Assessment.Why(), "schema",
		"the refusal does not say which declaration blocked it")

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", current.Version.String(),
		"a release that failed the gate was installed anyway")

	candidate, err := h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	assert.True(t, candidate.IsStaged(),
		"the release was not staged, so the operator's decision costs a download")
}

// TestAnUnattendedTickInstallsWhatDeclaresItselfRecoverable.
//
// The same machine, the same channel, one field different: this release's
// migrations leave the database at 12, which the installed release can still
// read -- so a failure compensates back rather than needing a restore.
//
// Both this and the test above are needed. Either alone passes on a gate that
// always says the same thing.
func TestAnUnattendedTickInstallsWhatDeclaresItselfRecoverable(t *testing.T) {
	h, _, _, _ := followingHarnessWith(t, func(t *testing.T, dir string) {
		t.Helper()
		path := filepath.Join(dir, "manifest.yaml")
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		patched := strings.Replace(string(raw),
			"database_schema_produces: 14", "database_schema_produces: 12", 1)
		require.NotEqual(t, string(raw), patched, "the fixture no longer declares what this patches")
		require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))
	})
	h.setHookEnv()
	applyBaseline(t, h)
	armAutoApply(t, h)
	ctx := context.Background()

	res, err := ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)

	assert.True(t, res.Applied, "refused: %s", res.Assessment.Why())

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String())

	candidate, err := h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	assert.True(t, candidate.IsZero(), "the installed candidate is still recorded as waiting")
}

// TestAutoApplyOffLeavesEverythingStaged.
//
// The default, and the one an operator should be able to rely on: a machine
// following a channel fetches and reports, and installs nothing until somebody
// turns auto-apply on.
func TestAutoApplyOffLeavesEverythingStaged(t *testing.T) {
	h, _, _, _ := followingHarnessWith(t, func(t *testing.T, dir string) {
		t.Helper()
		path := filepath.Join(dir, "manifest.yaml")
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(raw),
			"database_schema_produces: 14", "database_schema_produces: 12", 1)), 0o644))
	})
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	res, err := ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)

	assert.False(t, res.Applied)
	assert.Contains(t, res.Assessment.Why(), "auto_apply")

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", current.Version.String(),
		"a machine that never opted in installed a release by itself")
}

// TestATickThatCannotTakeTheLockDoesNotFailTheUnit.
//
// The next tick is soon, and an update waiting behind an operator's interactive
// backup is an update that starts at an unpredictable moment. What this asserts
// is the error's identity -- ErrLocked -- because that is what the command maps
// to exit 0; asserting the exit code here would test the CLI's mapping twice and
// this behaviour not at all.
func TestATickThatCannotTakeTheLockDoesNotFailTheUnit(t *testing.T) {
	// A bundle the gate admits, or the tick would stop before it ever
	// reached the lock and this test would pass having exercised nothing.
	h, _, _, _ := followingHarnessWith(t, func(t *testing.T, dir string) {
		t.Helper()
		path := filepath.Join(dir, "manifest.yaml")
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(raw),
			"database_schema_produces: 14", "database_schema_produces: 12", 1)), 0o644))
	})
	h.setHookEnv()
	applyBaseline(t, h)
	armAutoApply(t, h)
	ctx := context.Background()

	// Someone else is mid-operation.
	release, err := h.Locker.Acquire(ctx, "deployment", ports.LockOptions{})
	require.NoError(t, err)
	defer func() { _ = release() }()

	// The contention appears at the poll, which is where the tick first
	// writes: staging fetches a bundle into the release store and records a
	// candidate, both of which the lock protects. It used to appear at the
	// install, because staging ran outside the lock entirely.
	_, err = ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrLocked)

	// What the command does with that error is exit 0 and say so, and the
	// saying is a field rather than an absence: every quiet tick has an
	// empty result, so a machine reading `--json` could not otherwise tell
	// "another operation was running" from "the tag has not moved".
	skipped := ops.UnattendedResult{Skipped: "another operation holds the lock"}
	assert.Contains(t, skipped.Summary(), "another operation holds the lock")
}

// TestPrereleaseAdmissibilityDiffersByMode.
//
// RFC 0014 gives every development build a prerelease version, so this is not a
// corner case: it is what a vendor's own CI publishes on every commit. A
// production machine offered them would be told about its vendor's work in
// progress, and a channel that installed one would be a customer running an
// untagged build.
//
// Both directions asserted. A refusal that fired in either mode would be a
// sandbox that cannot do the one thing it exists for.
func TestPrereleaseAdmissibilityDiffersByMode(t *testing.T) {
	prerelease := func(t *testing.T, dir string) {
		t.Helper()
		for _, name := range []string{"manifest.yaml", "VERSION"} {
			path := filepath.Join(dir, name)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			patched := strings.ReplaceAll(string(raw), "1.3.0", "1.3.0-dev.7")
			require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))
		}
	}

	t.Run("a production machine refuses it", func(t *testing.T) {
		h, _, _, _ := followingHarnessWith(t, prerelease)

		_, err := ops.FollowChannel(context.Background(), h.Deps,
			ops.FollowChannelOptions{Explicit: true})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "prerelease")
	})

	t.Run("a sandbox stages it", func(t *testing.T) {
		h, _, _, _ := followingHarnessWith(t, prerelease)
		ctx := context.Background()

		// Created as a sandbox, which is the only moment a mode may be
		// chosen -- so the state is removed and written afresh rather
		// than edited, which the store refuses.
		inst, err := h.Deps.State.LoadInstallation(ctx)
		require.NoError(t, err)
		require.NoError(t, os.Remove(h.Paths.InstallationState()))
		inst.Mode = domain.ModeDev
		require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

		res, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
		require.NoError(t, err)
		assert.True(t, res.Candidate.IsStaged())
		assert.Equal(t, "1.3.0-dev.7", res.Candidate.Version.String())
	})
}

// TestTheUpdateTimerNeedsBothTheChannelAndPermission.
//
// A timer exists to poll, and polling is gated by `update.check`. A machine with
// a channel and checking off would install a unit that fails every night on a
// refusal -- which is exactly how an operator learns to ignore a unit, and then
// misses the night it means something.
func TestTheUpdateTimerNeedsBothTheChannelAndPermission(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Deps.Supervisor = h.Supervisor
	h.Supervisor.Present = true
	ctx := context.Background()

	// The machine manages units, which is what makes reconciliation its
	// business at all.
	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units))

	timer := "demo-update.timer"

	// A channel with no permission to use it.
	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{"update.channel": "oci://registry.example/demo/bundle:stable"},
	})
	require.NoError(t, err)
	assert.NotContains(t, h.Supervisor.Installed, timer,
		"a timer was installed on a machine that may not contact a registry")

	// Both, and it appears.
	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{"update.check": "true"},
	})
	require.NoError(t, err)
	assert.Contains(t, h.Supervisor.Installed, timer)

	// And clearing the channel takes it away again, which is the half a
	// unit set installed once at `init` could never do.
	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Unset: []string{"update.channel"},
	})
	require.NoError(t, err)
	assert.NotContains(t, h.Supervisor.Installed, timer,
		"the timer outlived the channel it was polling")
}

// TestARetryFinishesAUnitReconciliationThatFailed.
//
// The state is written before the units, deliberately: the units are derived
// from it, and a crash between the two leaves a timer to reconcile rather than a
// state nothing polls. What that ordering left open is the repair. Unit
// installation can fail on its own -- a busy systemd, a read-only /etc -- and
// the operator's obvious response is to run the command again, which matched
// every value, reported "no change" and returned before reaching the step that
// had not finished. The machine then advertises a channel it never polls, and
// nothing an operator can type fixes it.
//
// So a run that changes nothing still reconciles. That is the whole assertion:
// the second command is byte-for-byte the first.
func TestARetryFinishesAUnitReconciliationThatFailed(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Deps.Supervisor = h.Supervisor
	h.Supervisor.Present = true
	ctx := context.Background()

	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units))

	set := ops.SetSettingsOptions{Set: map[string]string{
		"update.check":   "true",
		"update.channel": "oci://registry.example/demo/bundle:stable",
	}}

	h.Supervisor.Fail = map[string]error{"InstallUnits": errors.New("systemd is busy")}
	_, err = ops.SetSettings(ctx, h.Deps, set)
	require.Error(t, err, "the failure to install the unit was not reported")

	timer := "demo-update.timer"
	require.NotContains(t, h.Supervisor.Installed, timer)

	// The state took the change even though the units did not, which is
	// what makes the retry necessary rather than merely tidy.
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	require.True(t, inst.Update.FollowsChannel())

	h.Supervisor.Fail = nil
	_, err = ops.SetSettings(ctx, h.Deps, set)
	require.NoError(t, err)
	assert.Contains(t, h.Supervisor.Installed, timer,
		"repeating the command that failed did not install the unit it failed to install")
}

// TestAnUpdateCheckOffersPrereleasesOnlyToASandbox.
//
// Found by the sabotage sweep, not by reading: deleting the filter left every
// test green, because the fake registry's tag list had no prerelease in it. The
// check had a rule nothing exercised.
//
// It matters because RFC 0014 gives every development build a prerelease
// version, so a vendor's repository accumulates them on every commit. A
// production machine told "1.4.0-dev.1 is available" learns to ignore the check.
func TestAnUpdateCheckOffersPrereleasesOnlyToASandbox(t *testing.T) {
	h, registry, srv, _ := followingHarness(t)
	ctx := context.Background()

	// A repository as a vendor's CI leaves it: releases beside the builds
	// that led to them.
	registry.tags = []string{"1.2.0", "1.3.0", "1.4.0-dev.1", "latest"}
	ref := ociRef(srv, "demo/bundle", "1.2.0").String()

	production, err := ops.CheckForUpdate(ctx, h.Deps,
		ops.UpdateCheckOptions{Ref: ref, Explicit: true})
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", production.Latest.String(),
		"a production machine was offered a development build")

	// The same repository, read by a sandbox, which exists to run them.
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	require.NoError(t, os.Remove(h.Paths.InstallationState()))
	inst.Mode = domain.ModeDev
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	sandbox, err := ops.CheckForUpdate(ctx, h.Deps,
		ops.UpdateCheckOptions{Ref: ref, Explicit: true})
	require.NoError(t, err)
	assert.Equal(t, "1.4.0-dev.1", sandbox.Latest.String(),
		"a sandbox was not offered the build it exists to test")
}

// TestTheTickSaysWhatItDid.
//
// The summary is what a systemd journal carries, and a journal read at 09:00 is
// the only account of a night nobody watched. The three outcomes have to be
// distinguishable in one line: nothing happened, something is waiting and why,
// or something was installed.
func TestTheTickSaysWhatItDid(t *testing.T) {
	unchanged := ops.UnattendedResult{
		Follow: ops.FollowChannelResult{Ref: "oci://registry.example/demo/bundle:stable"},
	}
	assert.Contains(t, unchanged.Summary(), "unchanged")

	waiting := ops.UnattendedResult{
		Follow: ops.FollowChannelResult{
			Moved: true,
			Candidate: domain.UpdateCandidate{
				Name: "demo", Version: domain.MustParseVersion("1.4.0"),
				Root: "/opt/demo/releases/1.4.0",
			},
		},
		Assessment: domain.UnattendedAssessment{
			Reasons: []string{"update.auto_apply is off, so installing is your decision"},
		},
	}
	// The version *and* the reason: "1.4.0 is staged" without why is a line
	// that sends somebody to the source to find out.
	assert.Contains(t, waiting.Summary(), "1.4.0")
	assert.Contains(t, waiting.Summary(), "auto_apply")

	applied := ops.UnattendedResult{
		Applied: true,
		Update:  &ops.Result{Summary: "updated demo from 1.3.0 to 1.4.0"},
	}
	assert.Contains(t, applied.Summary(), "updated demo")
}

// hookedLocker runs one function at the moment the lock is taken.
//
// It stands in for the other operation: something that finished writing
// installation state after this command read it and before this command locked.
// A test that arranged the race with goroutines would be asserting the same
// thing intermittently.
type hookedLocker struct {
	inner  ports.Locker
	before func()
}

func (l *hookedLocker) Acquire(
	ctx context.Context, name string, opts ports.LockOptions,
) (func() error, error) {
	if l.before != nil {
		f := l.before
		l.before = nil
		f()
	}
	return l.inner.Acquire(ctx, name, opts)
}

func (l *hookedLocker) Owner(ctx context.Context, name string) (ports.LockOwner, bool, error) {
	return l.inner.Owner(ctx, name)
}

// TestWhatChangedIsDecidedUnderTheLock.
//
// The pre-lock pass exists to refuse an unknown name and to answer a plan. It
// cannot decide what changed, because it read a copy: another operation writing
// between that read and the lock makes its answer stale, and a run that skipped
// the write on the strength of it would report "every value already matches"
// while the file said otherwise -- the silent lost update the lock exists to
// prevent, arriving through the code that takes it.
//
// Reachable because a settings run that changes nothing now still enters the
// lock to reconcile units, which is the fix for a failed reconciliation being
// unretryable. That fix is what made the stale list load-bearing.
func TestWhatChangedIsDecidedUnderTheLock(t *testing.T) {
	h := newHarness(t)
	h.install()
	ctx := context.Background()

	_, err := ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{"update.check": "true"},
	})
	require.NoError(t, err)

	// Somebody else turns it off between this command's read and its lock.
	h.Deps.Locker = &hookedLocker{inner: h.Deps.Locker, before: func() {
		inst, err := h.Deps.State.LoadInstallation(ctx)
		require.NoError(t, err)
		inst.Update.Check = false
		require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	}}

	// The same value the machine already had when this command started, so
	// the pre-lock pass sees nothing to do.
	res, err := ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{"update.check": "true"},
	})
	require.NoError(t, err)

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.True(t, inst.Update.Check,
		"the value the operator asked for was applied in memory and never written")
	assert.Contains(t, res.Summary, "update.check",
		"the summary reported no change for a change it made")
}
