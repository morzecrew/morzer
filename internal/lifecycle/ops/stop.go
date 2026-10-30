package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
)

// stopTimeout is how long each service gets to exit before it is killed: the
// backup's quiesce gives the same, for the same reason -- a process being
// stopped on purpose should be given time to flush.
const stopTimeout = 2 * time.Minute

// maxStopReason bounds `--reason`. It is printed by `status`, `doctor` and `ls`
// on every run until `start`, so it is a line, not a note.
const maxStopReason = 200

// StopOptions are `morzer stop`'s.
type StopOptions struct {
	Options

	// Reason is why, in the operator's words. Optional.
	Reason string
}

// Stop takes the product down on purpose and keeps it down (RFC 0040).
//
// The record is written before the runtime call, so a manager killed between
// the two leaves a product marked stopped and still running -- which the next
// boot leaves alone and `status` shows -- rather than a product down with no
// record, which the next boot would start. Never Down: the point of the command
// over a hand-typed `docker compose` is that the volumes are out of reach
// (decision 1).
func Stop(ctx context.Context, d *Deps, opts StopOptions) (Result, error) {
	if strings.ContainsAny(opts.Reason, "\r\n") {
		return Result{}, domain.Usage("--reason must be a single line")
	}
	if len(opts.Reason) > maxStopReason {
		return Result{}, domain.Usage("--reason is %d characters; at most %d are kept",
			len(opts.Reason), maxStopReason)
	}
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return Result{}, err
	}
	if current.Version.IsZero() || d.neverApplied(ctx) {
		return Result{}, domain.Usage("%s has never been started, so there is nothing to stop", inst.Product).
			WithHint("`morzer apply` starts it; `morzer stop` keeps it down after that")
	}
	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		return Result{}, err
	}
	// A stop killed between its steps is finished with --resume: its record
	// is unfinished, and the gate below refuses a fresh one over it.
	var prior *domain.OperationRecord
	if opts.Resume {
		if prior, err = d.findResumable(ctx, domain.OpTypeStop); err != nil {
			return Result{}, err
		}
	}
	if err := d.gateUnfinished(ctx, excludeID(prior)); err != nil {
		return Result{}, err
	}

	// Scrubbed of the secret values as a task's arguments are, or left out
	// when nothing could scrub it: the reason is journalled and printed on
	// every `status`, and an operator pasting a credential into it is a
	// credential written down twice.
	reason := opts.Reason
	if reason != "" && !opts.DryRun {
		if d.armRedaction(ctx) {
			reason = d.Redactor.Apply(reason)
		} else {
			reason = "(not recorded: the secret values could not be loaded to scrub it)"
		}
	}

	opID := d.newOpID()
	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeStop,
		Description: "stop " + inst.Product,
		To:          rel.Version(),
		Steps:       stopSteps(d, inst, rel, opID, reason),
	}
	if reason != "" {
		op.Flags = map[string]string{"reason": reason}
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeStop, opts.Options, func(ctx context.Context) error {
		var err error
		if opts.Resume {
			if prior, err = d.refreshResumable(ctx, domain.OpTypeStop, prior); err != nil {
				return err
			}
		}
		if err := d.gateUnfinished(ctx, excludeID(prior)); err != nil {
			return err
		}
		if err := d.refuseMovedRelease(ctx, current); err != nil {
			return err
		}
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, inst.ID, prior))
		return err
	})
	d.notifyFinished(ctx, opID, domain.OpTypeStop, result.Record, runErr)

	out := Result{Record: result.Record}
	if runErr != nil {
		return out, runErr
	}
	switch {
	case opts.DryRun:
		out.Summary = "would stop " + inst.Product
	case inst.Stopped != nil:
		out.Summary = fmt.Sprintf("%s was already %s; its services are stopped",
			inst.Product, inst.Stopped.Describe())
	default:
		out.Summary = fmt.Sprintf("%s stopped; it stays down until `morzer start`", inst.Product)
	}
	return out, nil
}

