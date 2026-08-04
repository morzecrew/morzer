package suite

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/target"
	"github.com/morzecrew/morzer/internal/adapters/target/localdir"
	"github.com/morzecrew/morzer/internal/adapters/target/sftp"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// The contract suite cannot interrupt a push -- it holds only the port, and the
// port has no way to say "fail after the second file". These tests reach past
// it, at the one shared package that decides what an interrupted transfer
// leaves behind. Every adapter runs that code, so proving it once here proves
// it for all three.

func TestAnInterruptedPushLeavesNothingRestorable(t *testing.T) {
	ctx := context.Background()

	fake := fakes.NewBackupTarget()
	fake.FailPushAt = "database.sql.age"
	ref := ports.TargetRef{Scheme: "memory", Path: "/backups"}

	local := writeTestBackup(t, "20260101T000000Z", map[string]string{
		"database.sql.age":  "the big one",
		"secrets.sops.yaml": "small",
	})

	_, err := fake.Push(ctx, ref, local, "20260101T000000Z")
	require.Error(t, err, "a target that goes away mid-push must fail the push")

	// The whole point of writing the manifest last. Some bytes did land --
	// that is unavoidable and fine. What must not have landed is anything
	// that makes this look like a backup somebody could restore.
	manifests, err := fake.List(ctx, ref)
	require.NoError(t, err)
	assert.Empty(t, manifests,
		"a half-pushed backup was listed as a backup; someone will eventually "+
			"restore from it, and it is missing a component")

	objects := fake.Objects()
	for _, key := range objects {
		assert.NotContains(t, key, ports.BackupManifestFileName,
			"the manifest reached the target although the push failed")
	}
}

func TestAnInterruptedRemovalLeavesNothingRestorable(t *testing.T) {
	ctx := context.Background()

	// Removal is the mirror: manifest first, so a removal that dies halfway
	// leaves the same invisible wreckage a push does rather than a backup
	// with a component missing.
	dir := t.TempDir()
	ref, err := ports.TargetURL("file://" + filepath.Join(dir, "offsite"))
	require.NoError(t, err)

	adapter := localdir.New()
	local := writeTestBackup(t, "20260101T000000Z", map[string]string{
		"database.sql.age": "data",
	})
	remote, err := adapter.Push(ctx, ref, local, "20260101T000000Z")
	require.NoError(t, err)

	// Simulate the interruption directly: delete the manifest and stop,
	// which is exactly the state Remove passes through.
	require.NoError(t, os.Remove(filepath.Join(dir, "offsite", "20260101T000000Z",
		ports.BackupManifestFileName)))

	manifests, err := adapter.List(ctx, ref)
	require.NoError(t, err)
	assert.Empty(t, manifests,
		"a backup whose manifest is gone must be invisible, not a backup with "+
			"nothing to describe it")

	// And the remaining files are still cleaned up when the removal is
	// retried, which is what makes retention self-healing.
	require.NoError(t, adapter.Remove(ctx, remote))
	entries, err := os.ReadDir(filepath.Join(dir, "offsite"))
	require.NoError(t, err)
	assert.Empty(t, entries, "the retried removal left the components behind")
}

// TestAFetchCannotBeToldToWriteOutsideItsDestination. A manifest on a target is
// a file this manager may not have written -- that is the whole premise of a
// target being somewhere else. A component path of "../../etc/passwd" must not
// be a way for whoever controls the bucket to decide where a fetch writes.
func TestAFetchCannotBeToldToWriteOutsideItsDestination(t *testing.T) {
	ctx := context.Background()

	dir := t.TempDir()
	root := filepath.Join(dir, "offsite")
	ref, err := ports.TargetURL("file://" + root)
	require.NoError(t, err)

	// A hostile backup, written by hand rather than pushed.
	backupDir := filepath.Join(root, "20260101T000000Z")
	require.NoError(t, os.MkdirAll(backupDir, 0o700))

	manifest := ports.BackupManifest{
		SchemaVersion: 2,
		ID:            "20260101T000000Z",
		Product:       "demo",
		CreatedAt:     domain.NewTime(mustTime(t, "20260101T000000Z")),
		Components: []ports.ComponentRecord{
			{Component: ports.ComponentDatabase, Path: "../../escaped.txt", Size: 3},
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(backupDir, ports.BackupManifestFileName),
		data, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "escaped.txt"), []byte("own"), 0o600))

	err = localdir.New().Fetch(ctx,
		ports.RemoteRef{Target: ref, ID: "20260101T000000Z"},
		filepath.Join(t.TempDir(), "dest"))
	require.Error(t, err, "a component path escaping the backup was followed")
	assert.Contains(t, strings.ToLower(domain.AsError(err).Message), "outside")
}

