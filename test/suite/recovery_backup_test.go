package suite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/backup/hookbackup"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/test/fakes"
)

// RFC 0017's claim is that a recovery key and a backup are enough — that the
// artifact an operator actually has is the one carrying identity, rather than
// the manual export nothing schedules and no check reports on. These are the
// tests of that claim, against the real secret store and real age keys.

// TestRecoveryRebuildsAMachineFromABackupAlone.
//
// No export file is taken at any point. This is the whole RFC in one test: the
// failure it was written after being asked about is an operator with a year of
// nightly backups, a recovery key in a password manager, and no export — who
// reads the documentation and concludes their data is unrecoverable.
func TestRecoveryRebuildsAMachineFromABackupAlone(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)

	const known = "the-value-that-must-survive"
	require.NoError(t, origin.Deps.Secrets.Set(ctx, "db_password", domain.NewSecret(known)))

	originInst, err := origin.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)

	origin.wireBackupEngine(ctx)
	ref, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err, "the backup that has to carry identity must be takeable")

	// The backup is the only thing that leaves the machine, so it is copied
	// out before the machine is destroyed -- exactly as a remote target
	// would have it.
	rescued := t.TempDir()
	copyTree(t, ref.Path, filepath.Join(rescued, ref.ID))

	// Gone. Not reset: a different root sharing nothing with the first.
	require.NoError(t, os.RemoveAll(origin.Root))

	rebuilt := newMachine(t, t.TempDir())
	stageBackups(t, rebuilt, rescued)

	found, err := ops.ExportFromBackup(rebuilt.Paths, ops.ExportFromBackupOptions{
		IdentityFile: recoveryPath,
	})
	require.NoError(t, err, "a backup plus the offline key must be enough to read identity")
	assert.Equal(t, ref.ID, found.Backup.ID)
	assert.True(t, found.FromNewest(), "the only backup here is the newest one")

	_, err = ops.Import(ctx, rebuilt.Deps, ops.ImportOptions{
		SourcePath:   "backup " + ref.ID,
		Export:       found.Export,
		IdentityFile: recoveryPath,
	})
	require.NoError(t, err, "import from a backup plus the offline key must succeed")

	got, err := rebuilt.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Equal(t, originInst.ID, got.ID,
		"the rebuilt machine must assume the original installation id, or it cannot "+
			"restore the very backup it was rebuilt from")
	assert.Equal(t, originInst.Product, got.Product)

	// Read through the rebuilt machine's *own* identity, not the recovery
	// key: import re-encrypts the state to the new machine key, and reading
	// it any other way would prove only that the offline key still works.
	set, err := rebuilt.Deps.Secrets.Load(ctx)
	require.NoError(t, err, "the secret state must come back")
	secret, ok := set.Get("db_password")
	require.True(t, ok, "the secret must survive a rebuild from a backup alone")
	assert.Equal(t, known, secret.Reveal(),
		"the value only the lost machine knew must survive, from the backup alone")
}

// TestTheExportInABackupIsTheSameDocument.
//
// RFC 0017 decision 2: one producer of the recovery payload, not two. The
// situation being fixed is a backup that carried a *near*-copy — the
// operator-facing installation.yaml, which this codebase already ships a
// `doctor` check for because it drifts from the authoritative state.
//
// Compared after decryption, and that is not a convenience. The component is
// encrypted to a different recipient set than the file, and age is
// non-deterministic regardless, so identical documents have different
// ciphertexts: a byte comparison of stored bytes would fail for reasons that
// have nothing to do with the claim.
func TestTheExportInABackupIsTheSameDocument(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	require.NoError(t, origin.Deps.Secrets.Set(ctx, "db_password", domain.NewSecret("v")))

	origin.wireBackupEngine(ctx)
	ref, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)

	fromBackup := decryptExportComponent(t, ref.Path, recoveryPath)

	exportPath := filepath.Join(t.TempDir(), "demo.export.yaml")
	_, err = ops.Export(ctx, origin.Deps, ops.ExportOptions{Path: exportPath})
	require.NoError(t, err)
	fromFile, err := ops.LoadExport(exportPath)
	require.NoError(t, err)

	// The timestamps differ by construction -- the two were taken seconds
	// apart -- so they are excluded rather than the comparison being
	// loosened to nothing.
	fromBackup.ExportedAt = fromFile.ExportedAt
	assert.Equal(t, fromFile, fromBackup,
		"the export in a backup and the export in a file must be the same document; "+
			"two producers of a recovery payload is what this replaced")
}

