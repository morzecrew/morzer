package ops

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
)

// The two checks whose subject is the machine rather than this installation.
//
// RFC 0020 declines to isolate installations from each other -- they share a
// Docker daemon, a port space, a /run tmpfs and a disk, and pretending
// otherwise would be a container-per-installation design that changes what this
// product is. What it does instead is make the sharing legible, and this is
// where that lands: `doctor` is the command an operator runs when something is
// wrong and they cannot tell what.
//
// Both warn and neither fails. A machine with two installations is a supported
// arrangement; a `doctor` that failed on one would teach operators that a red
// doctor is normal, which costs more than these checks are worth.

// checkMachineInstallations reports what else is on this host.
func (d *Deps) checkMachineInstallations() preflight.Check {
	return preflight.Check{
		ID:          "machine.installations",
		Category:    preflight.CategoryMachine,
		Description: "the installations on this machine",
		Run: func(ctx context.Context) events.CheckResult {
			entries, err := ListInstallations(ctx, d, ListOptions{})
			if err != nil {
				return preflight.Warn(
					"`morzer ls` reads the same directory and reports what it found",
					"cannot enumerate this machine's installations: %s",
					domain.AsError(err).Message)
			}
			if len(entries) == 0 {
				// Not a warning. `doctor` runs on a bare machine --
				// it is half of what makes `init` diagnosable --
				// and "there is nothing here" is the truth about
				// one, not a fault in it.
				return preflight.OK("no installations")
			}

			// An installation whose units are installed and whose
			// state will not load is the arrangement that confuses
			// everyone: systemd starts it on every boot, the manager
			// cannot tell it what to do, and nothing else says so.
			var broken []string
			for _, e := range entries {
				if e.Problem != "" && e.Units > 0 {
					broken = append(broken, e.Product)
				}
			}
			if len(broken) > 0 {
				return preflight.Warn(
					"run `morzer ls` for the reason, and `morzer --product <name> doctor` "+
						"to diagnose one of them",
					"%s: units are installed and the state will not load",
					strings.Join(broken, ", "))
			}

			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Product)
			}
			if len(entries) == 1 {
				return preflight.OK("one installation: %s", names[0])
			}
			// Informational rather than a warning: several
			// installations on one machine is what the layout has
			// always supported, and the price of that support is
			// that a command has to be told which one it means.
			return preflight.OK("%d installations: %s — each is operated on its own terms",
				len(entries), strings.Join(names, ", "))
		},
	}
}

// checkMachinePorts warns when two installations want the same host port.
//
// They cannot both be running. Today the second one's `apply` fails inside
// Compose with a message about a port already in use, which is true and says
// nothing about the neighbour that holds it -- so an operator reads it as a
// stray process and goes looking for one with `ss -tlnp`.
//
// Read from what each release *declares* it needs (`requirements.ports`,
// resolved against that installation's own parameters), not from what is bound
// right now. A collision matters before either is started, which is exactly when
// nothing is listening and a probe would report all clear.
func (d *Deps) checkMachinePorts() preflight.Check {
	return preflight.Check{
		ID:          "machine.ports",
		Category:    preflight.CategoryMachine,
		Description: "no two installations publish the same port",
		Run: func(ctx context.Context) events.CheckResult {
			claims, problems := d.declaredPorts(ctx)

			var collisions []string
			for _, port := range sortedPorts(claims) {
				if products := claims[port]; len(products) > 1 {
					collisions = append(collisions,
						fmt.Sprintf("%d (%s)", port, strings.Join(products, ", ")))
				}
			}
			if len(collisions) > 0 {
				return preflight.Warn(
					"they cannot both run; change one release's port parameter with "+
						"`morzer --product <name> config set`, or keep only one started",
					"two installations want the same host port: %s",
					strings.Join(collisions, "; "))
			}

			// Reported rather than silently narrowing the claim. A
			// check that could not read half the machine and said
			// "no collisions" would be answering a question it did
			// not ask.
			if len(problems) > 0 {
				return preflight.Warn(
					"`morzer ls` reports what each installation's state says",
					"no collisions among the installations that could be read; "+
						"could not read %s", strings.Join(problems, ", "))
			}
			if len(claims) == 0 {
				return preflight.OK("no installation on this machine publishes a fixed port")
			}
			return preflight.OK("%d port(s) claimed, none twice", len(claims))
		},
	}
}

// declaredPorts maps each host port to the installations that want it.
//
// Every installation is read through its own layout, so the parameters that
// resolve a `{{ .Parameters.http_port }}` are that installation's own -- which
// is the whole reason this cannot be answered from one manifest.
func (d *Deps) declaredPorts(ctx context.Context) (map[int][]string, []string) {
	claims := map[int][]string{}

	if d.StateFor == nil {
		// Unreachable: the check is only registered when the reader is
		// wired. Written as a problem rather than as an empty machine
		// anyway, because "unreachable" and "reports all clear having
		// read nothing" must never be the same code path.
		return claims, []string{"this machine's installations"}
	}
	products, err := DiscoverProducts(d.Paths.Root())
	if err != nil {
		return claims, []string{"this machine's installations"}
	}

	var problems []string
	for _, product := range products {
		scoped := d.forInstallation(product)

		inst, err := scoped.State.LoadInstallation(ctx)
		if err != nil {
			problems = append(problems, product)
			continue
		}
		current, err := scoped.State.CurrentRelease(ctx)
		if err != nil {
			problems = append(problems, product)
			continue
		}
		if current.IsZero() {
			// Nothing installed, so nothing declared. Not a
			// problem: an installation waiting for its first
			// release claims no ports and collides with nobody.
			continue
		}
		rel, err := scoped.resolveCurrentRelease(ctx, current)
		if err != nil {
			problems = append(problems, product)
			continue
		}
		params, err := scoped.parameters(rel, inst)
		if err != nil {
			problems = append(problems, product)
			continue
		}
		ports, err := rel.Manifest.ResolvePorts(params)
		if err != nil {
			problems = append(problems, product)
			continue
		}
		for _, port := range ports {
			// Deduplicated per installation: a manifest that
			// declares the same port twice is claiming it once, and
			// reporting that as a collision with itself would be a
			// warning an operator cannot act on.
			if !slices.Contains(claims[port], product) {
				claims[port] = append(claims[port], product)
			}
		}
	}
	return claims, problems
}

// sortedPorts orders the claims so the report is stable. Map iteration would
// otherwise reorder a warning between two runs of the same command, which is
// what makes a diff of two `doctor --json` outputs unreadable.
func sortedPorts(claims map[int][]string) []int {
	out := make([]int, 0, len(claims))
	for port := range claims {
		out = append(out, port)
	}
	sort.Ints(out)
	return out
}