// TestATargetIsRefusedWhenItIsWhereTheBackupAlreadyIs. Pointing a target at the
// backup directory would half-work -- every file copied over itself -- and the
// operator would believe they had an off-machine copy of everything.
func TestATargetIsRefusedWhenItIsWhereTheBackupAlreadyIs(t *testing.T) {
	local := writeTestBackup(t, "20260101T000000Z", map[string]string{"database.sql.age": "data"})

	ref, err := ports.TargetURL("file://" + filepath.Dir(local))
	require.NoError(t, err)

	_, err = localdir.New().Push(context.Background(), ref, local, "20260101T000000Z")
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "disk failing")
}

// TestATargetURLIsRefusedWhenItCarriesAPassword. The URL lands in
// installation.yaml, is printed by `doctor` and is echoed in every error
// message about the target. A password in it is a password in the journal.
func TestATargetURLIsRefusedWhenItCarriesAPassword(t *testing.T) {
	_, err := ports.TargetURL("ssh://backup:hunter2@host/srv/backups")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2",
		"the refusal quoted the password it was refusing")
	assert.Contains(t, domain.AsError(err).Hint, "--credentials")
}

func TestTargetURLGrammar(t *testing.T) {
	for name, tc := range map[string]struct {
		url    string
		scheme string
		bucket string
		prefix string
		bad    bool
	}{
		"a local directory":  {url: "file:///mnt/usb/backups", scheme: "file"},
		"an ssh host":        {url: "ssh://ops@backups.example/srv/backups", scheme: "ssh"},
		"a bucket":           {url: "s3://acme-backups/demo", scheme: "s3", bucket: "acme-backups", prefix: "demo"},
		"a bucket root":      {url: "s3://acme-backups", scheme: "s3", bucket: "acme-backups"},
		"no scheme":          {url: "/mnt/usb/backups", bad: true},
		"a bare file scheme": {url: "file://", bad: true},
		// file://host/path is legal URL syntax and means something under
		// SMB. Accepting it here would write somewhere other than the
		// path the operator read in their own configuration.
		"a file url with a host": {url: "file://nas/backups", bad: true},
		"ssh with no path":       {url: "ssh://backups.example", bad: true},
		"an https url":           {url: "https://backups.example/x", bad: true},
		// localhost is the one host a file:// URL may name, because it
		// means the same thing as naming none.
		"file with localhost":  {url: "file://localhost/mnt/backups", scheme: "file"},
		"ssh with a port":      {url: "ssh://ops@backups.example:2222/srv/x", scheme: "ssh"},
		"a bucket with a path": {url: "s3://acme/demo/nightly", scheme: "s3", bucket: "acme", prefix: "demo/nightly"},
		"an empty string":      {url: "", bad: true},
		"only a scheme marker": {url: "://x", bad: true},
		"a plaintext http url": {url: "http://backups.example/x", bad: true},
	} {
		t.Run(name, func(t *testing.T) {
			ref, err := ports.TargetURL(tc.url)
			if tc.bad {
				require.Error(t, err)
				assert.NotEmpty(t, domain.AsError(err).Hint,
					"a refusal about a URL must say what a good one looks like")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.scheme, ref.Scheme)

			if tc.bucket != "" {
				bucket, prefix := ref.Bucket()
				assert.Equal(t, tc.bucket, bucket)
				assert.Equal(t, tc.prefix, prefix)
			}
		})
	}
}