// TestTheMachineCannotReadItsOwnExportComponent.
//
// The security property of RFC 0017 decision 11, and the half a refactor would
// quietly drop. Compromising the live host — the one that is online and
// attackable — must yield the data and not the ability to become the machine.
func TestTheMachineCannotReadItsOwnExportComponent(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)

	origin.wireBackupEngine(ctx)
	ref, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)

	component := filepath.Join(ref.Path, "export.yaml"+agecrypt.Extension)
	require.FileExists(t, component)

	// The machine's own key is what every other component in this backup is
	// readable with, which is what makes this assertion meaningful rather
	// than a statement about a key that was never a candidate.
	machineKey := origin.Paths.AgeIdentityFile()
	require.FileExists(t, machineKey)

	in, err := os.Open(component)
	require.NoError(t, err)
	defer func() { _ = in.Close() }()
	err = agecrypt.Decrypt(&bytes.Buffer{}, in, machineKey)
	assert.Error(t, err,
		"the machine that wrote this backup could read the identity inside it, so "+
			"compromising the live host is enough to become it")

	// And the offline key does open it, or the component is unreadable by
	// everyone and the test above passes for the wrong reason.
	export := decryptExportComponent(t, ref.Path, recoveryPath)
	assert.Equal(t, "demo", export.Installation.Product)

	// The other components stay readable by the machine: the divergence is
	// deliberate and confined to identity, not a blanket change to how a
	// backup is encrypted.
	other := filepath.Join(ref.Path, "manifest.yaml"+agecrypt.Extension)
	require.FileExists(t, other)
	oin, err := os.Open(other)
	require.NoError(t, err)
	defer func() { _ = oin.Close() }()
	assert.NoError(t, agecrypt.Decrypt(&bytes.Buffer{}, oin, machineKey),
		"only the export component is withheld from the machine")
}

// TestAnInstallationWithNoRecoveryRecipientGetsNoExportComponent.
//
// Not one encrypted to the machine key. An identity bundle readable only by
// the key that dies with the machine is the appearance of a recovery path with
// none of the substance, and it would read as recoverable in every listing.
func TestAnInstallationWithNoRecoveryRecipientGetsNoExportComponent(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	origin := newMachine(t, t.TempDir())
	_, err := ops.Init(ctx, origin.Deps, ops.InitOptions{
		Product:         "demo",
		ReleasePath:     testBundlePath(t),
		Profile:         "embedded",
		NoRecoveryKey:   true,
		GenerateSecrets: true,
	})
	require.NoError(t, err)

	origin.wireBackupEngine(ctx)
	ref, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err, "an installation with no recovery key must still take backups")

	assert.NoFileExists(t, filepath.Join(ref.Path, "export.yaml"+agecrypt.Extension),
		"an export encrypted to the machine's own key is worse than none: it looks "+
			"like a recovery path and is readable only by the key that died")

	manifest, err := origin.Deps.Backup.Inspect(ctx, ref)
	require.NoError(t, err)
	for _, c := range manifest.Components {
		assert.NotEqual(t, ports.ComponentExport, c.Component,
			"the manifest advertises an export the backup does not carry")
	}

	// And `--from-backup` says which of the two reasons applies, because
	// the remedies differ: telling this operator to take a new backup
	// prescribes the action that reproduces the failure exactly.
	_, err = ops.ExportFromBackup(origin.Paths, ops.ExportFromBackupOptions{
		IdentityFile: filepath.Join(t.TempDir(), "unused.key"),
	})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "recipients add",
		"the refusal must name the missing recovery recipient, not tell the operator "+
			"to take another backup that would omit the component for the same reason")
}

