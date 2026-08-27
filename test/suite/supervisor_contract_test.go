package suite

import (
	"maps"
	"slices"
	"testing"

	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/test/contract"
	"github.com/morzecrew/morzer/test/fakes"
)

// Both ports.Supervisor implementations against the same enablement rules.
//
// RFC 0030 row 1 is a rule about what an install may do to a unit that already
// exists, and the suite asserts it through a fake. The fake used to write
// `Enabled: u.Enable` on every install, so it disagreed with the adapter in
// both directions -- and in the direction that matters, it re-enabled a unit
// the operator had switched off, which is the exact behaviour the row exists to
// remove. Nothing above the port could have noticed.

func TestTheSystemdAdapterHonoursTheEnablementRules(t *testing.T) {
	contract.RunSupervisorSuite(t, "systemd", func(t *testing.T) contract.SupervisorHarness {
		runner := exec.NewScripted()
		s := systemd.New(runner, systemd.WithUnitDir(t.TempDir()))
		return contract.SupervisorHarness{
			Supervisor: s,
			// Replayed from argv, because a scripted runner holds no
			// state and a host with no live daemon cannot be asked.
			// `enable` adds, `disable` removes, and nothing else in
			// this adapter touches enablement -- which is itself a
			// claim the replay would break if it stopped being true.
			Enabled: func() []string {
				on := map[string]bool{}
				for _, c := range runner.Calls() {
					if len(c.Argv) < 3 {
						continue
					}
					switch c.Argv[1] {
					case "enable":
						on[c.Argv[2]] = true
					case "disable":
						delete(on, c.Argv[2])
					}
				}
				out := slices.Collect(maps.Keys(on))
				return out
			},
		}
	})
}

func TestTheFakeSupervisorHonoursTheEnablementRules(t *testing.T) {
	contract.RunSupervisorSuite(t, "fake", func(t *testing.T) contract.SupervisorHarness {
		s := fakes.NewSupervisor()
		return contract.SupervisorHarness{
			Supervisor: s,
			Enabled: func() []string {
				var out []string
				for name, state := range s.States {
					if state.Enabled {
						out = append(out, name)
					}
				}
				return out
			},
		}
	})
}
