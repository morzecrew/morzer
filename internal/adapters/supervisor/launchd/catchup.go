package launchd

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// launchd starts a calendar job missed during sleep when the Mac wakes, but not
// one missed while it was off, which systemd's Persistent= timers catch up from
// a stamp. The manager's scheduled runs keep a stamp of their own, and the start
// at boot has this adapter run each job whose schedule passed since (RFC 0055
// §5.6).

// persistent are the jobs systemd marks Persistent=, with their stamps, in the
// order a catch-up runs them: backup and update both take the deployment lock,
// and no job waits for it, so two at once would leave one refused with exit 4.
// Metrics is not caught up, as on Linux, and sync runs on an interval.
func persistent(product string) []struct{ label, stamp string } {
	return []struct{ label, stamp string }{
		{BackupLabel(product), domain.StampBackup},
		{UpdateLabel(product), domain.StampUpdate},
		{FleetLabel(product), domain.StampFleet},
		{DoctorLabel(product), domain.StampDoctor},
	}
}

// overdueJob is a job whose schedule passed at due, after its last run.
type overdueJob struct {
	label, stamp string
	due          time.Time
}

// CatchUp starts each overdue job, and waits for it to exit before the next.
//
// Which jobs are overdue is read before any starts: a caught-up update installs
// the units again, which writes the plists a job with no stamp counts from. A
// job an operator disabled, or one launchd has not loaded, is passed over, as
// systemd starts no disabled timer and `doctor` reports an unloaded one. One
// that cannot be started, or whose exit cannot be read, is reported, and the
// rest still run, though none while another may be running.
func (s *Supervisor) CatchUp(ctx context.Context, p ports.UnitParams) ([]string, error) {
	jobs, err := s.jobs(p)
	if err != nil {
		return nil, err
	}
	calendars := make(map[string][]interval, len(jobs))
	for _, j := range jobs {
		calendars[j.label] = j.calendar
	}

	var (
		overdue  []overdueJob
		all      []string
		problems []string
	)
	now := s.now()
	for _, c := range persistent(p.Product) {
		all = append(all, c.label)
		times, wanted := calendars[c.label]
		if !wanted {
			continue
		}
		since, installed, err := s.lastRun(c.label, c.stamp)
		if err != nil {
			problems = append(problems, domain.AsError(err).Message)
			continue
		}
		if due := latest(times, now); installed && !due.IsZero() && due.After(since) {
			overdue = append(overdue, overdueJob{c.label, c.stamp, due})
		}
	}

	var ran []string
	for _, j := range overdue {
		started, err := s.catchUpOne(ctx, j, all)
		if ctx.Err() != nil {
			return ran, ctx.Err()
		}
		if err != nil {
			problems = append(problems, domain.AsError(err).Message)
		}
		if started && err == nil {
			ran = append(ran, j.label)
		}
	}
	if len(problems) > 0 {
		return ran, domain.Internal(nil, "%s", strings.Join(problems, "; "))
	}
	return ran, nil
}

// catchUpOne runs one overdue job once none of the product's persistent jobs
// runs: a schedule that fired during the start at boot may have started one,
// which holds the lock a backup or an update needs. A job whose own run reached
// the engine meanwhile has nothing left to catch up, read after the last look
// at the job, and one an operator disabled meanwhile stays off. It reports
// whether this pass started the job.
func (s *Supervisor) catchUpOne(ctx context.Context, j overdueJob, all []string) (bool, error) {
	if err := s.waitIdle(ctx, all); err != nil {
		return false, err
	}
	disabled, err := s.disabledJobs(ctx)
	if err != nil || disabled[j.label] {
		return false, err
	}
	props, loaded, err := s.print(ctx, j.label)
	if err != nil || !loaded {
		return false, err
	}
	if covered, err := s.covers(j); err != nil || covered {
		return false, err
	}
	started, err := s.runOnce(ctx, j.label, props)
	if err != nil {
		return started, err
	}
	// A run that exited before it reached the engine left its stamp as it was,
	// so the job is still due.
	if covered, err := s.covers(j); err != nil || !covered {
		if err == nil {
			err = domain.Internal(nil, "%s ran and left its stamp as it was, so it is still due", j.label)
		}
		return started, err
	}
	return started, nil
}

// covers is whether the job's stamp is at or after the time it was due.
func (s *Supervisor) covers(j overdueJob) (bool, error) {
	at, ok, err := s.stampedAt(j.stamp)
	return ok && !j.due.After(at), err
}

// lastRun is when a job's run last reached the engine: its stamp, or, before its
// first, when its plist was written, as systemd starts a persistent timer's
// stamp when the timer first starts. A job with no plist is not installed.
func (s *Supervisor) lastRun(label, stamp string) (time.Time, bool, error) {
	info, err := os.Stat(s.plistPath(label))
	switch {
	case os.IsNotExist(err):
		return time.Time{}, false, nil
	case err != nil:
		return time.Time{}, false, domain.Internal(err, "cannot read job %s", label)
	}
	if at, ok, err := s.stampedAt(stamp); err != nil || ok {
		return at, ok, err
	}
	return info.ModTime(), true, nil
}

// stampedAt is a stamp's time, and false when there is none. One that cannot be
// read is an error, not an absence: a plist older than it would start a job
// whose run already happened.
func (s *Supervisor) stampedAt(stamp string) (time.Time, bool, error) {
	switch info, err := os.Stat(s.stamp(stamp)); {
	case err == nil:
		return info.ModTime(), true, nil
	case os.IsNotExist(err):
		return time.Time{}, false, nil
	default:
		return time.Time{}, false, domain.Internal(err, "cannot read the stamp of the scheduled %s", stamp)
	}
}

// waitIdle waits until one look at every label finds none running, read from
// `launchctl print`: a job seen idle may start while another is waited for, so
// a wait starts the look again. A job whose state cannot be read may be
// running, so it is an error, and nothing is started while it lasts.
func (s *Supervisor) waitIdle(ctx context.Context, labels []string) error {
	for {
		busy := false
		for _, label := range labels {
			props, loaded, err := s.print(ctx, label)
			if err != nil {
				return err
			}
			if loaded && props["state"] == "running" {
				busy = true
				break
			}
		}
		if !busy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.poll):
		}
	}
}

// runOnce starts a loaded job and waits until launchd has run it once more and
// it is no longer running, read from `launchctl print` as a Mac printed it
// (spikes/macos-host/launchd-probe.txt). One already running is waited for. A
// run has no time limit, as no job has one; launchd has startBudget to start
// it, after `kickstart` has returned, which a Mac measured it does with the job
// already running. It reports whether it started the job.
func (s *Supervisor) runOnce(ctx context.Context, label string, props map[string]string) (bool, error) {
	runs := runCount(props)
	started := props["state"] != "running"
	if !started {
		runs--
	} else if err := s.Start(ctx, label); err != nil {
		return true, err
	}
	since := s.now()
	for {
		select {
		case <-ctx.Done():
			return started, ctx.Err()
		case <-time.After(s.poll):
		}
		props, ok, err := s.print(ctx, label)
		switch {
		case err != nil:
			return started, err
		case !ok:
			return started, domain.Internal(nil, "launchd no longer has %s", label)
		}
		n := runCount(props)
		if n > runs && props["state"] != "running" {
			return started, nil
		}
		if n <= runs && s.now().Sub(since) > s.startBudget {
			return started, domain.Internal(nil, "launchd did not start %s within %s", label, s.startBudget)
		}
	}
}

// runCount is `runs = 3`, how many times launchd has started a job.
func runCount(props map[string]string) int {
	n, _ := strconv.Atoi(props["runs"])
	return n
}