func stopSteps(d *Deps, inst domain.Installation, rel domain.Release, opID, reason string) []engine.Step {
	return []engine.Step{
		{
			ID:          "record-stop",
			Description: "record the stop",
			Idempotent:  true,
			OnFailure:   engine.Compensate,
			Timeout:     time.Minute,
			// A second `stop` keeps the first one's record: the time a
			// product went down is the fact `status` and an incident
			// review need, and a later stop does not move it.
			Check: func(ctx context.Context, _ *engine.State) (bool, error) {
				now, err := d.State.LoadInstallation(ctx)
				return err == nil && now.Stopped != nil, err
			},
			Execute: func(ctx context.Context, _ *engine.State) error {
				return d.setStopped(ctx, &domain.Stop{
					At:          domain.NewTime(d.now()),
					Reason:      reason,
					OperationID: opID,
					Release:     rel.Version(),
				})
			},
			Compensate: func(ctx context.Context, _ *engine.State) error {
				return d.setStopped(ctx, nil)
			},
		},
		{
			ID:          "stop-services",
			Description: "stop every service",
			Idempotent:  true,
			OnFailure:   engine.Compensate,
			Timeout:     10 * time.Minute,
			Execute: func(ctx context.Context, _ *engine.State) error {
				cfg, err := d.runtimeConfig(rel, inst, "")
				if err != nil {
					return err
				}
				return d.Runtime.Stop(ctx, cfg, nil, stopTimeout)
			},
		},
	}
}

// Start brings a stopped product back (RFC 0040 §5.2): `apply` for the current
// release, then the stop record is cleared as the last step. Cleared only once
// the release has converged, so a `start` that fails keeps boot and the update
// timer away from a half-converged machine.
func Start(ctx context.Context, d *Deps, opts Options) (Result, error) {
	return apply(ctx, d, opts, domain.OpTypeStart)
}

// stepClearStop is start's last step.
func stepClearStop(d *Deps) engine.Step {
	return engine.Step{
		ID:          "clear-stop",
		Description: "clear the stop",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     time.Minute,
		Check: func(ctx context.Context, _ *engine.State) (bool, error) {
			now, err := d.State.LoadInstallation(ctx)
			return err == nil && now.Stopped == nil, err
		},
		Execute: func(ctx context.Context, _ *engine.State) error {
			return d.setStopped(ctx, nil)
		},
	}
}

// setStopped rewrites the installation's stop record. A read-modify-write of
// the record `config set` also writes, so it runs under the deployment lock.
func (d *Deps) setStopped(ctx context.Context, stop *domain.Stop) error {
	inst, err := d.State.LoadInstallation(ctx)
	if err != nil {
		return err
	}
	inst.Stopped = stop
	return d.State.SaveInstallation(ctx, inst)
}

// refuseWhileStopped is decisions 3 and 5: an operation that would start the
// product is refused while an operator has it stopped, rather than undoing the
// stop as a side effect.
func refuseWhileStopped(inst domain.Installation, command string) error {
	if inst.Stopped == nil {
		return nil
	}
	return domain.Usage("%s is %s, and %s would start it", inst.Product, inst.Stopped.Describe(), command).
		WithHint("`morzer start` brings it back; run %s after that", command)
}

// refuseIfStoppedNow re-reads the record under the lock: a `stop` can land
// while an operation waits for the lock with --wait.
func (d *Deps) refuseIfStoppedNow(ctx context.Context, command string) error {
	inst, err := d.State.LoadInstallation(ctx)
	if err != nil {
		return err
	}
	return refuseWhileStopped(inst, command)
}

// leftDown is boot's answer while stopped: exit 0, no runtime call, and the
// line the journal keeps for whoever wonders why the product did not come up.
func leftDown(product string, stop domain.Stop) Result {
	return Result{Summary: fmt.Sprintf("%s is %s; leaving it down", product, stop.Describe())}
}

// startSummary is apply's summary, said as a start, or saying nothing was
// stopped so a script that calls `start` twice reads why the second did less.
func startSummary(rec domain.OperationRecord, inst domain.Installation, rel domain.Release, dryRun bool) string {
	if rec.Status != domain.StatusSucceeded {
		return ""
	}
	switch {
	case dryRun:
		return fmt.Sprintf("would start %s %s", rel.Name(), rel.Version())
	case inst.Stopped == nil:
		return fmt.Sprintf("%s was not stopped; %s", inst.Product, applySummary(rec, rel, false))
	default:
		return fmt.Sprintf("%s %s started", rel.Name(), rel.Version())
	}
}
