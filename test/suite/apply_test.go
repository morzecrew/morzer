package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/hooks"
	"github.com/morzecrew/morzer/internal/adapters/render/gotemplate"
	"github.com/morzecrew/morzer/internal/adapters/source/local"
	"github.com/morzecrew/morzer/internal/adapters/verify/checksum"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/infra/logging"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/infra/tools"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/test/fakes"
)

// harness is a complete deployment wired against in-memory adapters.
//
// This is the "fake-adapter integration" level from the testing strategy: a
// real operation, a real step engine, a real state store on a real temp
// filesystem — but no Docker, no root, no network. It runs in milliseconds,
// which is what makes it usable as the default regression net.
type harness struct {
	t *testing.T

	Deps    *ops.Deps
	Runtime *fakes.Runtime
	Secrets *fakes.SecretStore
	Health  *fakes.Health
	Backup  *fakes.Backup
	Events  *events.Collector

	Root    string
	Paths   domain.Paths
	Release domain.Release
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	root := t.TempDir()
	paths := domain.PathsUnder(root, "demo")

	// The release is a real copy of the example bundle, so the manifest
	// loader, the hook ABI and the template renderer are all genuinely
	// exercised.
	releaseRoot := filepath.Join(paths.ReleasesDir(), "1.2.0")
	copyBundle(t, testBundlePath(t), releaseRoot)
	retargetManifest(t, releaseRoot, root)

	rel, err := release.Load(releaseRoot)
	require.NoError(t, err)

	for _, dir := range paths.ManagedDirs() {
		require.NoError(t, os.MkdirAll(dir.Path, os.FileMode(dir.Mode)))
		require.NoError(t, os.Chmod(dir.Path, os.FileMode(dir.Mode)))
	}

	bus := events.NewStrictBus()
	collector := events.NewCollector()
	bus.Subscribe(collector)

	stateStore := state.New(paths)
	runner := infraexec.New()

	h := &harness{
		t:       t,
		Runtime: fakes.NewRuntime(),
		Secrets: fakes.NewSecretStore(),
		Health:  fakes.NewHealth(),
		Backup:  fakes.NewBackup(),
		Events:  collector,
		Root:    root,
		Paths:   paths,
		Release: rel,
	}

	_, redactor := logging.New(logging.Options{Writer: os.Stderr})

	h.Deps = &ops.Deps{
		Paths:    paths,
		State:    stateStore,
		Locker:   fakes.NewLocker(),
		Runtime:  h.Runtime,
		Secrets:  h.Secrets,
		Backup:   h.Backup,
		Health:   h.Health,
		Renderer: gotemplate.New(),
		// The real directory source and checksum verifier: both are
		// filesystem-only, so using the production adapters here costs
		// nothing and exercises them.
		Source:         local.New(),
		Verifier:       checksum.New(),
		Hooks:          hooks.NewRunner(runner),
		Tools:          tools.NewRegistry(runner),
		Bus:            bus,
		Engine:         engine.New(stateStore, bus),
		ManagerVersion: domain.MustParseVersion("1.0.0"),
		Redactor:       redactor,
		Now:            func() time.Time { return time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC) },
	}

	return h
}

// testBundlePath locates the example bundle relative to this test file.
func testBundlePath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Join(wd, "..", "..", "testdata", "bundle")
}

// copyBundle copies the example bundle, preserving the executable bit on
// hooks: a hook that arrives non-executable is a release validation error, and
// the loader would reject it.
func copyBundle(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dst, 0o755))

	require.NoError(t, filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	}))
}

