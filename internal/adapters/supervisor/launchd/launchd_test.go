package launchd_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/supervisor/launchd"
	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// launchd, like systemd, is an init system the acceptance run on a Mac
// exercises and CI cannot: these tests pin what this adapter decides -- what
// it writes, which launchctl commands it issues, and what it makes of the
// answers -- with launchctl's replies scripted.

var update = flag.Bool("update", false, "rewrite the golden files")

type fixture struct {
	root      string
	s         *launchd.Supervisor
	runner    *fakes.Scripted
	daemons   string
	newsyslog string
	logs      string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	paths := domain.PathsUnder(root, "demo")
	f := fixture{
		root:      root,
		runner:    fakes.NewScripted(),
		daemons:   filepath.Join(root, "Library", "LaunchDaemons"),
		newsyslog: filepath.Join(root, "etc", "newsyslog.d"),
		logs:      filepath.Join(paths.VarDir, "logs"),
	}
	f.fresh()
	return f
}

// fresh starts the supervisor again over the same directories, with a runner
// that has run nothing.
func (f *fixture) fresh() {
	f.runner = fakes.NewScripted()
	f.s = launchd.New(f.runner, domain.PathsUnder(f.root, "demo"),
		launchd.WithDaemonDir(f.daemons),
		launchd.WithNewsyslogDir(f.newsyslog),
		launchd.WithRoot(func() bool { return true }))
}

// every is a unit set with every job, as an installation that sets
// everything has.
func every(t *testing.T) ports.UnitParams {
	t.Helper()
	return ports.UnitParams{
		Product: "demo", ManagerPath: "/usr/local/bin/morzer", ConfigPath: "/etc/demo/installation.yaml",
		UpdateTimer: true, FleetTimer: true, SyncTimer: true, SyncInterval: 10 * time.Minute,
		DoctorTimer: true, MetricsDir: "/var/lib/node_exporter",
		UpdateSchedule: "Sat,Sun *-*-* 04:00:00",
	}
}

// Each job is pinned as a whole, so a key dropped from one turns this red:
// KeepAlive from the boot job above all (RFC 0055 §6).
func TestEachJobRendersAsItsGoldenFile(t *testing.T) {
	f := newFixture(t)
	units, err := f.s.Units(every(t))
	require.NoError(t, err)

	for _, u := range units {
		// The log path is the fixture's temporary root; the golden
		// file reads it as the installation's.
		got := strings.ReplaceAll(string(u.Contents), f.logs, "/var/lib/demo/logs")
		path := filepath.Join("testdata", u.Name+".plist")
		if *update {
			require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
			continue
		}
		want, err := os.ReadFile(path)
		require.NoError(t, err, "missing golden file; run `go test ./internal/adapters/supervisor/launchd -update`")
		assert.Equal(t, string(want), got, "%s changed; review the diff and re-run with -update", path)
	}
}

// One job per systemd service, the timer's schedule in it, for every
// combination of the switches that add one (logs/macos-host.md).
func TestTheJobsAreSystemdsServices(t *testing.T) {
	for name, p := range map[string]ports.UnitParams{
		"none":    {Product: "demo", SkipBackupTimer: true},
		"backups": {Product: "demo"},
		"all":     every(t),
		"some":    {Product: "demo", SkipBackupTimer: true, FleetTimer: true, MetricsDir: "/var/lib/m"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			services, err := systemd.New(nil).Units(p)
			require.NoError(t, err)
			var want []string
			for _, u := range services {
				if product, ok := strings.CutSuffix(u.Name, ".service"); ok {
					want = append(want, "dev.morzer."+product)
				}
			}

			jobs, err := f.s.Units(p)
			require.NoError(t, err)
			var got []string
			for _, u := range jobs {
				got = append(got, u.Name)
				assert.True(t, u.Enable, "%s is not enabled, so it would not load at boot", u.Name)
			}
			assert.Equal(t, want, got)
		})
	}
}

