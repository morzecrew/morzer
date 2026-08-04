package suite

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/backup/hookbackup"
	"github.com/morzecrew/morzer/internal/adapters/hooks"
	"github.com/morzecrew/morzer/internal/adapters/render/gotemplate"
	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
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
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/test/fakes"
)

// The recovery scenario, end to end.
//
// `init` insists on an offline recovery recipient and `doctor` warns when one
// is missing, so the manager has always claimed a machine could be rebuilt from
// one. Until these tests, nothing had ever done it. A safeguard whose use is
// untested is worse than no safeguard: it produces confidence without
// capability.
//
// These run against the real sops-age store with real age keys, because every
// claim being made here is a claim about cryptography. A fake secret store
// would make them pass without proving anything, which is why the fake
// deliberately does not implement ports.RecoverableSecretStore.

// machine is one host: its own root, its own age identity, its own state.
type machine struct {
	t *testing.T

	Root    string
	Paths   domain.Paths
	Deps    *ops.Deps
	Secrets *sopsage.Store
	Runtime *fakes.Runtime
}

// newMachine wires a host the way the CLI does, with the real secret store.
//
// Runtime and health stay fake: nothing here is a claim about Docker. Hooks,
// the state store, the release source and the verifier are real, so a release
// staged here is a release the loader accepted.
func newMachine(t *testing.T, root string) *machine {
	t.Helper()

	paths := domain.PathsUnder(root, "demo")
	stateStore := state.New(paths)
	runner := infraexec.New()

	bus := events.NewStrictBus()
	bus.Subscribe(events.NewCollector())
	_, redactor := logging.New(logging.Options{Writer: os.Stderr})

	secrets := sopsage.New(runner, paths.SecretsFile(), paths.AgeIdentityFile())
	runtime := fakes.NewRuntime()

	deps := &ops.Deps{
		Paths:          paths,
		State:          stateStore,
		Locker:         fakes.NewLocker(),
		Runtime:        runtime,
		Secrets:        secrets,
		Health:         fakes.NewHealth(),
		Renderer:       gotemplate.New(),
		Source:         local.New(),
		Verifier:       checksum.New(),
		Supervisor:     fakes.NewSupervisor(),
		Hooks:          hooks.NewRunner(runner),
		Tools:          tools.NewRegistry(runner),
		Bus:            bus,
		Engine:         engine.New(stateStore, bus),
		ManagerVersion: domain.MustParseVersion("1.0.0"),
		Redactor:       redactor,
		TargetPrefix:   root,
		Now:            func() time.Time { return time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC) },
	}

	return &machine{t: t, Root: root, Paths: paths, Deps: deps, Secrets: secrets, Runtime: runtime}
}

// wireBackupEngine attaches the real hook-driven backup engine, which is what
// stamps a backup with the installation ID and refuses one belonging to a
// different installation. That refusal is the whole reason import restores the
// original ID, so the test has to use the adapter that performs it.
func (m *machine) wireBackupEngine(ctx context.Context) {
	m.t.Helper()

	inst, err := m.Deps.State.LoadInstallation(ctx)
	require.NoError(m.t, err)

	current, err := m.Deps.State.CurrentRelease(ctx)
	require.NoError(m.t, err)
	require.False(m.t, current.IsZero(), "a release must be installed before backups make sense")

	rel, err := release.Load(current.Root)
	require.NoError(m.t, err)

	m.Deps.Backup = hookbackup.New(hookbackup.Config{
		Hooks:          m.Deps.Hooks,
		Release:        rel,
		Installation:   inst,
		Paths:          m.Paths,
		ManagerVersion: "1.0.0",

		// The real recipient list, so the backups these tests take are
		// encrypted to the same keys as the secret state -- which is
		// the whole point of the arrangement and what makes the
		// recovery scenario below prove anything.
		Recipients: func(ctx context.Context) ([]string, error) {
			recipients, err := m.Deps.Secrets.Recipients(ctx)
			if err != nil {
				return nil, err
			}
			keys := make([]string, 0, len(recipients))
			for _, r := range recipients {
				keys = append(keys, r.PublicKey)
			}
			return keys, nil
		},
	})
}

// requireSOPS skips when the binary is missing. `just contract-strict` asserts
// that nothing here skipped, so a CI run that quietly stopped exercising the
// real store fails rather than going green.
func requireSOPS(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sops"); err != nil {
		t.Skip("sops is not installed; skipping the recovery scenario")
	}
}

