package suite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// "My backups are not this manager's job" (RFC 0030 rows 4 and 5).
//
// The declaration is not a second spelling of `systemctl disable`, which row 1
// made durable in the same change. This one decides whether the unit exists at
// all -- so there is nothing to disable, nothing to re-enable, and nothing for
// `doctor` to want. That distinction is what these tests are about; the two
// mechanisms have to stay different or an operator has two switches and no way
// to tell which is in force.

// The timer is not generated, and an existing one is removed.
//
// Removed rather than generated-and-left-disabled: a unit that exists and never
// fires still shows up in `systemctl list-timers`, still has to be explained,
// and is one reconciliation away from being switched back on by something that
// reads its spec rather than the installation.
func TestDeclaringNoScheduledBackupsRemovesTheTimer(t *testing.T) {
	h := newHarness(t)
	h.install()
	ctx := context.Background()

	unitDir := t.TempDir()
	real := systemd.New(exec.NewScripted(), systemd.WithUnitDir(unitDir))
	h.Deps.Supervisor = availableSupervisor{Supervisor: real}

	units, err := h.Deps.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Deps.Supervisor.InstallUnits(ctx, units, ports.EnableAll))
	require.FileExists(t, filepath.Join(unitDir, "demo-backup.timer"),
		"the timer was never installed, so its removal proves nothing")

	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{"backup.scheduled": "false"},
	})
	require.NoError(t, err)

	assert.NoFileExists(t, filepath.Join(unitDir, "demo-backup.timer"),
		"the declaration left the timer on the machine")
	assert.NoFileExists(t, filepath.Join(unitDir, "demo-backup.service"),
		"the timer went and the oneshot it starts stayed")

	// The product's own service is untouched. This is a statement about
	// backups, not about whether the machine runs.
	assert.FileExists(t, filepath.Join(unitDir, "demo.service"))

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.True(t, inst.Policy.SkipScheduledBackups)

	// And back, because a declaration that cannot be withdrawn is a trap.
	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{"backup.scheduled": "true"},
	})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(unitDir, "demo-backup.timer"),
		"turning scheduled backups back on did not restore the timer")
}

// Unsetting returns to scheduled backups, which is the safe direction and the
// one an absent field already takes.
func TestUnsettingTheDeclarationRestoresScheduledBackups(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	ctx := context.Background()

	inst.Policy.SkipScheduledBackups = true
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	_, err := ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Unset: []string{"backup.scheduled"},
	})
	require.NoError(t, err)

	after, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.False(t, after.Policy.SkipScheduledBackups,
		"clearing the setting left backups switched off")
}

// A value that is not a boolean is refused, and nothing is written.
//
// `backup.scheduled=off` and `=no` are what somebody types who has used other
// tools. Accepting either as false would be bad; accepting it as *true* while
// they believed they had switched backups off would be worse, and a silent
// no-op worst of all -- so it is a refusal that names what the field takes.
func TestABackupScheduledValueThatIsNotABooleanIsRefused(t *testing.T) {
	for _, raw := range []string{"off", "no", "", "1.5"} {
		t.Run(raw, func(t *testing.T) {
			h := newHarness(t)
			h.install()
			ctx := context.Background()

			_, err := ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
				Set: map[string]string{"backup.scheduled": raw},
			})
			require.Error(t, err, "%q was accepted as a boolean", raw)
			assert.Contains(t, domain.AsError(err).Message, "not a boolean")

			after, err := h.Deps.State.LoadInstallation(ctx)
			require.NoError(t, err)
			assert.False(t, after.Policy.SkipScheduledBackups,
				"a refused value still reached the installation")
		})
	}
}

// `doctor` stops reporting an age it was told is not its business.
//
// A machine backed up at the storage layer takes no backups through morzer, so
// the newest one is for ever older than the threshold. The check fires on every
// run, and a check that fires on every run is one nobody reads on the run that
// meant something.
func TestBackupFreshnessHonoursTheDeclaration(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	ctx := context.Background()

	// The state that warns: no backups at all.
	before, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	require.NotEqual(t, string(events.CheckOK), findResult(t, before, "backup.freshness").Status,
		"this machine was not warning, so silencing it proves nothing")

	inst.Policy.SkipScheduledBackups = true
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	after, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	got := findResult(t, after, "backup.freshness")
	assert.Equal(t, string(events.CheckOK), got.Status,
		"the declaration did not stop the permanent warning: %s", got.Message)
	assert.Contains(t, got.Message, "backup.scheduled",
		"the check went quiet without saying why, which reads as a machine with fresh backups")
}

