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

// CatchUp starts each overdue job, and waits for it to exit before the next. A
// job an operator disabled, or one launchd has not loaded, is passed over, as
// systemd starts no disabled timer and `doctor` reports an unloaded one. One
// that cannot be started or waited for is reported, and the rest still run.
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
		ran      []string
		problems []string
		disabled map[string]bool
	)
	for _, c := range persistent(p.Product) {
		times, wanted := calendars[c.label]
		since, installed := s.lastRun(c.label, c.stamp)
		if !wanted || !installed {
			continue
		}
		if due := latest(times, s.now()); due.IsZero() || !due.After(since) {
			continue
		}
		if disabled == nil {
			if disabled, err = s.disabledJobs(ctx); err != nil {
				return ran, err
			}
		}
		if disabled[c.label] {
			continue
		}
		props, loaded, err := s.print(ctx, c.label)
		if err == nil && !loaded {
			continue
		}
		if err == nil {
			err = s.runOnce(ctx, c.label, props)
		}
		if ctx.Err() != nil {
			return ran, ctx.Err()
		}
		if err != nil {
			problems = append(problems, domain.AsError(err).Message)
			continue
		}
		ran = append(ran, c.label)
	}
	if len(problems) > 0 {
		return ran, domain.Internal(nil, "%s", strings.Join(problems, "; "))
	}
	return ran, nil
}

// lastRun is when a job's run last reached the engine: its stamp, or, before its
// first, when its plist was written, as systemd starts a persistent timer's
// stamp when the timer first starts. A job with no plist is not installed.
func (s *Supervisor) lastRun(label, stamp string) (time.Time, bool) {
	if info, err := os.Stat(s.stamp(stamp)); err == nil {
		return info.ModTime(), true
	}
	info, err := os.Stat(s.plistPath(label))
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// runOnce starts a loaded job and waits until launchd has run it once more and
// it is no longer running, read from `launchctl print` as S4 read it. One
// already running is waited for. A run has no time limit, as no job has one;
// launchd has startBudget to start it.
func (s *Supervisor) runOnce(ctx context.Context, label string, props map[string]string) error {
	runs := runCount(props)
	if props["state"] == "running" {
		runs--
	} else if err := s.Start(ctx, label); err != nil {
		return err
	}
	started := s.now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.poll):
		}
		props, ok, err := s.print(ctx, label)
		switch {
		case err != nil:
			return err
		case !ok:
			return domain.Internal(nil, "launchd no longer has %s", label)
		}
		n := runCount(props)
		if n > runs && props["state"] != "running" {
			return nil
		}
		if n <= runs && s.now().Sub(started) > s.startBudget {
			return domain.Internal(nil, "launchd did not start %s within %s", label, s.startBudget)
		}
	}
}

// runCount is `runs = 3`, how many times launchd has started a job.
func runCount(props map[string]string) int {
	n, _ := strconv.Atoi(props["runs"])
	return n
}
