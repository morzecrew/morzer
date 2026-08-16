package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// recordingHooks is a backup hook that writes a dump and counts its own runs.
//
// The count is the assertion: it is what tells "the hook never ran" apart from
// "the hook ran and its output was thrown away", which is the whole difference
// between refusing before anything is written and refusing after.
type recordingHooks struct {
	ran int

	// dump is how many bytes of database to write. Zero writes a token one;
	// a test about disk space says how big, because the dump's size is the
	// thing being reasoned about.
	dump int
}

func (h *recordingHooks) Run(
	_ context.Context, _ domain.Release, _ []string, env ports.HookEnv, _ time.Duration,
) (ports.HookOutcome, error) {
	h.ran++

	contents := []byte("-- the product's database\n")
	if h.dump > 0 {
		contents = bytes.Repeat([]byte("x"), h.dump)
	}

	path := filepath.Join(env.BackupDir, "database.sql")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return ports.HookOutcome{}, err
	}
	return ports.HookOutcome{Result: ports.HookResult{
		Artifacts: []ports.HookArtifact{{Name: "db", Path: "database.sql"}},
	}}, nil
}

// newVolumeFixture wires the real engine to a fake runtime, with real age keys
// so the encryption assertions mean something.
func newVolumeFixture(t *testing.T, spec domain.BackupSpec, allowDowntime bool) *volumeFixture {
	t.Helper()
	return newVolumeFixtureWithHook(t, spec, allowDowntime, nil, nil)
}

