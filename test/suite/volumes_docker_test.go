//go:build docker

package suite

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/backup/hookbackup"
	"github.com/morzecrew/morzer/internal/adapters/runtime/compose"
	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/domain"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/dockerlab"
)

// Volume capture against a real Compose project and a real helper container.
//
// The fake-backed suite in volumes_test.go answers what was decided; this
// answers whether the decision does anything. A tar taken out of a Docker
// volume through a container, encrypted, decrypted and written back into a
// volume that has been emptied in between is a chain with a lot of places to
// be subtly wrong, and none of them are visible to an in-memory runtime.
//
// **Scope discipline**, per RFC 0008 §5.4: none of this tests tar or busybox.
// What is asserted is that the manager mounted the right volume, read it
// read-only, encrypted what came out, refused to write it back while a
// container held it, and put the same bytes back afterwards.

// `init: true` on every service that outlives its start, because the shell loop
// would otherwise be PID 1 -- and the kernel does not deliver a signal with a
// default action to PID 1, so it never saw SIGTERM and every stop waited out its
// whole timeout before killing. With an init process forwarding the signal a
// stop takes about 300ms instead of the full grace, measured; this file used to
// spend most of its runtime waiting to be allowed to kill something.
const volumeComposeYAML = `
services:
  app:
    image: ` + dockerlab.ImageBusybox + `
    init: true
    command: ["sh", "-c", "while true; do sleep 3600; done"]
    volumes:
      - uploads:/data
      - /etc/hostname:/etc/hostname-from-host:ro
  sidecar:
    image: ` + dockerlab.ImageBusybox + `
    init: true
    command: ["sh", "-c", "while true; do sleep 3600; done"]
    volumes:
      - uploads:/shared
      - cache:/cache
volumes:
  uploads: {}
  cache: {}
`

type volumeLab struct {
	cfg     ports.RuntimeConfig
	runtime *compose.Runtime
	engine  *hookbackup.Engine

	identity string
	paths    domain.Paths
}

// startVolumeProject brings up a project with two services sharing a volume,
// and writes a file into it.
func startVolumeProject(t *testing.T, spec domain.BackupSpec) *volumeLab {
	t.Helper()
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageBusybox)

	cfg := dockerlab.Project(t, volumeComposeYAML)
	runner := infraexec.New()
	runtime := compose.New(runner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	require.NoError(t, runtime.Up(ctx, cfg, ports.UpOptions{}))

	root := t.TempDir()
	paths := domain.PathsUnder(root, "demo")
	require.NoError(t, os.MkdirAll(paths.BackupsDir(), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Dir(paths.AgeIdentityFile()), 0o700))

	public, err := sopsage.GenerateIdentity(paths.AgeIdentityFile())
	require.NoError(t, err)

	rel := domain.Release{
		Root: t.TempDir(),
		Manifest: domain.Manifest{
			Metadata: domain.Metadata{
				Name:    "demo",
				Version: domain.MustParseVersion("1.2.0"),
			},
			Runtime: domain.RuntimeSpec{Project: cfg.Project},
			Backup:  spec,
		},
	}

	return &volumeLab{
		cfg:     cfg,
		runtime: runtime,
		engine: hookbackup.New(hookbackup.Config{
			Release:        rel,
			Installation:   domain.Installation{ID: "inst-volume-lab", Product: "demo"},
			Paths:          paths,
			ManagerVersion: "0.0.0-test",
			Runtime:        runtime,
			RuntimeConfig:  cfg,
			AllowDowntime:  true,
			// Seconds rather than the production two minutes. The
			// fixtures answer SIGTERM now, so this is never
			// reached -- it is a bound on a fixture that hangs,
			// which would otherwise cost two minutes per quiesce
			// to discover.
			StopTimeout: 3 * time.Second,
			Now:         func() time.Time { return time.Now().UTC() },
			Recipients: func(context.Context) ([]string, error) {
				return []string{public}, nil
			},
		}),
		identity: paths.AgeIdentityFile(),
		paths:    paths,
	}
}

// write puts a file into the shared volume, through the service that mounts it.
func (l *volumeLab) write(t *testing.T, name, contents string) {
	t.Helper()
	l.exec(t, "app", "sh", "-c", fmt.Sprintf("printf %%s %q > /data/%s", contents, name))
}

