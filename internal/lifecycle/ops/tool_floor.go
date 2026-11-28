package ops

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
	"github.com/morzecrew/morzer/internal/ports"
)

// toolFloors is what a runtime says its tools' versions must meet, or nothing.
func toolFloors(r ports.Runtime) map[string]domain.Constraint {
	if floorer, ok := r.(ports.ToolFloorer); ok {
		return floorer.ToolFloors()
	}
	return nil
}

// toolFit looks a tool up and holds it to the runtime's floor for it. A tool
// that is missing or reports no version fails as the lookup does.
func (d *Deps) toolFit(ctx context.Context, runtime, name string, floor domain.Constraint) (ports.ToolInfo, error) {
	info, err := d.Tools.Lookup(ctx, name)
	if err != nil || floor.IsZero() || floor.Allows(info.Version) {
		return info, err
	}
	return info, domain.Preflight(domain.ErrToolIncompatible,
		"%s %s is older than the %s runtime can drive, which needs %s %s",
		name, info.Version, runtime, name, floor).
		WithHint("upgrade %s; where this machine's distribution ships none that new, "+
			"install with another runtime", name)
}

// refuseOldTools refuses the installation's runtime when one of its tools is
// older than the runtime can drive. A tool that is missing is not refused
// here: the commands that need it fail on it, and doctor says why.
func (d *Deps) refuseOldTools(ctx context.Context) error {
	if d.Tools == nil || d.Runtime == nil {
		return nil
	}
	floors := toolFloors(d.Runtime)
	for _, name := range slices.Sorted(maps.Keys(floors)) {
		if _, err := d.toolFit(ctx, d.Runtime.Name(), name, floors[name]); errors.Is(err, domain.ErrToolIncompatible) {
			return err
		}
	}
	return nil
}

// checkToolFloors reports each tool the installation's runtime holds to a
// floor, and fails while one is missing or older.
func (d *Deps) checkToolFloors() (preflight.Check, bool) {
	floors := toolFloors(d.Runtime)
	if len(floors) == 0 || d.Tools == nil {
		return preflight.Check{}, false
	}
	names := slices.Sorted(maps.Keys(floors))
	return preflight.Check{
		ID:          "runtime.tool-floor",
		Category:    preflight.CategoryRuntime,
		Description: "the runtime's tools are new enough for it",
		Fatal:       true,
		Run: func(ctx context.Context) events.CheckResult {
			have := make([]string, 0, len(names))
			for _, name := range names {
				info, err := d.toolFit(ctx, d.Runtime.Name(), name, floors[name])
				if err != nil {
					e := domain.AsError(err)
					return preflight.Fail(e.Hint, "%s", e.Message)
				}
				have = append(have, name+" "+info.Version.String())
			}
			return preflight.OK("%s", strings.Join(have, ", "))
		},
	}, true
}

// runtimeTool is preflight.Tool for a tool a runtime requires, held to the
// runtime's floor for it rather than to a release's constraint.
func (d *Deps) runtimeTool(runtime, name string, floor domain.Constraint) preflight.Check {
	check := preflight.Tool(d.Tools, name, domain.Constraint{})
	if floor.IsZero() {
		return check
	}
	check.Description += " " + floor.String()
	check.Run = func(ctx context.Context) events.CheckResult {
		info, err := d.toolFit(ctx, runtime, name, floor)
		if err != nil {
			e := domain.AsError(err)
			return preflight.Fail(e.Hint, "%s", e.Message)
		}
		return preflight.OK("%s", info.Version)
	}
	return check
}
