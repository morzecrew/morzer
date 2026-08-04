//go:build docker

package suite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/backup/hookbackup"
	"github.com/morzecrew/morzer/internal/adapters/hooks"
	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/test/dockerlab"
)

// A backup that has never been restored is a hope, not a backup.
//
// Everything else in this project's backup tests uses a hook that writes a
// text file, which proves the manager's half of the contract -- ordering, the
// manifest, checksums, retention -- and nothing about whether the data comes
// back. These use `pg_dump` and `psql` against a real Postgres, drop the rows
// in between, and query them back afterwards.
//
// **Scope discipline**, per RFC 0008 §5.4: none of this tests Postgres.
// "Postgres restored the rows" is the fixture; what is being asserted is that
// the engine ran the hooks in the right order, recorded what they produced,
// refused what it should refuse, and did not corrupt anything on the way
// through.

const (
	pgUser     = "postgres"
	pgPassword = "test-only-not-a-secret"
	pgDatabase = "appdb"
)

// pgFixture is a running Postgres with a table in it.
type pgFixture struct {
	container *dockerlab.Container
}

func startPostgres(t *testing.T) *pgFixture {
	t.Helper()
	dockerlab.Require(t)

	c := dockerlab.Start(t, dockerlab.ImagePostgres, []int{5432}, map[string]string{
		"POSTGRES_PASSWORD": pgPassword,
		"POSTGRES_USER":     pgUser,
		"POSTGRES_DB":       pgDatabase,
	})

	// Readiness is checked over TCP, not the socket. The entrypoint runs a
	// throwaway server during initialisation with `listen_addresses=''`, so
	// it answers on the unix socket while refusing every TCP connection --
	// and a `pg_isready` without -h reports that one as ready, moments
	// before it shuts down to hand over to the real server. Probing the
	// port is what distinguishes them.
	c.WaitReady(t, 2*time.Minute, "pg_isready", "-h", "127.0.0.1", "-U", pgUser, "-d", pgDatabase)

	f := &pgFixture{container: c}
	f.sql(t, `CREATE TABLE customers (id int primary key, name text);`)
	f.sql(t, `INSERT INTO customers VALUES (1, 'ada'), (2, 'grace'), (3, 'katherine');`)
	return f
}

func (f *pgFixture) sql(t *testing.T, statement string) string {
	t.Helper()
	out, err := f.container.Exec(t, "psql", "-U", pgUser, "-d", pgDatabase,
		"--no-align", "--tuples-only", "-c", statement)
	require.NoError(t, err, "psql: %s", out)
	return strings.TrimSpace(out)
}

func (f *pgFixture) rowCount(t *testing.T) string {
	t.Helper()
	return f.sql(t, `SELECT count(*) FROM customers;`)
}

// pgRelease copies the example bundle and replaces its backup and restore
// hooks with ones that really dump and really load.
//
// The hooks reach the database with `docker exec` rather than a host psql:
// what is under test is the manager's coordination, and requiring a Postgres
// client on the developer's machine would make the suite skip on exactly the
// machines it is meant to run on.
func pgRelease(t *testing.T, container string) domain.Release {
	t.Helper()

	dir := bundleCopy(t)

	writeHook(t, filepath.Join(dir, "hooks", "backup"), fmt.Sprintf(`#!/bin/sh
set -eu
dump="$DEMO_BACKUP_DIR/database.sql"
docker exec %[1]s pg_dump -U %[2]s --clean --if-exists %[3]s > "$dump"
size=$(wc -c < "$dump" | tr -d ' ')
echo "dumped $size bytes"
printf '{"message":"dumped %%s bytes","schema_version":11,"artifacts":[{"name":"db","path":"database.sql"}]}' "$size" >&3
`, container, pgUser, pgDatabase))

	writeHook(t, filepath.Join(dir, "hooks", "restore"), fmt.Sprintf(`#!/bin/sh
set -eu
dump="$DEMO_BACKUP_DIR/database.sql"
test -f "$dump" || { echo "no dump in the backup directory" >&2; exit 1; }
docker exec -i %[1]s psql -U %[2]s -d %[3]s --quiet -v ON_ERROR_STOP=1 < "$dump"
echo "restored from $dump"
`, container, pgUser, pgDatabase))

	rel, err := release.Load(dir)
	require.NoError(t, err)
	return rel
}