// An unset schedule runs when systemd's default does, so a Mac and a Linux
// machine set alike run alike.
func TestAnUnsetScheduleIsSystemdsDefault(t *testing.T) {
	f := newFixture(t)
	units, err := f.s.Units(ports.UnitParams{Product: "demo", ManagerPath: "/m", DoctorTimer: true})
	require.NoError(t, err)
	contents := map[string]string{}
	for _, u := range units {
		contents[u.Name] = string(u.Contents)
	}
	assert.Contains(t, contents["dev.morzer.demo-backup"],
		"<key>Hour</key>\n\t\t<integer>2</integer>\n\t\t<key>Minute</key>\n\t\t<integer>30</integer>")
	assert.Contains(t, contents["dev.morzer.demo-doctor"],
		"<key>Hour</key>\n\t\t<integer>6</integer>\n\t\t<key>Minute</key>\n\t\t<integer>0</integer>")
}

// A stored schedule launchd cannot run fails the render, naming the field,
// rather than writing a job that runs at other times.
func TestAStoredScheduleLaunchdCannotRunFailsTheRender(t *testing.T) {
	f := newFixture(t)
	_, err := f.s.Units(ports.UnitParams{Product: "demo", BackupSchedule: "*:0/5"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the backup schedule cannot be a launchd schedule")
}

func TestValidateScheduleNamesTheFormsLaunchdTakes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, f.s.ValidateSchedule(ctx, ""))
	require.NoError(t, f.s.ValidateSchedule(ctx, "Sun *-*-* 04:00:00"))

	err := f.s.ValidateSchedule(ctx, "*:0/5")
	require.ErrorIs(t, err, domain.ErrValidation)
	assert.Contains(t, err.Error(), `launchd cannot run the schedule "*:0/5" as written`)
	assert.Contains(t, domain.AsError(err).Hint, "`Sun *-*-* 04:00` weekly")
	assert.Empty(t, f.runner.Calls(), "the check asked launchd, which has no parser to ask")
}

