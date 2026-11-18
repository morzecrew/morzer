package launchd_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/supervisor/launchd"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// The catch-up of a schedule missed while the Mac was off (RFC 0055 §5.6),
// against a launchctl that runs what it is asked to: a kickstarted job reads as
// running for a few polls, then as exited with one more run, each in the layout
// a Mac printed. A job that runs stamps when it starts, as a scheduled run does
// once it reaches the engine.

type launchctlSim struct {
	mu sync.Mutex

	captures map[string]string

	runs map[string]int
	// running is a job launchd runs until a print has seen it exit, and busy
	// how many more prints read it as running.
	running map[string]bool
	busy    map[string]int
	runFor  int

	disabled  []string
	unloaded  map[string]bool
	failStart map[string]bool
	// neverStarts is a job launchd accepts a kickstart for and does not run,
	// noStamp one that runs and exits before it reaches the engine, and
	// stampOnExit one that stamps only once it exits.
	neverStarts map[string]bool
	noStamp     map[string]bool
	stampOnExit map[string]bool
	// printFails is how many more times launchd cannot be asked about a job.
	printFails map[string]int
	// ignoresCancel is a launchctl that answers after the catch-up was
	// cancelled, so a wait that ignored the cancellation would show.
	ignoresCancel    bool
	polledAfterAbort int

	// stamp is where each job stamps, and now when.
	stamp func(label string) string
	now   func() time.Time
	// onKickstart and onPrint are what else happens then; a print right after
	// print-disabled is the catch-up's last look before a start.
	onKickstart func(label string)
	onPrint     func(label string, lastLook bool)
	last        string

	kickstarted []string
	overlapped  bool
}

func newLaunchctlSim(captures map[string]string) *launchctlSim {
	return &launchctlSim{
		captures: captures,
		runs:     map[string]int{}, running: map[string]bool{}, busy: map[string]int{}, runFor: 2,
		unloaded: map[string]bool{}, failStart: map[string]bool{}, neverStarts: map[string]bool{},
		noStamp: map[string]bool{}, stampOnExit: map[string]bool{}, printFails: map[string]int{},
	}
}

// startsItself is a job launchd starts on its own schedule.
func (l *launchctlSim) startsItself(label string) {
	l.runs[label]++
	l.running[label], l.busy[label] = true, l.runFor
}