// retargetManifest rewrites the bundle's absolute paths into the test root.
//
// It edits the manifest on disk rather than the loaded struct, because every
// operation re-loads the release from disk: a rewrite held only in memory
// would be silently discarded and the test would write to the real /etc.
func retargetManifest(t *testing.T, releaseRoot, testRoot string) {
	t.Helper()

	path := filepath.Join(releaseRoot, "manifest.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	rewritten := strings.ReplaceAll(string(data), " /etc/demo/", " "+testRoot+"/etc/demo/")
	rewritten = strings.ReplaceAll(rewritten, " /run/demo/", " "+testRoot+"/run/demo/")

	require.NoError(t, os.WriteFile(path, []byte(rewritten), 0o644))
}

// install writes the installation and the release pointer, leaving the system
// in the state `init` produces.
func (h *harness) install() domain.Installation {
	h.t.Helper()
	ctx := context.Background()

	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_01TESTINSTALLATION",
		Product:       "demo",
		CreatedAt:     domain.NewTime(h.Deps.Now()),
		Profile:       "embedded",
		Domains:       []string{"demo.example"},
		Policy:        domain.DefaultPolicy(),
	}
	require.NoError(h.t, h.Deps.State.SaveInstallation(ctx, inst))

	require.NoError(h.t, h.Deps.State.SetCurrentRelease(ctx, domain.ReleaseRecord{
		SchemaVersion: domain.InstallationSchemaVersion,
		Name:          h.Release.Name(),
		Version:       h.Release.Version(),
		Digest:        h.Release.Digest,
		Root:          h.Release.Root,
		InstalledAt:   domain.NewTime(h.Deps.Now()),
	}))

	// The release the harness holds has rewritten config targets, so the
	// hook environment must point at the same place.
	h.Secrets.Seed(map[string]string{
		"db_password": "a-real-database-password",
		"session_key": "a-real-session-key-value",
	})

	return inst
}

// setHookEnv points the bundle's hooks at the test's own directories.
func (h *harness) setHookEnv() {
	h.t.Setenv("DEMO_DATA_DIR", h.Paths.DataDir())
	h.t.Setenv("DEMO_SECRETS_DIR", h.Paths.SecretsRenderDir())
	h.t.Setenv("DEMO_CONFIG_FILE", filepath.Join(h.Root, "/etc/demo/application.yaml"))
	h.t.Setenv("DEMO_BACKUP_DIR", h.Paths.BackupsDir())
}

func TestApplyRunsEveryStepAndConverges(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	result, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err, "a well-formed deployment must apply cleanly")

	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)

	// The runtime saw the calls in the order the pipeline declares.
	assert.Contains(t, h.Runtime.Calls, "Validate")
	assert.Contains(t, h.Runtime.Calls, "Pull")
	assert.Contains(t, h.Runtime.Calls, "Up")

	// Secrets landed on disk with the right permissions.
	secretPath := filepath.Join(h.Paths.SecretsRenderDir(), "db_password")
	info, err := os.Stat(secretPath)
	require.NoError(t, err, "the required secrets must be rendered")
	assert.Equal(t, os.FileMode(0o400), info.Mode().Perm())

	dirInfo, err := os.Stat(h.Paths.SecretsRenderDir())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	// The configuration was rendered, and it holds a *path* to the secret
	// rather than the secret itself.
	configPath := filepath.Join(h.Root, "/etc/demo/application.yaml")
	config, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(config), h.Paths.SecretsRenderDir()+"/db_password")
	assert.NotContains(t, string(config), "a-real-database-password",
		"a rendered config in /etc must never contain a credential")

	// The release pointer and the current symlink both moved.
	current, err := h.Deps.State.CurrentRelease(context.Background())
	require.NoError(t, err)
	assert.True(t, current.Version.Equal(domain.MustParseVersion("1.2.0")))

	target, err := os.Readlink(h.Paths.CurrentLink())
	require.NoError(t, err)
	assert.Equal(t, h.Release.Root, target)

	// The migrate hook ran and recorded the schema it left behind.
	schemaMarker := filepath.Join(h.Paths.DataDir(), ".schema")
	assert.FileExists(t, schemaMarker, "the migrate hook must have run")
}

func TestApplyIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	ctx := context.Background()

	_, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)

	second, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err, "applying an unchanged system must succeed")

	// The configuration step must report itself satisfied rather than
	// rewriting a byte-identical file.
	var configStep domain.StepRecord
	for _, s := range second.Record.Steps {
		if s.ID == "render-configuration" {
			configStep = s
		}
	}
	assert.Equal(t, domain.StepSkipped, configStep.Status,
		"a byte-identical config must not be rewritten; that is what makes apply a no-op")

	// The migrate hook exits 2 (nothing to do) on the second run, which
	// the hook ABI treats as success rather than failure.
	assert.Equal(t, domain.StatusSucceeded, second.Record.Status)
}

func TestApplyDryRunChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	result, err := ops.Apply(context.Background(), h.Deps, ops.Options{DryRun: true})
	require.NoError(t, err)

	assert.Empty(t, h.Runtime.Calls, "a plan must not touch the runtime")
	assert.NoFileExists(t, filepath.Join(h.Paths.SecretsRenderDir(), "db_password"),
		"a plan must not render secrets")
	assert.NoFileExists(t, filepath.Join(h.Root, "/etc/demo/application.yaml"),
		"a plan must not write configuration")

	plans := h.Events.OfKind(events.KindPlan)
	require.Len(t, plans, 1, "a dry run must emit exactly one plan")
	assert.NotEmpty(t, plans[0].Plan)

	// The plan shows the config change as a diff, which is what makes it
	// reviewable.
	var sawDiff bool
	for _, step := range plans[0].Plan {
		if step.ID == "render-configuration" && step.Diff != "" {
			sawDiff = true
		}
	}
	assert.True(t, sawDiff, "a configuration change must be shown as a diff in the plan")

	assert.Empty(t, result.Record.FinishedAt.IsZero(),
		"the plan still produces a record, it is just not journaled")
}

func TestApplyFailsWhenARequiredSecretIsMissing(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	// Remove a required secret. This must fail before anything mutates.
	require.NoError(t, h.Secrets.Remove(context.Background(), "session_key"))

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.Error(t, err)

	assert.Equal(t, domain.ExitSecrets, domain.ExitCode(err),
		"a missing secret must map to exit 6")
	assert.Empty(t, h.Runtime.CallsMatching("Up"),
		"nothing may start when a required secret is absent")
}

func TestApplyCompensatesWhenHealthChecksFail(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	h.Health.Healthy = false

	result, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.Error(t, err, "a product that never becomes healthy must fail the apply")

	assert.Equal(t, domain.StatusCompensated, result.Record.Status)
	assert.Equal(t, domain.ExitCompensated, domain.ExitCode(err))

	// The rendered secrets were cleaned up: leaving plaintext credentials
	// behind after a failed apply is not a state to walk away from.
	assert.NoDirExists(t, h.Paths.SecretsRenderDir(),
		"compensation must remove the rendered secrets")
}

func TestApplyFailsClosedWhenTheRuntimeCannotStart(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	h.Runtime.Fail["Up"] = domain.RuntimeError(nil, "the daemon refused to start the project")

	result, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.Error(t, err)

	assert.Equal(t, domain.StatusCompensated, result.Record.Status)

	// The release pointer must not have moved: recording a release that
	// never started would make `status` lie.
	current, err := h.Deps.State.CurrentRelease(context.Background())
	require.NoError(t, err)
	assert.True(t, current.Version.Equal(domain.MustParseVersion("1.2.0")),
		"the pointer stays where it was; it is not advanced by a failed apply")
}

func TestApplyJournalsEveryRun(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	ctx := context.Background()

	_, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)

	records, err := h.Deps.State.Operations(ctx, ops.OperationFilterAll())
	require.NoError(t, err)
	require.NotEmpty(t, records)

	rec := records[0]
	assert.Equal(t, domain.OpTypeApply, rec.Type)
	assert.Equal(t, domain.StatusSucceeded, rec.Status)
	assert.Equal(t, "1.0.0", rec.ManagerVersion)
	assert.Equal(t, "inst_01TESTINSTALLATION", rec.InstallationID)
	assert.NotEmpty(t, rec.Steps)

	// The journal is the audit trail, so every step must be accounted for.
	for _, step := range rec.Steps {
		assert.NotEmpty(t, step.ID)
		assert.NotEqual(t, domain.StepPending, step.Status,
			"a successful run must leave no step pending")
	}
}

func TestStatusReportsDeployedState(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	ctx := context.Background()

	_, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)

	status, err := ops.GetStatus(ctx, h.Deps)
	require.NoError(t, err)

	assert.Equal(t, "demo", status.Product)
	assert.Equal(t, "inst_01TESTINSTALLATION", status.InstallationID)
	require.NotNil(t, status.CurrentRelease)
	assert.True(t, status.CurrentRelease.Version.Equal(domain.MustParseVersion("1.2.0")))
	assert.Equal(t, "https://demo.example", status.PublicURL)
	assert.NotEmpty(t, status.Services)
	assert.True(t, status.Healthy())
	assert.Empty(t, status.NeedsAttention)
}

func TestStatusWorksWithoutARelease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// An installation with no release must still produce a status: this
	// is the state right after `init`.
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_fresh", Product: "demo",
		CreatedAt: domain.NewTime(h.Deps.Now()), Policy: domain.DefaultPolicy(),
	}))

	status, err := ops.GetStatus(ctx, h.Deps)
	require.NoError(t, err, "status must work on a fresh installation")
	assert.Nil(t, status.CurrentRelease, "no release installed must be null, not a zero-filled object")
	assert.Empty(t, status.Services)
}