// TestTheRetiredSecretsComponentIsGoneAndOldBackupsStillRestore.
func TestTheRetiredSecretsComponentIsGone(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	_, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)

	origin.wireBackupEngine(ctx)
	ref, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)

	assert.NoFileExists(t, filepath.Join(ref.Path, "secrets.sops.yaml"+agecrypt.Extension),
		"the secret state is in the export component byte for byte; keeping this "+
			"would put it in one backup twice")
	assert.NoFileExists(t, filepath.Join(ref.Path, "secrets.recipients.yaml"+agecrypt.Extension))

	// The forensic half stays. `installation.yaml` is useful in an incident
	// review precisely because it can disagree with the state.
	assert.FileExists(t, filepath.Join(ref.Path, "installation.yaml"+agecrypt.Extension),
		"config is forensic and stays; only secrets was subsumed")

	// And asking for it *by name* contributes nothing, which is the half
	// the default scope cannot test: `secrets` is no longer in
	// AllComponents, so the retirement there is enforced by the component
	// never being selected. A caller that names it explicitly -- which the
	// CLI still accepts, because a restore reads backups this manager did
	// not write -- reaches the code that must also decline.
	explicit, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: []ports.Component{ports.ComponentSecrets, ports.ComponentManifest},
		Reason:     "manual",
	}, nil)
	require.NoError(t, err, "naming a retired component must not be an error")
	assert.NoFileExists(t, filepath.Join(explicit.Path, "secrets.sops.yaml"+agecrypt.Extension),
		"`--component secrets` wrote the secret state again, so it is in the backup "+
			"twice: once here and once inside the export")
}

// TestTheSchemaVersionMovedWithTheComponent.
//
// The reader refuses a pre-export backup by comparing schema versions, and it
// spells the number itself rather than importing it from the engine — an
// adapter the reader must not depend on, since it reads backups on a machine
// where no engine can be constructed. This is what keeps the two in step.
func TestTheSchemaVersionMovedWithTheComponent(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	_, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	origin.wireBackupEngine(ctx)

	ref, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)

	manifest, err := origin.Deps.Backup.Inspect(ctx, ref)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, manifest.SchemaVersion, 4,
		"a backup carrying an export must announce a schema an older manager refuses, "+
			"or that manager restores the data and silently drops the identity")
}

// decryptExportComponent opens a backup's export with the given identity.
func decryptExportComponent(t *testing.T, backupDir, identity string) domain.InstallationExport {
	t.Helper()

	in, err := os.Open(filepath.Join(backupDir, "export.yaml"+agecrypt.Extension))
	require.NoError(t, err, "the backup carries no export component")
	defer func() { _ = in.Close() }()

	var plain bytes.Buffer
	require.NoError(t, agecrypt.Decrypt(&plain, in, identity))

	var export domain.InstallationExport
	require.NoError(t, yaml.Unmarshal(plain.Bytes(), &export))
	return export
}

// stageBackups puts rescued backups where the rebuilt machine looks for them.
func stageBackups(t *testing.T, m *machine, from string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(m.Paths.BackupsDir(), 0o700))

	entries, err := os.ReadDir(from)
	require.NoError(t, err)
	for _, e := range entries {
		copyTree(t, filepath.Join(from, e.Name()), filepath.Join(m.Paths.BackupsDir(), e.Name()))
	}
}

// TestNewestByDefaultAndTheStalenessWarning.
//
// Both halves, or the warning becomes a refusal or a no-op. Identity and data
// are separate choices by design — an operator restoring to a point in time
// must not silently inherit that moment's secrets — and the cost of that
// design is that an explicit older id has to be honoured *and* flagged.
func TestNewestByDefaultAndTheStalenessWarning(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	origin.wireBackupEngine(ctx)

	older, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)

	// A second backup, distinguishable by id. The engine's default id is a
	// timestamp to the second, so two in the same second would collide.
	newer := takeAnotherBackup(t, ctx, origin, older)

	byDefault, err := ops.ExportFromBackup(origin.Paths, ops.ExportFromBackupOptions{
		IdentityFile: recoveryPath,
	})
	require.NoError(t, err)
	assert.Equal(t, newer, byDefault.Backup.ID,
		"with no id the newest backup's identity must be used: staleness here only "+
			"ever loses information, so newest is strictly the most complete")
	assert.True(t, byDefault.FromNewest())
	assert.NotContains(t, strings.Join(ops.DescribeBackupExport(byDefault), " "), "NOT the newest",
		"the default choice must not warn about itself")

	explicit, err := ops.ExportFromBackup(origin.Paths, ops.ExportFromBackupOptions{
		BackupID:     older.ID,
		IdentityFile: recoveryPath,
	})
	require.NoError(t, err, "an explicit older id is honoured; point-in-time recovery is real")
	assert.False(t, explicit.FromNewest())
	assert.Equal(t, newer, explicit.Newest.ID)

	notes := strings.Join(ops.DescribeBackupExport(explicit), " ")
	assert.Contains(t, notes, "NOT the newest")
	assert.Contains(t, notes, newer, "the warning must name the newer backup, not merely exist")
}

