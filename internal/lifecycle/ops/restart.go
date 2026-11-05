package ops

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
)

// RestartOptions names the services `morzer restart` re-creates.
type RestartOptions struct {
	Options

	Services []string
}

// Restart re-creates named services from the installed release and waits for
// its health checks (RFC 0050 P1). An operation, unlike `exec`: it changes the
// deployment, so it takes the lock and is journalled. Not attested, since it
// moves neither a version nor a configuration (decision 3).
func Restart(ctx context.Context, d *Deps, opts RestartOptions) (Result, error) {
	if len(opts.Services) == 0 {
		return Result{}, domain.Usage("name the services to restart").
			WithHint("`morzer stop` and `morzer start` restart every service; `morzer apply` converges them")
	}
	// Named twice is restarted once, and said once.
	var services []string
	for _, name := range opts.Services {
		if !slices.Contains(services, name) {
			services = append(services, name)
		}
	}
	opts.Services = services

	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := refuseWhileStopped(inst, "`morzer restart`"); err != nil {
		return Result{}, err
	}
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return Result{}, err
	}
	if current.Version.IsZero() || d.neverApplied(ctx) {
		return Result{}, domain.Usage("%s has never been started, so there is nothing to restart", inst.Product).
			WithHint("`morzer apply` starts it")
	}
	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		return Result{}, err
	}
	if err := d.refuseUnstartedServices(ctx, inst, rel, opts.Services); err != nil {
		return Result{}, err
	}
	if err := d.gateUnfinished(ctx, ""); err != nil {
		return Result{}, err
	}

	opID := d.newOpID()
	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeRestart,
		Description: "restart " + strings.Join(opts.Services, ", "),
		To:          rel.Version(),
		Steps:       restartSteps(d, inst, rel, opts.Services),
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeRestart, opts.Options, func(ctx context.Context) error {
		if err := d.refuseIfStoppedNow(ctx, "`morzer restart`"); err != nil {
			return err
		}
		if err := d.gateUnfinished(ctx, ""); err != nil {
			return err
		}
		if err := d.refuseMovedRelease(ctx, current); err != nil {
			return err
		}
		var err error
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, inst.ID, nil))
		return err
	})
	d.notifyFinished(ctx, opID, domain.OpTypeRestart, result.Record, runErr)

	out := Result{Record: result.Record}
	if runErr != nil {
		return out, runErr
	}
	names := strings.Join(opts.Services, ", ")
	if opts.DryRun {
		out.Summary = fmt.Sprintf("would re-create %s from %s", names, rel.Version())
	} else {
		out.Summary = fmt.Sprintf("re-created %s from %s", names, rel.Version())
	}
	return out, nil
}

// restartSteps re-creates the services and waits for health. Both abort on
// failure: the containers they replaced are gone, and a record that read
// compensated would say something was put back.
func restartSteps(d *Deps, inst domain.Installation, rel domain.Release, services []string) []engine.Step {
	health := stepHealthChecks(d, inst, rel)
	health.OnFailure = engine.Abort
	return []engine.Step{
		{
			ID:          "recreate-services",
			Description: "re-create " + strings.Join(services, ", "),
			Idempotent:  true,
			OnFailure:   engine.Abort,
			Timeout:     10 * time.Minute,
			Execute: func(ctx context.Context, st *engine.State) error {
				// Forced, and a dependency is started if it is down but
				// never re-created (RFC 0050 decision 1).
				return d.recreate(ctx, inst, rel, services)
			},
		},
		health,
	}
}

// refuseUnstartedServices refuses, by name, a service the installed release's
// configuration does not start: an unknown name, or one behind an inactive
// profile, which `up` would start as a side effect of restarting it.
func (d *Deps) refuseUnstartedServices(
	ctx context.Context, inst domain.Installation, rel domain.Release, services []string,
) error {
	cfg, err := d.runtimeConfig(rel, inst, "")
	if err != nil {
		return err
	}
	rendered, err := d.Runtime.Validate(ctx, cfg)
	if err != nil {
		return err
	}
	for _, name := range services {
		if !slices.Contains(rendered.Services, name) {
			return domain.Usage("%s does not start a service named %q", rel.Version(), name).
				WithHint("the services it starts are %s", strings.Join(rendered.Services, ", "))
		}
	}
	return nil
}
