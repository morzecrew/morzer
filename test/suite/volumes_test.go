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

	"github.com/morzecrew/morzer/internal/adapters/backup/hookbackup"
	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// The volumes component, driven through the real backup engine against an
// in-memory runtime.
//
// What a fake can answer here is most of it: which volumes were captured, with
// what consistency, what was recorded as skipped and why, whether the services
// were stopped at the instant the copy was taken, and whether the tarball came
// out encrypted. What it cannot answer -- does a tar of a real Docker volume
// actually restore -- is volumes_docker_test.go's job.

// volumeStorage is a project with one volume per case: an uploads volume two
// services write to, a database volume, a cache, and a bind mount.
func volumeStorage() ports.ProjectStorage {
	return ports.ProjectStorage{
		Volumes: []ports.NamedVolume{
			{Name: "cache", Actual: "demo_cache", Services: []string{"worker"}},
			{Name: "pgdata", Actual: "demo_pgdata", Services: []string{"db"}},
			{Name: "uploads", Actual: "demo_uploads", Services: []string{"app", "worker"}},
		},
		Binds: []ports.BindMount{{Source: "/srv/legacy", Services: []string{"app"}}},
	}
}

type volumeFixture struct {
	engine   *hookbackup.Engine
	runtime  *fakes.Runtime
	identity string
	paths    domain.Paths
}

// newVolumeFixture wires the real engine to a fake runtime, with real age keys
// so the encryption assertions mean something.
func newVolumeFixture(t *testing.T, spec domain.BackupSpec, allowDowntime bool) *volumeFixture {
	t.Helper()

	root := t.TempDir()
	paths := domain.PathsUnder(root, "demo")
	require.NoError(t, os.MkdirAll(paths.BackupsDir(), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Dir(paths.AgeIdentityFile()), 0o700))

	public, err := sopsage.GenerateIdentity(paths.AgeIdentityFile())
	require.NoError(t, err)

	runtime := fakes.NewRuntime()
	runtime.Storage = volumeStorage()
	runtime.VolumeContents = map[string]string{
		"demo_uploads": "the-quarterly-report.pdf",
		"demo_pgdata":  "base/16384/2601",
		"demo_cache":   "thumbnails",
	}
	// Everything up, so a capture that does not stop anything is visible as
	// a witness naming running services.
	for _, name := range []string{"app", "worker", "db"} {
		runtime.Services[name] = ports.ServiceState{
			Name: name, State: "running", Health: ports.HealthHealthy,
		}
	}

	rel := domain.Release{
		Root: t.TempDir(),
		Manifest: domain.Manifest{
			Metadata: domain.Metadata{Name: "demo", Version: domain.MustParseVersion("1.2.0")},
			Backup:   spec,
		},
	}

	return &volumeFixture{
		engine: hookbackup.New(hookbackup.Config{
			// No hook runner, because the release declares no backup
			// operation: this fixture is the vendor who never wrote
			// one, which is half of what volumes are for.
			Hooks:          nil,
			Release:        rel,
			Installation:   domain.Installation{ID: "inst-volumes", Product: "demo"},
			Paths:          paths,
			ManagerVersion: "0.0.0-test",
			Runtime:        runtime,
			RuntimeConfig:  ports.RuntimeConfig{Project: "demo"},
			AllowDowntime:  allowDowntime,
			Now:            func() time.Time { return time.Now().UTC() },
			Recipients: func(context.Context) ([]string, error) {
				return []string{public}, nil
			},
		}),
		runtime:  runtime,
		identity: paths.AgeIdentityFile(),
		paths:    paths,
	}
}

func volumeRecord(t *testing.T, m ports.BackupManifest, name string) ports.ComponentRecord {
	t.Helper()
	for _, c := range m.VolumeRecords() {
		if c.Volume.Volume == name {
			return c
		}
	}
	t.Fatalf("volume %q is not in the backup; it holds %d volume(s)", name, len(m.VolumeRecords()))
	return ports.ComponentRecord{}
}