// TestATargetRegistryClosesEveryTarget. A target that holds an SSH connection
// has to be closed, and one that cannot tidy up must not stop the next one from
// trying.
func TestATargetRegistryClosesEveryTarget(t *testing.T) {
	// The embedded fake is initialised even though this test calls only
	// Schemes and Close: a nil embed makes every promoted method a panic
	// waiting for the first person to add an assertion.
	first := &closableTarget{BackupTarget: fakes.NewBackupTarget(), scheme: "one"}
	second := &closableTarget{BackupTarget: fakes.NewBackupTarget(), scheme: "two", err: assertError}

	registry, err := target.NewRegistry(first, second)
	require.NoError(t, err)

	err = registry.Close()
	require.Error(t, err, "a target that failed to close must be reported")
	assert.True(t, first.closed && second.closed, "every target must be closed even after one fails")
}

var assertError = domain.Internal(nil, "cannot close")

// requireNonRoot skips a test whose premise is that a permission bit stops a
// write. Root ignores them, so the test would pass without exercising anything
// -- which is worse than not running it, because the report says it did.
func requireNonRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root, which bypasses the permission bits this test relies on")
	}
}

type closableTarget struct {
	*fakes.BackupTarget
	scheme string
	err    error
	closed bool
}

func (c *closableTarget) Schemes() []string { return []string{c.scheme} }
func (c *closableTarget) Close() error {
	c.closed = true
	return c.err
}

// writeTestBackup builds a backup directory the way hookbackup does.
func writeTestBackup(t *testing.T, id string, files map[string]string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), id)
	require.NoError(t, os.MkdirAll(dir, 0o700))

	manifest := ports.BackupManifest{
		SchemaVersion:  2,
		ID:             id,
		InstallationID: "inst_test",
		Product:        "demo",
		ReleaseVersion: domain.MustParseVersion("1.0.0"),
		CreatedAt:      domain.NewTime(mustTime(t, id)),
		ManagerVersion: "0.0.0",
	}
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
		manifest.Components = append(manifest.Components, ports.ComponentRecord{
			Component:  ports.ComponentDatabase,
			Path:       name,
			Size:       int64(len(content)),
			Encryption: ports.EncryptionAge,
		})
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ports.BackupManifestFileName), data, 0o600))
	return dir
}

func mustTime(t *testing.T, id string) time.Time {
	t.Helper()
	parsed, err := time.Parse("20060102T150405Z", id)
	require.NoError(t, err)
	return parsed
}

// TestASymlinkAmongComponentsIsNotFollowed.
//
// A backup directory belongs to the manager, but a manifest is data and a
// component path is a string. Following a symlink would copy a file from
// outside the backup — the machine's age identity, say — onto a second machine
// nobody meant to put it on.
func TestASymlinkAmongComponentsIsNotFollowed(t *testing.T) {
	local := writeTestBackup(t, "20260101T000000Z", map[string]string{
		"database.sql.age": "ciphertext",
	})

	secret := filepath.Join(t.TempDir(), "age-identity")
	require.NoError(t, os.WriteFile(secret, []byte("AGE-SECRET-KEY-1..."), 0o600))

	// Replace a component with a link to something outside the backup.
	require.NoError(t, os.Remove(filepath.Join(local, "database.sql.age")))
	require.NoError(t, os.Symlink(secret, filepath.Join(local, "database.sql.age")))

	ref, err := ports.TargetURL("file://" + filepath.Join(t.TempDir(), "offsite"))
	require.NoError(t, err)

	_, err = localdir.New().Push(context.Background(), ref, local, "20260101T000000Z")
	require.Error(t, err, "a symlinked component was followed off the backup")
	assert.Contains(t, domain.AsError(err).Message, "regular file")
}