func (l *launchctlSim) Run(ctx context.Context, cmd exec.Command) (exec.Result, error) {
	if err := ctx.Err(); err != nil && !l.ignoresCancel {
		return exec.Result{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	last := l.last
	l.last = cmd.Argv[1]
	fail := func(code int, stderr string) (exec.Result, error) {
		return exec.Result{ExitCode: code, Stderr: stderr}, &exec.ExitError{Argv: cmd.Argv, ExitCode: code, Stderr: stderr}
	}
	switch cmd.Argv[1] {
	case "print-disabled":
		out := "\tdisabled services = {\n"
		for _, label := range l.disabled {
			out += fmt.Sprintf("\t\t%q => disabled\n", label)
		}
		return exec.Result{Stdout: out + "\t}\n"}, nil
	case "print":
		label := strings.TrimPrefix(cmd.Argv[2], "system/")
		if ctx.Err() != nil {
			if l.polledAfterAbort++; l.polledAfterAbort > 3 {
				return exec.Result{}, errors.New("launchctl was polled after the catch-up was cancelled")
			}
		}
		if l.onPrint != nil {
			l.onPrint(label, last == "print-disabled")
		}
		if l.printFails[label] > 0 {
			l.printFails[label]--
			return exec.Result{}, errors.New("launchctl: connection interrupted")
		}
		if l.unloaded[label] {
			return fail(113, "Could not find service \""+label+"\" in domain for system")
		}
		capture := "print-exited.txt"
		switch {
		case l.running[label] && l.busy[label] > 0:
			l.busy[label]--
			capture = "print-running.txt"
		case l.running[label]:
			l.running[label] = false
			if l.stampOnExit[label] {
				touch(l.stamp(label), l.now())
			}
		case l.runs[label] == 0:
			capture = "print-never-run.txt"
		}
		return exec.Result{Stdout: printedOnAMac(l.captures[capture], label, l.runs[label])}, nil
	case "kickstart":
		label := strings.TrimPrefix(cmd.Argv[2], "system/")
		l.kickstarted = append(l.kickstarted, label)
		for other, running := range l.running {
			if other != label && running {
				l.overlapped = true
			}
		}
		if l.failStart[label] {
			return fail(5, "Input/output error")
		}
		if !l.neverStarts[label] {
			l.startsItself(label)
			if !l.noStamp[label] && !l.stampOnExit[label] {
				// A run that cannot stamp is one that did not, as the
				// manager's warning has it.
				_ = touchErr(l.stamp(label), l.now())
			}
		}
		if l.onKickstart != nil {
			l.onKickstart(label)
		}
	}
	return exec.Result{}, nil
}

func (l *launchctlSim) Look(name string) (string, error) { return name, nil }

func touch(path string, at time.Time) {
	if err := touchErr(path, at); err != nil {
		panic(err)
	}
}

func touchErr(path string, at time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return err
	}
	return os.Chtimes(path, at, at)
}

// The clock the tests run at: a Wednesday, so a daily backup at 02:30 last ran
// this morning, and a weekend update on Sunday.
var catchUpNow = time.Date(2026, time.November, 18, 10, 0, 0, 0, time.UTC)

type catchUpFixture struct {
	sim     *launchctlSim
	s       *launchd.Supervisor
	paths   domain.Paths
	daemons string
}

// stampOf is each persistent job's stamp.
var stampOf = map[string]string{
	launchd.BackupLabel("demo"): domain.StampBackup,
	launchd.UpdateLabel("demo"): domain.StampUpdate,
	launchd.FleetLabel("demo"):  domain.StampFleet,
	launchd.DoctorLabel("demo"): domain.StampDoctor,
}

func newCatchUpFixture(t *testing.T, now func() time.Time) catchUpFixture {
	t.Helper()
	root := t.TempDir()
	f := catchUpFixture{
		sim:     newLaunchctlSim(macCaptures(t)),
		paths:   domain.PathsUnder(root, "demo"),
		daemons: filepath.Join(root, "Library", "LaunchDaemons"),
	}
	f.sim.now = now
	f.sim.stamp = func(label string) string { return f.paths.StampFile(stampOf[label]) }
	f.s = launchd.New(f.sim, f.paths,
		launchd.WithDaemonDir(f.daemons),
		launchd.WithNewsyslogDir(filepath.Join(root, "etc", "newsyslog.d")),
		launchd.WithRoot(func() bool { return true }),
		launchd.WithClock(now, time.Millisecond, time.Minute))
	// Every job is installed, last written a month before.
	require.NoError(t, os.MkdirAll(f.daemons, 0o755))
	for _, label := range launchd.Labels("demo") {
		f.written(t, label, catchUpNow.AddDate(0, -1, 0))
	}
	return f
}

// written sets when a job's plist was last written.
func (f catchUpFixture) written(t *testing.T, label string, at time.Time) {
	t.Helper()
	path := filepath.Join(f.daemons, label+".plist")
	require.NoError(t, os.WriteFile(path, []byte("<plist/>\n"), 0o644))
	require.NoError(t, os.Chtimes(path, at, at))
}

// stamped is a scheduled run that reached the engine at the time given.
func (f catchUpFixture) stamped(job string, at time.Time) { touch(f.paths.StampFile(job), at) }

func fixedClock() time.Time { return catchUpNow }

// bounded is a catch-up's context, which fails a test that would otherwise wait
// for ever on a job that does not exit.
func bounded(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// The jobs whose schedule passed since their stamp run, one at a time in the
// fixed order, each waited for until it exits; the rest, the ones systemd does
// not catch up and a schedule switched off, do not.
func TestACatchUpRunsEachOverdueJobOnceInOrder(t *testing.T) {
	f := newCatchUpFixture(t, fixedClock)
	f.stamped(domain.StampBackup, catchUpNow.Add(-31*time.Hour))   // before this morning's 02:30
	f.stamped(domain.StampUpdate, catchUpNow.Add(-48*time.Hour))   // after Sunday's 04:00
	f.stamped(domain.StampDoctor, catchUpNow.Add(-5*24*time.Hour)) // overdue, and its schedule is off
	// No fleet stamp: its plist was written before the hour struck.
	f.written(t, launchd.FleetLabel("demo"), catchUpNow.Add(-30*time.Minute))
	params := every(t)
	params.DoctorTimer = false

	ran, err := f.s.CatchUp(bounded(t), params)

	require.NoError(t, err)
	want := []string{launchd.BackupLabel("demo"), launchd.FleetLabel("demo")}
	assert.Equal(t, want, ran)
	assert.Equal(t, want, f.sim.kickstarted)
	assert.False(t, f.sim.overlapped, "a job was started while another ran")
	for _, label := range want {
		assert.Equal(t, 1, f.sim.runs[label], label)
		assert.False(t, f.sim.running[label], "%s was not waited for until it exited", label)
	}
}

// A job with no stamp, written after its schedule last passed, has missed
// nothing: a job installed and not yet due.
func TestAJobInstalledSinceItsScheduleLastPassedIsNotCaughtUp(t *testing.T) {
	f := newCatchUpFixture(t, fixedClock)
	for _, label := range launchd.Labels("demo") {
		f.written(t, label, catchUpNow)
	}

	ran, err := f.s.CatchUp(bounded(t), every(t))

	require.NoError(t, err)
	assert.Empty(t, ran)
	assert.Empty(t, f.sim.kickstarted)
}

// A job whose plist is gone is not installed, whatever its stamp says and
// whether or not launchd still has it.
func TestAJobWhosePlistIsGoneIsNotStarted(t *testing.T) {
	f := newCatchUpFixture(t, fixedClock)
	f.stamped(domain.StampBackup, catchUpNow.AddDate(0, 0, -3))
	require.NoError(t, os.Remove(filepath.Join(f.daemons, launchd.BackupLabel("demo")+".plist")))

	ran, err := f.s.CatchUp(bounded(t), every(t))

	require.NoError(t, err)
	assert.NotContains(t, f.sim.kickstarted, launchd.BackupLabel("demo"))
	assert.NotContains(t, ran, launchd.BackupLabel("demo"))
}

// Which jobs are due is read before any runs: a caught-up update installs the
// units again, which writes every plist a job with no stamp counts from.
func TestACaughtUpUpdateRewritingThePlistsCatchesUpTheRestAllTheSame(t *testing.T) {
	f := newCatchUpFixture(t, fixedClock)
	f.stamped(domain.StampBackup, catchUpNow)
	f.sim.onKickstart = func(label string) {
		if label == launchd.UpdateLabel("demo") {
			for _, l := range launchd.Labels("demo") {
				f.written(t, l, catchUpNow)
			}
		}
	}

	ran, err := f.s.CatchUp(bounded(t), every(t))

	require.NoError(t, err)
	assert.Equal(t, []string{launchd.UpdateLabel("demo"), launchd.FleetLabel("demo"), launchd.DoctorLabel("demo")}, ran)
}

// A job its own schedule started during the start at boot is waited for before
// anything else starts, whether or not it is due: a backup holds the lock an
// update needs.
func TestAJobAlreadyRunningIsWaitedForBeforeTheNextStarts(t *testing.T) {
	f := newCatchUpFixture(t, fixedClock)
	backup := launchd.BackupLabel("demo")
	f.stamped(domain.StampBackup, catchUpNow)
	f.sim.runFor = 3
	f.sim.startsItself(backup)

	ran, err := f.s.CatchUp(bounded(t), every(t))

	require.NoError(t, err)
	assert.NotContains(t, f.sim.kickstarted, backup)
	assert.NotContains(t, ran, backup)
	assert.False(t, f.sim.running[backup], "the running backup was not waited for until it exited")
	assert.False(t, f.sim.overlapped, "a job was started while the backup ran")
	assert.Contains(t, ran, launchd.UpdateLabel("demo"))
}

// A job seen idle that its schedule starts while another is waited for is
// waited for too: the look starts again after any wait.
func TestAJobThatStartsWhileAnotherIsWaitedForIsWaitedForToo(t *testing.T) {
	f := newCatchUpFixture(t, fixedClock)
	backup, fleet := launchd.BackupLabel("demo"), launchd.FleetLabel("demo")
	f.stamped(domain.StampBackup, catchUpNow)
	f.stamped(domain.StampFleet, catchUpNow)
	f.stamped(domain.StampDoctor, catchUpNow)
	f.sim.startsItself(fleet)
	started := false
	f.sim.onPrint = func(label string, _ bool) {
		if label == fleet && !started {
			started = true
			f.sim.startsItself(backup)
		}
	}

	ran, err := f.s.CatchUp(bounded(t), every(t))

	require.NoError(t, err)
	assert.Equal(t, []string{launchd.UpdateLabel("demo")}, ran)
	assert.False(t, f.sim.overlapped, "the update was started while the backup ran")
}

// A job whose own run reached the engine while an earlier one was caught up has
// nothing left to catch up, and one an operator disabled meanwhile stays off.
func TestWhatChangedWhileAnEarlierJobRanIsReadBeforeTheNext(t *testing.T) {
	f := newCatchUpFixture(t, fixedClock)
	f.sim.onKickstart = func(label string) {
		if label == launchd.BackupLabel("demo") {
			f.stamped(domain.StampUpdate, catchUpNow)
			f.sim.disabled = []string{launchd.FleetLabel("demo")}
		}
	}

	ran, err := f.s.CatchUp(bounded(t), every(t))

	require.NoError(t, err)
	assert.Equal(t, []string{launchd.BackupLabel("demo"), launchd.DoctorLabel("demo")}, ran)
	assert.Equal(t, ran, f.sim.kickstarted)
}

// The stamp is read after the last look at a job before it starts, so a run
// that reached the engine up to that look is not started again.
func TestAJobsOwnRunStampedByTheLastLookIsNotStartedAgain(t *testing.T) {
	f := newCatchUpFixture(t, fixedClock)
	update := launchd.UpdateLabel("demo")
	f.sim.onPrint = func(label string, lastLook bool) {
		if label == update && lastLook {
			f.stamped(domain.StampUpdate, catchUpNow)
		}
	}

	ran, err := f.s.CatchUp(bounded(t), every(t))

	require.NoError(t, err)
	assert.NotContains(t, f.sim.kickstarted, update)
	assert.NotContains(t, ran, update)
}

// A job launchd started on its own just before the catch-up would have is
// waited for, and is not reported as one the catch-up ran.
func TestAJobLaunchdStartedItselfIsNotReportedAsCaughtUp(t *testing.T) {
	f := newCatchUpFixture(t, fixedClock)
	backup := launchd.BackupLabel("demo")
	f.sim.stampOnExit[backup] = true
	f.sim.onPrint = func(label string, lastLook bool) {
		if label == backup && lastLook && f.sim.runs[backup] == 0 {
			f.sim.startsItself(backup)
		}
	}

	ran, err := f.s.CatchUp(bounded(t), every(t))

	require.NoError(t, err)
	assert.NotContains(t, f.sim.kickstarted, backup)
	assert.NotContains(t, ran, backup)
	assert.False(t, f.sim.overlapped, "a job was started while the backup ran")
}

// A job an operator disabled stays off, a job launchd has not loaded is not
// started, and one that will not start is reported while the rest still run.
func TestACatchUpPassesOverWhatItMustNotStartAndCarriesOn(t *testing.T) {
	f := newCatchUpFixture(t, fixedClock)
	f.sim.failStart[launchd.BackupLabel("demo")] = true
	f.sim.disabled = []string{launchd.UpdateLabel("demo")}
	f.sim.unloaded[launchd.FleetLabel("demo")] = true

	ran, err := f.s.CatchUp(bounded(t), every(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "launchctl kickstart system/dev.morzer.demo-backup failed")
	assert.Equal(t, []string{launchd.DoctorLabel("demo")}, ran)
	assert.Equal(t, []string{launchd.BackupLabel("demo"), launchd.DoctorLabel("demo")}, f.sim.kickstarted)
}

// A run that exits before it reaches the engine left its stamp, so it is
// reported as still due rather than caught up.
func TestARunThatLeftItsStampIsNotCaughtUp(t *testing.T) {
	f := newCatchUpFixture(t, fixedClock)
	f.sim.noStamp[launchd.BackupLabel("demo")] = true

	ran, err := f.s.CatchUp(bounded(t), every(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "dev.morzer.demo-backup ran and left its stamp as it was, so it is still due")
	assert.NotContains(t, ran, launchd.BackupLabel("demo"))
	assert.Contains(t, ran, launchd.UpdateLabel("demo"))
}

// A stamp that cannot be read is not an absent one: the plist older than it
// would start a job whose run already happened.
func TestAStampThatCannotBeReadStartsNothing(t *testing.T) {
	f := newCatchUpFixture(t, fixedClock)
	stamps := filepath.Dir(f.paths.StampFile(domain.StampBackup))
	require.NoError(t, os.MkdirAll(filepath.Dir(stamps), 0o755))
	require.NoError(t, os.WriteFile(stamps, nil, 0o600)) // a file where the directory goes

	ran, err := f.s.CatchUp(bounded(t), every(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot read the stamp of the scheduled backup")
	assert.Empty(t, ran)
	assert.Empty(t, f.sim.kickstarted)
}

// A started job whose state cannot be read may still hold the lock, so nothing
// starts while that lasts; once launchd says it has exited, the rest run.
func TestNothingStartsWhileAJobsExitCannotBeRead(t *testing.T) {
	for name, tc := range map[string]struct {
		fails   int
		started []string
	}{
		"never read again": {1 << 30, []string{launchd.BackupLabel("demo")}},
		"read again": {1, []string{launchd.BackupLabel("demo"), launchd.UpdateLabel("demo"),
			launchd.FleetLabel("demo"), launchd.DoctorLabel("demo")}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCatchUpFixture(t, fixedClock)
			f.sim.onKickstart = func(label string) {
				if label == launchd.BackupLabel("demo") {
					f.sim.printFails[label] = tc.fails
				}
			}

			ran, err := f.s.CatchUp(bounded(t), every(t))

			require.Error(t, err)
			assert.Contains(t, err.Error(), "cannot ask launchd about dev.morzer.demo-backup")
			assert.NotContains(t, ran, launchd.BackupLabel("demo"))
			assert.Equal(t, tc.started, f.sim.kickstarted)
			assert.False(t, f.sim.overlapped, "a job was started while the backup ran")
		})
	}
}

// A job launchd accepts and never runs is given up on, so the start at boot
// does not wait for it forever.
func TestAJobLaunchdNeverStartsIsGivenUpOn(t *testing.T) {
	var mu sync.Mutex
	now := catchUpNow
	f := newCatchUpFixture(t, func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(10 * time.Second)
		return now
	})
	f.sim.neverStarts[launchd.BackupLabel("demo")] = true

	ran, err := f.s.CatchUp(bounded(t), every(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "launchd did not start dev.morzer.demo-backup within 1m0s")
	assert.NotContains(t, ran, launchd.BackupLabel("demo"))
	assert.Contains(t, ran, launchd.DoctorLabel("demo"), "the jobs after it did not run")
}

// A run has no time limit, so only the start at boot being stopped ends the
// wait, which it does while the job runs, without asking launchd again.
func TestACatchUpEndsWhenItsContextDoes(t *testing.T) {
	f := newCatchUpFixture(t, fixedClock)
	f.sim.runFor = 1 << 30
	f.sim.ignoresCancel = true
	ctx, cancel := context.WithCancel(bounded(t))
	defer cancel()
	f.sim.onKickstart = func(string) { cancel() }

	ran, err := f.s.CatchUp(ctx, every(t))

	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, ran)
	assert.Equal(t, []string{launchd.BackupLabel("demo")}, f.sim.kickstarted)
	assert.Zero(t, f.sim.polledAfterAbort, "the wait asked launchd after it was cancelled")
}

var _ ports.CatchUp = (*launchd.Supervisor)(nil)