// Installing writes each job and the logs' rotation, enables what it
// creates, and loads the scheduled jobs; never the boot job, which runs when
// it is loaded and is launchd's to load at the next boot.
func TestInstallLoadsTheSchedulesAndLeavesTheBootJobToTheNextBoot(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.runner.OnExit("print system/", 113, "Could not find service")
	units, err := f.s.Units(ports.UnitParams{Product: "demo", ManagerPath: "/m"})
	require.NoError(t, err)

	require.NoError(t, f.s.InstallUnits(ctx, units, ports.EnableNew))

	for _, label := range []string{"dev.morzer.demo", "dev.morzer.demo-backup"} {
		info, err := os.Stat(filepath.Join(f.daemons, label+".plist"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
		assert.True(t, f.runner.Ran("enable system/"+label), f.runner.CommandLines())
	}
	assert.True(t, f.runner.Ran("bootstrap system "+filepath.Join(f.daemons, "dev.morzer.demo-backup.plist")),
		f.runner.CommandLines())
	assert.False(t, f.runner.Ran("dev.morzer.demo.plist"), "the boot job was loaded, which runs it:\n%s",
		f.runner.CommandLines())

	info, err := os.Stat(f.logs)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	conf, err := os.ReadFile(filepath.Join(f.newsyslog, "dev.morzer.demo.conf"))
	require.NoError(t, err)
	for _, label := range []string{"dev.morzer.demo", "dev.morzer.demo-backup"} {
		assert.Contains(t, string(conf), filepath.Join(f.logs, label+".log")+"\troot:wheel\t600\t5\t10240\t*\tNZ\n")
	}
}

// A second install leaves enablement as the machine has it and reloads only
// a loaded job whose plist changed -- and not one an operator disabled.
func TestAReinstallReloadsOnlyAChangedSchedule(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := ports.UnitParams{Product: "demo", ManagerPath: "/m", FleetTimer: true}
	units, err := f.s.Units(p)
	require.NoError(t, err)
	require.NoError(t, f.s.InstallUnits(ctx, units, ports.EnableAll))

	// Unchanged: nothing is enabled or loaded again.
	f.fresh()
	f.runner.OnOutput("print-disabled", "disabled services = {\n}\n")
	f.runner.OnOutput("print system/", "system/x = {\n\tstate = not running\n}\n")
	require.NoError(t, f.s.InstallUnits(ctx, units, ports.EnableNew))
	for _, verb := range []string{" enable ", " bootout ", " bootstrap "} {
		assert.False(t, f.runner.Ran(verb), "an unchanged reinstall ran%s:\n%s", verb, f.runner.CommandLines())
	}

	// Two schedules changed, one of them a job an operator disabled.
	f.fresh()
	f.runner.OnOutput("print-disabled", "disabled services = {\n\t\"dev.morzer.demo-fleet\" => disabled\n}\n")
	f.runner.OnOutput("print system/", "system/x = {\n\tstate = not running\n}\n")
	p.BackupSchedule = "*-*-* 01:00"
	p.FleetSchedule = "*-*-* *:30"
	units, err = f.s.Units(p)
	require.NoError(t, err)
	require.NoError(t, f.s.InstallUnits(ctx, units, ports.EnableNew))

	assert.False(t, f.runner.Ran(" enable "), "a reconciliation enabled a job that exists:\n%s", f.runner.CommandLines())
	assert.True(t, f.runner.Ran("bootout system/dev.morzer.demo-backup"), f.runner.CommandLines())
	assert.True(t, f.runner.Ran("bootstrap system "+filepath.Join(f.daemons, "dev.morzer.demo-backup.plist")))
	assert.False(t, f.runner.Ran("bootout system/dev.morzer.demo-fleet"), "a disabled job was reloaded:\n%s",
		f.runner.CommandLines())
	assert.False(t, f.runner.Ran(filepath.Join(f.daemons, "dev.morzer.demo-fleet.plist")), f.runner.CommandLines())
	assert.False(t, f.runner.Ran("bootstrap system "+filepath.Join(f.daemons, "dev.morzer.demo.plist")),
		"the boot job was loaded:\n%s", f.runner.CommandLines())
}

// A failure while loading undoes what the call created, so a retry finds the
// jobs fresh and enables them, rather than skipping them as existing.
func TestAFailedInstallRemovesWhatItCreated(t *testing.T) {
	f := newFixture(t)
	f.runner.OnExit("print system/", 113, "Could not find service")
	f.runner.OnExit("bootstrap", 5, "Bootstrap failed: 5: Input/output error")
	units, err := f.s.Units(ports.UnitParams{Product: "demo", ManagerPath: "/m"})
	require.NoError(t, err)

	err = f.s.InstallUnits(context.Background(), units, ports.EnableNew)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Bootstrap failed: 5")

	entries, err := os.ReadDir(f.daemons)
	require.NoError(t, err)
	assert.Empty(t, entries, "a failed install left a job behind")
	assert.NoFileExists(t, filepath.Join(f.newsyslog, "dev.morzer.demo.conf"))
	assert.True(t, f.runner.Ran("disable system/dev.morzer.demo-backup"), f.runner.CommandLines())
}

func TestRemoveUnloadsDeletesAndTakesTheRotationWithTheBootJob(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.runner.OnExit("print system/", 113, "Could not find service")
	units, err := f.s.Units(ports.UnitParams{Product: "demo", ManagerPath: "/m", FleetTimer: true})
	require.NoError(t, err)
	require.NoError(t, f.s.InstallUnits(ctx, units, ports.EnableNew))
	conf := filepath.Join(f.newsyslog, "dev.morzer.demo.conf")

	require.NoError(t, f.s.RemoveUnits(ctx, []string{"dev.morzer.demo-fleet"}))
	assert.True(t, f.runner.Ran("bootout system/dev.morzer.demo-fleet"))
	assert.NoFileExists(t, filepath.Join(f.daemons, "dev.morzer.demo-fleet.plist"))
	assert.FileExists(t, conf, "removing one schedule took the rotation of every log")

	require.NoError(t, f.s.RemoveUnits(ctx, f.s.ManagedUnitNames("demo")))
	installed, err := f.s.InstalledUnits(ctx, "demo")
	require.NoError(t, err)
	assert.Empty(t, installed)
	assert.NoFileExists(t, conf)

	assert.Error(t, f.s.RemoveUnits(ctx, []string{"dev.morzer.demo/../../x"}))
}

// launchctl print's own properties are one tab in, and only those are read:
// the deeper ones are the job's endpoints and the like.
const printed = `system/dev.morzer.demo-backup = {
	active count = 0
	path = /Library/LaunchDaemons/dev.morzer.demo-backup.plist
	type = LaunchDaemon
	state = %s

	program = /usr/local/bin/morzer
	arguments = {
		/usr/local/bin/morzer
		backup
	}

	runs = 3
	last exit code = %s

	endpoints = {
		"x" = {
			state = active
		}
	}
}
`

func TestStatusReadsLaunchctlPrintInSystemdsWords(t *testing.T) {
	for name, tc := range map[string]struct {
		state, exit string
		boot        bool
		active, sub string
		code        int
	}{
		"running":           {"running", "0", false, "active", "running", 0},
		"waiting":           {"not running", "0", false, "inactive", "waiting", 0},
		"never run":         {"not running", "(never exited)", false, "inactive", "waiting", 0},
		"failed":            {"not running", "3", false, "failed", "exited", 3},
		"failed with name":  {"not running", "78: EX_CONFIG", false, "failed", "exited", 78},
		"boot job retrying": {"not running", "7", true, "activating", "auto-restart", 7},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			params := ports.UnitParams{Product: "demo", ManagerPath: "/m"}
			units, err := f.s.Units(params)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(f.daemons, 0o755))
			for _, u := range units {
				require.NoError(t, os.WriteFile(filepath.Join(f.daemons, u.Name+".plist"), u.Contents, 0o644))
			}
			f.runner.OnOutput("print-disabled", "disabled services = {\n}\n")
			f.runner.OnOutput("print system/", strings.Replace(
				strings.Replace(printed, "%s", tc.state, 1), "%s", tc.exit, 1))

			label := "dev.morzer.demo-backup"
			if tc.boot {
				label = "dev.morzer.demo"
			}
			got, err := f.s.Status(context.Background(), label)
			require.NoError(t, err)
			assert.Equal(t, ports.UnitState{Name: label, Loaded: true, Enabled: true,
				Active: tc.active, Sub: tc.sub, ExitCode: tc.code}, got)
		})
	}
}

