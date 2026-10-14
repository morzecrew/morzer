package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// The update timer's schedule is a setting (RFC 0016 decision 25).
//
// The adapter has always rendered whatever UpdateSchedule it was handed, and
// nothing handed it one: every machine ticked at 03:30, and the documented way
// to change that -- a drop-in -- added a trigger instead of replacing one.
//
// Asserted against the real unit text, as the backup window is, because the
// value only exists once a template has rendered it.

// followingMachine installs the demo product and its units with a real systemd
// adapter, as `init` would, and returns the directory the units are written
// to. A reconciliation leaves a machine with no units alone, so without the
// initial set nothing below would render at all.
//
// mode is chosen at creation, the only moment it may be, so the state is
// removed and written afresh rather than edited -- which the store refuses.
func followingMachine(t *testing.T, mode domain.Mode) (*harness, string) {
	t.Helper()
	ctx := context.Background()
	h := newHarness(t)
	inst := h.install()
	if mode != "" {
		require.NoError(t, os.Remove(h.Paths.InstallationState()))
		inst.Mode = mode
		require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	}

	unitDir := t.TempDir()
	h.Deps.Supervisor = availableSupervisor{
		Supervisor: systemd.New(fakes.NewScripted(), systemd.WithUnitDir(unitDir)),
	}
	units, err := h.Deps.Supervisor.Units(ports.UnitParams{Product: inst.Product})
	require.NoError(t, err)
	require.NoError(t, h.Deps.Supervisor.InstallUnits(ctx, units, ports.EnableAll))
	return h, unitDir
}

func updateTimer(t *testing.T, unitDir string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(unitDir, "demo-update.timer"))
	require.NoError(t, err)
	return string(body)
}

func TestTheUpdateWindowCanBeSetAndUnset(t *testing.T) {
	h, unitDir := followingMachine(t, "")
	ctx := context.Background()

	_, err := ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{
			"update.channel":  "oci://registry.example/demo/bundle:stable",
			"update.check":    "true",
			"update.schedule": "  Sun *-*-* 04:00:00 ",
		},
	})
	require.NoError(t, err)

	timer := updateTimer(t, unitDir)
	assert.Contains(t, timer, "OnCalendar=Sun *-*-* 04:00:00\n",
		"the operator's window did not reach the timer")
	assert.NotContains(t, timer, systemd.DefaultUpdateSchedule,
		"the default tick is still there beside the window, which is the drop-in defect again")
	// Production keeps the spread whatever the schedule says: it protects
	// the vendor's registry, and a customer's short schedule is not the
	// vendor's decision. Asserted as whole lines: the template's trim
	// markers sit on either side of this one, and a directive joined onto
	// `Persistent=true` would still contain the substring.
	assert.Contains(t, timer, "Persistent=true\nRandomizedDelaySec=1800\n\n[Install]")

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Sun *-*-* 04:00:00", inst.Update.Schedule,
		"the schedule was not stored, or was stored with its padding")

	// Unset is the daily default, never an empty OnCalendar.
	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Unset: []string{"update.schedule"},
	})
	require.NoError(t, err)
	assert.Contains(t, updateTimer(t, unitDir), "OnCalendar="+systemd.DefaultUpdateSchedule+"\n")
}

// A sandbox that follows main every five minutes ticks every five minutes.
//
// RFC 0016 decision 26. With RandomizedDelaySec=1800 each tick is pushed back by
// up to half an hour, so "every five minutes" was every five to thirty-five --
// and the spread exists for a vendor's customer base, which a sandbox polling
// its own vendor's registry is not.
func TestASandboxUpdateTimerHasNoJitter(t *testing.T) {
	h, unitDir := followingMachine(t, domain.ModeDev)

	_, err := ops.SetSettings(context.Background(), h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{
			"update.channel":  "oci://registry.example/demo/bundle:main",
			"update.check":    "true",
			"update.schedule": "*:0/5",
		},
	})
	require.NoError(t, err)

	timer := updateTimer(t, unitDir)
	require.Contains(t, timer, "OnCalendar=*:0/5\n",
		"the schedule did not arrive, so the absence below proves nothing")
	assert.NotContains(t, timer, "RandomizedDelaySec",
		"a sandbox's update timer still spreads its ticks across half an hour")
	// The line went, and the lines around it stayed.
	assert.Contains(t, timer, "Persistent=true\n\n[Install]")
}

