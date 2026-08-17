package suite

import (
	"testing"

	"github.com/morzecrew/morzer/internal/adapters/runtime/compose"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/contract"
	"github.com/morzecrew/morzer/test/fakes"
)

// The option half of the runtime contract, against both implementations, with
// no build tag.
//
// The rest of the battery needs a daemon, so the real adapter's leg of it lives
// behind `docker` and runs in a lane that does not gate every commit. Resolving
// options needs nothing: it is a pure function of a config and the adapter's own
// defaults. Leaving it inside the tagged leg meant the one rule with two
// implementations -- the thing the battery exists to keep in step -- was checked
// against the real adapter only where Docker was present.
//
// So it is asked here as well. The tagged suite still runs it, and this costs
// milliseconds.

func TestOptionContract_Fake(t *testing.T) {
	contract.RunOptionSuite(t, func(t *testing.T) (ports.Runtime, ports.RuntimeConfig) {
		return fakes.NewRuntime(), ports.RuntimeConfig{Product: "demo"}
	})
}

// The real Compose adapter, driven by a runner that is never called: nothing in
// the option suite shells out. A scripted runner with no expectations recorded
// is therefore the honest fixture -- if resolution ever started running a
// command, this would fail rather than quietly start needing Docker.
func TestOptionContract_Compose(t *testing.T) {
	contract.RunOptionSuite(t, func(t *testing.T) (ports.Runtime, ports.RuntimeConfig) {
		rt := compose.New(infraexec.NewScripted(), compose.WithDockerBinary("/usr/bin/docker"))
		return rt, ports.RuntimeConfig{
			Product:    "demo",
			Files:      []string{"/rel/compose.yaml"},
			WorkingDir: "/rel",
		}
	})
}
