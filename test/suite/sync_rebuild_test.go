package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/adapters/target"
	"github.com/morzecrew/morzer/internal/adapters/target/localdir"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// A machine rebuilt with `import` keeps its installation and what it applied,
// and gets a new age identity and signing key (RFC 0057 §6). A document
// encrypted to the lost machine is refused as undecryptable. Once the roster
// names the new key, the authority's next document is encrypted to the
// recipient the rebuilt machine's row names, and applies.
//
// Real sops and the real signer, as the recovery scenario has: what is under
// test is what survives a rebuild, which a fake store would answer about
// itself.
func TestARebuiltMachineFollowsOnceTheAuthorityEncryptsToItsNewRecipient(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()
	offsite := filepath.Join(t.TempDir(), "offsite")
	url := "file://" + offsite

	bundle := filepath.Join(t.TempDir(), "bundle-1.2.0")
	copyBundle(t, testBundlePath(t), bundle)
	path := filepath.Join(bundle, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	patched := strings.Replace(string(raw), `min_manager_version: "0.0.0"`, `min_manager_version: "0.16.0"`, 1) +
		"\nmanaged:\n  parameters: [log_level]\n"
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))

	authority := signer.New(filepath.Join(t.TempDir(), "authority.key"), "authority")
	authorityKey, err := authority.EnsureKey(ctx)
	require.NoError(t, err)

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := newMachine(t, t.TempDir())
	followsAnAuthority(t, origin)
	_, err = ops.Init(ctx, origin.Deps, ops.InitOptions{
		Product: "demo", ReleasePath: bundle, Profiles: []string{"embedded"}, Domains: []string{"demo.example"},
		RecoveryRecipient: recoveryPub, GenerateSecrets: true,
		SyncTarget: url, SyncAuthority: authorityKey.Line,
	})
	require.NoError(t, err)
	inst, err := origin.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Sync.Enabled = true
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: url}}
	require.NoError(t, origin.Deps.State.SaveInstallation(ctx, inst))
	_, err = ops.FleetPublish(ctx, origin.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)
	inst, err = origin.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)

	roster := domain.FleetRoster{Schema: 1, Installations: []domain.FleetRosterEntry{{
		Product: inst.Product, ID: inst.ID, PublicKey: inst.Signing.PublicKey,
	}}}
	publish := func(d *ops.Deps, serial int64, doc string) ops.StatePublishReport {
		t.Helper()
		report, err := ops.StatePublish(ctx, d, ops.StatePublishOptions{
			TargetOptions: ops.TargetOptions{URL: url}, Roster: roster, Product: inst.Product,
			InstallationID: inst.ID, Document: []byte(doc), Signer: authority, Serial: serial,
		})
		require.NoError(t, err)
		return report
	}
	sync := func(m *machine) ops.SyncReport {
		t.Helper()
		report, err := ops.SyncNow(ctx, m.Deps, ops.SyncOptions{})
		require.NoError(t, err)
		return report
	}

	before := publish(origin.Deps, 10, "parameters:\n  log_level: debug\n")
	require.Equal(t, domain.SyncOK, sync(origin).Result)

	exportPath := filepath.Join(t.TempDir(), "demo.export.yaml")
	_, err = ops.Export(ctx, origin.Deps, ops.ExportOptions{Path: exportPath})
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(origin.Root))

	// The authority has not heard, and encrypts to the recipient the last
	// row named.
	stale := publish(origin.Deps, 20, "parameters:\n  log_level: warn\n")
	assert.Equal(t, before.Recipient, stale.Recipient)

	rebuilt := newMachine(t, t.TempDir())
	followsAnAuthority(t, rebuilt)
	export, err := ops.LoadExport(exportPath)
	require.NoError(t, err)
	_, err = ops.Import(ctx, rebuilt.Deps, ops.ImportOptions{
		SourcePath: exportPath, Export: export, IdentityFile: recoveryPath,
	})
	require.NoError(t, err)
	got, err := rebuilt.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	require.Equal(t, inst.Sync.Authority, got.Sync.Authority, "the rebuilt machine forgot the authority it follows")
	require.NotNil(t, got.Sync.Applied)
	assert.Equal(t, before.Serial, got.Sync.Applied.Serial, "the rebuilt machine forgot the document it applied")

	// Import restores identity, not software: the release comes back with
	// `update`, as after any rebuild.
	_, err = ops.Update(ctx, rebuilt.Deps, ops.UpdateOptions{Ref: bundle})
	require.NoError(t, err)

	report := sync(rebuilt)
	assert.Equal(t, domain.SyncUndecryptable, report.Result, report.Summary())
	assert.Equal(t, "debug", parameter(t, rebuilt, "log_level"))

	_, err = ops.FleetPublish(ctx, rebuilt.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)
	row, _ := publishedRow(t, offsite, inst)
	assert.NotEqual(t, before.Recipient, row.AgeRecipient, "the rebuilt machine's row names the lost machine's recipient")

	// A rebuilt machine signs with a key of its own (RFC 0028 §5.3), which the
	// roster has to learn out of band before it vouches for the row again.
	assert.NotEqual(t, inst.Signing.PublicKey, row.SigningKey)
	_, err = ops.StatePublish(ctx, rebuilt.Deps, ops.StatePublishOptions{
		TargetOptions: ops.TargetOptions{URL: url}, Roster: roster, Product: inst.Product,
		InstallationID: inst.ID, Document: []byte("parameters:\n  log_level: warn\n"), Signer: authority, Serial: 30,
	})
	require.Error(t, err, "the roster vouched for a key it does not name")
	assert.Contains(t, err.Error(), "does not verify against the roster's key")
	roster.Installations[0].PublicKey = row.SigningKey

	after := publish(rebuilt.Deps, 30, "parameters:\n  log_level: warn\n")
	assert.Equal(t, row.AgeRecipient, after.Recipient)
	require.Equal(t, domain.SyncOK, sync(rebuilt).Result)
	assert.Equal(t, "warn", parameter(t, rebuilt, "log_level"))
}

// followsAnAuthority wires what a machine needs to read documents and publish
// its row: an object store over the target registry, and a signature checker.
func followsAnAuthority(t *testing.T, m *machine) {
	t.Helper()
	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	m.Deps.Targets, m.Deps.Objects = registry, registry
	m.Deps.Checker = signer.NewChecker()
}

func parameter(t *testing.T, m *machine, name string) string {
	t.Helper()
	inst, err := m.Deps.State.LoadInstallation(context.Background())
	require.NoError(t, err)
	return inst.Parameters[name]
}
