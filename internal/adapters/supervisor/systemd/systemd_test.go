package systemd_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// systemd is the one adapter the acceptance run cannot exercise: it needs a
// real init system and root. What is testable is everything this adapter
// decides -- which commands it issues, in what order, and what it makes of the
// answers -- which is the whole of its behaviour.
//
// The unit directory is relocated so nothing touches /etc/systemd, and the
// runner is scripted so systemctl's replies are the ones a test needs rather
// than the ones a healthy machine happens to give.

func newSupervisor(t *testing.T) (*systemd.Supervisor, *exec.Scripted, string) {
	t.Helper()
	dir := t.TempDir()
	runner := exec.NewScripted()
	return systemd.New(runner,
		systemd.WithUnitDir(dir),
		systemd.WithSystemctl("/usr/bin/systemctl"),
	), runner, dir
}

// The unit directory is a decision, not a default (RFC 0030 row 3), and this is
// the only test that reads the value production uses.
//
// It exists because relocating the directory is what makes every other test in
// this file runnable without root -- so the constant they all replace is the one
// thing they cannot check. Measured before writing this: changing it to
// `/usr/lib/systemd/system` and running the entire suite passes.
//
// What the value buys, and what it costs, are both settled: `/etc` is where an
// administrator's units live, which is what these are, and systemd loads it in
// preference to `/usr/lib` -- so the manager's file is the one that takes
// effect. The cost is that `systemctl mask` needs this exact path for its
// symlink and finds it occupied (RFC 0030 §3.2). Moving to `/usr/lib` would buy
// masking back and cost the precedence, which is the trade row 3 declined.
func TestGeneratedUnitsLiveWhereAnAdministratorsUnitsLive(t *testing.T) {
	if systemd.UnitDir != "/etc/systemd/system" {
		t.Fatalf("unit directory is %q; RFC 0030 row 3 answers /etc/systemd/system, "+
			"and moving it shadows every unit already installed on an existing "+
			"machine -- change the RFC before changing this", systemd.UnitDir)
	}
}

