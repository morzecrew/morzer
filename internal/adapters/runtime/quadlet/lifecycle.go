package quadlet

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// unitTimeout bounds one systemctl call that starts or stops units. A start
// waits for each container to be created, which includes pulling nothing
// (Pull has run) but may include a slow first start.
const unitTimeout = 15 * time.Minute

// Up installs the bundle, reloads the user's manager, and starts the services.
//
// A running service whose definition changed, or every named one with
// ForceRecreate, is restarted rather than started: a start of an active unit
// does nothing, and the container would go on with what it was created from.
func (r *Runtime) Up(ctx context.Context, cfg ports.RuntimeConfig, opts ports.UpOptions) error {
	b, err := checkBundle(cfg)
	if err != nil {
		return err
	}
	dir, err := r.unitDir(cfg.Product)
	if err != nil {
		return err
	}

	targets := b.started()
	if len(opts.Services) > 0 {
		if err := b.defines(opts.Services); err != nil {
			return err
		}
		targets = opts.Services
	}

	before, err := installed(dir, cfg.Product)
	if err != nil {
		return err
	}
	if err := install(dir, b, cfg.Env, installOptions{parameters: true, containers: true}); err != nil {
		return err
	}
	if opts.RemoveOrphans {
		if err := r.removeOrphans(ctx, dir, before, b); err != nil {
			return err
		}
	}
	if err := r.reload(ctx); err != nil {
		return err
	}

	var start, restart []string
	for _, service := range targets {
		u, _ := b.service(service)
		unit := u.unitName()
		if opts.ForceRecreate {
			restart = append(restart, unit)
			continue
		}
		label, running, err := r.runningDefinition(ctx, b.containerName(u))
		if err != nil {
			return err
		}
		if running && label != definition(u, cfg.Env) {
			restart = append(restart, unit)
		} else {
			start = append(start, unit)
		}
	}
	// A unit an earlier start left failed is reset first, so a start limit
	// it ran into does not refuse this one (RFC 0023 row 28).
	r.resetFailed(ctx, append(append([]string{}, start...), restart...))
	if err := r.unitCommand(ctx, "start", start); err != nil {
		return err
	}
	if err := r.unitCommand(ctx, "restart", restart); err != nil {
		return err
	}
	if opts.Wait {
		return r.waitHealthy(ctx, b, targets, opts.WaitTimeout)
	}
	return nil
}

// defines refuses a service no `.container` file of the bundle defines.
func (b bundle) defines(services []string) error {
	for _, s := range services {
		if _, ok := b.service(s); !ok {
			return domain.ValidationError(nil, "no Quadlet file defines the service %q", s).
				WithHint("the services are the .container files, named %s-<service>.container", b.product)
		}
	}
	return nil
}