func bundleCopy(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)
	src := filepath.Join(wd, "..", "..", "testdata", "bundle")
	dst := filepath.Join(t.TempDir(), "release")

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
	return dst
}

func writeHook(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(body), 0o700))
}

func pgEngine(t *testing.T, rel domain.Release, root string) (*hookbackup.Engine, string) {
	t.Helper()

	paths := domain.PathsUnder(root, "demo")
	identity := paths.AgeIdentityFile()
	public, err := sopsage.GenerateIdentity(identity)
	require.NoError(t, err)

	return hookbackup.New(hookbackup.Config{
		Hooks:          hooks.NewRunner(infraexec.New()),
		Release:        rel,
		Installation:   domain.Installation{ID: "inst-postgres-fixture", Product: "demo"},
		Paths:          paths,
		ManagerVersion: "0.0.0-test",
		Now:            func() time.Time { return time.Now().UTC() },
		Recipients: func(context.Context) ([]string, error) {
			return []string{public}, nil
		},
	}), identity
}

// TestABackupOfARealDatabaseCanBeRestored is the round trip. Everything in the
// backup design exists to make this one sentence true.
func TestABackupOfARealDatabaseCanBeRestored(t *testing.T) {
	pg := startPostgres(t)
	root := t.TempDir()
	rel := pgRelease(t, pg.container.Name)
	engine, _ := pgEngine(t, rel, root)
	ctx := context.Background()

	require.Equal(t, "3", pg.rowCount(t), "the fixture did not load")

	ref, err := engine.Create(ctx, ports.Scope{Reason: "pre-update"},
		map[string]string{"taken-by": "the-test"})
	require.NoError(t, err)
	require.NotEmpty(t, ref.ID)
	assert.Greater(t, ref.Size, int64(0), "the backup reports a size of zero, so "+
		"retention has nothing to account for")

	// The dump is there, and it is not readable. This is the whole claim:
	// a backup that leaves the machine carries no data with it.
	stored, err := os.ReadFile(filepath.Join(ref.Path, "database.sql.age"))
	require.NoError(t, err, "the encrypted dump is not where the manifest says")
	assert.NotContains(t, string(stored), "katherine",
		"the stored dump is readable, so anyone who finds the file has the data")

	_, err = os.Stat(filepath.Join(ref.Path, "database.sql"))
	assert.Error(t, err, "the plaintext dump was left beside the encrypted one")

	// Nothing in the directory is readable except the manifest, which stays
	// plaintext so `backup list` works on a machine whose key is gone.
	entries, err := os.ReadDir(ref.Path)
	require.NoError(t, err)
	for _, e := range entries {
		if e.Name() == hookbackup.ManifestFileName {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(ref.Path, e.Name()))
		require.NoError(t, readErr)
		assert.True(t, strings.HasPrefix(string(body), "age-encryption.org/"),
			"%s is not encrypted", e.Name())
	}

	// What the manager recorded about it.
	manifest, err := engine.Inspect(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, "inst-postgres-fixture", manifest.InstallationID)
	assert.Equal(t, 11, manifest.SchemaAtBackup,
		"the schema version the hook reported was dropped, and rollback needs it")
	assert.Equal(t, "pre-update", manifest.Reason)
	assert.Equal(t, "the-test", manifest.Labels["taken-by"])

	var recorded ports.ComponentRecord
	for _, c := range manifest.Components {
		if c.Path == "database.sql.age" {
			recorded = c
		}
	}
	require.NotEmpty(t, recorded.SHA256,
		"the manager did not checksum the hook's artifact, so the backup "+
			"cannot be verified")
	assert.Equal(t, ports.ComponentDatabase, recorded.Component)
	assert.Equal(t, ports.EncryptionAge, recorded.Encryption,
		"the manifest does not record how the component is stored, so a restore "+
			"cannot know whether to decrypt it")
	assert.Equal(t, int64(len(stored)), recorded.Size,
		"the recorded size is not the size of the stored file, which is what "+
			"`backup verify` compares against")

	require.NoError(t, engine.Verify(ctx, ref))

	// Now lose the data, the way an operator does.
	pg.sql(t, `DELETE FROM customers;`)
	require.Equal(t, "0", pg.rowCount(t))

	require.NoError(t, engine.Restore(ctx, ref, ports.RestoreOptions{
		TargetInstallationID: "inst-postgres-fixture",
	}))

	assert.Equal(t, "3", pg.rowCount(t), "the restore ran and the rows did not come back")
	assert.Equal(t, "katherine", pg.sql(t, `SELECT name FROM customers WHERE id = 3;`))
}