func uncapturedRecord(t *testing.T, m ports.BackupManifest, name string) ports.UncapturedVolume {
	t.Helper()
	for _, u := range m.Uncaptured {
		if u.Volume == name {
			return u
		}
	}
	t.Fatalf("%q is not recorded as uncaptured; the manifest lists %v", name, m.Uncaptured)
	return ports.UncapturedVolume{}
}

// Goal 1 of the RFC: a release that declares no backup hook used to have
// `morzer backup` and nothing behind it.
func TestAReleaseWithNoBackupHookStillProducesABackup(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{Reason: "manual"}, nil)
	require.NoError(t, err, "a release with volumes and no hook produced no backup")

	manifest, err := f.engine.Inspect(ctx, ref)
	require.NoError(t, err)
	assert.Len(t, manifest.VolumeRecords(), 3,
		"the manager did not capture the volumes it can read for itself")
}

// ...and with nothing to put in it, it is still a refusal. A directory holding
// only a manifest is not a backup, and somebody would eventually restore it.
func TestABackupWithNoHookAndNoVolumesIsRefused(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)

	_, err := f.engine.Create(context.Background(), ports.Scope{
		Components: []ports.Component{ports.ComponentConfig},
	}, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares no backup operation")
}

// The refusal has to be about what was captured, not about what was intended.
//
// A release with no hook whose project declares no named volumes -- everything
// on bind mounts, which the example bundle itself did until this RFC -- passed
// the front gate because volumes were *in scope*, and produced a backup holding
// configuration and nothing of the product. `backup list` offers it, and
// somebody eventually restores it.
func TestABackupWithNothingCapturedIsRefusedEvenWhenVolumesWereInScope(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)

	// A project that mounts only bind mounts: volumes are in scope, the
	// capability is there, and there is nothing to take.
	f.runtime.Storage = ports.ProjectStorage{
		Binds: []ports.BindMount{{Source: "/srv/legacy", Services: []string{"app"}}},
	}

	_, err := f.engine.Create(context.Background(), ports.Scope{}, nil)

	require.Error(t, err, "a backup holding no product data was reported as a backup")
	assert.Contains(t, err.Error(), "captured nothing")

	backups, listErr := f.engine.List(context.Background())
	require.NoError(t, listErr)
	assert.Empty(t, backups, "the empty backup was left where a restore could find it")
}

// The consistency claim, asserted at the instant it has to be true.
func TestAColdVolumeIsReadWithItsServicesStopped(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	_, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	for _, volume := range []string{"demo_uploads", "demo_pgdata", "demo_cache"} {
		// Required before the emptiness check, which a volume that was
		// never captured at all would also satisfy -- a missing key and
		// "nothing was running" are the same nil.
		require.Contains(t, f.runtime.CaptureWitness, volume,
			"%s was never captured, so this says nothing about consistency", volume)
		assert.Empty(t, f.runtime.CaptureWitness[volume],
			"%s was read while %v were running, so the copy is crash-consistent "+
				"and the manifest calls it cold", volume, f.runtime.CaptureWitness[volume])
	}

	// And the deployment is serving again afterwards. A backup that leaves
	// the product down turns a nightly job into an outage.
	states, err := f.runtime.Status(ctx, ports.RuntimeConfig{})
	require.NoError(t, err)
	for _, s := range states {
		assert.True(t, s.Running(), "%s is still stopped after the backup", s.Name)
	}
}

func TestAHotVolumeIsReadWithoutStoppingAnything(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{Volumes: map[string]domain.VolumeSpec{
		"uploads": {Consistency: domain.VolumeHot},
		"cache":   {Consistency: domain.VolumeHot},
		"pgdata":  {Consistency: domain.VolumeExclude},
	}}, true)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	assert.NotContains(t, f.runtime.Calls, "Stop",
		"a backup of nothing but hot volumes stopped a service")
	assert.Equal(t, []string{"app", "db", "worker"}, f.runtime.CaptureWitness["demo_uploads"],
		"the uploads volume was declared hot, so the product should have been up")

	manifest, err := f.engine.Inspect(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, ports.ConsistencyHot, volumeRecord(t, manifest, "uploads").Volume.Consistency)
}

