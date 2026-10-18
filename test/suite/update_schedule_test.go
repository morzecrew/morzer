package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// `status` says what the update timer does, which was otherwise written down
// only in a unit file: whether anything polls the channel, when, and whether it
// installs what it finds. A dev machine meant to follow `main` within minutes
// is wrong about one of those three more often than it is broken.
func TestStatusSaysHowTheChannelIsFollowed(t *testing.T) {
	h, _ := followingMachine(t, "")
	ctx := context.Background()
	const channel = "oci://registry.example/demo/bundle:main"

	following := func() *ops.Following {
		t.Helper()
		st, err := ops.GetStatus(ctx, h.Deps)
		require.NoError(t, err)
		return st.Following
	}
	set := func(kv map[string]string) {
		t.Helper()
		_, err := ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Set: kv})
		require.NoError(t, err)
	}

	assert.Nil(t, following(), "a machine following no channel has nothing to report about one")

	// Production's spread, which nothing below changes.
	const spread = domain.Duration(domain.DefaultUpdateRandomizedDelay)

	set(map[string]string{"update.channel": channel})
	assert.Equal(t, &ops.Following{Channel: channel, RandomizedDelay: spread}, following(),
		"with update.check off no timer exists, and status has to say the channel is not polled")

	set(map[string]string{"update.check": "true", "update.schedule": "*:0/15"})
	assert.Equal(t, &ops.Following{
		Channel: channel, Polled: true, Schedule: "*:0/15", RandomizedDelay: spread,
	}, following())

	// Through the state rather than `config set`, which would first need a
	// signing key this test has no use for beyond passing validation.
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Policy.RequireSignature = true
	inst.Policy.SigningKeys = []string{"RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3"}
	inst.Update.AutoApply = true
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	assert.Equal(t, &ops.Following{
		Channel: channel, Polled: true, Schedule: "*:0/15", AutoApply: true, RandomizedDelay: spread,
	}, following())
}

// A channel's reference can carry a password in its userinfo, and `status` is
// the output an operator pastes into a ticket. It shows the channel the way
// every recorded release source is shown: with the password removed and the
// username kept, so the reader can still tell which account it is.
func TestStatusDoesNotPrintAChannelPassword(t *testing.T) {
	h, _ := followingMachine(t, "")
	ctx := context.Background()

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Update.Channel = "oci://robot:s3cr3t-token@registry.example/demo/bundle:main"
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	st, err := ops.GetStatus(ctx, h.Deps)
	require.NoError(t, err)
	require.NotNil(t, st.Following)
	assert.NotContains(t, st.Following.Channel, "s3cr3t-token")
	assert.Contains(t, st.Following.Channel, "robot@registry.example/demo/bundle:main")
}

// The spread is a setting too (RFC 0016 decision 31): an operator sets how late
// a tick may run, or none, and `status` shows it beside the schedule.
func TestTheUpdateDelayCanBeSetAndUnset(t *testing.T) {
	h, unitDir := followingMachine(t, "")
	ctx := context.Background()
	set := func(name, value string) error {
		_, err := ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Set: map[string]string{name: value}})
		return err
	}
	require.NoError(t, set("update.channel", "oci://registry.example/demo/bundle:stable"))
	require.NoError(t, set("update.check", "true"))

	require.NoError(t, set("update.randomized_delay", "10m"))
	assert.Contains(t, updateTimer(t, unitDir), "Persistent=true\nRandomizedDelaySec=600\n\n[Install]")
	status, err := ops.GetStatus(ctx, h.Deps)
	require.NoError(t, err)
	require.NotNil(t, status.Following)
	assert.Equal(t, domain.Duration(10*time.Minute), status.Following.RandomizedDelay,
		"status does not show the spread the timer was given")

	// Zero is none, stored as a value rather than as absent, so it is not
	// the production default coming back.
	require.NoError(t, set("update.randomized_delay", "0"))
	timer := updateTimer(t, unitDir)
	assert.NotContains(t, timer, "RandomizedDelaySec", "a delay of 0 still spreads the ticks")
	assert.Contains(t, timer, "Persistent=true\n\n[Install]")
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	require.NotNil(t, inst.Update.RandomizedDelay, "a delay of 0 was stored as unset")

	// The timer takes whole seconds, so 500ms would render as none.
	for _, bad := range []string{"-5m", "soon", "1800", "500ms", "1m0.5s"} {
		require.Error(t, set("update.randomized_delay", bad), "%q was stored as a delay", bad)
	}

	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Unset: []string{"update.randomized_delay"}})
	require.NoError(t, err)
	assert.Contains(t, updateTimer(t, unitDir), "Persistent=true\nRandomizedDelaySec=1800\n\n[Install]",
		"unset did not return production to its half-hour spread")
}

// A sandbox has no spread unless its operator asks for one.
func TestASandboxUpdateDelayCanBeSet(t *testing.T) {
	h, unitDir := followingMachine(t, domain.ModeDev)
	_, err := ops.SetSettings(context.Background(), h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{
			"update.channel":          "oci://registry.example/demo/bundle:main",
			"update.check":            "true",
			"update.randomized_delay": "2m",
		},
	})
	require.NoError(t, err)
	assert.Contains(t, updateTimer(t, unitDir), "Persistent=true\nRandomizedDelaySec=120\n\n[Install]")
}