func TestStatusOfAJobNotLoadedOrNotThere(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	got, err := f.s.Status(ctx, "dev.morzer.demo-backup")
	require.NoError(t, err)
	assert.Equal(t, ports.UnitState{Name: "dev.morzer.demo-backup"}, got)
	assert.Empty(t, f.runner.Calls(), "a job with no plist was asked about")

	require.NoError(t, os.MkdirAll(f.daemons, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.daemons, "dev.morzer.demo-backup.plist"), []byte("x"), 0o644))
	f.runner.OnOutput("print-disabled", "disabled services = {\n\t\"dev.morzer.demo-backup\" => true\n}\n")
	f.runner.OnExit("print system/", 113, "Could not find service")
	got, err = f.s.Status(ctx, "dev.morzer.demo-backup")
	require.NoError(t, err)
	assert.Equal(t, ports.UnitState{Name: "dev.morzer.demo-backup", Loaded: true, Enabled: false,
		Active: "inactive", Sub: "unloaded"}, got, "an older launchctl's `=> true` is a disabled job")

	// The boot job is not loaded until the next boot, and that is not a
	// schedule that has stopped.
	units, err := f.s.Units(ports.UnitParams{Product: "demo", ManagerPath: "/m"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.daemons, "dev.morzer.demo.plist"), units[0].Contents, 0o644))
	got, err = f.s.Status(ctx, "dev.morzer.demo")
	require.NoError(t, err)
	assert.Equal(t, "dead", got.Sub)
}

func TestTheManagerPathsAreThePlistsAndWhatLaunchdLoaded(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.MkdirAll(f.daemons, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.daemons, "dev.morzer.demo.plist"), []byte("x"), 0o644))
	f.runner.OnOutput("plutil -extract ProgramArguments.0 raw", "/usr/local/bin/morzer\n")
	f.runner.OnOutput("print system/dev.morzer.demo", "system/dev.morzer.demo = {\n\tprogram = /opt/old/morzer\n}\n")

	paths, err := f.s.UnitsManagerPaths(context.Background(), "demo")
	require.NoError(t, err)
	assert.Equal(t, []string{"/usr/local/bin/morzer", "/opt/old/morzer"}, paths)
}

func TestAvailableNeedsRootAndLaunchctl(t *testing.T) {
	root := t.TempDir()
	runner := fakes.NewScripted()
	notRoot := launchd.New(runner, domain.PathsUnder(root, "demo"), launchd.WithRoot(func() bool { return false }))
	if _, err := os.Stat("/bin/launchctl"); err != nil {
		assert.False(t, launchd.New(runner, domain.PathsUnder(root, "demo"),
			launchd.WithRoot(func() bool { return true })).Available(context.Background()),
			"a host without launchctl has no launchd")
	}
	assert.False(t, notRoot.Available(context.Background()))
}