// The manifest is where a post-incident review finds out what was promised.
func TestTheManifestRecordsWhatWasCapturedAndWhatWasNot(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{Volumes: map[string]domain.VolumeSpec{
		"uploads": {Consistency: domain.VolumeHot},
		"pgdata":  {Consistency: domain.VolumeExclude},
	}}, true)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	manifest, err := f.engine.Inspect(ctx, ref)
	require.NoError(t, err)

	uploads := volumeRecord(t, manifest, "uploads")
	assert.Equal(t, ports.ConsistencyHot, uploads.Volume.Consistency)
	assert.Equal(t, "demo_uploads", uploads.Volume.Actual)
	assert.Equal(t, []string{"app", "worker"}, uploads.Volume.Services,
		"the service list is what a restore refuses against; without it the "+
			"refusal cannot name anything")

	cache := volumeRecord(t, manifest, "cache")
	assert.Equal(t, ports.ConsistencyCold, cache.Volume.Consistency)

	excluded := uncapturedRecord(t, manifest, "pgdata")
	assert.Contains(t, excluded.Reason, "exclude")

	bind := uncapturedRecord(t, manifest, "/srv/legacy")
	assert.Equal(t, ports.VolumeKindBind, bind.Kind)
}

// The same protection every other component gets, and worth asserting rather
// than assuming: a volume tarball is the product's files, and a backup that
// leaves the machine must carry no data with it.
func TestAVolumeTarballIsEncryptedInTheBackup(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	stored, err := os.ReadFile(filepath.Join(ref.Path, "volumes", "uploads.tar.age"))
	require.NoError(t, err, "the encrypted tarball is not where the manifest says")
	assert.True(t, strings.HasPrefix(string(stored), "age-encryption.org/"))
	assert.NotContains(t, string(stored), "the-quarterly-report.pdf",
		"the stored volume is readable, so anyone who finds the file has the data")

	_, err = os.Stat(filepath.Join(ref.Path, "volumes", "uploads.tar"))
	assert.Error(t, err, "the plaintext tarball was left beside the encrypted one")

	// And `verify` reads it back, so rot in a volume component is caught by
	// the same pass that catches it in a database dump.
	require.NoError(t, f.engine.Verify(ctx, ref))
}

// Decision 6, and it has to name the services: "stop the services" is not an
// instruction anybody can follow.
func TestRestoringAVolumeIsRefusedWhileItsServicesRun(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	// The capture put everything back up, which is exactly the state a
	// restore must refuse from.
	err = f.engine.Restore(ctx, ref, ports.RestoreOptions{
		IdentityFile: f.identity, TargetInstallationID: "inst-volumes",
	})

	require.Error(t, err)
	message := err.Error()
	assert.Contains(t, message, "still holds it open")
	for _, service := range []string{"app", "worker", "db"} {
		assert.Contains(t, message, service, "the refusal does not name %s", service)
	}
	assert.Contains(t, message, "uploads", "the refusal does not name the volume at risk")
	assert.Contains(t, message, "is running",
		"the refusal does not say what state the service is in, which is the "+
			"difference between `stop it` and `unpause it`")
}

// The refusal has to read who mounts the volume *now*, not who mounted it when
// the backup was taken.
//
// A release that adds a service on an existing volume records nothing about it
// in an older backup, so a refusal consulting only the recorded list would untar
// straight into a volume the new service is holding open -- the same blindness
// as checking only for `running`, reached by a different route.
func TestRestoringAVolumeConsidersServicesAddedSinceTheBackup(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	// Everything the backup knew about is stopped, so the recorded list
	// alone would raise no objection.
	require.NoError(t, f.runtime.Stop(ctx, ports.RuntimeConfig{}, nil, 0))

	// The release grew a service that mounts uploads, and it is running.
	f.runtime.Storage.Volumes = []ports.NamedVolume{
		{Name: "cache", Actual: "demo_cache", Services: []string{"worker"}},
		{Name: "pgdata", Actual: "demo_pgdata", Services: []string{"db"}},
		{Name: "uploads", Actual: "demo_uploads", Services: []string{"app", "thumbnailer", "worker"}},
	}
	f.runtime.Services["thumbnailer"] = ports.ServiceState{
		Name: "thumbnailer", State: ports.StateRunning,
	}

	err = f.engine.Restore(ctx, ref, ports.RestoreOptions{
		IdentityFile: f.identity, TargetInstallationID: "inst-volumes",
	})

	require.Error(t, err, "a restore wrote into a volume a newly added service was holding")
	assert.Contains(t, err.Error(), "thumbnailer",
		"the refusal named only the services the backup remembered")
}