// The update schedule is rendered into `OnCalendar=` exactly as the backup
// schedule is, so it is refused on load exactly as that one is. A guard on
// `config set` alone would cover the door this threat does not come through.
func TestAHandEditedUpdateScheduleIsRefusedOnLoad(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	ctx := context.Background()

	// The cheaper door first, while the state is still loadable.
	_, err := ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{"update.schedule": "daily\nUnit=attacker.service"},
	})
	require.Error(t, err, "`config set` stored a schedule with a second line in it")

	inst.Update.Schedule = "daily"
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	path := h.Deps.Paths.InstallationState()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"schedule": "daily"`,
		"the state file does not hold the schedule where this test patches it")

	patched := strings.Replace(string(raw),
		`"schedule": "daily"`, `"schedule": "\ndaily\nUnit=attacker.service"`, 1)
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o600))

	_, err = h.Deps.State.LoadInstallation(ctx)
	require.Error(t, err,
		"a state file carrying a second unit directive was loaded and would be rendered")
	assert.Contains(t, domain.AsError(err).Message, "update.schedule")

}

// A schedule systemd cannot parse is refused at the door, for both timers.
//
// It used to be stored, rendered into OnCalendar=, and leave a timer that does
// not load -- no backups, or no unattended updates, from a `config set` that
// exited 0. Played through the real adapter with systemd-analyze scripted to
// refuse, so what is under test is the wiring from the setting to the parser
// and what a refusal leaves behind: nothing.
func TestAScheduleSystemdCannotParseIsRefused(t *testing.T) {
	for _, setting := range []string{"update.schedule", "backup.schedule"} {
		t.Run(setting, func(t *testing.T) {
			h, unitDir := followingMachine(t, "")
			ctx := context.Background()
			_, err := ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
				Set: map[string]string{
					"update.channel": "oci://registry.example/demo/bundle:stable",
					"update.check":   "true",
				},
			})
			require.NoError(t, err)

			runner := fakes.NewScripted().OnExit("calendar -- every 5m", 1,
				"Failed to parse calendar specification 'every 5m': Invalid argument")
			h.Deps.Supervisor = availableSupervisor{
				Supervisor: systemd.New(runner, systemd.WithUnitDir(unitDir)),
			}
			before := updateTimer(t, unitDir)

			_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
				Set: map[string]string{setting: "every 5m"},
			})
			require.ErrorIs(t, err, domain.ErrValidation,
				"a schedule systemd refused was stored")
			assert.Contains(t, domain.AsError(err).Message, "every 5m")

			inst, err := h.Deps.State.LoadInstallation(ctx)
			require.NoError(t, err)
			assert.Empty(t, inst.Update.Schedule)
			assert.Empty(t, inst.Policy.BackupSchedule)
			assert.Equal(t, before, updateTimer(t, unitDir), "a refused setting rewrote a unit")

			// And a schedule it accepts still goes through the same door.
			_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
				Set: map[string]string{setting: "Sun *-*-* 04:00:00"},
			})
			require.NoError(t, err)
			assert.True(t, runner.Ran("calendar -- Sun *-*-* 04:00:00"),
				"the accepted schedule was never put to systemd:\n%s", runner.CommandLines())
		})
	}
}

// `init --backup-schedule` asks before anything exists, as `--recovery-recipient`
// does: found at unit installation, a bad schedule would unwind a half-built
// installation over one flag.
func TestInitRefusesABackupScheduleBeforeCreatingAnything(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	fake := fakes.NewSupervisor()
	fake.Fail["ValidateSchedule"] = domain.ValidationError(nil, "systemd cannot parse the schedule")
	h.Deps.Supervisor = fake

	_, err := ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, BackupSchedule: "every 5m",
	})
	require.ErrorIs(t, err, domain.ErrValidation)
	assert.Contains(t, domain.AsError(err).Message, "systemd cannot parse the schedule",
		"init failed, but not on the schedule")

	exists, err := h.Deps.State.InstallationExists(ctx)
	require.NoError(t, err)
	assert.False(t, exists, "init refused the schedule after creating the installation")

	// Shape before parser: a second line is refused as one, and never reaches
	// systemd-analyze, which would call it a bad calendar instead.
	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, BackupSchedule: "daily\nUnit=attacker.service",
	})
	require.ErrorIs(t, err, domain.ErrValidation)
	assert.Contains(t, domain.AsError(err).Message, "one line")
}