// generateRecoveryKey writes an offline identity somewhere neither machine can
// reach, which is the only place a recovery key is worth anything.
func generateRecoveryKey(t *testing.T) (path, publicKey string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "recovery.key")
	publicKey, err := sopsage.GenerateIdentity(path)
	require.NoError(t, err)
	return path, publicKey
}

// initOriginMachine creates the installation that will later be lost.
func initOriginMachine(t *testing.T, ctx context.Context, recoveryPub string) *machine {
	t.Helper()

	origin := newMachine(t, t.TempDir())
	_, err := ops.Init(ctx, origin.Deps, ops.InitOptions{
		Product:           "demo",
		ReleasePath:       testBundlePath(t),
		Profile:           "embedded",
		Domains:           []string{"demo.example"},
		RecoveryRecipient: recoveryPub,
		GenerateSecrets:   true,
	})
	require.NoError(t, err, "init must succeed before anything can be recovered")
	return origin
}

func TestRecoveryRebuildsAMachineFromAnOfflineKey(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)

	// A value only this installation knows. Whether it survives is the
	// question the whole feature answers.
	const known = "the-value-that-must-survive"
	require.NoError(t, origin.Deps.Secrets.Set(ctx, "db_password", domain.NewSecret(known)))

	originInst, err := origin.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	originMachineKey, err := origin.Secrets.IdentityPublicKey(ctx)
	require.NoError(t, err)

	exportPath := filepath.Join(t.TempDir(), "demo.export.yaml")
	_, err = ops.Export(ctx, origin.Deps, ops.ExportOptions{Path: exportPath})
	require.NoError(t, err)

	// The export leaves the machine, so what is in it matters more than
	// what is in any other file this program writes.
	raw, err := os.ReadFile(exportPath)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), known,
		"an export must never carry a plaintext secret; it is a file operators copy around")

	info, err := os.Stat(exportPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"the export names domains, policy and layout; it is not world-readable")

	// The machine is gone. Not "reset" -- gone: a different root, sharing
	// nothing with the first, exactly as a replacement VM would be.
	require.NoError(t, os.RemoveAll(origin.Root))

	rebuilt := newMachine(t, t.TempDir())
	export, err := ops.LoadExport(exportPath)
	require.NoError(t, err)

	_, err = ops.Import(ctx, rebuilt.Deps, ops.ImportOptions{
		SourcePath:   exportPath,
		Export:       export,
		IdentityFile: recoveryPath,
	})
	require.NoError(t, err, "import from an export plus the offline key must succeed")

	t.Run("the installation identity survives", func(t *testing.T) {
		got, err := rebuilt.Deps.State.LoadInstallation(ctx)
		require.NoError(t, err)

		// The ID is the property everything else depends on. Backups
		// are stamped with it and restore checks against it, so a fresh
		// one would make the rebuilt machine unable to restore its own
		// backups -- the point of having recovered at all.
		assert.Equal(t, originInst.ID, got.ID,
			"the rebuilt machine must assume the original installation id")
		assert.Equal(t, originInst.Product, got.Product)
		assert.Equal(t, originInst.Domains, got.Domains)
		assert.Equal(t, originInst.Profile, got.Profile)
		assert.Equal(t, originInst.CreatedAt, got.CreatedAt)
	})

	t.Run("the secrets are readable by the new machine's own key", func(t *testing.T) {
		// Through the rebuilt machine's own store, with its own
		// identity -- not through the recovery key. Anything less would
		// prove only that the recovery key still works.
		set, err := rebuilt.Deps.Secrets.Load(ctx)
		require.NoError(t, err)

		got, ok := set.Get("db_password")
		require.True(t, ok, "the secret must survive the rebuild")
		assert.Equal(t, known, got.Reveal())

		// The generated secrets came across too, so the product's own
		// credentials are intact rather than merely present.
		assert.True(t, set.Has("session_key"),
			"every secret in the state must survive, not only the one the test set")
	})

	t.Run("the lost machine's key is revoked", func(t *testing.T) {
		recipients, err := rebuilt.Deps.Secrets.Recipients(ctx)
		require.NoError(t, err)

		newMachineKey, err := rebuilt.Secrets.IdentityPublicKey(ctx)
		require.NoError(t, err)
		assert.NotEqual(t, originMachineKey, newMachineKey,
			"the rebuilt host must have its own identity, not the dead one's")

		var keys []string
		for _, r := range recipients {
			keys = append(keys, r.PublicKey)
		}
		// A decommissioned host retaining the ability to decrypt makes
		// the rebuild ceremonial, which matters most when the reason
		// for it was a compromise.
		assert.NotContains(t, keys, originMachineKey,
			"the previous machine's key must lose access")
		assert.Contains(t, keys, newMachineKey, "this machine must be able to read its own state")
		assert.Contains(t, keys, recoveryPub, "the recovery key must still work for the next rebuild")
	})

	t.Run("the recovery key alone still opens the state", func(t *testing.T) {
		// The next incident needs this to be true, and it is easy to
		// break by re-encrypting for the machine key alone.
		recoverable, ok := rebuilt.Deps.Secrets.(ports.RecoverableSecretStore)
		require.True(t, ok)

		set, err := recoverable.WithIdentity(recoveryPath).Load(ctx)
		require.NoError(t, err)
		got, _ := set.Get("db_password")
		assert.Equal(t, known, got.Reveal())
	})

	t.Run("no release is installed", func(t *testing.T) {
		current, err := rebuilt.Deps.State.CurrentRelease(ctx)
		require.NoError(t, err)
		assert.True(t, current.IsZero(),
			"import restores identity, not software; `update` and `apply` follow")

		// It does record what was running, so an operator knows which
		// bundle to fetch. A release root is a path on a machine that
		// no longer exists; the version and digest are portable.
		assert.Equal(t, "1.2.0", export.Release.Version.String())
		assert.NotEmpty(t, export.Release.Digest)
	})

	t.Run("the import is in the journal as its own operation type", func(t *testing.T) {
		records, err := rebuilt.Deps.State.Operations(ctx, ops.OperationFilterAll())
		require.NoError(t, err)
		require.NotEmpty(t, records)

		var found bool
		for _, r := range records {
			if r.Type == domain.OpTypeImport {
				found = true
				assert.Equal(t, domain.StatusSucceeded, r.Status)
				assert.Equal(t, originInst.ID, r.InstallationID)
			}
		}
		assert.True(t, found,
			"an incident review must be able to see this machine assumed an identity")
	})
}