// A paused container is frozen mid-write with its handles open. It is neither
// running nor stopped, and a check for `running` alone let a restore untar
// straight over a volume two paused containers were holding -- silently,
// because nothing in the refusal path looked at any other state.
func TestRestoringAVolumeIsRefusedWhileItsServicesArePaused(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	for name := range f.runtime.Services {
		f.runtime.Services[name] = ports.ServiceState{Name: name, State: "paused"}
	}

	err = f.engine.Restore(ctx, ref, ports.RestoreOptions{
		IdentityFile: f.identity, TargetInstallationID: "inst-volumes",
	})

	require.Error(t, err, "a restore wrote into a volume a paused container held open")
	assert.Contains(t, err.Error(), "is paused",
		"the refusal does not say the service is paused, so the operator is "+
			"told to stop something that is already not running")
}

// The same gap on the capture side: a paused service was not stopped, so its
// volume was read while a container had it open and the manifest recorded the
// copy as `cold` -- which is the one claim the whole component rests on.
func TestAPausedServiceIsStoppedBeforeItsVolumeIsRead(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	f.runtime.Services["worker"] = ports.ServiceState{Name: "worker", State: "paused"}

	_, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	assert.Contains(t, f.runtime.Calls, "Stop:app,db,worker",
		"the paused service was left holding the volume it mounts; the runtime "+
			"was asked to stop %v", f.runtime.Calls)
}

// ...and a state that means "there is nothing there" must still not be started.
func TestAnExitedServiceIsNotStartedByABackup(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	for name := range f.runtime.Services {
		f.runtime.Services[name] = ports.ServiceState{Name: name, State: "exited"}
	}

	_, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	assert.NotContains(t, f.runtime.Calls, "Stop",
		"a backup stopped services that were already down")
	assert.NotContains(t, f.runtime.Calls, "Start",
		"a backup started a product the operator had taken down")
}

func TestAVolumeIsRestoredOnceItsServicesAreStopped(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	// The deployment moves on: the file is replaced, and then everything is
	// stopped for the restore the way `morzer restore` does it.
	f.runtime.VolumeContents["demo_uploads"] = "something-else-entirely"
	require.NoError(t, f.runtime.Stop(ctx, ports.RuntimeConfig{}, nil, 0))

	require.NoError(t, f.engine.Restore(ctx, ref, ports.RestoreOptions{
		IdentityFile: f.identity, TargetInstallationID: "inst-volumes",
	}))

	assert.Equal(t, "the-quarterly-report.pdf", f.runtime.VolumeContents["demo_uploads"],
		"the volume was not put back, so the restore reported success and "+
			"changed nothing")
}

// A restore that does not want the volumes must not pay for them. Staging
// decrypts every component the hook might read; a volume is not one of those,
// and a hundred gigabytes of uploads decrypted to be deleted unread is a long
// wait for nothing.
func TestARestoreScopedAwayFromVolumesDoesNotTouchThem(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	f.runtime.VolumeContents["demo_uploads"] = "written-since-the-backup"
	require.NoError(t, f.runtime.Stop(ctx, ports.RuntimeConfig{}, nil, 0))

	// Config only. There is no restore hook either, so this exercises the
	// staging decision on its own.
	require.NoError(t, f.engine.Restore(ctx, ref, ports.RestoreOptions{
		Components:           []ports.Component{ports.ComponentConfig},
		IdentityFile:         f.identity,
		TargetInstallationID: "inst-volumes",
	}))

	assert.NotContains(t, f.runtime.Calls, "RestoreVolume:demo_uploads",
		"a restore scoped to config wrote into a volume")
	assert.Equal(t, "written-since-the-backup", f.runtime.VolumeContents["demo_uploads"])
}