// A backup that exists is still expected to leave the machine.
//
// The declaration says no backups are *scheduled*; it does not say none are
// taken. `morzer backup` still works, and a backup sitting only on the machine
// it describes is still every copy of the data in one place -- which is worth
// saying however the backup was started.
func TestTheDeclarationDoesNotSilenceTheOffMachineCheck(t *testing.T) {
	h := newHarness(t)
	inst, _ := h.withTargets(t)
	ctx := context.Background()

	inst.Policy.SkipScheduledBackups = true
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	// A backup taken by hand, which the declaration does not forbid: it
	// says none are *scheduled*, not that none are taken.
	_, err := ops.Backup(ctx, h.Deps, ops.BackupOptions{})
	require.NoError(t, err)

	report, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	got := findResult(t, report, "backup.target-freshness")
	assert.NotEqual(t, string(events.CheckOK), got.Status,
		"a backup that never left the machine was accepted because scheduled backups are off: %s",
		got.Message)
}

// `status` stops calling the age a problem, for the same reason and in the
// louder place: it is the command run far more often than `doctor`.
func TestStatusStopsCallingTheBackupAgeAProblem(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	ctx := context.Background()

	// A backup old enough to be stale against the default threshold. The
	// harness clock is fixed, so a backup taken through the engine is always
	// zero seconds old and could never reach the branch under test.
	stale := ports.BackupRef{
		ID:   "backup-old",
		At:   domain.NewTime(h.Deps.Now().Add(-30 * 24 * time.Hour)),
		Size: 1024,
	}
	h.Deps.Backup = backupsOfSize{Backup: h.Backup, refs: []ports.BackupRef{stale}}

	before, err := ops.GetStatus(ctx, h.Deps)
	require.NoError(t, err)
	require.Contains(t, before.Problems, "the most recent backup is 720h0m0s old",
		"this machine was not reporting a stale backup, so silencing it proves nothing")

	inst.Policy.SkipScheduledBackups = true
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	after, err := ops.GetStatus(ctx, h.Deps)
	require.NoError(t, err)
	assert.NotContains(t, after.Problems, "the most recent backup is 720h0m0s old",
		"the declaration did not reach `status`, which is the command run most often")

	// The age is still reported. A fact is not a problem, and hiding it
	// would leave an operator unable to see how old the backup is at all.
	require.NotNil(t, after.LastBackup)
	assert.NotEmpty(t, after.LastBackup.Age)
}

// The unit check does not want a timer nobody declared.
//
// Without this the declaration would trade one permanent warning for another:
// `demo-backup.timer: not installed`, on a machine that asked for it not to be.
func TestTheUnitCheckDoesNotWantAnUndeclaredTimer(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	h.Deps.Supervisor = h.Supervisor
	h.Supervisor.Present = true
	ctx := context.Background()

	inst.Policy.SkipScheduledBackups = true
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	// The units a declared machine has, listed here rather than asked for
	// from the supervisor.
	//
	// Asking for them made this test vacuous: whatever the supervisor left
	// out of the set, it also left off the machine, so `doctor` found every
	// unit it expected and passed either way. What has to be installed is
	// the *machine's* unit set, so that a `doctor` which still wants a backup
	// timer reports one that is missing.
	require.NoError(t, h.Supervisor.InstallUnits(ctx, []ports.Unit{
		{Name: "demo.service", Contents: []byte("x"), Enable: true},
	}, ports.EnableAll))

	report, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	got := findResult(t, report, "system.units")
	assert.Equal(t, string(events.CheckOK), got.Status,
		"a machine that declared no backup timer was told it is missing one: %s", got.Message)
}

// The remedy names both ways out of `<unit>: not enabled`.
//
// Since row 1 a disabled unit stays disabled, so the operator who meant it
// needs a way to stop being told and the one who did not needs the repair. A
// remedy with only the second is a warning that fires for ever at somebody who
// was right.
func TestTheUnitRemedyNamesBothWaysOut(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Deps.Supervisor = h.Supervisor
	h.Supervisor.Present = true
	ctx := context.Background()

	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableAll))
	require.NoError(t, h.Supervisor.Disable(ctx, "demo-backup.timer"))

	report, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	got := findResult(t, report, "system.units")
	require.Equal(t, string(events.CheckWarn), got.Status, got.Message)
	assert.Contains(t, got.Remedy, "init --repair --install-units")
	assert.Contains(t, got.Remedy, "backup.scheduled=false")
}