// TestARestoreIsRefusedWhenTheBackupIsCorrupt is the check that stands between
// a bad backup and a working system.
//
// Restoring a corrupt dump over a live database is the worst outcome available
// here, and it is worse than the failure that prompted the restore.
func TestARestoreIsRefusedWhenTheBackupIsCorrupt(t *testing.T) {
	pg := startPostgres(t)
	root := t.TempDir()
	engine, _ := pgEngine(t, pgRelease(t, pg.container.Name), root)
	ctx := context.Background()

	ref, err := engine.Create(ctx, ports.Scope{Reason: "manual"}, nil)
	require.NoError(t, err)

	// A single flipped byte, which is what bit rot looks like.
	dumpPath := filepath.Join(ref.Path, "database.sql.age")
	data, err := os.ReadFile(dumpPath)
	require.NoError(t, err)
	data[len(data)/2] ^= 0x20
	require.NoError(t, os.WriteFile(dumpPath, data, 0o600))

	// Two guards now, and the checksum is the one that needs no key.
	err = engine.Verify(ctx, ref)
	require.Error(t, err, "a corrupted dump passed verification")
	assert.Contains(t, err.Error(), "checksum mismatch")
	assert.Contains(t, domain.AsError(err).Hint, "fresh",
		"the operator is told the backup failed but not what to do about it")

	// And the restore refuses on its own, without the caller having to
	// remember to verify first.
	pg.sql(t, `DELETE FROM customers;`)
	err = engine.Restore(ctx, ref, ports.RestoreOptions{})
	require.Error(t, err, "a corrupt backup was restored over a live database")
	assert.Equal(t, "0", pg.rowCount(t),
		"the refused restore still ran the hook and touched the data")
}