func TestBackupCreatesVerifiesAndPrunes(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	ctx := context.Background()

	result, err := ops.Backup(ctx, h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true, Prune: true,
	})
	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)

	backups, err := h.Deps.Backup.List(ctx)
	require.NoError(t, err)
	require.Len(t, backups, 1)

	assert.Equal(t, 1, h.Backup.Verified[backups[0].ID],
		"a backup that has never been read back is a hope, not a backup")
}

func TestRestoreRequiresForceAndTypedConfirmation(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	h.setHookEnv()
	ctx := context.Background()

	// Restore re-applies the release and runs the smoke test, so the
	// deployment has to exist before it can be restored onto.
	_, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)

	_, err = ops.Backup(ctx, h.Deps, ops.BackupOptions{Reason: "manual"})
	require.NoError(t, err)

	t.Run("refuses without force", func(t *testing.T) {
		_, err := ops.Restore(ctx, h.Deps, ops.RestoreOptions{
			ConfirmedInstallationID: inst.ID,
		})
		require.Error(t, err)
		assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
	})

	t.Run("refuses without the typed installation id", func(t *testing.T) {
		_, err := ops.Restore(ctx, h.Deps, ops.RestoreOptions{
			Options: ops.Options{Force: true},
		})
		require.Error(t, err, "a destructive restore must not be confirmable by reflex")
		assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
	})

	t.Run("refuses a wrong installation id", func(t *testing.T) {
		_, err := ops.Restore(ctx, h.Deps, ops.RestoreOptions{
			Options:                 ops.Options{Force: true},
			ConfirmedInstallationID: "inst_someone_elses",
		})
		require.Error(t, err)
	})

	t.Run("proceeds with both", func(t *testing.T) {
		result, err := ops.Restore(ctx, h.Deps, ops.RestoreOptions{
			Options:                 ops.Options{Force: true},
			ConfirmedInstallationID: inst.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, domain.StatusSucceeded, result.Record.Status)

		// Writers were stopped before the restore and the release was
		// re-applied afterwards.
		assert.Contains(t, h.Runtime.Calls, "Down")
		assert.Contains(t, h.Runtime.Calls, "Up")
	})
}

func TestDoctorReportsRemediesForEveryProblem(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err, "doctor must never fail; it reports failures")

	require.NotEmpty(t, report.Results)

	for _, res := range report.Results {
		assert.NotEmpty(t, res.ID, "every check needs a stable id for monitoring")
		assert.NotEmpty(t, res.Category)
		assert.NotEmpty(t, res.Description)

		if res.Status != events.CheckOK {
			assert.NotEmpty(t, res.Remedy,
				"check %q reports a problem without saying what to do about it", res.ID)
		}
	}
}

func TestDoctorFlagsAnUnfinishedOperation(t *testing.T) {
	h := newHarness(t)
	h.install()
	ctx := context.Background()

	// An operation that needs a human must keep surfacing until cleared.
	require.NoError(t, h.Deps.State.AppendOperation(ctx, domain.OperationRecord{
		SchemaVersion: 1, ID: "op_stuck", Type: domain.OpTypeUpdate,
		Status: domain.StatusManualIntervention, StartedAt: domain.NewTime(h.Deps.Now()),
	}))

	report, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)

	var found bool
	for _, res := range report.Results {
		if res.ID == "config.operations" {
			found = true
			assert.Equal(t, events.CheckFail, res.Status)
			assert.Contains(t, res.Message, "op_stuck")
			assert.Contains(t, res.Remedy, "clear-intervention")
		}
	}
	assert.True(t, found, "doctor must check for unfinished operations")

	// And clearing it must resolve the flag.
	_, err = ops.ClearIntervention(ctx, h.Deps, "op_stuck")
	require.NoError(t, err)

	unfinished, err := h.Deps.State.UnfinishedOperations(ctx)
	require.NoError(t, err)
	for _, rec := range unfinished {
		assert.NotEqual(t, "op_stuck", rec.ID, "the cleared operation must no longer need attention")
	}
}

func TestSecretsNeverReachTheJournal(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	ctx := context.Background()

	_, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)

	// Read the raw journal file rather than the parsed records: the
	// assertion is about what is on disk, because that is what an operator
	// or an attacker would read.
	journal, err := os.ReadFile(h.Paths.JournalFile())
	require.NoError(t, err)

	assert.NotContains(t, string(journal), "a-real-database-password",
		"redaction happens before writing, not at display time")
	assert.NotContains(t, string(journal), "a-real-session-key-value")
}