// newVolumeFixtureWithHook is the same fixture for the vendor who *did* write a
// backup hook, so a test can assert what the manager does before running it.
//
// free stubs the free-space reading, and nil uses the real filesystem. A space
// check measured against the host's own disk is a check whose test passes on the
// machine that wrote it and fails on a bigger one.
func newVolumeFixtureWithHook(
	t *testing.T, spec domain.BackupSpec, allowDowntime bool, hook *recordingHooks,
	free func(string) (int64, error),
) *volumeFixture {
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
	var hooks ports.HookRunner
	if hook != nil {
		hooks = hook
		rel.Manifest.Operations = map[string]domain.OperationSpec{
			domain.OpBackup: {Kind: domain.OperationKindHook, Command: []string{"hooks/backup"}},
		}
	}

	return &volumeFixture{
		engine: hookbackup.New(hookbackup.Config{
			// Nil unless a test asked for a hook, because the
			// release then declares no backup operation: this
			// fixture is the vendor who never wrote one, which is
			// half of what volumes are for.
			Hooks:          hooks,
			Release:        rel,
			Installation:   domain.Installation{ID: "inst-volumes", Product: "demo"},
			Paths:          paths,
			ManagerVersion: "0.0.0-test",
			Runtime:        runtime,
			RuntimeConfig:  ports.RuntimeConfig{Product: "demo"},
			AllowDowntime:  allowDowntime,
			Now:            func() time.Time { return time.Now().UTC() },
			FreeSpace:      free,
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

// The refusal above must not take away the remedy it points at.
//
// A backup whose volume metadata is damaged is exactly the backup an operator
// scopes their way around -- "give me the database, I will deal with the files
// by hand". Refusing that too would mean the corruption costs them the one
// recovery path still open.
func TestADamagedVolumeRecordStillAllowsARestoreThatExcludesVolumes(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)
	stripVolumeMetadata(t, ref.Path, "uploads")

	require.NoError(t, f.runtime.Stop(ctx, ports.RuntimeConfig{}, nil, 0))

	// Config only: the damaged component is not in scope, so nothing about
	// it is being relied on.
	err = f.engine.Restore(ctx, ref, ports.RestoreOptions{
		Components:           []ports.Component{ports.ComponentConfig},
		IdentityFile:         f.identity,
		TargetInstallationID: "inst-volumes",
	})
	require.NoError(t, err,
		"a restore that excludes volumes was refused because of a volume it "+
			"was never going to touch")
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

// stripVolumeMetadata removes a volume component's `volume` object from a
// backup's manifest, producing the malformed schema-3 backup a partial write or
// a hand-edited manifest would leave.
func stripVolumeMetadata(t *testing.T, dir, name string) {
	t.Helper()

	path := filepath.Join(dir, ports.BackupManifestFileName)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var manifest map[string]any
	require.NoError(t, json.Unmarshal(raw, &manifest))

	components, ok := manifest["components"].([]any)
	require.True(t, ok, "the manifest has no component list to damage")

	stripped := false
	for _, entry := range components {
		c, ok := entry.(map[string]any)
		if !ok || c["component"] != string(ports.ComponentVolumes) {
			continue
		}
		volume, ok := c["volume"].(map[string]any)
		if !ok || volume["volume"] != name {
			continue
		}
		delete(c, "volume")
		stripped = true
	}
	require.True(t, stripped, "volume %q is not in the manifest, so nothing was damaged", name)

	edited, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, edited, 0o600))
}

// A volume component that does not say which volume it holds is a backup this
// manager cannot fully restore, and it has to refuse rather than narrow.
//
// The accessor that finds volume records skips a record with no metadata, which
// is the right answer for a listing and the wrong one for a restore: the backup
// read as one that simply had no volumes, so the restore put back everything
// else and reported success with the product's files still missing.
func TestAVolumeComponentWithNoMetadataIsRefusedRatherThanSkipped(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	stripVolumeMetadata(t, ref.Path, "uploads")

	// Everything down and the volume moved on, so the damaged record is the
	// only thing that can stop the restore -- and the only thing that can
	// explain the volume still holding the newer bytes afterwards.
	require.NoError(t, f.runtime.Stop(ctx, ports.RuntimeConfig{}, nil, 0))
	f.runtime.VolumeContents["demo_uploads"] = "written-since-the-backup"

	err = f.engine.Restore(ctx, ref, ports.RestoreOptions{
		IdentityFile: f.identity, TargetInstallationID: "inst-volumes",
	})

	require.Error(t, err,
		"a backup whose volume component names no volume restored the rest and "+
			"reported success, leaving the volume data behind")
	assert.Contains(t, err.Error(), "does not say which volume it holds")
	assert.Equal(t, "written-since-the-backup", f.runtime.VolumeContents["demo_uploads"],
		"the damaged record was acted on rather than refused")
}

// A restore must refuse when it cannot resolve the project, rather than falling
// back to the volume name the backup recorded.
//
// For a project renamed since the backup, that name is a volume no container
// mounts: the restore untarred somebody's uploads into storage nothing reads,
// reported success, and changed nothing the deployment uses.
func TestARestoreIsRefusedWhenTheProjectsVolumesCannotBeRead(t *testing.T) {
	f := newVolumeFixture(t, domain.BackupSpec{}, true)
	ctx := context.Background()

	ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
	require.NoError(t, err)

	require.NoError(t, f.runtime.Stop(ctx, ports.RuntimeConfig{}, nil, 0))
	f.runtime.VolumeContents["demo_uploads"] = "written-since-the-backup"
	f.runtime.Fail["Volumes"] = assert.AnError

	err = f.engine.Restore(ctx, ref, ports.RestoreOptions{
		IdentityFile: f.identity, TargetInstallationID: "inst-volumes",
	})

	require.Error(t, err,
		"a restore wrote a volume back without being able to confirm the project "+
			"still mounts it")
	assert.Contains(t, err.Error(), "which volume to write into")

	assert.NotContains(t, f.runtime.Calls, "RestoreVolume:demo_uploads",
		"the restore wrote into the name the backup remembered, which for a "+
			"renamed project is a volume nothing mounts")
	assert.Equal(t, "written-since-the-backup", f.runtime.VolumeContents["demo_uploads"])
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
//
// It is now the space check that catches it rather than the capture, because
// the measurement runs through the same image and refuses first. That is the
// same message a step earlier: nothing the hook wrote, nothing stopped. Asserted
// rather than assumed, since "refusing early costs nothing here" is the whole
// reason the check may refuse on a measurement at all.
func TestAMissingHelperImageFailsWithThePullCommand(t *testing.T) {
	hook := &recordingHooks{}
	f := newVolumeFixtureWithHook(t, domain.BackupSpec{}, true, hook, nil)
	f.runtime.HelperMissing = true

	_, err := f.engine.Create(context.Background(), ports.Scope{}, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not on this machine")

	assert.Zero(t, hook.ran,
		"the product dumped its database for a backup that could never have "+
			"captured a volume")
	assert.NotContains(t, f.runtime.Calls, "Stop",
		"the product was stopped for a backup that was then refused")

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

// ...and "before anything is written" includes the release's own backup hook,
// which dumps a database onto the very disk the check is about.
//
// Measured after the hook, an operator whose disk was too small paid for a full
// pg_dump before being told the backup would not fit -- and was told so while
// the dump was still occupying the space it was refused for.
func TestABackupThatWillNotFitIsRefusedBeforeTheHookRuns(t *testing.T) {
	hook := &recordingHooks{}
	f := newVolumeFixtureWithHook(t, domain.BackupSpec{}, true, hook, nil)

	f.runtime.VolumeSizes = map[string]int64{"demo_uploads": 1 << 50}

	_, err := f.engine.Create(context.Background(), ports.Scope{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs about")

	assert.Zero(t, hook.ran,
		"the product dumped its database for a backup the manager already knew "+
			"would not fit, and the dump was written to the disk it did not fit on")
	assert.NotContains(t, f.runtime.Calls, "Stop",
		"the product was stopped for a backup that was then refused")
}

// The volumes are copied in plaintext and then encrypted one component at a
// time, each ciphertext written beside its plaintext -- so the space a capture
// needs is the volumes plus one more copy of the largest *component*.
//
// After a hook has run, the largest component is normally its database dump and
// not any volume. Reserving `volumes + largest volume` at the recheck left the
// difference unclaimed: the copy went ahead, the product was stopped for it, and
// the backup met "no space left on device" while encrypting the dump -- the one
// point where a refusal costs an outage that has already been paid for.
func TestTheHooksOwnOutputIsCountedBeforeTheVolumesAreCopied(t *testing.T) {
	// A dump far larger than any volume in the fixture, which is the
	// ordinary shape: the volumes here are a few dozen bytes of simulated
	// contents apiece.
	hook := &recordingHooks{dump: 64 << 10}

	// Roomy before the hook, and afterwards more than the volumes need but
	// less than encrypting the dump will: the exact window the reservation
	// used to miss.
	var reads int
	free := func(string) (int64, error) {
		reads++
		if reads == 1 {
			return 1 << 30, nil
		}
		return 4 << 10, nil
	}
	f := newVolumeFixtureWithHook(t, domain.BackupSpec{}, true, hook, free)

	_, err := f.engine.Create(context.Background(), ports.Scope{}, nil)

	require.Error(t, err,
		"the backup went on to stop the product and copy its volumes with no room "+
			"left to encrypt the dump the hook had just written")
	message := err.Error()
	assert.Contains(t, message, "needs about", "the refusal does not say how much it needs")
	assert.Contains(t, message, "is free on", "the refusal does not say how much there is")

	assert.Equal(t, 1, hook.ran, "the hook is what produced the dump being accounted for")
	assert.NotContains(t, f.runtime.Calls, "Stop",
		"the product was stopped for a backup that was then refused")
	assert.NotContains(t, f.runtime.Calls, "CaptureVolume:demo_uploads",
		"a volume was copied for a backup that was then refused")

	backups, listErr := f.engine.List(context.Background())
	require.NoError(t, listErr)
	assert.Empty(t, backups, "the refused backup was left where a restore could find it")
}

// ...and the other direction, which costs an operator just as much: a
// reservation that pads rather than models refuses backups that would have
// fitted. Room for the volumes and one more copy of the dump is enough, and
// nothing beyond it may be demanded.
func TestABackupThatFitsWithRoomForOneEncryptedCopyIsTaken(t *testing.T) {
	hook := &recordingHooks{dump: 64 << 10}

	// The volumes are tens of bytes, so this is the dump's ciphertext and
	// very little else -- and deliberately far short of the dump plus every
	// component at once, which is what reserving a sum would need.
	free := func(string) (int64, error) { return 80 << 10, nil }
	f := newVolumeFixtureWithHook(t, domain.BackupSpec{}, true, hook, free)

	ref, err := f.engine.Create(context.Background(), ports.Scope{}, nil)
	require.NoError(t, err, "a backup that fits was refused")

	manifest, err := f.engine.Inspect(context.Background(), ref)
	require.NoError(t, err)
	assert.Len(t, manifest.VolumeRecords(), 3)
}

// unreadableMeasurement is what the compose adapter returns when the helper ran
// and its answer could not be used: an image without `find`, a `du` whose output
// will not parse, a figure past what a byte count holds. Each is a fixed
// property of the helper in this environment and says the same thing tomorrow.
func unreadableMeasurement(message, hint string) error {
	return domain.RuntimeError(nil, "%s", message).WithHint("%s", hint)
}

// measurementDidNotRun is the other shape: the `docker run` that would have
// measured never completed, so nothing was learned about the volume and it may
// well work next time.
func measurementDidNotRun(volume string) error {
	return domain.RuntimeError(
		fmt.Errorf("%w: %w", domain.ErrMeasureIncomplete, errors.New("exited with code 1")),
		"cannot measure volume %s", volume)
}

// A volume the manager cannot measure refuses the backup, and refuses it at the
// only moment where a refusal is free.
//
// This used to be waved through -- and worse, waved the *whole* reservation
// through with it -- on the reasoning that a backup refused over a measurement
// is refused for a reason that has nothing to do with whether it fits, and that
// the copy would fail honestly if it did not. It would: after the hook has
// dumped the database and after the services have been stopped for the copy. The
// honest failure costs an outage that was already paid for, which is the whole
// reason the gate is in front of both.
func TestAVolumeThatCannotBeMeasuredRefusesTheBackupBeforeAnythingIsWritten(t *testing.T) {
	cases := map[string]struct{ message, hint string }{
		// The helper image an operator overrode with something that has
		// no `find`. `tar` is still there, so the capture would have
		// worked -- and gone ahead unbudgeted.
		"a helper image that cannot walk the volume": {
			message: "cannot measure volume demo_uploads: the helper reported no " +
				"entries at all, not even the mount point",
			hint: "the volume helper image needs `find` and `wc` beside `du`",
		},
		// The inversion this gate exists to prevent, arrived at from the
		// other end: a volume too large to express as bytes disabled the
		// check instead of failing it.
		"a size no byte count can hold": {
			message: "cannot measure volume demo_uploads: du reported " +
				"9223372036854775807 KiB, which is more than a byte count can hold",
			hint: "volumes are measured with `du` inside the helper image",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			hook := &recordingHooks{}
			// Room for a thousand of this deployment, so the refusal
			// is unambiguously about the measurement rather than
			// about the disk.
			free := func(string) (int64, error) { return 1 << 40, nil }
			f := newVolumeFixtureWithHook(t, domain.BackupSpec{}, true, hook, free)
			f.runtime.VolumeSizeErrors = map[string]error{
				"demo_uploads": unreadableMeasurement(tc.message, tc.hint),
			}

			_, err := f.engine.Create(context.Background(), ports.Scope{}, nil)

			require.Error(t, err,
				"a volume nobody could size was copied onto a disk nobody checked")
			assert.Contains(t, err.Error(), "cannot measure volume uploads",
				"the refusal does not name the volume it could not measure")
			assert.Contains(t, err.Error(), tc.message,
				"the adapter's own diagnosis was lost on the way out")
			assert.Contains(t, domain.AsError(err).Hint, tc.hint,
				"the refusal carries no remedy, so an operator has a diagnosis "+
					"and nothing to do about it")

			assert.Zero(t, hook.ran,
				"the product dumped its database for a backup the manager was "+
					"never going to be able to check")
			assert.NotContains(t, f.runtime.Calls, "Stop",
				"the product was stopped for a backup that was then refused")
			assert.NotContains(t, f.runtime.Calls, "CaptureVolume:demo_pgdata",
				"a volume was copied for a backup that was then refused")

			backups, listErr := f.engine.List(context.Background())
			require.NoError(t, listErr)
			assert.Empty(t, backups, "the refused backup was left where a restore could find it")
		})
	}
}

// One volume nobody could measure decides nothing about the volumes beside it.
//
// This is the defect at its sharpest. A single failed measurement returned the
// zero value for the *whole* plan, so a petabyte volume that measured perfectly
// well was carried into the capture with no reservation at all -- and then the
// services were stopped and the copy began. The failure it was protecting
// against arrived anyway, one step later and with the downtime already spent.
func TestOneUnmeasurableVolumeDoesNotDisableTheCheckOnTheOnesBesideIt(t *testing.T) {
	free := func(string) (int64, error) { return 1 << 30, nil }
	f := newVolumeFixtureWithHook(t, domain.BackupSpec{}, true, nil, free)

	f.runtime.VolumeSizes = map[string]int64{"demo_uploads": 1 << 50}
	f.runtime.VolumeSizeErrors = map[string]error{
		"demo_cache": measurementDidNotRun("demo_cache"),
	}

	_, err := f.engine.Create(context.Background(), ports.Scope{}, nil)

	require.Error(t, err,
		"a cache volume that could not be measured abandoned the reservation for "+
			"the petabyte beside it, and the backup went on to stop the product "+
			"and copy it")
	message := err.Error()
	assert.Contains(t, message, "needs about", "the refusal does not say how much it needs")
	assert.Contains(t, message, "TiB",
		"the volume that measured fine was not counted, so the figure is a few "+
			"dozen bytes of simulated contents rather than a petabyte")

	assert.NotContains(t, f.runtime.Calls, "Stop",
		"the product was stopped for a backup that was then refused")
	assert.NotContains(t, f.runtime.Calls, "CaptureVolume:demo_uploads")
}

// ...and the other direction, which is what stops this becoming a blanket
// refusal.
//
// A `docker run` that exits non-zero on one awkward volume says nothing about
// the volume: it may work tomorrow, and the deployment was backing up fine
// yesterday. Refusing every backup over it would trade a disk that might fill
// for data that certainly goes stale, so the volume goes unbudgeted -- alone,
// rather than taking the gate down with it -- and the backup is taken.
func TestABackupThatFitsIsStillTakenWhenOneMeasurementDidNotRun(t *testing.T) {
	free := func(string) (int64, error) { return 1 << 30, nil }
	f := newVolumeFixtureWithHook(t, domain.BackupSpec{}, true, nil, free)

	f.runtime.VolumeSizeErrors = map[string]error{
		"demo_cache": measurementDidNotRun("demo_cache"),
	}

	ref, err := f.engine.Create(context.Background(), ports.Scope{}, nil)
	require.NoError(t, err,
		"a deployment whose helper exited non-zero on one volume stopped backing "+
			"up altogether, which is the failure the refusal was meant to avoid")

	manifest, err := f.engine.Inspect(context.Background(), ref)
	require.NoError(t, err)
	assert.Len(t, manifest.VolumeRecords(), 3,
		"the volume that could not be measured was not captured either")
}

// A cold capture must refuse when something holding one of its volumes cannot
// be stopped.
//
// `removing` occupies the volume but cannot be quiesced -- stopping it and
// starting it back fails, because by then there is no container. Omitting it
// from the stop list meant the copy was taken while that container still had
// the volume open, and the manifest recorded the result as `cold`: the one
// claim the whole component rests on, made about a copy that does not have it.
func TestAColdCaptureIsRefusedWhenAServiceCannotBeStopped(t *testing.T) {
	for _, state := range []string{ports.StateRemoving, "a-state-from-a-later-runtime"} {
		t.Run(state, func(t *testing.T) {
			f := newVolumeFixture(t, domain.BackupSpec{}, true)
			f.runtime.Services["worker"] = ports.ServiceState{Name: "worker", State: state}

			_, err := f.engine.Create(context.Background(), ports.Scope{}, nil)

			require.Error(t, err,
				"a volume was copied while %s held it open and the manifest called "+
					"the copy cold", "worker")
			message := err.Error()
			assert.Contains(t, message, "worker", "the refusal does not name the service")
			assert.Contains(t, message, state,
				"the refusal does not say what state the service is in, so the "+
					"operator cannot tell whether waiting will help")
			assert.Contains(t, message, "cold copy")

			// And it has to say what to do, because `removing` is
			// transient and "retry" is the answer.
			hint := domain.AsError(err).Hint
			assert.Contains(t, hint, "again",
				"the refusal leaves the operator with a diagnosis and nothing to do")

			assert.NotContains(t, f.runtime.Calls, "CaptureVolume:demo_uploads",
				"the volume was read anyway")

			backups, listErr := f.engine.List(context.Background())
			require.NoError(t, listErr)
			assert.Empty(t, backups, "a backup claiming a cold copy it did not take survived")
		})
	}
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

// Ctrl-C during a backup is the operator's own doing, and every volume
// operation runs a container -- so an interruption arrives as a failure from one
// of them, and whichever one it reaches must not dress it up as a diagnosis.
//
// The exit code was never what broke: it matches the interruption sentinel
// through any number of wraps, and the step engine asks the same way. What a
// wrap replaces is the sentence the operator reads and the `code` field
// anything machine-readable sorts on -- so a cancelled backup reported itself
// as "cannot capture volume uploads" with a remedy for a helper image that was
// never the problem.
func TestAnInterruptedBackupIsNotReportedAsAVolumeFailure(t *testing.T) {
	// Each volume operation the engine can be interrupted inside of, and
	// the fake's method name for it. Measuring runs before the copy and the
	// copy before anything else, so each needs its own run to be reached.
	for _, method := range []string{"VolumeSize", "CaptureVolume", "Status"} {
		t.Run(method, func(t *testing.T) {
			f := newVolumeFixture(t, domain.BackupSpec{}, true)
			f.runtime.Fail[method] = domain.Interrupted("docker run was cancelled")

			_, err := f.engine.Create(context.Background(), ports.Scope{}, nil)

			require.Error(t, err)
			structured := domain.AsError(err)
			assert.Equal(t, domain.CodeInterrupted, structured.Code,
				"a backup the operator stopped reports itself as a %s failure, and "+
					"machine-readable output carries that code out to whatever "+
					"reads it", structured.Code)
			assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(err))
			assert.Empty(t, structured.Hint,
				"the operator was handed a remedy for a problem they do not have")
		})
	}
}

// The restore side of the same rule, and it needs its own test because the two
// paths reach different call sites: a backup reads the project's volumes and
// returns that failure untouched, while a restore wraps it in order to explain
// which volume it could not identify. Only the wrapped one can lose the
// interruption.
func TestAnInterruptedRestoreIsNotReportedAsAVolumeFailure(t *testing.T) {
	for _, method := range []string{"Volumes", "RestoreVolume"} {
		t.Run(method, func(t *testing.T) {
			f := newVolumeFixture(t, domain.BackupSpec{}, true)
			ctx := context.Background()

			ref, err := f.engine.Create(ctx, ports.Scope{}, nil)
			require.NoError(t, err)

			// Stopped first: a restore refuses outright while anything
			// holds the volume, and that refusal would mask the one
			// under test.
			require.NoError(t, f.runtime.Stop(ctx, ports.RuntimeConfig{}, nil, 0))
			f.runtime.Fail[method] = domain.Interrupted("docker run was cancelled")

			err = f.engine.Restore(ctx, ref, ports.RestoreOptions{
				IdentityFile: f.identity, TargetInstallationID: "inst-volumes",
			})

			require.Error(t, err)
			structured := domain.AsError(err)
			assert.Equal(t, domain.CodeInterrupted, structured.Code,
				"a restore the operator stopped reports itself as a %s failure",
				structured.Code)
			assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(err))
		})
	}
}
