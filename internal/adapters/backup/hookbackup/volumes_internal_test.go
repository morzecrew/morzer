package hookbackup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

func storageFixture() ports.ProjectStorage {
	return ports.ProjectStorage{
		Volumes: []ports.NamedVolume{
			{Name: "caddy_data", Actual: "demo_caddy_data", Services: []string{"caddy"}},
			{Name: "pgdata", Actual: "demo_pgdata", Services: []string{"db"}},
			{Name: "uploads", Actual: "demo_uploads", Services: []string{"app", "worker"}},
		},
		Binds: []ports.BindMount{
			{Source: "/srv/legacy", Services: []string{"app"}},
		},
	}
}

func planned(t *testing.T, plan volumePlan, name string) plannedVolume {
	t.Helper()
	for _, v := range plan.capture {
		if v.volume.Name == name {
			return v
		}
	}
	t.Fatalf("volume %q is not in the capture list", name)
	return plannedVolume{}
}

func uncaptured(t *testing.T, plan volumePlan, name string) ports.UncapturedVolume {
	t.Helper()
	for _, u := range plan.uncaptured {
		if u.Volume == name {
			return u
		}
	}
	t.Fatalf("%q is not in the uncaptured list", name)
	return ports.UncapturedVolume{}
}

// This is decision 4, and it is the reason the RFC exists. A volume nobody has
// classified is one the manager knows nothing about, and reading an unknown
// volume live produces a copy that restores most of the time.
func TestAnUndeclaredVolumeIsCapturedCold(t *testing.T) {
	plan := planVolumes(storageFixture(), domain.BackupSpec{}, true)

	require.Len(t, plan.capture, 3)
	for _, v := range plan.capture {
		assert.Equal(t, ports.ConsistencyCold, v.consistency,
			"%s was captured %s without the release saying it was safe",
			v.volume.Name, v.consistency)
	}
}

// Decision 5: `hot` is a claim the vendor makes, never a guess the manager
// makes on their behalf.
func TestHotIsOnlyEverWhatTheManifestDeclared(t *testing.T) {
	spec := domain.BackupSpec{Volumes: map[string]domain.VolumeSpec{
		"uploads":    {Consistency: domain.VolumeHot},
		"caddy_data": {Consistency: domain.VolumeHot},
	}}

	plan := planVolumes(storageFixture(), spec, true)

	assert.Equal(t, ports.ConsistencyHot, planned(t, plan, "uploads").consistency)
	assert.Equal(t, ports.ConsistencyHot, planned(t, plan, "caddy_data").consistency)
	assert.Equal(t, ports.ConsistencyCold, planned(t, plan, "pgdata").consistency,
		"pgdata was not declared, so nothing may read it live")
}

func TestAnExcludedVolumeIsRecordedRatherThanSilentlyDropped(t *testing.T) {
	spec := domain.BackupSpec{Volumes: map[string]domain.VolumeSpec{
		"pgdata": {Consistency: domain.VolumeExclude},
	}}

	plan := planVolumes(storageFixture(), spec, true)

	for _, v := range plan.capture {
		assert.NotEqual(t, "pgdata", v.volume.Name, "an excluded volume was captured")
	}

	skipped := uncaptured(t, plan, "pgdata")
	assert.Equal(t, ports.VolumeKindNamed, skipped.Kind)
	assert.Contains(t, skipped.Reason, "exclude")
	assert.Equal(t, []string{"db"}, skipped.Services)
}

// Decision 2. A bind mount is an arbitrary host path, and the operator finding
// out that it was never in the backup should not happen during a restore.
func TestABindMountIsReportedAndNeverCaptured(t *testing.T) {
	plan := planVolumes(storageFixture(), domain.BackupSpec{}, true)

	for _, v := range plan.capture {
		assert.NotEqual(t, "/srv/legacy", v.volume.Name)
	}

	bind := uncaptured(t, plan, "/srv/legacy")
	assert.Equal(t, ports.VolumeKindBind, bind.Kind)
	assert.Contains(t, bind.Reason, "bind mount")
	assert.Equal(t, []string{"app"}, bind.Services)
}