// TestRecoveryKeepsBackupsRestorable is the reason import restores the original
// installation ID rather than generating a fresh one.
//
// The real backup engine stamps every backup with the installation it came
// from and refuses one belonging to another installation. So this test is the
// difference between a recovery that works and a machine that comes back up
// unable to read its own history.
func TestRecoveryKeepsBackupsRestorable(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	origin.wireBackupEngine(ctx)

	// Something in the data directory for the backup hook to capture, so
	// the restore has an observable effect rather than a nominal one. A
	// marker file rather than the schema: the schema is rewritten by the
	// migrate hook on the way back up, which would make the assertion pass
	// whether or not anything was restored.
	require.NoError(t, os.MkdirAll(origin.Paths.DataDir(), 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(origin.Paths.DataDir(), "marker"), []byte("from-the-lost-machine"), 0o640))

	backupResult, err := ops.Backup(ctx, origin.Deps, ops.BackupOptions{Reason: "pre-recovery"})
	require.NoError(t, err, "the real hook-driven backup must succeed")
	require.Equal(t, domain.StatusSucceeded, backupResult.Record.Status)

	originInst, err := origin.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)

	exportPath := filepath.Join(t.TempDir(), "demo.export.yaml")
	_, err = ops.Export(ctx, origin.Deps, ops.ExportOptions{Path: exportPath})
	require.NoError(t, err)

	// The backups are what an operator keeps offsite. Everything else about
	// the machine goes.
	offsite := filepath.Join(t.TempDir(), "offsite")
	copyTree(t, origin.Paths.BackupsDir(), offsite)
	require.NoError(t, os.RemoveAll(origin.Root))

	rebuilt := newMachine(t, t.TempDir())
	export, err := ops.LoadExport(exportPath)
	require.NoError(t, err)
	_, err = ops.Import(ctx, rebuilt.Deps, ops.ImportOptions{
		SourcePath:   exportPath,
		Export:       export,
		IdentityFile: recoveryPath,
	})
	require.NoError(t, err)

	// The operator brings the release and the backups back, and converges
	// once: restore re-applies the release afterwards, which needs the
	// configuration this step renders.
	stageRelease(t, ctx, rebuilt)
	applyRelease(t, ctx, rebuilt)
	copyTree(t, offsite, rebuilt.Paths.BackupsDir())
	rebuilt.wireBackupEngine(ctx)

	// No --allow-cross-installation. The backup is accepted because the ID
	// genuinely matches, not because a flag waved the check away.
	//
	// The offline key opens it. The rebuilt machine has a new identity that
	// was never a recipient of the lost machine's backups, so the key the
	// operator kept off the machine is the only thing that can read them --
	// which is exactly the arrangement `init` insists on at the start.
	restoreResult, err := ops.Restore(ctx, rebuilt.Deps, ops.RestoreOptions{
		Options:                 ops.Options{Force: true},
		ConfirmedInstallationID: originInst.ID,
		IdentityFile:            recoveryPath,
	})
	require.NoError(t, err,
		"a backup taken before the machine was lost must restore onto the rebuilt one")
	assert.Equal(t, domain.StatusSucceeded, restoreResult.Record.Status)

	restored, err := os.ReadFile(filepath.Join(rebuilt.Paths.DataDir(), "marker"))
	require.NoError(t, err, "the restore hook must have written the data back")
	assert.Equal(t, "from-the-lost-machine", strings.TrimSpace(string(restored)))
}

