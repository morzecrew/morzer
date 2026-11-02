package ops

import (
	"context"
	"errors"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
	"github.com/morzecrew/morzer/internal/ports"
)

// vendorChecks are the checks the release ships (RFC 0045), each run as a
// bundle executable under the hook ABI's `preflight` phase. refuse says whether
// a fatal one may refuse here: not at boot, in `start` or in `rollback`, which
// bring back a release that already ran (decision 4).
func (d *Deps) vendorChecks(rel domain.Release, env ports.HookEnv, refuse bool) []preflight.Check {
	names := rel.Manifest.PreflightNames()
	checks := make([]preflight.Check, 0, len(names))
	for _, name := range names {
		spec := rel.Manifest.Preflight[name]
		checks = append(checks, preflight.Check{
			ID:          "vendor." + name,
			Category:    preflight.CategoryVendor,
			Description: spec.Description,
			Fatal:       spec.Fatal && refuse,
			Run: func(ctx context.Context) events.CheckResult {
				return d.runVendorCheck(ctx, rel, spec, env)
			},
		})
	}
	return checks
}

// preflightEnv is what a check is told (decision 10): the hook ABI's
// environment without the rendered secrets, the configuration file or the
// backup directory, which the step runs before, and never a dry run.
func (d *Deps) preflightEnv(
	inst domain.Installation, rel domain.Release, previous domain.Version, opID string, opType domain.OperationType,
) ports.HookEnv {
	env := d.hookEnv(inst, rel, previous, opID, opType, ports.PhasePreflight, false)
	env.SecretsDir, env.ConfigFile, env.BackupDir = "", "", ""
	return env
}

// runVendorCheck maps a check's run onto a result (§5.2): exit 0 passes, exit 2
// passes as not applicable, and anything else fails, quoting what the check
// said.
func (d *Deps) runVendorCheck(
	ctx context.Context, rel domain.Release, spec domain.PreflightCheck, env ports.HookEnv,
) events.CheckResult {
	timeout := spec.EffectiveTimeout()
	outcome, err := d.Hooks.Run(ctx, rel, spec.Command, env, timeout)
	message := strings.TrimSpace(outcome.Result.Message)
	switch {
	case err == nil && outcome.Skipped:
		if message == "" {
			return preflight.OK("not applicable here")
		}
		return preflight.OK("not applicable here: %s", message)
	case err == nil:
		if message == "" {
			return preflight.OK("passed")
		}
		return preflight.OK("%s", message)
	case errors.Is(err, context.DeadlineExceeded):
		return preflight.Fail(spec.Remedy, "did not finish within %s", timeout)
	case outcome.ExitCode > 0 && outcome.ExitCode != ports.HookExitSkipped:
		if message == "" {
			message = domain.LastLines(outcome.Stderr, 3)
		}
		if message == "" {
			message = domain.LastLines(outcome.Stdout, 3)
		}
		if message == "" {
			return preflight.Fail(spec.Remedy, "exited with code %d", outcome.ExitCode)
		}
		return preflight.Fail(spec.Remedy, "%s", message)
	default:
		return preflight.Fail(spec.Remedy, "%s", domain.AsError(err).Message)
	}
}
