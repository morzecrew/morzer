package suite

import (
	"bytes"
	"context"
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