// --no-downtime, which skips and reports rather than silently taking a hot copy
// of a volume nobody classified.
func TestNoDowntimeSkipsUndeclaredVolumesAndSaysSo(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{Volumes: map[string]domain.VolumeSpec{
		"uploads": {Consistency: domain.VolumeHot},
	}}, false)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	assert.NotContains(t, f.runtime.Calls, "Stop", "--no-downtime stopped a service")

	manifest, err := f.engine.Inspect(ctx, ref)
	require.NoError(t, err)
	require.Len(t, manifest.VolumeRecords(), 1)
	assert.Equal(t, "uploads", manifest.VolumeRecords()[0].Volume.Volume)

	for _, name := range []string{"pgdata", "cache"} {
		skipped := uncapturedRecord(t, manifest, name)
		assert.Contains(t, skipped.Reason, "undeclared")
	}
}

// The air-gapped machine, which is the one this matters for. It must learn
// which image to pull, not read a Docker error.
func TestAMissingHelperImageFailsWithThePullCommand(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	f.runtime.HelperMissing = true

	_, err := f.engine.Create(context.Background(), ports.Scope{}, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not on this machine")

	// The remedy, and it has to survive the wrap on the way out. The hint is
	// what the CLI prints under the message, so an error that loses it
	// leaves the operator with a diagnosis and nothing to do about it --
	// on the one machine where that matters most.
	hint := domain.AsError(err).Hint
	assert.Contains(t, hint, "docker pull",
		"the failure does not tell the operator what to do about it")
	assert.Contains(t, hint, "busybox")
}

// Decision 8. "No space left on device" halfway through a hundred-gigabyte copy
// arrives after the product has already been stopped, and does not say by how
// much it missed.
func TestABackupThatWillNotFitIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)

	// A petabyte, which is larger than any machine this suite runs on and
	// costs nothing to claim: the fake reports whatever size it is told.
	f.runtime.VolumeSizes = map[string]int64{"demo_uploads": 1 << 50}

	_, err := f.engine.Create(context.Background(), ports.Scope{}, nil)
	require.Error(t, err)

	// Both numbers, because "no space left on device" is the message this
	// exists to replace and it contains neither.
	message := err.Error()
	assert.Contains(t, message, "needs about", "the refusal does not say how much it needs")
	assert.Contains(t, message, "is free on", "the refusal does not say how much there is")
	assert.Contains(t, message, "TiB", "the required figure is not in the message")

	// Before anything: nothing stopped, nothing written.
	assert.NotContains(t, f.runtime.Calls, "Stop",
		"the product was stopped for a backup that was then refused")
	assert.NotContains(t, f.runtime.Calls, "CaptureVolume:demo_uploads")

	backups, err := f.engine.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, backups)
}

// A failure during a cold capture must not leave the product stopped.
func TestAFailedCaptureStillStartsTheServicesBackUp(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	f.runtime.Fail["CaptureVolume"] = assert.AnError

	_, err := f.engine.Create(context.Background(), ports.Scope{}, nil)
	require.Error(t, err)

	states, err := f.runtime.Status(context.Background(), ports.RuntimeConfig{})
	require.NoError(t, err)
	for _, s := range states {
		assert.True(t, s.Running(),
			"%s was left stopped by a failed backup, which turns a nightly "+
				"job into an outage", s.Name)
	}
}

// A backup that fails leaves nothing behind that looks like a backup.
func TestAFailedVolumeCaptureLeavesNoPartialBackup(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	f.runtime.Fail["CaptureVolume"] = assert.AnError

	_, err := f.engine.Create(context.Background(), ports.Scope{}, nil)
	require.Error(t, err)

	backups, err := f.engine.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, backups, "a half-written backup directory survived a failed capture")
}

// An older manager must refuse a backup it would half-restore rather than
// silently returning the database and none of the files.
func TestAVolumeBackupDeclaresASchemaAnOlderManagerRefuses(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	manifest, err := f.engine.Inspect(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, hookbackup.BackupManifestSchemaVersion, manifest.SchemaVersion)
	assert.GreaterOrEqual(t, manifest.SchemaVersion, 3,
		"volumes shipped without bumping the backup manifest schema, so a "+
			"manager that predates them would restore this backup and lose them")
}
