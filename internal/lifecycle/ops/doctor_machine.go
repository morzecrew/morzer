package ops

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
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
			// An installation whose units are installed and whose
			// state will not load is the arrangement that confuses
			// everyone: systemd starts it on every boot, the manager
			// cannot tell it what to do, and nothing else says so.
			//
			// A unit count nobody could read counts as "may have
			// them". The check exists to catch units running beside
			// unreadable state, and a supervisor this process cannot
			// read is not evidence that there are none -- deciding
			// the reassuring way from a failed read is how a warning
			// disarms itself exactly on the machine that needed it.
			var broken, skipped []string
			for _, e := range entries {
				switch {
				case e.Skipped:
					// Not an installation as far as this
					// process can tell, so not something to
					// diagnose -- but worth naming, because
					// the count below would otherwise be the
					// whole answer.
					skipped = append(skipped, e.Product)
				case e.Problem != "" && (e.Units == nil || *e.Units > 0):
					broken = append(broken, e.Product)
				}
			}
			if len(broken) > 0 {
				return preflight.Warn(
					"run `morzer ls` for the reason, and `morzer --product <name> doctor` "+
						"to diagnose one of them",
					"%s: the state will not load and units may be installed",
					strings.Join(broken, ", "))
			}

			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if !e.Skipped {
					names = append(names, e.Product)
				}
			}
			if len(skipped) > 0 {
				return preflight.Warn(
					"re-run as root if any of them is an installation",
					"%d installation(s): %s — and %s could not be read by this process",
					len(names), strings.Join(names, ", "), strings.Join(skipped, ", "))
			}
			if len(names) == 0 {
				// Not a warning. `doctor` runs on a bare machine
				// -- it is half of what makes `init` diagnosable
				// -- and "there is nothing here" is the truth
				// about one, not a fault in it.
				return preflight.OK("no installations")
			}
			if len(names) == 1 {
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

			collisions := portCollisions(claims)
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

// portClaim is one installation's claim on a port, as it declares it.
type portClaim struct {
	product string
	port    domain.PortRequirement
}

// portKey is what two claims must share to meet: protocol and port.
type portKey struct {
	port     uint16
	protocol domain.PortProtocol
}

// declaredPorts maps each protocol and port to the installations that claim
// it, and where.
//
// Every installation is read through its own layout, so the parameters and
// profiles that resolve a `{{ .Parameters.http_port }}` are that installation's
// own -- which is the whole reason this cannot be answered from one manifest.
func (d *Deps) declaredPorts(ctx context.Context) (map[portKey][]portClaim, []string) {
	claims := map[portKey][]portClaim{}

	if d.StateFor == nil {
		// Unreachable: the check is only registered when the reader is
		// wired. Written as a problem rather than as an empty machine
		// anyway, because "unreachable" and "reports all clear having
		// read nothing" must never be the same code path.
		return claims, []string{"this machine's installations"}
	}
	inv, err := DiscoverProducts(d.Paths.Root())
	if err != nil {
		return claims, []string{"this machine's installations"}
	}

	// A directory nobody could open declares nothing this check can read,
	// and saying so is the honest version of "none twice".
	problems := append([]string(nil), inv.Undecidable...)
	for _, product := range inv.Products {
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
		profiles, err := scoped.readSelection(rel, inst, nil)
		if err != nil {
			problems = append(problems, product)
			continue
		}
		ports, err := rel.Manifest.ResolvePorts(params, profiles)
		if err != nil {
			problems = append(problems, product)
			continue
		}
		for _, port := range ports {
			// Deduplicated per installation: a manifest that
			// declares the same port twice is claiming it once, and
			// reporting that as a collision with itself would be a
			// warning an operator cannot act on.
			key := portKey{port.Port, port.Protocol}
			if !slices.ContainsFunc(claims[key], func(c portClaim) bool {
				return c.product == product && c.port.Address == port.Address
			}) {
				claims[key] = append(claims[key], portClaim{product, port})
			}
		}
	}
	return claims, problems
}

// portCollisions names each protocol and port two installations claim on
// overlapping addresses, with the claims that meet: "443/tcp (dotdash on
// 203.0.113.11, web on every address)" (RFC 0059 decision 4, RFC 0020
// decision 5e). Ordered by port, then protocol, so the report is stable: map
// iteration would otherwise reorder a warning between two runs of the same
// command, which is what makes a diff of two `doctor --json` outputs
// unreadable.
func portCollisions(claims map[portKey][]portClaim) []string {
	keys := slices.SortedFunc(maps.Keys(claims), func(a, b portKey) int {
		if a.port != b.port {
			return cmp.Compare(a.port, b.port)
		}
		return strings.Compare(string(a.protocol), string(b.protocol))
	})
	var out []string
	for _, key := range keys {
		var meet []string
		for _, c := range claims[key] {
			if slices.ContainsFunc(claims[key], func(o portClaim) bool {
				return o.product != c.product && o.port.Overlaps(c.port)
			}) {
				where := "every address"
				if c.port.Address.IsValid() {
					where = c.port.Address.String()
				}
				meet = append(meet, c.product+" on "+where)
			}
		}
		if len(meet) > 0 {
			out = append(out, fmt.Sprintf("%d/%s (%s)", key.port, key.protocol, strings.Join(meet, ", ")))
		}
	}
	return out
}
