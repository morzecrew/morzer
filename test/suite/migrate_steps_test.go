package suite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// `operations.migrate` may list several steps, run in order, before services
// start: migrations, then the application's own bootstrap. Asked for by a
// bundle whose first install needs both, and whose bootstrap must not run
// against a schema that is not there yet.

const singleMigrate = "  migrate:\n    kind: hook\n    command: [\"hooks/migrate\"]\n    timeout: 10m\n"

// withMigrateSteps rewrites a bundle's migrate operation as the given YAML
// list, declares the manager version a list needs, and writes any hooks the
// list names that the bundle does not carry.
func withMigrateSteps(t *testing.T, bundle, steps string, hooks map[string]string) {
	t.Helper()
	path := filepath.Join(bundle, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	patched := strings.Replace(string(raw), singleMigrate, "  migrate:\n"+steps, 1)
	require.NotEqual(t, string(raw), patched, "the fixture's migrate operation is no longer the one this rewrites")
	patched = strings.Replace(patched, `min_manager_version: "0.0.0"`,
		`min_manager_version: "`+domain.MigrateStepsMinManagerVersion+`"`, 1)
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))
	for name, body := range hooks {
		require.NoError(t, os.WriteFile(filepath.Join(bundle, "hooks", name), []byte(body), 0o755))
	}
}

const twoSteps = "    - kind: hook\n      command: [\"hooks/migrate\"]\n" +
	"    - kind: hook\n      command: [\"hooks/bootstrap\"]\n"

// bootstrapAfterMigrate refuses to run before the schema exists, which is the
// ordering the feature exists to guarantee, and leaves a marker when it ran.
const bootstrapAfterMigrate = `#!/bin/sh
set -eu
[ -f "${DEMO_DATA_DIR:?}/.schema" ] || { echo "bootstrap ran before the migrations" >&2; exit 1; }
echo bootstrapped > "${DEMO_DATA_DIR}/.bootstrapped"
`

func TestMigrateRunsItsStepsInOrder(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	src := stageRecoverableUpgradeSource(t, h)
	withMigrateSteps(t, src, twoSteps, map[string]string{"bootstrap": bootstrapAfterMigrate})
	require.NoError(t, os.Remove(filepath.Join(h.Paths.DataDir(), ".schema")),
		"the baseline left a schema behind, so the order would go unobserved")

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(h.Paths.DataDir(), ".bootstrapped"), "the second step never ran")

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, 12, current.SchemaAtInstall, "the schema the migrations reported was lost")
}

// The second step failing is a failed migrate: the first has changed the
// database, and nothing the manager knows can take that back.
func TestAFailedLaterStepIsAFailedMigrate(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	src := stageRecoverableUpgradeSource(t, h)
	withMigrateSteps(t, src, twoSteps, map[string]string{
		"bootstrap": "#!/bin/sh\necho 'first administrator already exists with another email' >&2\nexit 1\n",
	})

	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	assert.Equal(t, domain.ExitManualIntervention, domain.ExitCode(err))
	assert.Equal(t, domain.StatusManualIntervention, result.Record.Status)
	assert.Contains(t, err.Error(), "migrate", "the failure does not name the step it happened in")
}

// A migration service with nothing to migrate exits 2, as a hook does, and the
// adapter hands that back as data for exactly this reading.
func TestAMigrationServiceWithNothingToDoIsNotAFailure(t *testing.T) {
	for exit, fails := range map[int]bool{2: false, 3: true} {
		h := newHarness(t)
		h.install()
		h.setHookEnv()
		applyBaseline(t, h)
		ctx := context.Background()

		src := stageRecoverableUpgradeSource(t, h)
		withMigrateSteps(t, src,
			"    - kind: runtime-service\n      service: app\n"+
				"    - kind: hook\n      command: [\"hooks/migrate\"]\n", nil)
		h.Runtime.OneShotResults = map[string]ports.ExitResult{"app": {ExitCode: exit}}

		_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
		if !fails {
			require.NoError(t, err, "a migration service reporting nothing to do stopped the update")
			continue
		}
		require.Error(t, err, "exit %d is a failed migration", exit)
		assert.Contains(t, err.Error(), "migration step 1 of 2")
	}
}