// /Library/LaunchDaemons is the directory launchd loads root's jobs from at
// boot; a job anywhere else is never started.
func TestJobsLiveWhereLaunchdLoadsThemAtBoot(t *testing.T) {
	assert.Equal(t, "/Library/LaunchDaemons", launchd.DaemonDir)
	assert.Equal(t, "/etc/newsyslog.d", launchd.NewsyslogDir)
}

// A job launchd still has keeps running its schedule until the next boot,
// so a failed unload keeps the plist and fails the removal for a retry; one
// launchd does not have is removed.
func TestAJobThatWouldNotUnloadIsNotRemoved(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, os.MkdirAll(f.daemons, 0o755))
	plist := filepath.Join(f.daemons, "dev.morzer.demo-backup.plist")
	require.NoError(t, os.WriteFile(plist, []byte("x"), 0o644))
	f.runner.OnExit("bootout", 5, "Boot-out failed: 5: Input/output error")
	f.runner.OnOutput("print system/dev.morzer.demo-backup", "system/dev.morzer.demo-backup = {\n\tstate = running\n}\n")

	err := f.s.RemoveUnits(ctx, []string{"dev.morzer.demo-backup"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Boot-out failed: 5")
	assert.FileExists(t, plist, "the plist of a job launchd still runs was removed")

	f.fresh()
	f.runner.OnExit("bootout", 3, "Boot-out failed: 3: No such process")
	f.runner.OnExit("print system/", 113, "Could not find service")
	require.NoError(t, f.s.RemoveUnits(ctx, []string{"dev.morzer.demo-backup"}))
	assert.NoFileExists(t, plist)
}

// A write that fails partway removes the jobs written before it, so a retry
// finds them fresh rather than skipping them as existing.
func TestAFailedWriteRemovesTheJobsWrittenBeforeIt(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(f.daemons, "dev.morzer.demo-backup.plist"), 0o755))
	units, err := f.s.Units(ports.UnitParams{Product: "demo", ManagerPath: "/m"})
	require.NoError(t, err)

	require.Error(t, f.s.InstallUnits(context.Background(), units, ports.EnableNew))
	assert.NoFileExists(t, filepath.Join(f.daemons, "dev.morzer.demo.plist"),
		"the boot job written before the failure was left for a retry to skip")
	assert.NoFileExists(t, filepath.Join(f.newsyslog, "dev.morzer.demo.conf"))
}

// A schedule unloaded to be loaded again, and refused, gets its old plist and
// its old load back, so the job runs as it did and a retry still sees the
// change to make.
func TestAFailedReloadPutsTheOldScheduleBack(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := ports.UnitParams{Product: "demo", ManagerPath: "/m"}
	units, err := f.s.Units(p)
	require.NoError(t, err)
	require.NoError(t, f.s.InstallUnits(ctx, units, ports.EnableAll))
	plist := filepath.Join(f.daemons, "dev.morzer.demo-backup.plist")
	old, err := os.ReadFile(plist)
	require.NoError(t, err)

	f.fresh()
	f.runner.OnOutput("print-disabled", "disabled services = {\n}\n")
	f.runner.OnOutput("print system/", "system/x = {\n\tstate = not running\n}\n")
	f.runner.OnExit("bootstrap system "+plist, 5, "Bootstrap failed: 5: Input/output error")
	p.BackupSchedule = "*-*-* 01:00"
	changed, err := f.s.Units(p)
	require.NoError(t, err)
	require.Error(t, f.s.InstallUnits(ctx, changed, ports.EnableNew), "the refused load was not reported")

	now, err := os.ReadFile(plist)
	require.NoError(t, err)
	assert.Equal(t, string(old), string(now), "the new schedule was left on disk with the job unloaded")
	var loads int
	for _, c := range f.runner.Calls() {
		if strings.Contains(strings.Join(c.Argv, " "), "bootstrap system "+plist) {
			loads++
		}
	}
	assert.Equal(t, 2, loads, "the old schedule was not loaded again:\n%s", f.runner.CommandLines())
}