// removeOrphans stops and deletes the files an earlier release installed and
// this one no longer declares.
func (r *Runtime) removeOrphans(ctx context.Context, dir string, before, now bundle) error {
	var units []string
	var gone []unitFile
	for _, u := range before.units {
		if _, ok := now.file(u.name); !ok {
			units = append(units, u.unitName())
			gone = append(gone, u)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	if err := r.unitCommand(ctx, "stop", units); err != nil {
		return err
	}
	for _, u := range gone {
		if err := remove(dir, u); err != nil {
			return domain.RuntimeError(err, "cannot remove %s, which the release no longer declares", u.name)
		}
	}
	return nil
}

// reload has the user's manager run the generator again over the installed
// files.
func (r *Runtime) reload(ctx context.Context) error {
	if _, err := r.systemctlUser(ctx, 2*time.Minute, "daemon-reload"); err != nil {
		return wrapExit(err, "cannot reload the user's systemd manager",
			"run `systemctl --user daemon-reload` to see why, and check that the user's manager is running")
	}
	return nil
}

// resetFailed clears a failed state so a start is not refused for one. It is
// best effort: a unit that never failed has nothing to reset.
func (r *Runtime) resetFailed(ctx context.Context, units []string) {
	if len(units) == 0 {
		return
	}
	_, _ = r.systemctlUser(ctx, time.Minute, append([]string{"reset-failed"}, units...)...)
}

// unitCommand runs one systemctl verb over units, and names the first that
// failed when it fails.
func (r *Runtime) unitCommand(ctx context.Context, verb string, units []string) error {
	if len(units) == 0 {
		return nil
	}
	res, err := r.systemctlUser(ctx, unitTimeout, append([]string{verb}, units...)...)
	if err == nil {
		return nil
	}
	detail := firstLine(res.Stderr)
	var exitErr *exec.ExitError
	if asExit(err, &exitErr) {
		detail = firstLine(res.Stderr + exitErr.Stderr)
	}
	return wrapExit(err, fmt.Sprintf("cannot %s %s: %s", verb, strings.Join(units, ", "), detail),
		"run `journalctl --user -u "+units[0]+"` for what the unit wrote")
}

// inspected is the part of `podman inspect` the adapter reads.
type inspected struct {
	Name  string `json:"Name"`
	Image string `json:"ImageName"`
	State struct {
		Status   string `json:"Status"`
		ExitCode int    `json:"ExitCode"`
		Health   *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// inspect reads containers by name. A name with no container is left out
// rather than failing the read: a stopped Quadlet unit has none.
func (r *Runtime) inspect(ctx context.Context, names []string) (map[string]inspected, error) {
	out := map[string]inspected{}
	if len(names) == 0 {
		return out, nil
	}
	res, err := r.podmanRun(ctx, time.Minute, "ps", "--all", "--format", "{{.Names}}")
	if err != nil {
		return nil, wrapExit(err, "cannot list this user's containers", "check that Podman runs for this user")
	}
	exists := map[string]bool{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		exists[strings.TrimSpace(line)] = true
	}
	var present []string
	for _, n := range names {
		if exists[n] {
			present = append(present, n)
		}
	}
	if len(present) == 0 {
		return out, nil
	}
	res, err = r.podmanRun(ctx, time.Minute, append([]string{"inspect", "--type", "container"}, present...)...)
	if err != nil {
		var exitErr *exec.ExitError
		// One removed between the listing and the read is a stopped unit,
		// and the others are still worth reading; podman then exits 125
		// with what it found on stdout.
		if !asExit(err, &exitErr) || strings.TrimSpace(res.Stdout) == "" {
			return nil, wrapExit(err, "cannot inspect the containers", "")
		}
	}
	var all []inspected
	if err := json.Unmarshal([]byte(res.Stdout), &all); err != nil {
		return nil, domain.RuntimeError(err, "cannot read `podman inspect`")
	}
	for _, c := range all {
		out[strings.TrimPrefix(c.Name, "/")] = c
	}
	return out, nil
}

// runningDefinition reads a running container's definition label.
func (r *Runtime) runningDefinition(ctx context.Context, name string) (label string, running bool, err error) {
	found, err := r.inspect(ctx, []string{name})
	if err != nil {
		return "", false, err
	}
	c, ok := found[name]
	if !ok || c.State.Status != ports.StateRunning {
		return "", false, nil
	}
	return c.Config.Labels[definitionLabel], true, nil
}

// waitHealthy waits until every target's container runs, and is healthy where
// it has a healthcheck. One that reports unhealthy, or a unit that failed,
// ends the wait at once.
func (r *Runtime) waitHealthy(ctx context.Context, b bundle, targets []string, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var names []string
	for _, s := range targets {
		u, _ := b.service(s)
		if !u.oneshot() {
			names = append(names, b.containerName(u))
		}
	}
	for {
		found, err := r.inspect(ctx, names)
		if err != nil {
			return err
		}
		pending := ""
		for _, n := range names {
			c, ok := found[n]
			switch {
			case !ok || c.State.Status != ports.StateRunning:
				pending = n
			case c.State.Health != nil && c.State.Health.Status == "unhealthy":
				return domain.RuntimeError(nil, "%s is unhealthy", n).
					WithHint("run `podman healthcheck run %s` and `journalctl --user -t %s` to see why", n, n)
			case c.State.Health != nil && c.State.Health.Status == "starting":
				pending = n
			}
		}
		if pending == "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return domain.RuntimeError(ctx.Err(), "%s did not become ready in time", pending)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Down stops the installation's units and removes them. Volumes are removed
// only with Volumes set, which the CLI asks for behind a confirmation.
func (r *Runtime) Down(ctx context.Context, cfg ports.RuntimeConfig, opts ports.DownOptions) error {
	dir, err := r.unitDir(cfg.Product)
	if err != nil {
		return err
	}
	b, err := installed(dir, cfg.Product)
	if err != nil {
		return err
	}
	var units []string
	for _, u := range b.units {
		units = append(units, u.unitName())
	}
	if err := r.unitCommand(ctx, "stop", units); err != nil {
		return err
	}
	if opts.Volumes {
		// The volumes the configuration declares too: a Down without
		// volumes removes the units, and the volumes would then be
		// named by nothing installed.
		if declared, err := checkBundle(cfg); err == nil {
			b.units = append(b.units, declared.units...)
		}
		if err := r.removeVolumes(ctx, b); err != nil {
			return err
		}
	}
	for _, n := range b.networks() {
		r.removeNetwork(ctx, n)
	}
	if err := removeAll(dir); err != nil {
		return domain.RuntimeError(err, "cannot remove the unit directory %s", dir)
	}
	if err := r.reload(ctx); err != nil {
		return err
	}
	// A unit removed while failed stays listed as failed until reset.
	r.resetFailed(ctx, units)
	return nil
}

// Stop halts services without removing their units. Quadlet's stop removes a
// service's container, and Start runs a new one from the same unit and the
// same parameter copy (RFC 0023 row 29). The timeout is the units' own stop
// timeout, which the vendor sets with StopTimeout=; this bounds the call.
func (r *Runtime) Stop(ctx context.Context, cfg ports.RuntimeConfig, services []string, timeout time.Duration) error {
	units, err := r.installedUnits(cfg, services)
	if err != nil {
		return err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout+time.Minute)
		defer cancel()
	}
	return r.unitCommand(ctx, "stop", units)
}

// Start starts services Stop halted, from the installed units and the
// parameter copy, with no reload and nothing written: cfg's values are not
// the ones the services were created with, and are not read.
func (r *Runtime) Start(ctx context.Context, cfg ports.RuntimeConfig, services []string) error {
	units, err := r.installedUnits(cfg, services)
	if err != nil {
		return err
	}
	r.resetFailed(ctx, units)
	return r.unitCommand(ctx, "start", units)
}

// Restart restarts services from their installed units. A one-shot the
// configuration defines is not running, so there is nothing to restart.
func (r *Runtime) Restart(ctx context.Context, cfg ports.RuntimeConfig, services []string) error {
	b, err := checkBundle(cfg)
	if err != nil {
		return err
	}
	if err := b.defines(services); err != nil {
		return err
	}
	units, err := r.installedUnits(cfg, services)
	if err != nil {
		return err
	}
	r.resetFailed(ctx, units)
	return r.unitCommand(ctx, "restart", units)
}

// installedUnits are the units of the named services, or of every installed
// service Up starts, that are installed. A one-shot is left out: it runs to
// completion and has nothing to stop, start or restart.
func (r *Runtime) installedUnits(cfg ports.RuntimeConfig, services []string) ([]string, error) {
	dir, err := r.unitDir(cfg.Product)
	if err != nil {
		return nil, err
	}
	b, err := installed(dir, cfg.Product)
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		services = b.started()
	}
	var units []string
	for _, s := range services {
		if u, ok := b.service(s); ok && !u.oneshot() {
			units = append(units, u.unitName())
		}
	}
	return units, nil
}