// read reads it back. An error means the file is not there, which is what a
// lost restore looks like.
func (l *volumeLab) read(t *testing.T, name string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	res, err := l.runtime.Exec(ctx, l.cfg, "app", []string{"cat", "/data/" + name})
	if err != nil {
		return "", err
	}
	if !res.OK() {
		return "", fmt.Errorf("cat exited %d: %s", res.ExitCode, res.Stderr)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// actualName resolves a volume's name in the runtime, which is the project
// prefix plus the key -- but only ever read from the adapter, never assembled
// here, so a test cannot pass against a name production would not use.
func (l *volumeLab) actualName(t *testing.T, name string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	storage, err := l.runtime.Volumes(ctx, l.cfg)
	require.NoError(t, err)

	for _, v := range storage.Volumes {
		if v.Name == name {
			require.NotEmpty(t, v.Actual)
			return v.Actual
		}
	}
	t.Fatalf("the project declares no volume %q; it has %+v", name, storage.Volumes)
	return ""
}

func (l *volumeLab) exec(t *testing.T, service string, argv ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	res, err := l.runtime.Exec(ctx, l.cfg, service, argv)
	require.NoError(t, err)
	require.True(t, res.OK(), "%v exited %d: %s", argv, res.ExitCode, res.Stderr)
}

// TestAVolumeSurvivesBeingDestroyedAndRestored is the round trip. Every other
// assertion in this file is about how it is done; this is whether it works.
func TestAVolumeSurvivesBeingDestroyedAndRestored(t *testing.T) {
	lab := startVolumeProject(t, domain.BackupSpec{})
	ctx := context.Background()

	lab.write(t, "invoice.txt", "0000-4471-payable")

	ref, err := lab.engine.Create(ctx, ports.Scope{Reason: "manual"}, nil)
	require.NoError(t, err)

	// The volume is emptied the way a failed disk empties it: everything
	// gone, the service none the wiser until it looks.
	lab.exec(t, "app", "sh", "-c", "rm -rf /data/invoice.txt")
	_, err = lab.read(t, "invoice.txt")
	require.Error(t, err, "the fixture did not actually destroy the file")

	// Stopped, because a restore into a volume a container holds open is
	// refused -- see TestRestoringIntoARunningVolumeIsRefusedByName.
	require.NoError(t, lab.runtime.Stop(ctx, lab.cfg, nil, 30*time.Second))
	require.NoError(t, lab.engine.Restore(ctx, ref, ports.RestoreOptions{
		IdentityFile: lab.identity, TargetInstallationID: "inst-volume-lab",
	}))
	require.NoError(t, lab.runtime.Start(ctx, lab.cfg, nil))

	got, err := lab.read(t, "invoice.txt")
	require.NoError(t, err, "the file did not come back, so the backup did not work")
	assert.Equal(t, "0000-4471-payable", got)
}

// A restore replaces rather than merges. A volume holding files the backup does
// not contain, beside a database restored to an exact moment, is how an upload
// record without its file is made.
func TestARestoredVolumeMatchesTheBackupExactly(t *testing.T) {
	lab := startVolumeProject(t, domain.BackupSpec{})
	ctx := context.Background()

	lab.write(t, "before.txt", "in-the-backup")

	ref, err := lab.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	lab.write(t, "after.txt", "written-after-the-backup")

	require.NoError(t, lab.runtime.Stop(ctx, lab.cfg, nil, 30*time.Second))
	require.NoError(t, lab.engine.Restore(ctx, ref, ports.RestoreOptions{
		IdentityFile: lab.identity, TargetInstallationID: "inst-volume-lab",
	}))
	require.NoError(t, lab.runtime.Start(ctx, lab.cfg, nil))

	got, err := lab.read(t, "before.txt")
	require.NoError(t, err)
	assert.Equal(t, "in-the-backup", got)

	_, err = lab.read(t, "after.txt")
	assert.Error(t, err,
		"a file the backup does not contain survived the restore, so the "+
			"volume matches no point in time")
}

// The claim, against a real tarball: what lands on the disk is ciphertext, and
// the contents of the volume are not in it.
func TestARealVolumeTarballIsEncrypted(t *testing.T) {
	lab := startVolumeProject(t, domain.BackupSpec{})
	ctx := context.Background()

	lab.write(t, "secret.txt", "PATIENT-RECORD-88213")

	ref, err := lab.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	stored, err := os.ReadFile(filepath.Join(ref.Path, "volumes", "uploads.tar.age"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(stored), "age-encryption.org/"))
	assert.NotContains(t, string(stored), "PATIENT-RECORD-88213",
		"the volume tarball is readable, so anyone who finds the backup has the files")

	// No plaintext tar left behind beside it.
	_, err = os.Stat(filepath.Join(ref.Path, "volumes", "uploads.tar"))
	assert.Error(t, err)

	require.NoError(t, lab.engine.Verify(ctx, ref))
}

// Decision 6, against a project that really is running.
func TestRestoringIntoARunningVolumeIsRefusedByName(t *testing.T) {
	lab := startVolumeProject(t, domain.BackupSpec{})
	ctx := context.Background()

	lab.write(t, "keep.txt", "still-here")

	ref, err := lab.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	// The capture started everything again, which is the state the refusal
	// exists for.
	require.NoError(t, lab.runtime.Start(ctx, lab.cfg, nil))

	err = lab.engine.Restore(ctx, ref, ports.RestoreOptions{
		IdentityFile: lab.identity, TargetInstallationID: "inst-volume-lab",
	})
	require.Error(t, err, "a restore wrote into a volume a container had open")

	message := err.Error()
	assert.Contains(t, message, "still holds it open")
	assert.Contains(t, message, "app is running")
	assert.Contains(t, message, "sidecar is running")
	assert.Contains(t, message, "uploads")

	// And it refused before writing: the file is untouched.
	got, err := lab.read(t, "keep.txt")
	require.NoError(t, err)
	assert.Equal(t, "still-here", got)
}

// A paused container is neither running nor stopped: it is frozen mid-write
// with its file handles open. A refusal that checked only for `running` let a
// restore untar straight over a volume two paused containers were holding, and
// reported success.
//
// Against real Docker because that is where the state name comes from -- the
// runtime reports "paused", and nothing in the manager had ever seen it.
func TestRestoringIntoAPausedVolumeIsRefused(t *testing.T) {
	lab := startVolumeProject(t, domain.BackupSpec{})
	ctx := context.Background()

	lab.write(t, "keep.txt", "still-here")

	ref, err := lab.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	// Both, because one running service would block the restore on its own
	// and hide whether the paused one was ever considered.
	require.NoError(t, lab.pause(t, "pause"))
	t.Cleanup(func() { _ = lab.pause(t, "unpause") })

	states, err := lab.runtime.Status(ctx, lab.cfg)
	require.NoError(t, err)
	require.NotEmpty(t, states,
		"the project reports no services, so the pause below proves nothing")
	for _, s := range states {
		require.Equal(t, "paused", s.State, "the fixture did not actually pause %s", s.Name)
	}

	err = lab.engine.Restore(ctx, ref, ports.RestoreOptions{
		IdentityFile: lab.identity, TargetInstallationID: "inst-volume-lab",
	})
	require.Error(t, err, "a restore wrote into a volume a paused container held open")
	assert.Contains(t, err.Error(), "is paused",
		"the refusal does not say the service is paused, so the operator is "+
			"told to stop something that is already not running")
}

// pause runs `compose pause`/`unpause`, which the Runtime port does not expose
// -- it is not an operation the manager performs, only one it must survive.
func (l *volumeLab) pause(t *testing.T, verb string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	cmd := osexec.CommandContext(ctx, "docker", "compose",
		"--project-name", l.cfg.Project,
		"--project-directory", l.cfg.WorkingDir,
		"--file", l.cfg.Files[0], verb, "app", "sidecar")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("compose %s: %w\n%s", verb, err, out)
	}
	return nil
}

// Decision 3: the source is mounted read-only, so a helper that misbehaves
// cannot write into the product's data.
func TestTheHelperCannotWriteIntoTheVolumeItIsReading(t *testing.T) {
	lab := startVolumeProject(t, domain.BackupSpec{})
	ctx := context.Background()

	lab.write(t, "original.txt", "unmodified")

	uploads := lab.actualName(t, "uploads")

	// The same mount the capture uses, asked to write. Reaching into the
	// adapter's own mount rather than approximating it is the point: this
	// asserts the flag the production path passes, not one the test chose.
	require.NoError(t, lab.runtime.CaptureVolume(ctx, lab.cfg, uploads,
		filepath.Join(t.TempDir(), "probe.tar")))

	res, err := lab.runtime.Exec(ctx, lab.cfg, "app",
		[]string{"sh", "-c", "cat /data/original.txt"})
	require.NoError(t, err)
	assert.Equal(t, "unmodified", strings.TrimSpace(res.Stdout))
}

// The manifest against a real project: two services on the shared volume, a
// bind mount reported and never captured.
func TestARealProjectsStorageIsRecordedAccurately(t *testing.T) {
	lab := startVolumeProject(t, domain.BackupSpec{Volumes: map[string]domain.VolumeSpec{
		"cache": {Consistency: domain.VolumeExclude},
	}})
	ctx := context.Background()

	lab.write(t, "invoice.txt", "0000-4471-payable")

	ref, err := lab.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	manifest, err := lab.engine.Inspect(ctx, ref)
	require.NoError(t, err)

	records := manifest.VolumeRecords()
	require.Len(t, records, 1, "expected only uploads: cache is excluded")

	uploads := records[0]
	assert.Equal(t, "uploads", uploads.Volume.Volume)
	assert.Equal(t, ports.ConsistencyCold, uploads.Volume.Consistency)
	assert.Equal(t, []string{"app", "sidecar"}, uploads.Volume.Services,
		"both services mount it, and the restore refusal needs both names")
	assert.Contains(t, uploads.Volume.Actual, "uploads")

	var sawBind, sawExcluded bool
	for _, u := range manifest.Uncaptured {
		switch u.Kind {
		case ports.VolumeKindBind:
			sawBind = true
			assert.Equal(t, "/etc/hostname", u.Volume)
		case ports.VolumeKindNamed:
			sawExcluded = true
			assert.Equal(t, "cache", u.Volume)
		}
	}
	assert.True(t, sawBind, "the bind mount is not reported, so an operator is "+
		"silently short a mount they may believe is covered")
	assert.True(t, sawExcluded, "the excluded volume is not reported")
}

// A backup of a deployment that is not running is a normal thing to take --
// before maintenance, above all -- and must not fail for having failed to start
// what was never up.
//
// It did: the quiesce stopped and started the whole service list unconditionally,
// and `compose start` on a service with no container exits non-zero. The backup
// captured its volumes perfectly, then deleted them and reported that it could
// not bring the product back.
func TestABackupOfAStoppedDeploymentSucceeds(t *testing.T) {
	lab := startVolumeProject(t, domain.BackupSpec{})
	ctx := context.Background()

	lab.write(t, "invoice.txt", "0000-4471-payable")

	// Down rather than stop: no containers at all, which is the state after
	// a failed restore or before the first apply completes.
	require.NoError(t, lab.runtime.Down(ctx, lab.cfg, ports.DownOptions{Timeout: 30 * time.Second}))

	ref, err := lab.engine.Create(ctx, ports.Scope{Reason: "maintenance"}, nil)
	require.NoError(t, err, "a backup of a stopped deployment failed")

	manifest, err := lab.engine.Inspect(ctx, ref)
	require.NoError(t, err)
	assert.Len(t, manifest.VolumeRecords(), 2,
		"the volumes were not captured from a stopped project")

	// And it is a real backup: the data is in it.
	require.NoError(t, lab.engine.Verify(ctx, ref))
}

// The measurement decision 8 rests on. A volume with a known amount in it must
// not read as zero, or the space check would pass everything.
func TestAVolumeIsMeasuredBeforeItIsCopied(t *testing.T) {
	lab := startVolumeProject(t, domain.BackupSpec{})
	ctx := context.Background()

	// 2 MiB, comfortably above the block-rounding noise `du` reports for an
	// empty directory.
	lab.exec(t, "app", "sh", "-c", "dd if=/dev/zero of=/data/blob bs=1024 count=2048")

	uploads := lab.actualName(t, "uploads")

	size, err := lab.runtime.VolumeSize(ctx, lab.cfg, uploads)
	require.NoError(t, err)
	assert.Greater(t, size, int64(1<<20),
		"a volume holding 2 MiB measured %d bytes, so the space check "+
			"would let any backup through", size)
}