// TestABackupWithoutAnExportIsRefusedAndStillRestores.
//
// The pair, because the refusal must not widen. A backup with no identity is
// still a backup: refusing to *read* it would turn retiring a component into a
// refusal to restore data, which is the failure decision 10 exists to prevent.
func TestABackupWithoutAnExportIsRefusedAndStillRestores(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	origin.wireBackupEngine(ctx)

	ref, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)

	// A backup as an older manager wrote it: no export component, and a
	// schema that says so.
	stripExportComponent(t, ref.Path)

	_, err = ops.ExportFromBackup(origin.Paths, ops.ExportFromBackupOptions{
		BackupID:     ref.ID,
		IdentityFile: recoveryPath,
	})
	require.Error(t, err, "a backup with no identity must not yield one")
	assert.Contains(t, err.Error(), "predates",
		"a pre-export backup and one from an installation with no recovery recipient "+
			"need different messages: only one of them is fixed by taking another backup")

	// And it is still a backup the manager can read.
	manifest, err := origin.Deps.Backup.Inspect(ctx, ref)
	require.NoError(t, err, "refusing the identity must not refuse the backup")
	assert.Equal(t, ref.ID, manifest.ID)
	assert.NoError(t, origin.Deps.Backup.Verify(ctx, ref),
		"a backup without an export must still verify; retiring a component is not "+
			"a refusal to restore one that has it")
}

// TestAnExportSwappedBetweenTwoValidBackupsIsRefused.
//
// Decision 14. Both exports here are individually valid and both decrypt with
// the same key — which is the point: a test that fetched and decrypted
// successfully would pass without the binding that makes a component belong to
// its backup.
func TestAnExportSwappedBetweenTwoValidBackupsIsRefused(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	origin.wireBackupEngine(ctx)

	first, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)
	second := takeAnotherBackup(t, ctx, origin, first)

	// Each is readable before the swap, so the refusals below are about the
	// swap rather than about the fixtures.
	for _, id := range []string{first.ID, second} {
		_, err := ops.ExportFromBackup(origin.Paths, ops.ExportFromBackupOptions{
			BackupID: id, IdentityFile: recoveryPath,
		})
		require.NoError(t, err, "backup %s was not readable before the swap", id)
	}

	name := "export.yaml" + agecrypt.Extension
	a := filepath.Join(origin.Paths.BackupsDir(), first.ID, name)
	b := filepath.Join(origin.Paths.BackupsDir(), second, name)
	swapFiles(t, a, b)

	for _, id := range []string{first.ID, second} {
		_, err := ops.ExportFromBackup(origin.Paths, ops.ExportFromBackupOptions{
			BackupID: id, IdentityFile: recoveryPath,
		})
		require.Error(t, err,
			"backup %s accepted an export that belongs to another backup", id)
		assert.Contains(t, err.Error(), "not the one its manifest records")
	}
}