// TestAComponentTheManifestNamesButIsNotThere is caught before anything is
// uploaded, and points at the command that would have caught it earlier.
func TestAComponentTheManifestNamesButIsNotThere(t *testing.T) {
	local := writeTestBackup(t, "20260101T000000Z", map[string]string{
		"database.sql.age": "ciphertext",
	})
	require.NoError(t, os.Remove(filepath.Join(local, "database.sql.age")))

	ref, err := ports.TargetURL("file://" + filepath.Join(t.TempDir(), "offsite"))
	require.NoError(t, err)

	_, err = localdir.New().Push(context.Background(), ref, local, "20260101T000000Z")
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "backup verify",
		"the remedy is the command that would have caught this before the push")
}

// TestATargetOnAReadOnlyMediumFailsRatherThanHalfWriting. The ordinary shape of
// a full disk or a medium mounted read-only.
func TestATargetOnAReadOnlyMediumFailsRatherThanHalfWriting(t *testing.T) {
	requireNonRoot(t)

	local := writeTestBackup(t, "20260101T000000Z", map[string]string{
		"database.sql.age": "ciphertext",
	})

	offsite := filepath.Join(t.TempDir(), "offsite")
	require.NoError(t, os.MkdirAll(offsite, 0o500))
	t.Cleanup(func() { _ = os.Chmod(offsite, 0o700) })

	ref, err := ports.TargetURL("file://" + offsite)
	require.NoError(t, err)

	_, err = localdir.New().Push(context.Background(), ref, local, "20260101T000000Z")
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "mounted",
		"the remedy for a target that will not take a write is almost always the mount")

	manifests, err := localdir.New().List(context.Background(), ref)
	require.NoError(t, err)
	assert.Empty(t, manifests, "a push that could not write reported a backup anyway")
}

// TestEveryRegistryMethodRefusesAnUnbuiltScheme.
//
// The dispatcher has one of these per method, and a missed one is a nil
// dereference rather than a refusal — at whichever moment an operator first
// uses that verb against a transport this build does not have.
func TestEveryRegistryMethodRefusesAnUnbuiltScheme(t *testing.T) {
	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)

	ctx := context.Background()
	unbuilt := ports.TargetRef{Scheme: "gs", Path: "/bucket"}
	remote := ports.RemoteRef{Target: unbuilt, ID: "20260101T000000Z"}

	for name, call := range map[string]func() error{
		"push": func() error {
			_, err := registry.Push(ctx, unbuilt, t.TempDir(), "20260101T000000Z")
			return err
		},
		"list": func() error {
			_, err := registry.List(ctx, unbuilt)
			return err
		},
		"fetch":  func() error { return registry.Fetch(ctx, remote, t.TempDir()) },
		"verify": func() error { return registry.Verify(ctx, remote) },
		"remove": func() error { return registry.Remove(ctx, remote) },
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			require.Error(t, err, "the registry dispatched %s to a scheme it does not have", name)
			assert.Contains(t, domain.AsError(err).Hint, "file",
				"the refusal must name the schemes this build does have")
		})
	}
}

// TestAFetchOfAnUnusableTargetFailsBeforeItWrites, rather than creating a
// destination directory and then discovering the target is not addressable.
func TestAFetchOfAnUnusableTargetFailsBeforeItWrites(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "dest")

	// A file:// ref with no path is the shape a hand-edited installation
	// produces, and the shape a URL parser would never emit.
	err := localdir.New().Fetch(context.Background(),
		ports.RemoteRef{Target: ports.TargetRef{Scheme: "file"}, ID: "20260101T000000Z"},
		dest)
	require.Error(t, err)

	_, statErr := os.Stat(dest)
	assert.True(t, os.IsNotExist(statErr),
		"the destination was created for a fetch that could not have succeeded")
}

// TestVerifyAndRemoveOnAnUnusableTargetAlsoRefuse, so every verb fails at the
// same point rather than three different ones.
func TestVerifyAndRemoveOnAnUnusableTargetAlsoRefuse(t *testing.T) {
	adapter := localdir.New()
	ref := ports.RemoteRef{Target: ports.TargetRef{Scheme: "file"}, ID: "20260101T000000Z"}

	require.Error(t, adapter.Verify(context.Background(), ref))
	require.Error(t, adapter.Remove(context.Background(), ref))

	_, err := adapter.List(context.Background(), ports.TargetRef{Scheme: "file"})
	require.Error(t, err)
}

