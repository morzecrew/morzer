package ops

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// What the runtime was told, and what happens when a release changes its mind.
// RFC 0023 decision 15.

// recorded is an installation fixed to the legacy runtime with a given
// baseline. Nil options mean one created before schema 10, which is a different
// case from an empty map and the tables below rely on the distinction.
func recorded(options map[string]string) domain.Installation {
	return domain.Installation{
		SchemaVersion:  domain.InstallationSchemaVersion,
		ID:             "inst_01",
		Product:        "demo",
		Runtime:        "compose",
		RuntimeOptions: options,
	}
}

// resolvingDeps is a manager wired to a runtime that fills in its own defaults,
// which is what the compose adapter does and what the fake mirrors.
func resolvingDeps() *Deps { return &Deps{Runtime: fakes.NewRuntime()} }

// noResolver hides ports.OptionResolver from the runtime it wraps.
//
// The only honest way to model an adapter that declines, and the same shape the
// fake's own documentation prescribes for RequiredTools: embedding the
// interface satisfies ports.Runtime without carrying the optional capability
// through.
type noResolver struct{ ports.Runtime }

// decliningDeps is a manager wired to a runtime with no defaults to fill in.
func decliningDeps() *Deps { return &Deps{Runtime: noResolver{fakes.NewRuntime()}} }

func TestAChangedRuntimeOptionIsRefused(t *testing.T) {
	cases := map[string]struct {
		was, now map[string]string
		refused  bool
		names    string
	}{
		"unchanged": {
			was: map[string]string{"project": "myapp"},
			now: map[string]string{"project": "myapp"},
		},
		"nothing either way": {
			was: map[string]string{},
			now: map[string]string{},
		},
		"the value changed": {
			was: map[string]string{"project": "myapp"},
			now: map[string]string{"project": "renamed"},
			// The whole reason this check exists: under compose the
			// project prefixes every volume, so this deploys against
			// storage nothing has written to.
			refused: true,
			names:   "project",
		},
		"the option was dropped": {
			was:     map[string]string{"project": "myapp"},
			now:     map[string]string{},
			refused: true,
			names:   "project",
		},
		"an option was added": {
			// A release that adds `project` to a deployment created
			// without one is renaming it away from the product-name
			// default, which is the same rename wearing a different
			// hat.
			was:     map[string]string{},
			now:     map[string]string{"project": "myapp"},
			refused: true,
			names:   "project",
		},
		// The two halves of R-4, carried from wave 28. `demo` is this
		// installation's product, and the compose adapter resolves an
		// absent `project` to exactly that -- so neither of these changes
		// the namespace a single volume lives in, and refusing them tells
		// a vendor to put back a value that was never doing anything.
		"the release makes the adapter's own default explicit": {
			was: map[string]string{},
			now: map[string]string{"project": "demo"},
		},
		"the release drops a project that only restated the default": {
			was: map[string]string{"project": "demo"},
			now: map[string]string{},
		},
		// And the guard the wave exists to keep: the same shapes with a
		// value that is *not* the default are still renames.
		"a project that is not the default is still a rename": {
			was:     map[string]string{},
			now:     map[string]string{"project": "elsewhere"},
			refused: true,
			names:   "project",
		},
		"a key nobody understands changed": {
			// Refused, and deliberately: the manager cannot tell a
			// durable option from a cosmetic one -- only the adapter
			// knows what any of them mean -- so it treats them all as
			// durable. Refusing a harmless change costs a message.
			was:     map[string]string{"unit_prefix": "a"},
			now:     map[string]string{"unit_prefix": "b"},
			refused: true,
			names:   "unit_prefix",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := resolvingDeps().checkRuntimeOptions(recorded(tc.was), tc.now)
			if !tc.refused {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.names,
				"the refusal must name what changed, or a vendor cannot act on it")
			assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err))
		})
	}
}