// An undo puts enablement back as it was: a job this call found enabled stays
// enabled, so a failure in `init --repair` does not switch off what was on.
func TestAFailedRepairLeavesEnabledJobsEnabled(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := ports.UnitParams{Product: "demo", ManagerPath: "/m", FleetTimer: true}
	units, err := f.s.Units(p)
	require.NoError(t, err)
	require.NoError(t, f.s.InstallUnits(ctx, units, ports.EnableAll))

	f.fresh()
	f.runner.OnOutput("print-disabled", "disabled services = {\n\t\"dev.morzer.demo-fleet\" => disabled\n}\n")
	f.runner.OnExit("print system/", 113, "Could not find service")
	f.runner.OnExit("bootstrap system "+filepath.Join(f.daemons, "dev.morzer.demo-fleet.plist"), 5,
		"Bootstrap failed: 5: Input/output error")
	require.Error(t, f.s.InstallUnits(ctx, units, ports.EnableAll))

	assert.False(t, f.runner.Ran("disable system/dev.morzer.demo-backup"),
		"the undo switched off a job that was on:\n%s", f.runner.CommandLines())
	for _, c := range f.runner.Calls() {
		if strings.Join(c.Argv, " ") == "/bin/launchctl disable system/dev.morzer.demo" {
			t.Errorf("the undo switched off the boot job, which was on:\n%s", f.runner.CommandLines())
		}
	}
	assert.True(t, f.runner.Ran("disable system/dev.morzer.demo-fleet"),
		"the job that was off was not switched off again:\n%s", f.runner.CommandLines())
}

// A repair that switches a job back on reloads its changed schedule too.
func TestARepairReloadsAJobItSwitchesBackOn(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := ports.UnitParams{Product: "demo", ManagerPath: "/m"}
	units, err := f.s.Units(p)
	require.NoError(t, err)
	require.NoError(t, f.s.InstallUnits(ctx, units, ports.EnableAll))

	f.fresh()
	f.runner.OnOutput("print-disabled", "disabled services = {\n\t\"dev.morzer.demo-backup\" => disabled\n}\n")
	f.runner.OnOutput("print system/", "system/x = {\n\tstate = not running\n}\n")
	p.BackupSchedule = "*-*-* 01:00"
	units, err = f.s.Units(p)
	require.NoError(t, err)
	require.NoError(t, f.s.InstallUnits(ctx, units, ports.EnableAll))
	assert.True(t, f.runner.Ran("enable system/dev.morzer.demo-backup"), f.runner.CommandLines())
	assert.True(t, f.runner.Ran("bootstrap system "+filepath.Join(f.daemons, "dev.morzer.demo-backup.plist")),
		"the schedule of a job switched back on was not reloaded:\n%s", f.runner.CommandLines())
}

// A schedule reloaded before a later job fails is reloaded again from the
// old plist the undo puts back, so the file and what launchd runs agree.
func TestAnUndoReloadsTheSchedulesItHadReloaded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := ports.UnitParams{Product: "demo", ManagerPath: "/m", FleetTimer: true}
	units, err := f.s.Units(p)
	require.NoError(t, err)
	require.NoError(t, f.s.InstallUnits(ctx, units, ports.EnableAll))
	backup := filepath.Join(f.daemons, "dev.morzer.demo-backup.plist")

	f.fresh()
	f.runner.OnOutput("print-disabled", "disabled services = {\n}\n")
	f.runner.OnOutput("print system/", "system/x = {\n\tstate = not running\n}\n")
	f.runner.OnExit("bootstrap system "+filepath.Join(f.daemons, "dev.morzer.demo-fleet.plist"), 5,
		"Bootstrap failed: 5: Input/output error")
	p.BackupSchedule, p.FleetSchedule = "*-*-* 01:00", "*-*-* *:30"
	changed, err := f.s.Units(p)
	require.NoError(t, err)
	require.Error(t, f.s.InstallUnits(ctx, changed, ports.EnableNew))

	var loads []string
	for _, c := range f.runner.Calls() {
		if line := strings.Join(c.Argv, " "); strings.Contains(line, "bootstrap system "+backup) ||
			strings.Contains(line, "bootout system/dev.morzer.demo-backup") {
			loads = append(loads, c.Argv[1])
		}
	}
	assert.Equal(t, []string{"bootout", "bootstrap", "bootout", "bootstrap"}, loads,
		"the backup job, reloaded before the fleet job failed, was not reloaded from its old plist:\n%s",
		f.runner.CommandLines())
	assert.Equal(t, string(units[1].Contents), func() string { b, _ := os.ReadFile(backup); return string(b) }())
}