// TestATargetThatBecomesUnwritableMidPushFailsTheWholePush.
//
// The ordinary shape of a disk filling up: the first component lands, the
// second cannot. What matters is not the error -- it is that nothing on the
// target afterwards looks like a backup.
func TestATargetThatBecomesUnwritableMidPushFailsTheWholePush(t *testing.T) {
	local := writeTestBackup(t, "20260101T000000Z", map[string]string{
		"aaa-first.age":  "lands",
		"zzz-second.age": "does not",
	})

	requireNonRoot(t)

	offsite := filepath.Join(t.TempDir(), "offsite")
	ref, err := ports.TargetURL("file://" + offsite)
	require.NoError(t, err)

	adapter := localdir.New()

	// Push once so the target directory exists, then make it read-only: the
	// components are written into a subdirectory that can no longer be
	// created.
	_, err = adapter.Push(context.Background(), ref, local, "20260101T000000Z")
	require.NoError(t, err)
	require.NoError(t, adapter.Remove(context.Background(),
		ports.RemoteRef{Target: ref, ID: "20260101T000000Z"}))
	require.NoError(t, os.Chmod(offsite, 0o500))
	t.Cleanup(func() { _ = os.Chmod(offsite, 0o700) })

	_, err = adapter.Push(context.Background(), ref, local, "20260101T000000Z")
	require.Error(t, err)

	require.NoError(t, os.Chmod(offsite, 0o700))
	manifests, err := adapter.List(context.Background(), ref)
	require.NoError(t, err)
	assert.Empty(t, manifests,
		"a push that could not finish left something that lists as a backup")
}

// TestReadingAComponentThatIsNotThereIsNotFound, which is what verification and
// fetch both have to be able to distinguish from an unreachable medium: one
// means look somewhere else, the other means fix the mount.
func TestReadingAComponentThatIsNotThereIsNotFound(t *testing.T) {
	local := writeTestBackup(t, "20260101T000000Z", map[string]string{
		"database.sql.age": "ciphertext",
	})

	offsite := filepath.Join(t.TempDir(), "offsite")
	ref, err := ports.TargetURL("file://" + offsite)
	require.NoError(t, err)

	adapter := localdir.New()
	remote, err := adapter.Push(context.Background(), ref, local, "20260101T000000Z")
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(offsite, "20260101T000000Z", "database.sql.age")))

	err = adapter.Fetch(context.Background(), remote, filepath.Join(t.TempDir(), "dest"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrNotFound), "got: %v", err)
	assert.Contains(t, domain.AsError(err).Hint, "did not finish",
		"the remedy has to say the push was incomplete, not that the disk is broken")
}

// TestASecondTargetOnOneHostGetsItsOwnHandshake.
//
// Connections are cached so one backup does not open a session per file. The
// cache key was `user@host`, which meant a second target on the same host
// reused the first one's connection — and therefore was never handshaked, so
// **its host key was never checked**. An operator could configure one target
// with a correct pin and a second with a wrong one, and nothing would object.
//
// The pin is what makes "the backup reached the target" mean "the backup
// reached *that* machine", so a path that skips it is worth a test of its own.
func TestASecondTargetOnOneHostGetsItsOwnHandshake(t *testing.T) {
	client := newSSHKey(t)
	host := newSSHKey(t)
	impostor := newSSHKey(t)

	root := t.TempDir()
	addr := startInProcessSSH(t, host, client.public, root)

	adapter := sftp.New()
	t.Cleanup(func() { _ = adapter.Close() })

	good := ports.TargetRef{
		Scheme: "ssh", Host: addr, Path: root, User: "ops",
		URL: "ssh://ops@" + addr + root,
		Credentials: ports.TargetCredentials{
			PrivateKey: client.private,
			KnownHosts: sshKnownHostsLine(t, addr, host.public),
		},
	}
	_, err := adapter.List(context.Background(), good)
	require.NoError(t, err, "the correctly pinned target should connect")

	// Same user, same host, a pin for a key this server does not have.
	bad := good
	bad.Credentials.KnownHosts = sshKnownHostsLine(t, addr, impostor.public)

	_, err = adapter.List(context.Background(), bad)
	require.Error(t, err,
		"a second target on the same host reused the first one's connection, so "+
			"its host key was never checked")
}