// TestTheCapturedNothingRefusalStaysVolumeSpecific.
//
// A guard rather than a test of the export component. The refusal counts
// *volumes* — `!hasHook && capturedVolumes == 0` — so an always-present
// component structurally cannot satisfy it. What this catches is a later
// refactor generalising that predicate into a component count, which would
// make every hookless release able to produce a backup holding nothing of the
// product.
func TestTheCapturedNothingRefusalStaysVolumeSpecific(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	_, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	origin.wireBackupEngine(ctx)

	// A release with no backup hook, and no runtime to read volumes
	// through: the shape that must still be refused now that every backup
	// carries an always-present component.
	stripped := origin.Deps.Backup
	origin.Deps.Backup = backupEngineWithoutHook(t, ctx, origin)

	_, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.Error(t, err,
		"a release with no hook and no captured volume must still be refused a "+
			"backup; the export component must not make an empty backup look full")

	// Two gates refuse this shape and the earlier one answers first: with no
	// runtime, volumes are not even in scope, so the refusal arrives before
	// anything is captured. The later predicate — `!hasHook &&
	// capturedVolumes == 0` — is the one an always-present component could
	// in principle satisfy, and it counts volumes specifically, which is why
	// it structurally cannot. What this pins is the outcome both of them
	// exist to produce: a backup holding nothing of the product is refused
	// rather than written.
	assert.Contains(t, err.Error(), "no backup operation",
		"the refusal must name what the release is missing")

	origin.Deps.Backup = stripped
}

// takeAnotherBackup produces a second backup with a distinguishable id.
//
// The engine's default id is a timestamp to the second, so two taken in the
// same second collide. The clock is moved rather than the test sleeping.
func takeAnotherBackup(
	t *testing.T, ctx context.Context, m *machine, previous ports.BackupRef,
) string {
	t.Helper()

	later := previous.At.Add(90 * time.Second)
	m.Deps.Backup = hookbackup.New(hookbackup.Config{
		Hooks:          m.Deps.Hooks,
		Release:        currentRelease(t, ctx, m),
		Installation:   loadInstallation(t, ctx, m),
		Paths:          m.Paths,
		ManagerVersion: "1.0.0",
		Now:            func() time.Time { return later },
		Export: func(ctx context.Context) (domain.InstallationExport, bool, error) {
			return ops.ExportForBackup(ctx, m.Deps)
		},
		Recipients: func(ctx context.Context) ([]string, error) {
			return recipientKeys(ctx, m)
		},
	})

	ref, err := m.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)
	require.NotEqual(t, previous.ID, ref.ID, "the second backup reused the first one's id")
	return ref.ID
}

// backupEngineWithoutHook builds an engine for a release that declares no
// backup operation and has no runtime to read volumes through.
func backupEngineWithoutHook(t *testing.T, ctx context.Context, m *machine) ports.BackupEngine {
	t.Helper()

	rel := currentRelease(t, ctx, m)
	rel.Manifest.Operations = nil

	return hookbackup.New(hookbackup.Config{
		Hooks:          m.Deps.Hooks,
		Release:        rel,
		Installation:   loadInstallation(t, ctx, m),
		Paths:          m.Paths,
		ManagerVersion: "1.0.0",
		Export: func(ctx context.Context) (domain.InstallationExport, bool, error) {
			return ops.ExportForBackup(ctx, m.Deps)
		},
		Recipients: func(ctx context.Context) ([]string, error) {
			return recipientKeys(ctx, m)
		},
	})
}

func currentRelease(t *testing.T, ctx context.Context, m *machine) domain.Release {
	t.Helper()
	current, err := m.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	rel, err := release.Load(current.Root)
	require.NoError(t, err)
	return rel
}

func loadInstallation(t *testing.T, ctx context.Context, m *machine) domain.Installation {
	t.Helper()
	inst, err := m.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	return inst
}

func recipientKeys(ctx context.Context, m *machine) ([]string, error) {
	recipients, err := m.Deps.Secrets.Recipients(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(recipients))
	for _, r := range recipients {
		keys = append(keys, r.PublicKey)
	}
	return keys, nil
}

// stripExportComponent rewrites a backup as an older manager would have
// written it: no export, and a schema that says so.
func stripExportComponent(t *testing.T, dir string) {
	t.Helper()

	path := filepath.Join(dir, ports.BackupManifestFileName)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var m ports.BackupManifest
	require.NoError(t, json.Unmarshal(data, &m))

	kept := m.Components[:0]
	for _, c := range m.Components {
		if c.Component == ports.ComponentExport {
			require.NoError(t, os.Remove(filepath.Join(dir, c.Path)))
			continue
		}
		kept = append(kept, c)
	}
	m.Components = kept
	m.SchemaVersion = 3

	out, err := json.MarshalIndent(m, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0o600))
}