// TestARestoreIsRefusedAcrossInstallations protects the case where two
// machines' backups end up in the same directory.
func TestARestoreIsRefusedAcrossInstallations(t *testing.T) {
	pg := startPostgres(t)
	root := t.TempDir()
	engine, _ := pgEngine(t, pgRelease(t, pg.container.Name), root)
	ctx := context.Background()

	ref, err := engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	pg.sql(t, `DELETE FROM customers;`)

	err = engine.Restore(ctx, ref, ports.RestoreOptions{
		TargetInstallationID: "some-other-machine",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inst-postgres-fixture",
		"the refusal has to name which installation the backup belongs to")
	assert.Contains(t, domain.AsError(err).Hint, "installation import",
		"the hint has to point at the recovery path, or a rebuilt machine is stuck")
	assert.Equal(t, "0", pg.rowCount(t), "the refused restore ran anyway")

	// The deliberate override does go through, or a genuine migration
	// between machines would be impossible.
	require.NoError(t, engine.Restore(ctx, ref, ports.RestoreOptions{
		TargetInstallationID: "some-other-machine", Force: true,
	}))
	assert.Equal(t, "3", pg.rowCount(t))
}

// TestAFailedBackupLeavesNothingBehind: a partial backup directory that looks
// like a backup is worse than no backup, because somebody will try to restore
// it.
func TestAFailedBackupLeavesNothingBehind(t *testing.T) {
	pg := startPostgres(t)
	root := t.TempDir()
	rel := pgRelease(t, pg.container.Name)

	// A hook that fails after writing something, which is what a dump of a
	// database that goes away mid-run looks like.
	writeHook(t, filepath.Join(rel.Root, "hooks", "backup"), `#!/bin/sh
set -eu
echo "partial" > "$DEMO_BACKUP_DIR/database.sql"
echo "pg_dump: error: connection to server was lost" >&2
exit 1
`)

	engine, _ := pgEngine(t, rel, root)
	ctx := context.Background()

	_, err := engine.Create(ctx, ports.Scope{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backup hook failed")
	assert.Contains(t, err.Error(), "connection to server was lost",
		"the hook's own diagnostic is what tells the operator what broke")

	// Nothing a later `backup list` could mistake for a backup.
	refs, err := engine.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, refs, "a failed backup left a directory behind")

	entries, err := os.ReadDir(domain.PathsUnder(root, "demo").BackupsDir())
	if err == nil {
		assert.Empty(t, entries, "the partial backup directory was not removed")
	}
}

// TestAHookThatWritesOutsideTheBackupDirectoryIsRefused. An artifact the
// manager cannot manage would not be pruned, moved or restored with the rest,
// so it is refused rather than recorded.
func TestAHookThatWritesOutsideTheBackupDirectoryIsRefused(t *testing.T) {
	pg := startPostgres(t)
	root := t.TempDir()
	rel := pgRelease(t, pg.container.Name)
	stray := filepath.Join(t.TempDir(), "elsewhere.sql")

	writeHook(t, filepath.Join(rel.Root, "hooks", "backup"), fmt.Sprintf(`#!/bin/sh
set -eu
docker exec %[1]s pg_dump -U %[2]s %[3]s > %[4]s
printf '{"artifacts":[{"name":"db","path":"%[4]s"}]}' >&3
`, pg.container.Name, pgUser, pgDatabase, stray))

	backupEngine, _ := pgEngine(t, rel, root)
	_, err := backupEngine.Create(context.Background(), ports.Scope{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the backup directory")
	assert.Contains(t, domain.AsError(err).Hint, "BACKUP_DIR",
		"the hint has to name the variable the hook was supposed to use")
}

// TestPruneKeepsTheReasonsItWasToldTo covers retention against real dumps,
// where "keep 1" and "keep the pre-update one" can disagree.
func TestPruneKeepsTheReasonsItWasToldTo(t *testing.T) {
	pg := startPostgres(t)
	root := t.TempDir()
	engine, _ := pgEngine(t, pgRelease(t, pg.container.Name), root)
	ctx := context.Background()

	reasons := []string{"scheduled", "pre-update", "manual"}
	made := make([]ports.BackupRef, 0, len(reasons))
	for _, reason := range reasons {
		// The id is a UTC timestamp to the second, so two backups taken
		// inside one second would collide.
		time.Sleep(1100 * time.Millisecond)
		ref, err := engine.Create(ctx, ports.Scope{Reason: reason}, nil)
		require.NoError(t, err)
		made = append(made, ref)
	}

	listed, err := engine.List(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 3)
	assert.Equal(t, made[2].ID, listed[0].ID, "List must be newest first")

	removed, err := engine.Prune(ctx, ports.RetentionPolicy{
		Keep: 1, KeepReasons: []string{"pre-update"},
	})
	require.NoError(t, err)

	remaining, err := engine.List(ctx)
	require.NoError(t, err)

	kept := map[string]bool{}
	for _, r := range remaining {
		m, err := engine.Inspect(ctx, r)
		require.NoError(t, err)
		kept[m.Reason] = true
	}
	assert.True(t, kept["manual"], "the newest backup was pruned")
	assert.True(t, kept["pre-update"], "an exempt reason was pruned anyway, which "+
		"is how a rollback loses the copy taken to make it possible")
	assert.False(t, kept["scheduled"], "nothing was actually pruned")
	assert.Len(t, removed, 1)

	// Every surviving backup still restores, which is the only thing
	// retention is for.
	for _, r := range remaining {
		require.NoError(t, engine.Verify(ctx, r))
	}
}

// TestPruneNeverRemovesTheOnlyCopy: a retention policy of zero is a
// configuration mistake, and honouring it literally would delete the only copy
// of the data.
func TestPruneNeverRemovesTheOnlyCopy(t *testing.T) {
	pg := startPostgres(t)
	root := t.TempDir()
	engine, _ := pgEngine(t, pgRelease(t, pg.container.Name), root)
	ctx := context.Background()

	_, err := engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	removed, err := engine.Prune(ctx, ports.RetentionPolicy{Keep: 0})
	require.NoError(t, err)
	assert.Empty(t, removed)

	remaining, err := engine.List(ctx)
	require.NoError(t, err)
	assert.Len(t, remaining, 1, "a retention policy of zero deleted the only backup")
}

// TestASchemaOneBackupStillRestores is the compatibility rule this project
// applies everywhere: a new manager reads an old backup.
//
// A backup taken before backups were encrypted holds a plaintext dump and a
// manifest with no `encryption` field. It has to restore untouched, because an
// operator upgrading the manager has a directory full of them and no way to
// re-take one for a machine that is already gone.
func TestASchemaOneBackupStillRestores(t *testing.T) {
	pg := startPostgres(t)
	root := t.TempDir()
	rel := pgRelease(t, pg.container.Name)
	engine, identity := pgEngine(t, rel, root)
	ctx := context.Background()

	ref, err := engine.Create(ctx, ports.Scope{Reason: "manual"}, nil)
	require.NoError(t, err)

	// Rewrite it into the shape the previous manager produced: plaintext
	// artifacts, no encryption field, schema 1.
	downgradeToSchemaOne(t, ctx, ref.Path, engine, identity)

	pg.sql(t, `DELETE FROM customers;`)
	require.Equal(t, "0", pg.rowCount(t))

	require.NoError(t, engine.Restore(ctx, ref, ports.RestoreOptions{
		TargetInstallationID: "inst-postgres-fixture",
	}), "a backup taken by an older manager could not be restored")

	assert.Equal(t, "3", pg.rowCount(t))
}

// downgradeToSchemaOne decrypts a backup in place and rewrites its manifest,
// producing exactly what the previous manager wrote.
func downgradeToSchemaOne(
	t *testing.T, ctx context.Context, dir string, engine *hookbackup.Engine, identity string,
) {
	t.Helper()

	manifest, err := engine.Inspect(ctx, ports.BackupRef{Path: dir})
	require.NoError(t, err)

	components := make([]ports.ComponentRecord, 0, len(manifest.Components))
	for _, c := range manifest.Components {
		sealed, openErr := os.Open(filepath.Join(dir, c.Path))
		require.NoError(t, openErr)

		plainName := strings.TrimSuffix(c.Path, ".age")
		out, createErr := os.OpenFile(filepath.Join(dir, plainName),
			os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		require.NoError(t, createErr)

		require.NoError(t, agecrypt.Decrypt(out, sealed, identity))
		require.NoError(t, out.Close())
		require.NoError(t, sealed.Close())
		require.NoError(t, os.Remove(filepath.Join(dir, c.Path)))

		sum, digestErr := atomicfs.DigestFile(filepath.Join(dir, plainName))
		require.NoError(t, digestErr)
		info, statErr := os.Stat(filepath.Join(dir, plainName))
		require.NoError(t, statErr)

		components = append(components, ports.ComponentRecord{
			Component: c.Component, Path: plainName,
			Size: info.Size(), SHA256: sum,
		})
	}

	manifest.SchemaVersion = 1
	manifest.Components = components
	body, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, hookbackup.ManifestFileName), append(body, '\n'), 0o600))
}