// TestCredentialsFromAFileAreRedactedToo.
//
// The secret-store path armed the redactor and the file path did not — which
// is the path a recovery uses, and the one where an operator has a private key
// sitting in a file they just typed the name of.
func TestCredentialsFromAFileAreRedactedToo(t *testing.T) {
	h := newHarness(t)
	h.withTargets(t)

	const secret = "AKIA-THIS-IS-THE-SECRET-HALF"
	offsite := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.MkdirAll(offsite, 0o700))

	_, err := ops.ListRemote(context.Background(), h.Deps, ops.TargetOptions{
		URL: "file://" + offsite,
		Credentials: ports.TargetCredentials{
			AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: secret,
		},
	})
	require.NoError(t, err)

	var registered bool
	for _, v := range h.Deps.Redactor.Values() {
		if v == secret {
			registered = true
		}
	}
	assert.True(t, registered,
		"a credential supplied from a file was never registered for redaction, so "+
			"the second line of defence is missing on the recovery path")
}

// TestTwoSpellingsOfOneTargetAreOneTarget.
//
// `String()` returns what the operator wrote, so comparing raw URLs let three
// spellings of one directory into an installation. Each would then be pushed to
// and pruned separately, every pass seeing a state the other two had just
// changed.
func TestTwoSpellingsOfOneTargetAreOneTarget(t *testing.T) {
	for _, pair := range [][2]string{
		{"file:///mnt/a", "file://localhost/mnt/a"},
		{"file:///mnt/a", "file:///mnt/a/"},
		{"s3://bucket/demo", "s3://bucket/demo/"},
		{"ssh://ops@host/srv/x", "ssh://ops@host/srv/x/"},
	} {
		first, err := ports.TargetURL(pair[0])
		require.NoError(t, err)
		second, err := ports.TargetURL(pair[1])
		require.NoError(t, err)

		assert.Equal(t, first.Canonical(), second.Canonical(),
			"%q and %q are the same target and must compare equal", pair[0], pair[1])
	}

	// And genuinely different targets stay different.
	a, err := ports.TargetURL("file:///mnt/a")
	require.NoError(t, err)
	b, err := ports.TargetURL("file:///mnt/b")
	require.NoError(t, err)
	assert.NotEqual(t, a.Canonical(), b.Canonical())
}

// TestAnInstallationRefusesTwoSpellingsOfOneTarget, which is where the
// canonical form has to be applied for it to matter.
func TestAnInstallationRefusesTwoSpellingsOfOneTarget(t *testing.T) {
	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_x",
		Product:       "demo",
		Backup: domain.BackupConfig{Targets: []domain.BackupTargetConfig{
			{URL: "file:///mnt/a"},
			{URL: "file://localhost/mnt/a/"},
		}},
	}

	err := inst.Validate()
	require.Error(t, err, "one directory was accepted as two targets")
	assert.Contains(t, err.Error(), "twice")
}