// swapFiles exchanges two files' contents.
func swapFiles(t *testing.T, a, b string) {
	t.Helper()
	da, err := os.ReadFile(a)
	require.NoError(t, err)
	db, err := os.ReadFile(b)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(a, db, 0o600))
	require.NoError(t, os.WriteFile(b, da, 0o600))
}

// TestARestoreDoesNotTryToReadTheExportComponent.
//
// Found by the acceptance run rather than by design: the export is encrypted to
// the recovery keys alone, and `restore` decrypts every component with the
// machine's identity. So a perfectly good backup failed at the step that puts
// the data back, having already stopped the services -- the worst place in the
// operation to discover it.
//
// Skipping it is also correct on its own terms. A restore puts back data;
// identity comes from `import`, before it, with different preconditions. A
// restore hook has no business being handed the installation's identity.
func TestARestoreDoesNotTryToReadTheExportComponent(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	_, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	origin.wireBackupEngine(ctx)

	ref, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(ref.Path, "export.yaml"+agecrypt.Extension),
		"the backup must carry an export, or this proves nothing")

	inst, err := origin.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)

	// The machine's own identity, which is the default and the one that
	// cannot open the export.
	err = origin.Deps.Backup.Restore(ctx, ref, ports.RestoreOptions{
		Force:                true,
		TargetInstallationID: inst.ID,
	})
	require.NoError(t, err,
		"a restore failed on a backup it should be able to read: the export is "+
			"encrypted to the recovery keys, and staging it asks the machine for a "+
			"key it does not have")
}

// TestIdentityComesOffATargetWithoutTheBackup.
//
// The remote half of the claim, and the one an operator actually uses: the
// machine is gone, the backups are in a bucket, and what they have is an
// access key and a recovery key. Fetching the archive to read four kilobytes
// of it would cost an hour at the worst possible moment.
//
// The byte count is the assertion. A correct identity obtained by downloading
// everything passes every equality check and defeats the point.
func TestIdentityComesOffATargetWithoutTheBackup(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	require.NoError(t, origin.Deps.Secrets.Set(ctx, "db_password", domain.NewSecret("v")))
	origin.wireBackupEngine(ctx)

	fake := fakes.NewBackupTarget()
	origin.Deps.Targets = fake

	ref, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)

	targetRef := ports.TargetRef{Scheme: "memory", Path: "/backups"}
	_, err = fake.Push(ctx, targetRef, ref.Path, ref.ID)
	require.NoError(t, err)

	// The whole backup is on the target; only a fraction of it may move.
	total := backupBytesOn(t, fake)
	fake.ResetBytesRead()

	found, err := ops.ExportFromRemoteBackup(ctx, origin.Deps,
		ops.TargetOptions{URL: "memory:///backups"}, "", recoveryPath)
	require.NoError(t, err, "identity must be readable off a target")
	assert.Equal(t, ref.ID, found.Backup.ID)
	assert.Equal(t, "demo", found.Export.Installation.Product)
	assert.NotEmpty(t, found.Export.Secrets.State, "the secret state must have come with it")

	moved := fake.BytesRead()
	assert.Less(t, moved, total,
		"reading an identity transferred the whole backup (%d of %d bytes)", moved, total)

	// And specifically: the volume and database components stayed put. The
	// comparison above would still pass if a manifest happened to be large,
	// so the components that dominate a real backup are named.
	manifest, err := origin.Deps.Backup.Inspect(ctx, ref)
	require.NoError(t, err)
	var identityBytes int64
	for _, c := range manifest.Components {
		if c.Component == ports.ComponentExport {
			identityBytes = c.Size
		}
	}
	require.NotZero(t, identityBytes, "the backup carries no export to measure")
	assert.Less(t, moved, identityBytes*4,
		"more than the identity document and a manifest crossed the network")
}