func TestInstallUnitsWritesReloadsThenEnables(t *testing.T) {
	s, runner, dir := newSupervisor(t)

	units := []ports.Unit{
		{Name: "demo.service", Contents: []byte("[Unit]\n"), Enable: true},
		{Name: "demo-backup.service", Contents: []byte("[Unit]\n"), Enable: false},
		{Name: "demo-backup.timer", Contents: []byte("[Timer]\n"), Enable: true},
	}
	if err := s.InstallUnits(context.Background(), units, ports.EnableAll); err != nil {
		t.Fatalf("InstallUnits: %v", err)
	}

	for _, u := range units {
		info, err := os.Stat(filepath.Join(dir, u.Name))
		if err != nil {
			t.Fatalf("%s was not written: %v", u.Name, err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Errorf("%s has mode %04o, want 0644", u.Name, got)
		}
	}

	// One reload for the whole set, not one per unit: systemd would
	// otherwise briefly see a half-installed set.
	reloads, reloadAt, firstEnable := 0, -1, -1
	for i, c := range runner.Calls() {
		line := strings.Join(c.Argv, " ")
		if strings.Contains(line, "daemon-reload") {
			reloads++
			if reloadAt < 0 {
				reloadAt = i
			}
		}
		if firstEnable < 0 && strings.Contains(line, "enable") {
			firstEnable = i
		}
	}
	if reloads != 1 {
		t.Errorf("issued %d daemon-reloads, want exactly 1:\n%s", reloads, runner.CommandLines())
	}
	// And the reload comes first, or systemd is asked to enable a unit it
	// has not read yet.
	if reloadAt < 0 || firstEnable < 0 || reloadAt > firstEnable {
		t.Errorf("reload at %d, first enable at %d:\n%s",
			reloadAt, firstEnable, runner.CommandLines())
	}

	if !runner.Ran("enable demo.service") || !runner.Ran("enable demo-backup.timer") {
		t.Errorf("a unit that asked to be enabled was not:\n%s", runner.CommandLines())
	}
	if runner.Ran("enable demo-backup.service") {
		t.Error("the oneshot backup service was enabled, which would run it at every boot")
	}
}

// The scope decides whether a second install re-enables (RFC 0030 §8.1).
//
// This is where row 1 lives, so this is where it is pinned. The measurement in
// §3.1 was that an unrelated `config set` issued `enable demo-backup.timer`
// every time, which is what made `systemctl disable` a decision with a
// half-life. The second install below is that `config set`.
//
// The unit *files* are rewritten on both calls in both scopes, and that is
// asserted too: contents and existence stay the manager's, and a change that
// bought a durable `disable` by leaving stale unit contents on disk would have
// answered a different question.
func TestASecondInstallRespectsTheScope(t *testing.T) {
	units := []ports.Unit{
		{Name: "demo.service", Contents: []byte("[Unit]\nX=1\n"), Enable: true},
		{Name: "demo-backup.service", Contents: []byte("[Unit]\nX=1\n"), Enable: false},
		{Name: "demo-backup.timer", Contents: []byte("[Timer]\nX=1\n"), Enable: true},
	}
	rewritten := []ports.Unit{
		{Name: "demo.service", Contents: []byte("[Unit]\nX=2\n"), Enable: true},
		{Name: "demo-backup.service", Contents: []byte("[Unit]\nX=2\n"), Enable: false},
		{Name: "demo-backup.timer", Contents: []byte("[Timer]\nX=2\n"), Enable: true},
	}

	for name, tc := range map[string]struct {
		scope        ports.EnableScope
		wantSecondly int
	}{
		"a reconciliation leaves enablement alone": {ports.EnableNew, 0},
		"a repair re-asserts it":                   {ports.EnableAll, 2},
	} {
		t.Run(name, func(t *testing.T) {
			s, runner, dir := newSupervisor(t)
			ctx := context.Background()

			// The first install creates the files, so both scopes
			// enable here: a unit nobody has seen yet is not a
			// decision anybody has made.
			if err := s.InstallUnits(ctx, units, tc.scope); err != nil {
				t.Fatalf("first install: %v", err)
			}
			if got := enablesSince(runner, 0); got != 2 {
				t.Fatalf("the first install issued %d enable(s), want 2:\n%s",
					got, runner.CommandLines())
			}

			afterFirst := len(runner.Calls())
			if err := s.InstallUnits(ctx, rewritten, tc.scope); err != nil {
				t.Fatalf("second install: %v", err)
			}
			if got := enablesSince(runner, afterFirst); got != tc.wantSecondly {
				t.Errorf("the second install issued %d enable(s), want %d:\n%s",
					got, tc.wantSecondly, runner.CommandLines())
			}

			// Never, in either scope: switching a unit off is the
			// operator's, and removal is RemoveUnits' job.
			if runner.Ran("disable") {
				t.Errorf("an install disabled a unit:\n%s", runner.CommandLines())
			}

			for _, u := range rewritten {
				got, err := os.ReadFile(filepath.Join(dir, u.Name))
				if err != nil {
					t.Fatalf("%s: %v", u.Name, err)
				}
				if string(got) != string(u.Contents) {
					t.Errorf("%s was not rewritten: %q", u.Name, got)
				}
			}
		})
	}
}

// A unit whose file is missing is created and enabled even when its siblings
// are not, which is the half of row 1 that is easy to lose.
//
// `config set update.channel=…` adds the update pair to a machine that already
// has the other units. Under a rule that skipped enablement whenever *any* unit
// existed, the new timer would be installed and never switched on -- a timer
// that exists, is asked for, and does not fire.
func TestAUnitAddedByAReconciliationIsStillEnabled(t *testing.T) {
	s, runner, _ := newSupervisor(t)
	ctx := context.Background()

	existing := []ports.Unit{{Name: "demo.service", Contents: []byte("x"), Enable: true}}
	if err := s.InstallUnits(ctx, existing, ports.EnableNew); err != nil {
		t.Fatalf("first install: %v", err)
	}

	afterFirst := len(runner.Calls())
	if err := s.InstallUnits(ctx, append(existing,
		ports.Unit{Name: "demo-update.timer", Contents: []byte("y"), Enable: true},
	), ports.EnableNew); err != nil {
		t.Fatalf("second install: %v", err)
	}

	second := runner.Calls()[afterFirst:]
	if !ranIn(second, "enable demo-update.timer") {
		t.Errorf("a newly added timer was installed and not enabled:\n%s", runner.CommandLines())
	}
	if ranIn(second, "enable demo.service") {
		t.Errorf("the unit that already existed was re-enabled:\n%s", runner.CommandLines())
	}
}

// enablesSince counts `systemctl enable` calls made after the first n calls.
//
// Positional rather than a reset on the runner, so the assertion is about one
// install rather than about everything the test has done so far -- and so a
// second install that issued its enables *before* the reload would still be
// counted here and caught by the ordering assertion elsewhere.
func enablesSince(runner *exec.Scripted, from int) int {
	n := 0
	for _, c := range runner.Calls()[from:] {
		// `enable`, not `is-enabled`: the query is not the assertion.
		if len(c.Argv) > 1 && c.Argv[1] == "enable" {
			n++
		}
	}
	return n
}

func ranIn(calls []exec.Command, match string) bool {
	for _, c := range calls {
		if strings.Contains(strings.Join(c.Argv, " "), match) {
			return true
		}
	}
	return false
}

// TestInstallUnitsRefusesANameThatIsAPath is the traversal guard. A unit name
// derives from the product name and reaches both the filesystem and argv.
func TestInstallUnitsRefusesANameThatIsAPath(t *testing.T) {
	s, runner, dir := newSupervisor(t)

	for _, name := range []string{
		"../escape.service",
		"/etc/systemd/system/evil.service",
		"demo evil.service",
		"sub/demo.service",
		"",
	} {
		if err := s.InstallUnits(context.Background(),
			[]ports.Unit{{Name: name, Contents: []byte("x")}}, ports.EnableAll); err == nil {
			t.Errorf("a unit named %q was accepted", name)
		}
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a refused unit still wrote something: %v", entries)
	}
	if len(runner.Calls()) != 0 {
		t.Errorf("a refused unit still ran systemctl:\n%s", runner.CommandLines())
	}
}

func TestInstallUnitsStopsWhenTheReloadFails(t *testing.T) {
	s, runner, _ := newSupervisor(t)
	runner.OnError("daemon-reload", &exec.ExitError{
		Argv: []string{"systemctl", "daemon-reload"}, ExitCode: 1,
		Stderr: "Failed to reload daemon",
	})

	err := s.InstallUnits(context.Background(),
		[]ports.Unit{{Name: "demo.service", Contents: []byte("x"), Enable: true}}, ports.EnableAll)
	if err == nil {
		t.Fatal("a failed daemon-reload was ignored")
	}
	// Enabling a unit systemd has not read is worse than not enabling it.
	if runner.Ran("enable") {
		t.Errorf("a unit was enabled after the reload failed:\n%s", runner.CommandLines())
	}
}

func TestRemoveUnitsStopsAndDisablesBeforeDeleting(t *testing.T) {
	s, runner, dir := newSupervisor(t)

	path := filepath.Join(dir, "demo.service")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.RemoveUnits(context.Background(), []string{"demo.service"}); err != nil {
		t.Fatalf("RemoveUnits: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the unit file survived removal")
	}

	// Deleting the file from under a running unit leaves systemd holding
	// one it can no longer describe.
	if !runner.Ran("stop demo.service") || !runner.Ran("disable demo.service") {
		t.Errorf("the unit was not stopped and disabled before removal:\n%s",
			runner.CommandLines())
	}
}

// TestRemoveUnitsToleratesAUnitThatWasNeverInstalled matters because `init`
// compensating a failed run removes units it may never have written.
func TestRemoveUnitsToleratesAUnitThatWasNeverInstalled(t *testing.T) {
	s, runner, _ := newSupervisor(t)
	runner.OnError("stop", &exec.ExitError{ExitCode: 5, Stderr: "Unit not loaded."})
	runner.OnError("disable", &exec.ExitError{ExitCode: 1, Stderr: "does not exist"})

	if err := s.RemoveUnits(context.Background(), []string{"demo.service"}); err != nil {
		t.Errorf("removing a unit that was never installed: %v", err)
	}
}

func TestRemoveUnitsRefusesAPathName(t *testing.T) {
	s, _, _ := newSupervisor(t)
	if err := s.RemoveUnits(context.Background(), []string{"../../etc/passwd"}); err == nil {
		t.Error("a traversing unit name was accepted for removal")
	}
}

func TestStatusReadsMachineReadableProperties(t *testing.T) {
	s, runner, _ := newSupervisor(t)
	runner.OnOutput("show demo.service", strings.Join([]string{
		"LoadState=loaded",
		"ActiveState=active",
		"SubState=running",
		"UnitFileState=enabled",
		"ExecMainStatus=0",
	}, "\n"))

	state, err := s.Status(context.Background(), "demo.service")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !state.Loaded || !state.Enabled {
		t.Errorf("state = %+v; want loaded and enabled", state)
	}
	if state.Active != "active" || state.Sub != "running" {
		t.Errorf("state = %+v; want active/running", state)
	}

	// `show`, not `status`: show emits key=value pairs meant for machines,
	// and its format does not change between systemd versions.
	if !runner.Ran("show") {
		t.Errorf("Status must query with `show`:\n%s", runner.CommandLines())
	}
}

func TestStatusUnderstandsEveryEnabledSpelling(t *testing.T) {
	for _, spelling := range []string{"enabled", "enabled-runtime"} {
		s, runner, _ := newSupervisor(t)
		runner.OnOutput("show", "LoadState=loaded\nUnitFileState="+spelling)

		state, err := s.Status(context.Background(), "demo.service")
		if err != nil {
			t.Fatal(err)
		}
		if !state.Enabled {
			t.Errorf("UnitFileState=%s was read as not enabled", spelling)
		}
	}
}

func TestStatusReportsAFailedUnitRatherThanHidingIt(t *testing.T) {
	s, runner, _ := newSupervisor(t)
	runner.OnOutput("show", strings.Join([]string{
		"LoadState=loaded",
		"ActiveState=failed",
		"SubState=failed",
		"UnitFileState=enabled",
		"ExecMainStatus=137",
	}, "\n"))

	state, err := s.Status(context.Background(), "demo.service")
	if err != nil {
		t.Fatal(err)
	}
	if state.Active != "failed" || state.ExitCode != 137 {
		t.Errorf("state = %+v; a failed unit and its exit code must both survive", state)
	}
}

// TestStatusOfAnUnknownUnitIsAStateNotAnError is what lets `doctor` ask about
// units that may never have been installed.
func TestStatusOfAnUnknownUnitIsAStateNotAnError(t *testing.T) {
	s, runner, _ := newSupervisor(t)
	runner.OnError("show", &exec.ExitError{ExitCode: 4, Stderr: "Unit could not be found."})

	state, err := s.Status(context.Background(), "demo.service")
	if err != nil {
		t.Fatalf("an unknown unit must be a state, not an error: %v", err)
	}
	if state.Loaded {
		t.Error("an unknown unit was reported loaded")
	}
	if state.Name != "demo.service" {
		t.Errorf("the state is not attributed to the unit: %q", state.Name)
	}
}

func TestStatusRefusesAPathName(t *testing.T) {
	s, _, _ := newSupervisor(t)
	if _, err := s.Status(context.Background(), "../evil"); err == nil {
		t.Error("a traversing unit name was accepted for a status query")
	}
}

func TestAvailableIsFalseWithoutSystemctl(t *testing.T) {
	missing := exec.NewScripted()
	missing.LookErr = errors.New("systemctl not found in PATH")

	s := systemd.New(missing, systemd.WithUnitDir(t.TempDir()))
	if s.Available(context.Background()) {
		t.Error("systemd reported available with no systemctl on PATH")
	}
}

func TestBuildUnitsRendersTheSetAndItsDefaults(t *testing.T) {
	units, err := systemd.BuildUnits(systemd.UnitParams{
		Product:     "demo",
		ManagerPath: "/usr/local/bin/morzer",
		ConfigPath:  "/etc/demo/installation.yaml",
	})
	if err != nil {
		t.Fatalf("BuildUnits: %v", err)
	}
	if len(units) != 3 {
		t.Fatalf("rendered %d units, want the service, the backup service and the timer", len(units))
	}

	byName := map[string]ports.Unit{}
	for _, u := range units {
		byName[u.Name] = u
	}
	// UnitNames is the superset -- it is what *removal* walks, so it names
	// the update pair whether or not this installation generates one.
	for _, want := range []string{
		systemd.ServiceUnitName("demo"),
		systemd.BackupServiceUnitName("demo"),
		systemd.BackupTimerUnitName("demo"),
	} {
		if _, ok := byName[want]; !ok {
			t.Errorf("no unit named %s", want)
		}
	}

	// A machine that follows no channel gets no update timer: an installed
	// timer that polls nothing still appears in `systemctl list-timers` as
	// though it did.
	if _, ok := byName[systemd.UpdateTimerUnitName("demo")]; ok {
		t.Error("an installation with no channel got an update timer")
	}
	// The same rule for the fleet pair, on the same reasoning: a machine
	// with no target to publish to would fail on every tick.
	if _, ok := byName[systemd.FleetTimerUnitName("demo")]; ok {
		t.Error("an installation with no target got a fleet timer")
	}

	// The timer is enabled and the oneshot backup service is not: enabling
	// a oneshot would run it at every boot.
	if !byName[systemd.BackupTimerUnitName("demo")].Enable {
		t.Error("the backup timer is not enabled, so nothing would schedule a backup")
	}
	if byName[systemd.BackupServiceUnitName("demo")].Enable {
		t.Error("the oneshot backup service is enabled, so it would run at every boot")
	}

	service := string(byName[systemd.ServiceUnitName("demo")].Contents)
	if !strings.Contains(service, "/usr/local/bin/morzer") {
		t.Error("the unit does not embed an absolute manager path, so systemd could not run it")
	}

	// An unset schedule takes the declared default rather than rendering an
	// empty OnCalendar, which systemd refuses to load.
	timer := string(byName[systemd.BackupTimerUnitName("demo")].Contents)
	if !strings.Contains(timer, systemd.DefaultBackupSchedule) {
		t.Errorf("the timer has no schedule:\n%s", timer)
	}
}

func TestBuildUnitsHonoursAnExplicitSchedule(t *testing.T) {
	units, err := systemd.BuildUnits(systemd.UnitParams{
		Product: "demo", ManagerPath: "/bin/morzer", BackupSchedule: "Mon *-*-* 04:00:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		if strings.HasSuffix(u.Name, ".timer") &&
			!strings.Contains(string(u.Contents), "Mon *-*-* 04:00:00") {
			t.Errorf("the operator's schedule was not used:\n%s", u.Contents)
		}
	}
}

func TestManagerPathIsAbsolute(t *testing.T) {
	if p := systemd.ManagerPath(); !filepath.IsAbs(p) && p != "morzer" {
		t.Errorf("ManagerPath returned %q; a relative path breaks when systemd runs it from /", p)
	}
}

// TestTheUpdateTimerIsGeneratedOnlyWhenAsked, and carries what makes it safe to
// leave running.
func TestTheUpdateTimerIsGeneratedOnlyWhenAsked(t *testing.T) {
	units, err := systemd.BuildUnits(systemd.UnitParams{
		Product:     "demo",
		ManagerPath: "/usr/local/bin/morzer",
		UpdateTimer: true,
	})
	if err != nil {
		t.Fatalf("BuildUnits: %v", err)
	}

	byName := map[string]ports.Unit{}
	for _, u := range units {
		byName[u.Name] = u
	}

	service, ok := byName[systemd.UpdateServiceUnitName("demo")]
	if !ok {
		t.Fatal("no update service unit")
	}
	if !strings.Contains(string(service.Contents), "update --unattended") {
		t.Errorf("the update service does not run an unattended update:\n%s", service.Contents)
	}
	// A oneshot that just refused an update must not be retried every
	// thirty seconds until the journal is the only thing on the disk.
	if strings.Contains(string(service.Contents), "Restart=") {
		t.Errorf("the update service restarts on failure:\n%s", service.Contents)
	}
	if service.Enable {
		t.Error("the update service is enabled, so it would run at every boot")
	}

	timer, ok := byName[systemd.UpdateTimerUnitName("demo")]
	if !ok {
		t.Fatal("no update timer unit")
	}
	if !timer.Enable {
		t.Error("the update timer is not enabled, so nothing would poll")
	}
	// Without a spread, every installation of a product asks the vendor's
	// registry at the same second, and the vendor discovers their customer
	// base by watching their own rate limiter.
	if !strings.Contains(string(timer.Contents), "RandomizedDelaySec=") {
		t.Errorf("the update timer has no randomised delay:\n%s", timer.Contents)
	}
	if !strings.Contains(string(timer.Contents), systemd.DefaultUpdateSchedule) {
		t.Errorf("the update timer does not carry the default schedule:\n%s", timer.Contents)
	}
}

// TestTheUpdateScheduleIsTheMaintenanceWindow.
//
// Worth an assertion because "add a maintenance window" is the obvious next
// feature request, and the answer is that OnCalendar already is one.
func TestTheUpdateScheduleIsTheMaintenanceWindow(t *testing.T) {
	units, err := systemd.BuildUnits(systemd.UnitParams{
		Product:        "demo",
		ManagerPath:    "/usr/local/bin/morzer",
		UpdateTimer:    true,
		UpdateSchedule: "Sun *-*-* 04:00:00",
	})
	if err != nil {
		t.Fatalf("BuildUnits: %v", err)
	}
	for _, u := range units {
		if u.Name == systemd.UpdateTimerUnitName("demo") {
			if !strings.Contains(string(u.Contents), "OnCalendar=Sun *-*-* 04:00:00") {
				t.Errorf("the operator's window is not in the timer:\n%s", u.Contents)
			}
			return
		}
	}
	t.Fatal("no update timer unit")
}

// TestTheFleetTimerIsGeneratedOnlyWhenThereIsSomewhereToPublish.
//
// RFC 0026 P4, the last phase of that design and last on purpose: a scheduled
// publisher built before the payload was stable would have put badly-shaped
// objects in twelve buckets, and objects in buckets are the one thing this
// design cannot recall.
func TestTheFleetTimerIsGeneratedOnlyWhenThereIsSomewhereToPublish(t *testing.T) {
	units, err := systemd.BuildUnits(systemd.UnitParams{
		Product:     "demo",
		ManagerPath: "/usr/local/bin/morzer",
		ConfigPath:  "/etc/demo/installation.yaml",
		FleetTimer:  true,
	})
	if err != nil {
		t.Fatalf("BuildUnits: %v", err)
	}

	byName := map[string]ports.Unit{}
	for _, u := range units {
		byName[u.Name] = u
	}

	service, ok := byName[systemd.FleetServiceUnitName("demo")]
	if !ok {
		t.Fatal("no fleet service unit")
	}
	if !strings.Contains(string(service.Contents), "fleet publish") {
		t.Errorf("the fleet service does not publish a row:\n%s", service.Contents)
	}
	if !strings.Contains(string(service.Contents), "--config /etc/demo/installation.yaml") {
		t.Errorf("the fleet service does not name the installation it publishes:\n%s",
			service.Contents)
	}
	// A publish that failed is a gap in a view whose subject is fine, and
	// the next tick carries the current truth. Restarting would retry the
	// old one thirty seconds later.
	if strings.Contains(string(service.Contents), "Restart=") {
		t.Errorf("the fleet service restarts on failure:\n%s", service.Contents)
	}
	if service.Enable {
		t.Error("the oneshot fleet service is enabled, so it would run at every boot")
	}
	// After the product's own service: health counts come from the runtime,
	// and "0/3 up" thirty seconds into a boot is a true statement about a
	// moment nobody wants a fleet screen showing.
	if !strings.Contains(string(service.Contents), "After=network-online.target demo.service") {
		t.Errorf("the fleet service does not wait for the deployment:\n%s", service.Contents)
	}

	timer, ok := byName[systemd.FleetTimerUnitName("demo")]
	if !ok {
		t.Fatal("no fleet timer unit")
	}
	if !timer.Enable {
		t.Error("the fleet timer is not enabled, so nothing would publish")
	}
	if !strings.Contains(string(timer.Contents), systemd.DefaultFleetSchedule) {
		t.Errorf("the fleet timer carries no schedule:\n%s", timer.Contents)
	}
	// Twelve machines sharing one prefix must not all write at the same
	// second, and a machine that was off should publish once at boot rather
	// than wait for the next hour.
	if !strings.Contains(string(timer.Contents), "RandomizedDelaySec=") {
		t.Errorf("the fleet timer has no randomised delay:\n%s", timer.Contents)
	}
	if !strings.Contains(string(timer.Contents), "Persistent=true") {
		t.Errorf("the fleet timer does not catch up a missed run:\n%s", timer.Contents)
	}
}

// The fleet schedule is more frequent than the others, and that is a decision.
//
// A row's only value is its age. `fleet ls` calls one stale after a day by
// default, so a publisher on the daily schedule the other timers use would sit
// at the threshold and report healthy machines as stale whenever jitter went
// the wrong way.
func TestTheFleetTimerRunsMoreOftenThanTheBackupOne(t *testing.T) {
	if systemd.DefaultFleetSchedule == systemd.DefaultBackupSchedule {
		t.Error("the fleet row is published on the backup's daily schedule, " +
			"which is the staleness threshold it is read against")
	}
	if !strings.Contains(systemd.DefaultFleetSchedule, "*:") {
		t.Errorf("the default fleet schedule %q is not sub-daily",
			systemd.DefaultFleetSchedule)
	}
}

// Removal walks the superset, so a unit this installation stopped generating is
// still taken away.
//
// The property that lets a machine which once had a target stop having a timer.
// A list narrowed to what the *current* configuration generates would leave the
// orphan running -- publishing on a schedule to a target its operator removed.
func TestUnitNamesIsTheSupersetIncludingTheFleetPair(t *testing.T) {
	names := map[string]bool{}
	for _, n := range systemd.UnitNames("demo") {
		names[n] = true
	}
	for _, want := range []string{
		systemd.ServiceUnitName("demo"),
		systemd.BackupServiceUnitName("demo"),
		systemd.BackupTimerUnitName("demo"),
		systemd.UpdateServiceUnitName("demo"),
		systemd.UpdateTimerUnitName("demo"),
		systemd.FleetServiceUnitName("demo"),
		systemd.FleetTimerUnitName("demo"),
	} {
		if !names[want] {
			t.Errorf("%s is not in the removal set, so an orphan of it would keep running", want)
		}
	}
}

// A reload failure must not strand a newly written unit as never-enabled.
//
// The write succeeds, the reload fails, and the call returns an error. On the
// retry the file exists, so `EnableNew` computes it as not fresh and skips it
// -- and the timer the operator just configured is installed, wanted, and off,
// until somebody runs a repair. Before the scope existed the retry re-enabled
// everything and this could not happen.
func TestAFailedReloadDoesNotStrandANewUnitDisabled(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	units := []ports.Unit{{Name: "demo-update.timer", Contents: []byte("x"), Enable: true}}

	// The failing run. A separate runner per attempt, because a scripted
	// rule cannot be withdrawn -- and two runners over one unit directory is
	// exactly the situation: the transient failure clears, the files it
	// wrote do not.
	failing := exec.NewScripted()
	failing.OnError("daemon-reload", &exec.ExitError{
		Argv: []string{"systemctl", "daemon-reload"}, ExitCode: 1,
		Stderr: "Failed to reload daemon",
	})
	first := systemd.New(failing, systemd.WithUnitDir(dir), systemd.WithSystemctl("/usr/bin/systemctl"))
	if err := first.InstallUnits(ctx, units, ports.EnableNew); err == nil {
		t.Fatal("a failed daemon-reload was reported as success")
	}

	// The operator runs the command again once the machine is healthy.
	healthy := exec.NewScripted()
	second := systemd.New(healthy, systemd.WithUnitDir(dir), systemd.WithSystemctl("/usr/bin/systemctl"))
	if err := second.InstallUnits(ctx, units, ports.EnableNew); err != nil {
		t.Fatalf("retry: %v", err)
	}

	if !healthy.Ran("enable demo-update.timer") {
		t.Errorf("the retry left the new timer installed and switched off, "+
			"so re-running does not converge:\n%s", healthy.CommandLines())
	}
}

// The rollback removes only what the call created.
//
// A unit whose file was already there is one the machine had before this call,
// and its contents are overwritten either way -- deleting it would turn a
// failed reload into a machine missing the units it had a moment ago, which is
// a worse outcome than the one the rollback exists to prevent.
func TestARollbackLeavesTheUnitsThatWereAlreadyThere(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	existing := []ports.Unit{{Name: "demo.service", Contents: []byte("old"), Enable: true}}

	healthy := exec.NewScripted()
	first := systemd.New(healthy, systemd.WithUnitDir(dir), systemd.WithSystemctl("/usr/bin/systemctl"))
	if err := first.InstallUnits(ctx, existing, ports.EnableNew); err != nil {
		t.Fatal(err)
	}

	failing := exec.NewScripted()
	failing.OnError("daemon-reload", &exec.ExitError{
		Argv: []string{"systemctl", "daemon-reload"}, ExitCode: 1,
	})
	second := systemd.New(failing, systemd.WithUnitDir(dir), systemd.WithSystemctl("/usr/bin/systemctl"))
	if err := second.InstallUnits(ctx, append(existing,
		ports.Unit{Name: "demo-update.timer", Contents: []byte("new"), Enable: true},
	), ports.EnableNew); err == nil {
		t.Fatal("a failed daemon-reload was reported as success")
	}

	if _, err := os.Stat(filepath.Join(dir, "demo.service")); err != nil {
		t.Errorf("the rollback removed a unit the machine already had: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "demo-update.timer")); !os.IsNotExist(err) {
		t.Errorf("the unit this call created survived the rollback: %v", err)
	}
	// And it did not disable anything, because nothing was enabled before
	// the reload failed.
	if failing.Ran("disable") {
		t.Errorf("a rollback before any enable still disabled a unit:\n%s",
			failing.CommandLines())
	}
}

// An enable that fails part-way leaves nothing half-enabled.
func TestARollbackDisablesWhatItHadAlreadyEnabled(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewScripted()
	runner.OnError("enable demo-backup.timer", &exec.ExitError{
		Argv: []string{"systemctl", "enable", "demo-backup.timer"}, ExitCode: 1,
	})
	s := systemd.New(runner, systemd.WithUnitDir(t.TempDir()),
		systemd.WithSystemctl("/usr/bin/systemctl"))

	err := s.InstallUnits(ctx, []ports.Unit{
		{Name: "demo.service", Contents: []byte("x"), Enable: true},
		{Name: "demo-backup.timer", Contents: []byte("y"), Enable: true},
	}, ports.EnableNew)
	if err == nil {
		t.Fatal("a failed enable was reported as success")
	}

	if !runner.Ran("disable demo.service") {
		t.Errorf("the unit enabled before the failure was left enabled, with its "+
			"file removed:\n%s", runner.CommandLines())
	}
}
