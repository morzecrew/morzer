package ops

import (
	"context"
	"errors"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
	"github.com/morzecrew/morzer/internal/ports"
)

// checkEngineScope fails a user installation whose engine is not the user's
// own (RFC 0023 row 32): there it would share a project, its containers and its
// named volumes, with the system's installation of the same product. A probe
// that cannot run warns, since the runtime checks say why the engine does not
// answer.
func (d *Deps) checkEngineScope(prober ports.EngineScopeProber) preflight.Check {
	return preflight.Check{
		ID:          "runtime.engine-scope",
		Category:    preflight.CategoryRuntime,
		Description: "the container engine of a user installation is the user's own",
		Fatal:       true,
		Run: func(ctx context.Context) events.CheckResult {
			err := prober.ProbeEngineScope(ctx)
			e := domain.AsError(err)
			switch {
			case err == nil:
				return preflight.OK("the engine is this user's own")
			case errors.Is(err, domain.ErrPreflight):
				return preflight.Fail(e.Hint, "%s", e.Message)
			}
			return preflight.Warn(e.Hint, "%s; nothing was probed", e.Message)
		},
	}
}

// engineScopeProber is the runtime's probe when this is a user installation
// and the runtime has one.
func (d *Deps) engineScopeProber() (ports.EngineScopeProber, bool) {
	prober, ok := d.Runtime.(ports.EngineScopeProber)
	return prober, ok && d.Paths.UserLayout()
}

// refuseForeignEngine refuses `init` of a user installation on an engine that
// is not the user's own, before anything is created (RFC 0023 row 32). A probe
// that cannot run does not refuse: an installation with no release needs no
// engine yet, and doctor reports it.
func (d *Deps) refuseForeignEngine(ctx context.Context) error {
	prober, ok := d.engineScopeProber()
	if !ok {
		return nil
	}
	if err := prober.ProbeEngineScope(ctx); errors.Is(err, domain.ErrPreflight) {
		return err
	}
	return nil
}