// backupBytesOn is everything the target holds, which is what a naive
// implementation would transfer.
func backupBytesOn(t *testing.T, fake *fakes.BackupTarget) int64 {
	t.Helper()
	var total int64
	for _, key := range fake.Objects() {
		data, ok := fake.Object(key)
		require.True(t, ok)
		total += int64(len(data))
	}
	require.NotZero(t, total, "nothing was pushed to the target")
	return total
}

// TestAnUnencryptedExportComponentIsRefused.
//
// The recovery key is what anchors this whole path: an export the key never
// had to open is one an attacker with write access to a backup store could
// have written. Nothing produces an unencrypted component, which is exactly
// why refusing one costs nothing and closes the shape.
func TestAnUnencryptedExportComponentIsRefused(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	origin.wireBackupEngine(ctx)

	ref, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)

	// A backup whose export is plaintext and whose manifest says so, with
	// the digest updated so the binding check is not what refuses it.
	plantPlaintextExport(t, ref.Path)

	_, err = ops.ExportFromBackup(origin.Paths, ops.ExportFromBackupOptions{
		BackupID:     ref.ID,
		IdentityFile: recoveryPath,
	})
	require.Error(t, err, "an unencrypted identity document was accepted")
	assert.Contains(t, err.Error(), "not encrypted")
}

// plantPlaintextExport replaces the export with a readable one, consistently.
func plantPlaintextExport(t *testing.T, dir string) {
	t.Helper()

	path := filepath.Join(dir, ports.BackupManifestFileName)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var m ports.BackupManifest
	require.NoError(t, json.Unmarshal(data, &m))

	forged := []byte("api_version: v1alpha1\nkind: installation-export\n")
	for i, c := range m.Components {
		if c.Component != ports.ComponentExport {
			continue
		}
		require.NoError(t, os.Remove(filepath.Join(dir, c.Path)))
		name := strings.TrimSuffix(c.Path, agecrypt.Extension)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), forged, 0o600))

		sum := sha256.Sum256(forged)
		m.Components[i].Path = name
		m.Components[i].Encryption = ports.EncryptionNone
		m.Components[i].Size = int64(len(forged))
		m.Components[i].SHA256 = hex.EncodeToString(sum[:])
	}

	out, err := json.MarshalIndent(m, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0o600))
}

// TestClearingTheDigestDoesNotDisableTheBinding.
//
// `backup.json` is unsigned plaintext by design, so a check that only runs
// when a field is populated is a check an attacker turns off by deleting the
// field. Reproduced before it was fixed: with the digest cleared, an export
// swapped in from another backup was accepted and its identity imported.
func TestClearingTheDigestDoesNotDisableTheBinding(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	origin.wireBackupEngine(ctx)

	first, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)
	second := takeAnotherBackup(t, ctx, origin, first)

	name := "export.yaml" + agecrypt.Extension
	swapFiles(t,
		filepath.Join(origin.Paths.BackupsDir(), first.ID, name),
		filepath.Join(origin.Paths.BackupsDir(), second, name))
	clearExportDigest(t, filepath.Join(origin.Paths.BackupsDir(), first.ID))

	_, err = ops.ExportFromBackup(origin.Paths, ops.ExportFromBackupOptions{
		BackupID:     first.ID,
		IdentityFile: recoveryPath,
	})
	require.Error(t, err,
		"an export swapped in from another backup was accepted once the digest was "+
			"cleared, which is an edit to a plaintext file")
	assert.Contains(t, err.Error(), "records no digest")
}

// TestAManifestThatNamesADifferentBackupIsSkipped.
//
// Every step after the read uses the manifest's id as a directory name, so an
// id that disagrees with the directory it was read from makes the reader open
// a component from somewhere else. Reproduced before the fix with an id of
// `../../../../tmp`, which reached outside the backup store entirely.
func TestAManifestThatNamesADifferentBackupIsSkipped(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	origin.wireBackupEngine(ctx)

	ref, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)

	plantManifestWithID(t, filepath.Join(origin.Paths.BackupsDir(), "evil"),
		filepath.Join(origin.Paths.BackupsDir(), ref.ID), "../../../../tmp")

	_, err = ops.ExportFromBackup(origin.Paths, ops.ExportFromBackupOptions{
		BackupID:     "../../../../tmp",
		IdentityFile: recoveryPath,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no backup",
		"a manifest whose id is not its directory was still selectable")

	// The genuine backup beside it stays readable: skipping a bad neighbour
	// must not make the rest unreachable during a recovery.
	found, err := ops.ExportFromBackup(origin.Paths, ops.ExportFromBackupOptions{
		IdentityFile: recoveryPath,
	})
	require.NoError(t, err, "one forged directory made the real backups unreachable")
	assert.Equal(t, ref.ID, found.Backup.ID)
}