// A backup unit that was deleted rather than disabled gets the same remedy.
//
// Both states warn on every run and the declaration answers both, so a remedy
// that reached only the disabled one would be permanent for the other. `not
// installed` is the state an operator reaches by removing the unit file by
// hand, which is the other thing somebody tries when a timer will not stay off.
func TestTheUnitRemedyCoversADeletedBackupUnit(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Deps.Supervisor = h.Supervisor
	h.Supervisor.Present = true
	ctx := context.Background()

	// Installed without the backup pair, which is what deleting the files
	// leaves behind: the machine manages units, and those two are gone.
	require.NoError(t, h.Supervisor.InstallUnits(ctx, []ports.Unit{
		{Name: "demo.service", Contents: []byte("x"), Enable: true},
	}, ports.EnableAll))

	report, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	got := findResult(t, report, "system.units")
	require.Equal(t, string(events.CheckWarn), got.Status, got.Message)
	require.Contains(t, got.Message, "demo-backup.timer: not installed")
	assert.Contains(t, got.Remedy, "backup.scheduled=false",
		"a deleted backup timer warns for ever with no way to say it was meant")
}

// And offers it only for the unit it would work on.
//
// `backup.scheduled=false` removes the backup pair and nothing else, so
// printing it beside a disabled *update* timer is a remedy that cannot clear
// the warning it is printed with -- the same defect this check was fixed for
// once already, when it walked the removal superset and told operators to
// repair units their machine was never meant to have.
func TestTheUnitRemedyDoesNotOfferBackupAdviceForAnotherTimer(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	h.Deps.Supervisor = h.Supervisor
	h.Supervisor.Present = true
	ctx := context.Background()

	inst.Update.Channel = "oci://registry.example/demo/bundle:stable"
	inst.Update.Check = true
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo", UpdateTimer: true})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableAll))
	require.NoError(t, h.Supervisor.Disable(ctx, "demo-update.timer"))

	report, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	got := findResult(t, report, "system.units")
	require.Equal(t, string(events.CheckWarn), got.Status, got.Message)
	require.Contains(t, got.Message, "demo-update.timer: not enabled")
	assert.NotContains(t, got.Remedy, "backup.scheduled",
		"an operator whose update timer is off was told to turn off scheduled backups")
	assert.Contains(t, got.Remedy, "systemctl status",
		"the general remedy was dropped along with the backup-specific one")
}

// The declaration survives the command that rebuilds an installation.
//
// `init --repair` carries Policy whole, so this holds by construction -- and it
// is asserted anyway, because "reproduced by a repair" is half of why the
// declaration exists rather than living only in the operator's init system.
func TestARepairKeepsTheDeclaration(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	_, recoveryPub := generateRecoveryKey(t)
	h := initOriginMachine(t, ctx, recoveryPub)

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Policy.SkipScheduledBackups = true
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", Repair: true, RecoveryRecipient: recoveryPub,
	})
	require.NoError(t, err)

	after, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.True(t, after.Policy.SkipScheduledBackups,
		"a repair put the backup timer back on a machine that declared it wanted none")
}

// A state file written before the field existed is a machine with a timer.
//
// The direction a missing bool falls is the whole reason the stored field is
// named for the unsafe outcome, and a hand-edited file is exactly where it goes
// missing.
func TestAnInstallationWithNoDeclarationKeepsItsTimer(t *testing.T) {
	h := newHarness(t)
	h.install()
	ctx := context.Background()

	raw, err := os.ReadFile(h.Deps.Paths.InstallationState())
	require.NoError(t, err)
	require.NotContains(t, string(raw), "skip_scheduled_backups",
		"the field was written out, so its absence is not what this tests")

	unitDir := t.TempDir()
	real := systemd.New(exec.NewScripted(), systemd.WithUnitDir(unitDir))
	h.Deps.Supervisor = availableSupervisor{Supervisor: real}
	units, err := h.Deps.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Deps.Supervisor.InstallUnits(ctx, units, ports.EnableAll))

	// Through a real reconciliation, which is where the absent field is read
	// and turned into a decision about a unit.
	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{"update.check": "true"},
	})
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(unitDir, "demo-backup.timer"),
		"an installation that never mentioned the field lost its backup timer")
}
