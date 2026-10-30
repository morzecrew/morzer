package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
)

// A plan names what the retire step would remove from the installation its
// Check read. A dry run takes no lock, so a value can be recorded after the
// operation's first read; a plan that ran the step and named only what was
// read before would hide the one name it was about to retire.
func TestARetirePlanNamesWhatItsCheckRead(t *testing.T) {
	ctx := context.Background()
	paths := domain.PathsUnder(t.TempDir(), "demo")
	store := state.New(paths)
	if err := store.SaveInstallation(ctx, domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_test",
		Product:       "demo",
		CreatedAt:     domain.NewTime(nowForTest()),
		Parameters:    map[string]string{"legacy_flag": "on"},
	}); err != nil {
		t.Fatalf("cannot save the installation: %v", err)
	}

	bus := events.NewBus()
	var plan []events.PlanStep
	bus.SubscribeFunc(func(e events.Event) {
		if e.Kind == events.KindPlan {
			plan = e.Plan
		}
	})
	d := &Deps{Paths: paths, State: store, Bus: bus}

	op := engine.Operation{
		ID: "op_test", Type: domain.OpTypeUpdate,
		Steps: []engine.Step{stepRetireParameters(d, summarised("demo", "1.3.0"))},
	}
	if _, err := engine.New(store, bus).Run(ctx, op, engine.Options{DryRun: true}); err != nil {
		t.Fatalf("plan: %v", err)
	}

	if len(plan) != 1 || !plan[0].WillRun {
		t.Fatalf("the plan does not run the retire step: %+v", plan)
	}
	if !strings.Contains(plan[0].Description, "legacy_flag") {
		t.Errorf("the plan runs the step without naming what it retires: %q", plan[0].Description)
	}
}