// A runtime that declines ports.OptionResolver gets the comparison as it was
// before the capability existed: declared against declared.
//
// Which means it still refuses the redundant case above. That is the intended
// side to fail on -- a manager that cannot ask what a value resolves to must
// not assume two spellings mean the same thing, because assuming it wrongly is
// the data-loss direction.
func TestARuntimeThatCannotResolveFallsBackToComparingWhatWasDeclared(t *testing.T) {
	d := decliningDeps()
	_, isResolver := d.Runtime.(ports.OptionResolver)
	require.False(t, isResolver, "the fixture must stand in for a runtime with no defaults to fill in")

	// Identical declarations still agree.
	require.NoError(t, d.checkRuntimeOptions(
		recorded(map[string]string{"project": "demo"}),
		map[string]string{"project": "demo"}))

	// And the redundant one is refused, where a resolving runtime allows it.
	err := d.checkRuntimeOptions(recorded(map[string]string{}), map[string]string{"project": "demo"})
	require.Error(t, err, "without a resolver the manager cannot know `demo` was already in force")
	assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err))
}

// An unknown key is compared, not resolved away. A resolver that dropped what
// it did not understand would turn "this release changed a setting you cannot
// see" into silence -- and the manager treats every option as durable precisely
// because it cannot tell which ones are.
func TestAKeyTheRuntimeDoesNotUnderstandSurvivesResolution(t *testing.T) {
	err := resolvingDeps().checkRuntimeOptions(
		recorded(map[string]string{"unit_prefix": "a"}),
		map[string]string{"unit_prefix": "b"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unit_prefix")
}

// Every installation created before schema 10 records nothing, and there is no
// baseline to compare against. Refusing them would refuse every machine that
// upgrades to this version; adopting is what the next operation does.
func TestAnInstallationFromBeforeTheFieldIsNotRefused(t *testing.T) {
	inst := recorded(nil)
	require.NoError(t, resolvingDeps().checkRuntimeOptions(inst,
		map[string]string{"project": "anything"}))
}

// The hook ABI half: what the runtime supplies arrives namespaced, under the
// name vendors' hooks have always read.
func TestTheRuntimesHookVariablesReachTheHookEnvironment(t *testing.T) {
	d := &Deps{Runtime: fakes.NewRuntime(), Paths: domain.PathsUnder(t.TempDir(), "demo")}

	rel := domain.Release{
		Root: "/opt/demo/releases/1",
		Manifest: domain.Manifest{
			Metadata: domain.Metadata{Name: "demo"},
			Runtimes: domain.Runtimes{"compose": {
				Files:   []string{"compose.yaml"},
				Options: map[string]string{"project": "myapp"},
			}},
		},
	}
	inst := recorded(map[string]string{"project": "myapp"})

	env := d.hookEnv(inst, rel, domain.Version{}, "op_1", domain.OpTypeApply, "migrate", false)

	assert.Equal(t, "myapp", env.Extra["DEMO_COMPOSE_PROJECT"],
		"the variable is unchanged for a compose installation; only who supplies it moved")
}

// The baseline write is a read-modify-write of a record other operations also
// write, so it re-reads under the lock and yields to whatever it finds.
//
// Two things are asserted, and the second is the one a race would break: it
// writes when the field is absent, and it leaves an already-recorded value
// alone rather than replacing it with the copy this operation read minutes
// earlier.
func TestTheBaselineWriteYieldsToWhatIsAlreadyRecorded(t *testing.T) {
	ctx := context.Background()
	paths := domain.PathsUnder(t.TempDir(), "demo")
	for _, dir := range paths.ManagedDirs() {
		require.NoError(t, os.MkdirAll(dir.Path, os.FileMode(dir.Mode)))
	}
	d := &Deps{Paths: paths, State: state.New(paths)}

	inst := recorded(nil)
	require.NoError(t, d.State.SaveInstallation(ctx, inst))

	require.NoError(t, d.persistRuntimeBaseline(ctx, map[string]string{"project": "myapp"}))
	after, err := d.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Equal(t, "myapp", after.RuntimeOptions["project"], "an absent baseline is written")

	// Another operation got there first with a different answer. This one
	// must not put its own back.
	require.NoError(t, d.persistRuntimeBaseline(ctx, map[string]string{"project": "stale"}))
	after, err = d.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Equal(t, "myapp", after.RuntimeOptions["project"],
		"a recorded baseline is never overwritten by a copy read before the lock")
}
