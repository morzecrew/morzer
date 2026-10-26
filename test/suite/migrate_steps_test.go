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

// serviceMigration is a migrate operation that runs a one-shot service, which
// has no way to report the schema it left.
const serviceMigration = "    - kind: runtime-service\n      service: app\n"

// A re-apply whose migrations report nothing has not moved the schema, so it
// keeps the one recorded (#143). It reset it to 0, and a release that migrates
// with a service lost any schema it was given -- and with it the schema checks
// of rollback and the unattended gate.
func TestAReapplyKeepsASchemaItsMigrationsDoNotReport(t *testing.T) {
	h := newHarness(t)
	withMigrateSteps(t, h.Release.Root, serviceMigration, nil)
	rel, err := release.Load(h.Release.Root)
	require.NoError(t, err)
	h.Release = rel // so install records the patched bundle's digest

	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	setSchema(t, h, 14)

	applyBaseline(t, h)

	current, err := h.Deps.State.CurrentRelease(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 14, current.SchemaAtInstall, "a re-apply that migrated nothing forgot the schema")
}

// Not carried across an update. A new release's migrations that report nothing
// may still have moved the schema, and the old value would then claim one the
// database has left -- which rollback reads as safe to go back to.
func TestAnUpdateWhoseMigrationsReportNothingRecordsNoSchema(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	src := stageRecoverableUpgradeSource(t, h)
	withMigrateSteps(t, src, serviceMigration, nil)

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", current.Version.String())
	assert.Zero(t, current.SchemaAtInstall,
		"the previous release's schema was claimed for a database its successor migrated")
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

// A migration service starts what it depends on, and on an update recreates a
// dependency whose configuration changed -- before start-services, which is
// what the restore used to look for. A failed migration then left the old
// release's application running against the new release's database service.
// Running one counts as touching the services, so the old release is put back.
func TestAFailedMigrationServicePutsThePreviousReleaseBack(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	src := stageRecoverableUpgradeSource(t, h)
	withMigrateSteps(t, src,
		"    - kind: runtime-service\n      service: app\n"+
			"    - kind: hook\n      command: [\"hooks/migrate\"]\n", nil)
	h.Runtime.OneShotResults = map[string]ports.ExitResult{"app": {ExitCode: 3}}
	ups := len(h.Runtime.UpConfigs)

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	assert.Equal(t, domain.ExitManualIntervention, domain.ExitCode(err), "a failed migration still needs a person")
	require.Len(t, h.Runtime.UpConfigs, ups+1, "the previous release was not brought back")
	assert.Equal(t, "1.2.0", h.Runtime.UpConfigs[ups].Env["DEMO_VERSION"])
}

// tellsContext appends the operation it runs under, the version it replaces and
// its phase, so a test can read back what each run was told.
const tellsContext = `#!/bin/sh
set -eu
echo "${DEMO_OPERATION_TYPE:-unset} ${DEMO_PREVIOUS_VERSION:-none} ${DEMO_PHASE:-unset}" >> "${DEMO_DATA_DIR:?}/.context"
`

// A migrate step and the smoke test are told the operation running them and,
// on an update, the version it replaces (#100). Every one was told `apply`
// with no previous version, so an update and a re-apply looked the same.
func TestAStepIsToldWhatIsRunningIt(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	src := stageRecoverableUpgradeSource(t, h)
	withMigrateSteps(t, src, "    - kind: hook\n      command: [\"hooks/migrate\"]\n"+
		"    - kind: hook\n      command: [\"hooks/context\"]\n",
		map[string]string{"context": tellsContext, "smoke-test": tellsContext})

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)
	_, err = ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	// The second rollback bounces back to 1.3.0, the release with the hook,
	// so it is told it replaces 1.2.0.
	for range 2 {
		_, err = ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
		require.NoError(t, err)
	}

	told, err := os.ReadFile(filepath.Join(h.Paths.DataDir(), ".context"))
	require.NoError(t, err)
	assert.Equal(t, "update 1.2.0 migrate\nupdate 1.2.0 smoke-test\n"+
		"apply none migrate\napply none smoke-test\n"+
		"rollback 1.2.0 migrate\nrollback 1.2.0 smoke-test\n", string(told))
}

// A migration service gets the hook ABI's variables that mean the same inside
// a container (#99). Host paths name the manager's filesystem, the result
// descriptor is not open in there, and parameters reach a service the way the
// vendor's Compose file wires them.
func TestAMigrationServiceIsToldWhatIsRunningIt(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	src := stageRecoverableUpgradeSource(t, h)
	withMigrateSteps(t, src,
		"    - kind: runtime-service\n      service: app\n"+
			"    - kind: hook\n      command: [\"hooks/migrate\"]\n", nil)

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.NoError(t, err)

	require.Len(t, h.Runtime.OneShots, 1)
	env := h.Runtime.OneShots[0].Env
	installation, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	for name, want := range map[string]string{
		"DEMO_PRODUCT":          "demo",
		"DEMO_INSTALLATION_ID":  installation.ID,
		"DEMO_OPERATION_TYPE":   "update",
		"DEMO_PHASE":            "migrate",
		"DEMO_RELEASE_VERSION":  "1.3.0",
		"DEMO_PREVIOUS_VERSION": "1.2.0",
		"DEMO_LOG_LEVEL":        "info",
		"DEMO_DRY_RUN":          "0",
	} {
		assert.Equal(t, want, env[name], name)
	}
	assert.NotEmpty(t, env["DEMO_OPERATION_ID"])
	for name := range env {
		for _, host := range []string{"_DIR", "_FILE", "_FD", "_PARAM_", "_COMPOSE_PROJECT"} {
			assert.NotContains(t, name, host, "%s means nothing inside the container", name)
		}
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
