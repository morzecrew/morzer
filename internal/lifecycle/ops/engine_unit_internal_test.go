package ops

import (
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// namedEngine is a runtime that names the unit its engine runs as.
type namedEngine struct {
	ports.Runtime
	unit string
}

func (n namedEngine) EngineUnit() string { return n.unit }

// The units wait for the engine the runtime names, and for none when it names
// none: a product unit that required a unit nothing installs would never start
// (RFC 0023 row 14).
func TestTheUnitsWaitForTheEngineTheRuntimeNames(t *testing.T) {
	inst := domain.Installation{Product: "demo"}

	d := &Deps{Runtime: namedEngine{unit: "engine.service"}}
	if got := d.unitParams(inst).EngineUnit; got != "engine.service" {
		t.Errorf("the units wait for %q, want the unit the runtime names", got)
	}

	d = &Deps{Runtime: struct{ ports.Runtime }{}}
	if got := d.unitParams(inst).EngineUnit; got != "" {
		t.Errorf("a runtime that names no engine got units waiting for %q", got)
	}
}