// TestASymlinkOnATargetIsNotFollowed.
//
// A target is somewhere this deployment does not control -- that is the whole
// premise -- so whoever owns the medium can replace a component with a link to
// a local file. The manifest names an innocent path, the lexical check agrees,
// and without an os.Root the manager reads /etc/shadow into the backup it is
// fetching.
func TestASymlinkOnATargetIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	offsite := filepath.Join(dir, "offsite")

	local := writeTestBackup(t, "20260101T000000Z", map[string]string{
		"database.sql.age": "ciphertext",
	})
	ref, err := ports.TargetURL("file://" + offsite)
	require.NoError(t, err)

	adapter := localdir.New()
	remote, err := adapter.Push(context.Background(), ref, local, "20260101T000000Z")
	require.NoError(t, err)

	// The medium's owner swaps a component for a link out of the target.
	secret := filepath.Join(dir, "local-secret")
	require.NoError(t, os.WriteFile(secret, []byte("SHOULD-NOT-BE-FETCHED"), 0o600))

	component := filepath.Join(offsite, "20260101T000000Z", "database.sql.age")
	require.NoError(t, os.Remove(component))
	require.NoError(t, os.Symlink(secret, component))

	dest := filepath.Join(t.TempDir(), "dest")
	err = adapter.Fetch(context.Background(), remote, dest)
	require.Error(t, err, "a symlinked component was followed off the target")

	if data, readErr := os.ReadFile(filepath.Join(dest, "database.sql.age")); readErr == nil {
		assert.NotContains(t, string(data), "SHOULD-NOT-BE-FETCHED",
			"a local file was read into the fetched backup")
	}
}

// TestALegalNameContainingDotsWorksOnEveryTransport. A substring test for ".."
// rejected `notes..age`, which the filesystem target accepts -- so a backup was
// restorable or not depending on which transport it happened to be pushed with.
func TestALegalNameContainingDotsWorksOnEveryTransport(t *testing.T) {
	offsite := filepath.Join(t.TempDir(), "offsite")
	ref, err := ports.TargetURL("file://" + offsite)
	require.NoError(t, err)

	local := writeTestBackup(t, "20260101T000000Z", map[string]string{
		"database..age": "ciphertext",
	})

	adapter := localdir.New()
	remote, err := adapter.Push(context.Background(), ref, local, "20260101T000000Z")
	require.NoError(t, err, "a legal component name containing dots was refused")

	back := filepath.Join(t.TempDir(), "fetched")
	require.NoError(t, adapter.Fetch(context.Background(), remote, back))
	_, err = os.Stat(filepath.Join(back, "database..age"))
	require.NoError(t, err)
}

// TestCanonicalKeepsTheUserApart. Two accounts on one host are two targets: the
// backups one can read are not necessarily the backups the other can.
func TestCanonicalKeepsTheUserApart(t *testing.T) {
	operator, err := ports.TargetURL("ssh://ops@host/srv/backups")
	require.NoError(t, err)
	admin, err := ports.TargetURL("ssh://admin@host/srv/backups")
	require.NoError(t, err)

	assert.NotEqual(t, operator.Canonical(), admin.Canonical())
	assert.Contains(t, operator.Canonical(), "ops@")
}

// TestACredentialDocumentIsCheckedFieldateField, so a document that parses as
// YAML but names nothing useful is refused where it is read rather than at the
// first request.
func TestACredentialDocumentIsCheckedFieldByField(t *testing.T) {
	for name, tc := range map[string]struct {
		raw string
		bad bool
	}{
		"an s3 pair":         {raw: "access_key_id: AKIA\nsecret_access_key: s3kr3t\n"},
		"an ssh key and pin": {raw: "private_key: |\n  KEY\nknown_hosts: host ssh-ed25519 AAAA\n"},
		"an endpoint alone":  {raw: "endpoint: minio.internal\n"},
		"empty":              {raw: "", bad: true},
		"only whitespace":    {raw: "   \n\t\n", bad: true},
		"valid yaml, no known field": {
			raw: "colour: blue\nsize: large\n", bad: true,
		},
		"not yaml at all": {raw: "access_key_id: [unclosed\n", bad: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ops.ParseTargetCredentials(tc.raw)
			if tc.bad {
				require.Error(t, err, "a document naming no credential was accepted")
				assert.NotEmpty(t, domain.AsError(err).Hint,
					"a refusal about a credential document must say what one looks like")
				return
			}
			require.NoError(t, err)
		})
	}
}