// TestBackupsAreRefusedByAFreshlyInitialisedMachine is the negative control for
// the test above: without import, the same backup is rejected.
//
// It is here because decision 1 -- import restores the original ID -- reads as
// a mistake unless you can see what happens otherwise.
func TestBackupsAreRefusedByAFreshlyInitialisedMachine(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	origin.wireBackupEngine(ctx)

	require.NoError(t, os.MkdirAll(origin.Paths.DataDir(), 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(origin.Paths.DataDir(), "marker"), []byte("from-the-lost-machine"), 0o640))
	_, err := ops.Backup(ctx, origin.Deps, ops.BackupOptions{Reason: "pre-recovery"})
	require.NoError(t, err)

	offsite := filepath.Join(t.TempDir(), "offsite")
	copyTree(t, origin.Paths.BackupsDir(), offsite)
	require.NoError(t, os.RemoveAll(origin.Root))

	// Rebuilt the wrong way: `init` instead of `installation import`, which
	// mints a new installation ID.
	fresh := initOriginMachine(t, ctx, recoveryPub)
	stageRelease(t, ctx, fresh)
	applyRelease(t, ctx, fresh)
	copyTree(t, offsite, fresh.Paths.BackupsDir())
	fresh.wireBackupEngine(ctx)

	freshInst, err := fresh.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)

	_, err = ops.Restore(ctx, fresh.Deps, ops.RestoreOptions{
		Options:                 ops.Options{Force: true},
		ConfirmedInstallationID: freshInst.ID,
	})
	require.Error(t, err,
		"a backup from another installation must be refused; this is what import exists to avoid")
	assert.Contains(t, strings.ToLower(err.Error()), "belongs to installation",
		"the refusal must name the reason, so an operator knows to import instead")

	// The escape hatch is a flag of its own. `--force` cannot serve: every
	// restore already requires it, so using it here would have made this
	// guard unreachable -- which is exactly the defect this test found.
	//
	// It still needs a key that can read the backup. This machine shares
	// the recovery *public* key with the one that took it, which is what
	// makes the backup addressable at all; the private half is what opens
	// it, and it is deliberately not on either machine.
	_, err = ops.Restore(ctx, fresh.Deps, ops.RestoreOptions{
		Options:                 ops.Options{Force: true},
		ConfirmedInstallationID: freshInst.ID,
		AllowCrossInstallation:  true,
		IdentityFile:            recoveryPath,
	})
	require.NoError(t, err,
		"restoring another deployment's data on purpose must still be possible")

	restored, err := os.ReadFile(filepath.Join(fresh.Paths.DataDir(), "marker"))
	require.NoError(t, err)
	assert.Equal(t, "from-the-lost-machine", strings.TrimSpace(string(restored)))
}

// applyRelease converges a machine, which is what leaves a rendered
// configuration behind for the restore's re-apply and smoke test to find.
func applyRelease(t *testing.T, ctx context.Context, m *machine) {
	t.Helper()
	result, err := ops.Apply(ctx, m.Deps, ops.Options{})
	require.NoError(t, err)
	require.Equal(t, domain.StatusSucceeded, result.Record.Status)
}