// What the loader accepts and refuses, through the same decode a bundle gets.
func TestWhatAMigrateListMayLookLike(t *testing.T) {
	parse := func(t *testing.T, edit func(string) string) error {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(testBundlePath(t), "manifest.yaml"))
		require.NoError(t, err)
		edited := edit(string(raw))
		require.NotEqual(t, string(raw), edited, "the edit changed nothing")
		_, err = release.ParseManifest([]byte(edited), "manifest.yaml")
		return err
	}
	list := func(steps, minimum string) func(string) string {
		return func(s string) string {
			s = strings.Replace(s, singleMigrate, "  migrate:\n"+steps, 1)
			return strings.Replace(s, `min_manager_version: "0.0.0"`, `min_manager_version: "`+minimum+`"`, 1)
		}
	}

	t.Run("a list, declaring the manager that reads it", func(t *testing.T) {
		require.NoError(t, parse(t, list(twoSteps, domain.MigrateStepsMinManagerVersion)))
	})
	t.Run("a list, declaring an older manager", func(t *testing.T) {
		err := parse(t, list(twoSteps, "0.3.0"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "compatibility.min_manager_version")
	})
	t.Run("an empty list", func(t *testing.T) {
		err := parse(t, list("    []\n", domain.MigrateStepsMinManagerVersion))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "operations.migrate")
	})
	t.Run("a list under another operation", func(t *testing.T) {
		err := parse(t, func(s string) string {
			return strings.Replace(s,
				"  smoke_test:\n    kind: hook\n    command: [\"hooks/smoke-test\"]\n    timeout: 5m\n",
				"  smoke_test:\n    - kind: hook\n      command: [\"hooks/smoke-test\"]\n"+
					"    - kind: hook\n      command: [\"hooks/smoke-test\"]\n", 1)
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "operations.smoke_test")
	})
	t.Run("a typo inside a step is named, with where it is", func(t *testing.T) {
		err := parse(t, list("    - kind: hook\n      comand: [\"hooks/migrate\"]\n",
			domain.MigrateStepsMinManagerVersion))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "comand")
	})
	t.Run("a step is checked where it sits", func(t *testing.T) {
		err := parse(t, list("    - kind: hook\n      command: [\"hooks/migrate\"]\n"+
			"    - kind: runtime-service\n", domain.MigrateStepsMinManagerVersion))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "operations.migrate[1].service")
	})
}

// A manager that cannot read a list says it is too old, not that the manifest
// is malformed -- which is what the declaration above buys.
func TestAnOlderManagerRefusesAListAsTooOld(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(testBundlePath(t), "manifest.yaml"))
	require.NoError(t, err)
	edited := strings.Replace(string(raw), singleMigrate, "  migrate:\n"+twoSteps, 1)
	edited = strings.Replace(edited, `min_manager_version: "0.0.0"`,
		`min_manager_version: "`+domain.MigrateStepsMinManagerVersion+`"`, 1)

	release.SetManagerVersion(domain.MustParseVersion("0.3.0"))
	t.Cleanup(func() { release.SetManagerVersion(domain.Version{}) })

	_, err = release.ParseManifest([]byte(edited), "manifest.yaml")
	require.Error(t, err)
	assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err), "%v", err)
}

// One step reads as the object it always was, so `release show --json` is
// unchanged for every bundle that does not use a list.
func TestASingleStepKeepsItsShapeInJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(testBundlePath(t), "manifest.yaml"))
	require.NoError(t, err)
	edited := strings.Replace(string(raw), singleMigrate, "  migrate:\n"+twoSteps, 1)
	edited = strings.Replace(edited, `min_manager_version: "0.0.0"`,
		`min_manager_version: "`+domain.MigrateStepsMinManagerVersion+`"`, 1)
	m, err := release.ParseManifest([]byte(edited), "manifest.yaml")
	require.NoError(t, err)

	out, err := json.Marshal(m.Operations)
	require.NoError(t, err)
	var shapes map[string]any
	require.NoError(t, json.Unmarshal(out, &shapes))
	assert.IsType(t, map[string]any{}, shapes["smoke_test"], "a single step changed shape")
	assert.IsType(t, []any{}, shapes["migrate"], "a list did not come out as one")
}

// The loader checks a listed hook's executable as it checks a single one.
func TestAMissingStepHookIsRefusedBeforeAnythingRuns(t *testing.T) {
	h := newHarness(t)
	src := stageUpgradeSource(t, h)
	withMigrateSteps(t, src, twoSteps, nil)

	_, err := release.Load(src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "operations.migrate[1]")
}
