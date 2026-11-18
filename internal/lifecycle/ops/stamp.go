package ops

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/infra/logging"
	"github.com/morzecrew/morzer/internal/ports"
)

// The scheduled runs of `backup`, `update`, `fleet publish` and `doctor` stamp
// when they reach the engine, on every platform, as systemd stamps the timers
// it catches up with Persistent= (RFC 0055 decision 29). launchd catches
// nothing up, so the start at boot has its adapter run each job whose schedule
// passed since its stamp. Linux reads none.

// scheduledRun is where a scheduled run of job begins: it asks the engine
// first, and stamps once the engine answers. A run that cannot reach it defers
// with exit 7 where the supervisor catches it up once the start at boot has
// converged, so a job that fires before a Mac's login is neither reported as a
// failure nor lost. Elsewhere it carries on, and reports what it always has
// (decision 31).
func (d *Deps) scheduledRun(ctx context.Context, job string, dryRun bool) error {
	if dryRun {
		return nil
	}
	err := d.askEngine(ctx)
	// Interrupted on any of the question's paths, whatever it answered, it is
	// an interruption, and the next boot still catches it up.
	if ctx.Err() != nil && !errors.Is(err, domain.ErrInterrupted) {
		err = domain.Interrupted("interrupted before the scheduled %s began", job)
	}
	switch _, catches := d.Supervisor.(ports.CatchUp); {
	case err == nil:
		d.stamp(job)
	case errors.Is(err, domain.ErrInterrupted):
		return err
	case catches:
		return domain.AsError(err).WithHint("Docker Desktop starts at login; the start at boot runs this once the engine answers")
	}
	return nil
}

// askEngine is the start at boot's question, for a scheduled run. One that
// cannot read what the question needs fails on its own, and stamps as every
// failure but the engine's does (§5.6).
func (d *Deps) askEngine(ctx context.Context) error {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return nil
	}
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return nil
	}
	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		return nil
	}
	cfg, err := d.runtimeConfig(rel, inst, nil)
	if err != nil {
		return nil
	}
	answer := d.engineStatus(ctx, cfg)
	switch {
	case answer == nil:
		return nil
	case errors.Is(answer, domain.ErrInterrupted):
		return answer
	}
	// The question reads the project before it reaches the engine, so a
	// project that will not load fails it with the engine up. Reading one
	// needs no engine: a project that will not load is the run's own
	// failure, and stamps. A read that timed out settles nothing.
	if _, err := d.Runtime.Validate(ctx, cfg); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return answer
}

// stampFailure stamps a scheduled run that failed, or answered, before the
// engine's question, as §5.6 has every outcome but the engine's stamp. An
// interrupted run is none of them.
func (d *Deps) stampFailure(ctx context.Context, job string, dryRun bool) {
	if !dryRun && ctx.Err() == nil {
		d.stamp(job)
	}
}

// stamp records that a scheduled run reached the engine. A stamp that cannot be
// written costs at most this run again at the next boot, so it is a warning.
func (d *Deps) stamp(job string) {
	path := d.Paths.StampFile(job)
	err := atomicfs.MkdirAll(filepath.Dir(path), 0o700)
	if err == nil {
		err = atomicfs.WriteFile(path, nil, 0o600)
	}
	if err != nil {
		d.warnf("cannot stamp the scheduled %s, so the next start at boot may run it again: %s",
			job, domain.AsError(err).Message)
	}
}

// catchUp has the supervisor run each schedule missed while the machine was
// off, once the start at boot has converged and released the lock, where the
// supervisor does not do so itself (RFC 0055 §5.6). What it cannot do is a
// warning: the start at boot succeeded, and a failed exit would have launchd
// converge again in 30 seconds and catch the same jobs up.
func (d *Deps) catchUp(ctx context.Context) string {
	c, ok := d.Supervisor.(ports.CatchUp)
	if !ok {
		return ""
	}
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		d.warnf("cannot catch up the schedules missed while this machine was off: %s", domain.AsError(err).Message)
		return ""
	}
	ran, err := c.CatchUp(ctx, d.unitParams(inst))
	if err != nil {
		d.warnf("cannot catch up every schedule missed while this machine was off: %s", domain.AsError(err).Message)
	}
	if len(ran) == 0 {
		return ""
	}
	logging.FromContext(ctx).Info("caught up the schedules missed while this machine was off", "jobs", ran)
	return "; caught up " + strings.Join(ran, ", ")
}