func TestImportRefusesAnIdentityThatIsNotARecipient(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	_, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	require.NoError(t, origin.Deps.Secrets.Set(ctx, "db_password", domain.NewSecret("a-value")))

	exportPath := filepath.Join(t.TempDir(), "demo.export.yaml")
	_, err := ops.Export(ctx, origin.Deps, ops.ExportOptions{Path: exportPath})
	require.NoError(t, err)

	// A perfectly valid age key that simply is not one of this export's
	// recipients -- the wrong file out of a directory of them.
	strangerPath, _ := generateRecoveryKey(t)

	rebuilt := newMachine(t, t.TempDir())
	export, err := ops.LoadExport(exportPath)
	require.NoError(t, err)

	_, err = ops.Import(ctx, rebuilt.Deps, ops.ImportOptions{
		SourcePath:   exportPath,
		Export:       export,
		IdentityFile: strangerPath,
	})
	require.Error(t, err, "an identity that cannot open the export must be refused")

	// Refused *before* anything was created. Discovering it after the
	// directories and a new machine key exist would leave a half-built
	// machine at the worst possible moment.
	assert.NoFileExists(t, rebuilt.Paths.InstallationFile(),
		"a refused import must not leave a partial installation behind")
	assert.NoFileExists(t, rebuilt.Paths.SecretsFile())
}

func TestImportRefusesAProviderThatCannotRecover(t *testing.T) {
	ctx := context.Background()

	// The fake secret store holds plaintext and cannot model an identity
	// swap, so it does not implement the capability. Import says so by
	// name rather than failing three steps in with a message about age.
	h := newHarness(t)
	_, err := ops.Import(ctx, h.Deps, ops.ImportOptions{
		SourcePath: "irrelevant",
		Export: domain.InstallationExport{
			APIVersion: domain.APIVersionV1Alpha1,
			Kind:       domain.KindInstallationExport,
			Installation: domain.Installation{
				SchemaVersion: domain.InstallationSchemaVersion,
				ID:            "inst_1", Product: "demo",
			},
			Secrets: domain.ExportedSecrets{
				State: "sops: {}",
				Recipients: []domain.ExportedRecipient{
					{PublicKey: "age1x", Kind: domain.RecipientKindRecovery},
				},
			},
		},
		IdentityFile: "/nonexistent",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support export or import")
}

func TestExportValidationRefusesAnUnusableDocument(t *testing.T) {
	base := func() domain.InstallationExport {
		return domain.InstallationExport{
			APIVersion: domain.APIVersionV1Alpha1,
			Kind:       domain.KindInstallationExport,
			Installation: domain.Installation{
				SchemaVersion: domain.InstallationSchemaVersion,
				ID:            "inst_1", Product: "demo",
			},
			Secrets: domain.ExportedSecrets{
				State: "sops: {}",
				Recipients: []domain.ExportedRecipient{
					{PublicKey: "age1recovery", Kind: domain.RecipientKindRecovery},
				},
			},
		}
	}

	require.NoError(t, base().Validate())

	t.Run("no secret state", func(t *testing.T) {
		e := base()
		e.Secrets.State = ""
		require.Error(t, e.Validate())
	})

	t.Run("no recipients", func(t *testing.T) {
		e := base()
		e.Secrets.Recipients = nil
		require.Error(t, e.Validate())
	})

	t.Run("only the exporting machine can decrypt", func(t *testing.T) {
		// The failure mode this catches is an export taken on a machine
		// created with --no-recovery-recipient: a file that looks like
		// an insurance policy and is not one. Better to say so now than
		// during the recovery it was taken for.
		e := base()
		e.Secrets.Recipients = []domain.ExportedRecipient{
			{PublicKey: "age1machine", Kind: domain.RecipientKindMachine},
		}
		err := e.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nothing else can decrypt")
	})

	t.Run("wrong kind of document", func(t *testing.T) {
		e := base()
		e.Kind = "application-release"
		require.Error(t, e.Validate())
	})
}

// stageRelease puts the example bundle into a machine's release store and
// points at it, the way `update` would.
func stageRelease(t *testing.T, ctx context.Context, m *machine) {
	t.Helper()

	dest := m.Paths.ReleaseDir("1.2.0")
	// Not retargeted, unlike the shared harness: these machines set
	// TargetPrefix, which is the production mechanism for relocating a
	// manifest's absolute targets. Rewriting the manifest as well would
	// apply the prefix twice.
	copyBundle(t, testBundlePath(t), dest)

	rel, err := release.Load(dest)
	require.NoError(t, err)

	require.NoError(t, m.Deps.State.SetCurrentRelease(ctx, domain.ReleaseRecord{
		SchemaVersion: domain.InstallationSchemaVersion,
		Name:          rel.Name(),
		Version:       rel.Version(),
		Digest:        rel.Digest,
		Root:          dest,
		InstalledAt:   domain.NewTime(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)),
	}))
}

// copyTree copies a directory recursively, creating the destination.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dst, 0o700))

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
			return os.MkdirAll(target, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	}))
}