// An anonymous volume holds real data and no restore can put it back, so the
// only thing the manager can do for the operator is say so. Tracking it in a map
// nothing reads -- which is what this did -- means the data is absent from every
// backup and absent from every report of what is absent.
func TestAnAnonymousVolumeIsReportedAsUncapturable(t *testing.T) {
	storage := storageFixture()
	storage.Anonymous = []ports.AnonymousVolume{{Service: "app", Target: "/scratch"}}

	plan := planVolumes(storage, domain.BackupSpec{}, true)

	for _, v := range plan.capture {
		assert.NotEqual(t, "/scratch", v.volume.Name, "an anonymous volume was captured")
	}

	anon := uncaptured(t, plan, "/scratch")
	assert.Equal(t, ports.VolumeKindAnonymous, anon.Kind)
	assert.Equal(t, []string{"app"}, anon.Services)
	assert.Contains(t, anon.Reason, "recreated",
		"the reason does not explain why naming the volume is the remedy")
}

// --no-downtime skips; it never downgrades to a hot copy. Silently taking a hot
// copy of an undeclared volume would be the manager making the vendor's claim
// for them, which is exactly what decision 5 forbids.
func TestNoDowntimeSkipsRatherThanCapturingHot(t *testing.T) {
	spec := domain.BackupSpec{Volumes: map[string]domain.VolumeSpec{
		"uploads": {Consistency: domain.VolumeHot},
	}}

	plan := planVolumes(storageFixture(), spec, false)

	require.Len(t, plan.capture, 1)
	assert.Equal(t, "uploads", plan.capture[0].volume.Name)
	assert.Equal(t, ports.ConsistencyHot, plan.capture[0].consistency)

	for _, name := range []string{"pgdata", "caddy_data"} {
		skipped := uncaptured(t, plan, name)
		assert.Contains(t, skipped.Reason, "undeclared")
		assert.Contains(t, skipped.Reason, "stop")
	}
}

// One downtime window, not one per volume: the total is the same and an
// operator watching the deployment sees a single dip rather than five.
func TestColdVolumesShareOneQuiesceWindow(t *testing.T) {
	spec := domain.BackupSpec{Volumes: map[string]domain.VolumeSpec{
		"caddy_data": {Consistency: domain.VolumeHot},
	}}

	plan := planVolumes(storageFixture(), spec, true)

	require.True(t, plan.hasCold())
	// Sorted, deduplicated, and without `caddy` -- a hot volume's services
	// have no reason to stop.
	assert.Equal(t, []string{"app", "db", "worker"}, plan.quiesceServices())
}

func TestAnAllHotPlanStopsNothing(t *testing.T) {
	spec := domain.BackupSpec{Volumes: map[string]domain.VolumeSpec{
		"uploads":    {Consistency: domain.VolumeHot},
		"pgdata":     {Consistency: domain.VolumeHot},
		"caddy_data": {Consistency: domain.VolumeHot},
	}}

	plan := planVolumes(storageFixture(), spec, true)

	assert.False(t, plan.hasCold())
	assert.Empty(t, plan.quiesceServices())
}

// A volume name becomes a file name inside the backup, and it comes out of a
// Compose file somebody else wrote. Compose is unlikely to accept a name with a
// separator in it, but depending on another tool to remember a rule that
// protects this one is how the rule stops being enforced.
func TestAVolumeNameThatWouldEscapeTheBackupIsRefused(t *testing.T) {
	for _, name := range []string{"../../etc/cron.d/x", "a/b", "..", ".", ""} {
		err := checkVolumeNames(ports.ProjectStorage{
			Volumes: []ports.NamedVolume{{Name: name, Actual: "demo_x"}},
		})
		require.Error(t, err, "volume name %q was accepted", name)
		assert.Contains(t, err.Error(), "not a usable file name")
	}
}

func TestOrdinaryVolumeNamesArePermitted(t *testing.T) {
	for _, name := range []string{"uploads", "caddy_data", "pg-data", "v1.2", "_x"} {
		require.NoError(t, checkVolumeNames(ports.ProjectStorage{
			Volumes: []ports.NamedVolume{{Name: name, Actual: "demo_" + name}},
		}), "volume name %q was refused", name)
	}
}

// A declaration for a volume the project does not have is not an error --
// releases drop volumes -- but it must not conjure one into the capture list.
func TestADeclarationForAnAbsentVolumeCapturesNothing(t *testing.T) {
	spec := domain.BackupSpec{Volumes: map[string]domain.VolumeSpec{
		"gone": {Consistency: domain.VolumeHot},
	}}

	plan := planVolumes(ports.ProjectStorage{}, spec, true)

	assert.Empty(t, plan.capture)
	assert.Empty(t, plan.uncaptured)
}