// clearExportDigest removes the export component's digest from a manifest.
func clearExportDigest(t *testing.T, dir string) {
	t.Helper()

	path := filepath.Join(dir, ports.BackupManifestFileName)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var m ports.BackupManifest
	require.NoError(t, json.Unmarshal(data, &m))
	for i := range m.Components {
		if m.Components[i].Component == ports.ComponentExport {
			m.Components[i].SHA256 = ""
		}
	}
	out, err := json.MarshalIndent(m, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0o600))
}

// plantManifestWithID copies a real manifest into a directory under a forged
// id, which is what an attacker with write access to a backup store can do.
func plantManifestWithID(t *testing.T, dir, from, id string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))

	data, err := os.ReadFile(filepath.Join(from, ports.BackupManifestFileName))
	require.NoError(t, err)

	var m ports.BackupManifest
	require.NoError(t, json.Unmarshal(data, &m))
	m.ID = id

	out, err := json.MarshalIndent(m, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ports.BackupManifestFileName), out, 0o600))
}

// TestAnIntactReplicaIsUsedWhenTheFirstIsDamaged.
//
// The same backup on two targets is normally the same bytes, and "normally" is
// doing the work: bit rot, a half-restored bucket or a tampered replica all
// list fine and fail to read. Taking the first copy and stopping would mean a
// recovery that fails while an intact copy sits on the next target.
func TestAnIntactReplicaIsUsedWhenTheFirstIsDamaged(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)
	origin.wireBackupEngine(ctx)

	ref, err := origin.Deps.Backup.Create(ctx, ports.Scope{
		Components: ports.AllComponents, Reason: "manual",
	}, nil)
	require.NoError(t, err)

	fake := fakes.NewBackupTarget()
	origin.Deps.Targets = fake

	damaged := ports.TargetRef{Scheme: "memory", Path: "/one"}
	intact := ports.TargetRef{Scheme: "memory", Path: "/two"}
	for _, target := range []ports.TargetRef{damaged, intact} {
		_, pushErr := fake.Push(ctx, target, ref.Path, ref.ID)
		require.NoError(t, pushErr)
	}

	// One replica's identity document is corrupted, the way storage
	// corrupts things: same length, different bytes, manifest untouched.
	key := "one/" + ref.ID + "/export.yaml" + agecrypt.Extension
	original, ok := fake.Object(key)
	require.True(t, ok, "the export is not on the target under %s", key)
	rotted := make([]byte, len(original))
	copy(rotted, original)
	for i := range rotted {
		rotted[i] ^= 0xff
	}
	fake.SetObject(key, rotted)

	// Named individually first, so the damage is real rather than assumed.
	_, err = ops.ExportFromRemoteBackup(ctx, origin.Deps,
		ops.TargetOptions{URL: "memory:///one"}, "", recoveryPath)
	require.Error(t, err, "the damaged replica must not read as intact")

	// Then both in one invocation, damaged first, which is the case the
	// fallback exists for. Naming them one at a time never puts two
	// replicas in scope and would pass with the fallback deleted.
	inst, err := origin.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Backup.Targets = []domain.BackupTargetConfig{
		{URL: "memory:///one"},
		{URL: "memory:///two"},
	}
	require.NoError(t, origin.Deps.State.SaveInstallation(ctx, inst))

	found, err := ops.ExportFromRemoteBackup(ctx, origin.Deps,
		ops.TargetOptions{}, "", recoveryPath)
	require.NoError(t, err,
		"recovery failed on the damaged replica while an intact copy sat on the "+
			"next target")
	assert.Equal(t, "demo", found.Export.Installation.Product)
	assert.Equal(t, ref.ID, found.Backup.ID)
}
