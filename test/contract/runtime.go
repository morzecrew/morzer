package contract

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/ports"
)

// RuntimeFactory builds a runtime plus the configuration to exercise it with.
//
// The config comes from the factory because a real adapter needs real Compose
// files while the fake needs nothing: the suite must not assume either.
type RuntimeFactory func(t *testing.T) (ports.Runtime, ports.RuntimeConfig)

// RunRuntimeSuite runs every Runtime contract test.
//
// These are the behaviours the lifecycle layer depends on. The ones that
// matter most are idempotence and the volume-removal refusal: `apply` calls Up
// on every boot, and a compensation that silently destroyed data would be
// worse than the failure it was undoing.
func RunRuntimeSuite(t *testing.T, newRuntime RuntimeFactory) {
	t.Helper()
	ctx := context.Background()

	t.Run("validate has no side effects", func(t *testing.T) {
		rt, cfg := newRuntime(t)

		before, err := rt.Status(ctx, cfg)
		require.NoError(t, err)

		_, err = rt.Validate(ctx, cfg)
		require.NoError(t, err)

		after, err := rt.Status(ctx, cfg)
		require.NoError(t, err)
		assert.Equal(t, len(before), len(after),
			"Validate must not start, stop, or create anything")
	})

	t.Run("validate reports the services it resolved", func(t *testing.T) {
		rt, cfg := newRuntime(t)

		rendered, err := rt.Validate(ctx, cfg)
		require.NoError(t, err)
		assert.NotEmpty(t, rendered.Services,
			"the plan view and the health checks both need the service list")
	})

	t.Run("up is idempotent", func(t *testing.T) {
		rt, cfg := newRuntime(t)

		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}))
		first, err := rt.Status(ctx, cfg)
		require.NoError(t, err)

		// systemd calls `apply --startup` at every boot. If Up were not
		// idempotent, every reboot would be a deployment.
		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}),
			"Up on an already-converged project must succeed")

		second, err := rt.Status(ctx, cfg)
		require.NoError(t, err)
		assert.Equal(t, len(first), len(second),
			"a second Up must not change the number of services")
	})

	t.Run("status reports running services after up", func(t *testing.T) {
		rt, cfg := newRuntime(t)
		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}))

		states, err := rt.Status(ctx, cfg)
		require.NoError(t, err)
		require.NotEmpty(t, states)

		for _, s := range states {
			assert.NotEmpty(t, s.Name, "every service state needs a name to be actionable")
		}
	})

	t.Run("down without the volume flag preserves volumes", func(t *testing.T) {
		rt, cfg := newRuntime(t)
		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}))

		// The default must never destroy data. Compensation paths call
		// Down, and a default that removed volumes would make a failed
		// update delete the database.
		require.NoError(t, rt.Down(ctx, cfg, ports.DownOptions{}))

		states, err := rt.Status(ctx, cfg)
		require.NoError(t, err)
		for _, s := range states {
			assert.NotEqual(t, "running", s.State, "Down must stop the services")
		}
	})

	t.Run("down then up recovers", func(t *testing.T) {
		rt, cfg := newRuntime(t)

		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}))
		require.NoError(t, rt.Down(ctx, cfg, ports.DownOptions{}))
		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}),
			"a stopped project must be startable again; compensation relies on it")

		states, err := rt.Status(ctx, cfg)
		require.NoError(t, err)
		require.NotEmpty(t, states)
	})

	t.Run("status on a project that was never started does not error", func(t *testing.T) {
		rt, cfg := newRuntime(t)

		// `doctor` and `status` run before anything has been applied.
		// An error here would make them useless on a fresh machine.
		states, err := rt.Status(ctx, cfg)
		require.NoError(t, err, "Status must work before the first Up")
		assert.Empty(t, states)
	})

	t.Run("a non-zero one-shot exit is data, not an error", func(t *testing.T) {
		rt, cfg := newRuntime(t)

		// The hook ABI gives exit 2 the meaning "nothing to do". If the
		// runtime turned a non-zero exit into an error, the caller
		// could never distinguish that from a failure.
		res, err := rt.RunOneShot(ctx, cfg, "migrate", ports.RunOptions{Remove: true})
		require.NoError(t, err, "a process that ran and exited is a result, not a transport failure")
		assert.GreaterOrEqual(t, res.ExitCode, 0)
	})

	runQuiesceSuite(t, newRuntime)
	runVolumeSuite(t, newRuntime)
}

// runQuiesceSuite covers Stop and Start -- the pair a backup uses to get
// writers out of the way before it reads their storage.
//
// The interesting assertion is not that Stop returns nil. It is *what state the
// runtime then reports*, because the backup engine reads that state back
// through ServiceState.OccupiesVolume to decide whether writing into a volume is
// safe. A runtime that reported "stopped" where another reports "exited" would
// leave `morzer restore` refusing forever on one backend and proceeding on the
// other, with nothing in either backend's own tests to notice.
func runQuiesceSuite(t *testing.T, newRuntime RuntimeFactory) {
	t.Helper()
	ctx := context.Background()

	t.Run("stop releases the volumes without removing the services", func(t *testing.T) {
		rt, cfg := newRuntime(t)
		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}))

		// The positive control. Without it, "nothing occupies a volume
		// after Stop" is also true of a project that never started, and
		// the check below would pass while measuring nothing.
		before, err := rt.Status(ctx, cfg)
		require.NoError(t, err)
		require.NotEmpty(t, before, "the fixture did not start anything")
		assert.True(t, anyOccupies(before),
			"nothing held a volume before Stop, so this suite cannot tell "+
				"whether Stop is what released them")

		require.NoError(t, rt.Stop(ctx, cfg, nil, 30*time.Second))

		after, err := rt.Status(ctx, cfg)
		require.NoError(t, err)

		// Stop halts; it does not tear down. Down is the one that
		// removes, and the backup engine relies on the difference to
		// put back exactly what it took away.
		assert.Len(t, after, len(before),
			"Stop removed services rather than halting them; Start cannot put "+
				"back a container that no longer exists")

		for _, s := range after {
			assert.False(t, s.OccupiesVolume(),
				"after Stop, %s reports state %q, which the backup engine reads "+
					"as still holding its volume -- a restore would refuse forever",
				s.Name, s.State)
		}
	})

	t.Run("start puts back what stop halted", func(t *testing.T) {
		rt, cfg := newRuntime(t)
		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}))
		require.NoError(t, rt.Stop(ctx, cfg, nil, 30*time.Second))
		require.NoError(t, rt.Start(ctx, cfg, nil),
			"a backup stops services to read their volumes and must be able to "+
				"start them again; failing here leaves the product down")

		after, err := rt.Status(ctx, cfg)
		require.NoError(t, err)
		require.NotEmpty(t, after)
		assert.True(t, anyOccupies(after),
			"Start reported success but nothing came back up")
	})
}

// anyOccupies reports whether any service still holds its volumes open.
func anyOccupies(states []ports.ServiceState) bool {
	for _, s := range states {
		if s.OccupiesVolume() {
			return true
		}
	}
	return false
}

// runVolumeSuite covers the optional volume capabilities.
//
// Required rather than skipped when absent: every Runtime in this repository
// implements them, and a suite that quietly skips is how a contract stops being
// checked -- the failure RFC 0005 records for the secret store. A runtime that
// genuinely cannot read volumes should have to edit this line on purpose.
func runVolumeSuite(t *testing.T, newRuntime RuntimeFactory) {
	t.Helper()
	ctx := context.Background()

	t.Run("the project's volumes are reported sorted", func(t *testing.T) {
		rt, cfg := newRuntime(t)
		inspector, ok := rt.(ports.VolumeInspector)
		require.True(t, ok, "this runtime cannot report volumes")

		storage, err := inspector.Volumes(ctx, cfg)
		require.NoError(t, err)
		require.NotEmpty(t, storage.Volumes,
			"the fixture declares no named volume, so this suite proves nothing")

		// The port promises sorted, and the backup manifest records the
		// order. Unsorted output makes two backups of an unchanged
		// project differ, which is the thing the promise exists to stop.
		assert.True(t, sort.SliceIsSorted(storage.Volumes, func(i, j int) bool {
			return storage.Volumes[i].Name < storage.Volumes[j].Name
		}), "Volumes are not sorted by name: %+v", storage.Volumes)

		assert.True(t, sort.SliceIsSorted(storage.Binds, func(i, j int) bool {
			return storage.Binds[i].Source < storage.Binds[j].Source
		}), "Binds are not sorted by source: %+v", storage.Binds)

		for _, v := range storage.Volumes {
			assert.NotEmpty(t, v.Actual,
				"%s has no runtime name, so a capture would mount nothing", v.Name)
			assert.True(t, sort.StringsAreSorted(v.Services),
				"%s lists its services unsorted: %v", v.Name, v.Services)
		}
	})

	t.Run("a captured volume restores byte for byte", func(t *testing.T) {
		rt, cfg := newRuntime(t)
		capturer, ok := rt.(ports.VolumeCapturer)
		require.True(t, ok, "this runtime cannot capture volumes")
		inspector, ok := rt.(ports.VolumeInspector)
		require.True(t, ok, "this runtime cannot report volumes")

		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}))

		storage, err := inspector.Volumes(ctx, cfg)
		require.NoError(t, err)
		require.NotEmpty(t, storage.Volumes)
		volume := storage.Volumes[0].Actual

		tarball := filepath.Join(t.TempDir(), "volume.tar")
		require.NoError(t, capturer.CaptureVolume(ctx, cfg, volume, tarball))

		// The discriminating detail: a capture that wrote nothing would
		// still "succeed", and the restore below would still "work".
		info, err := os.Stat(tarball)
		require.NoError(t, err, "CaptureVolume reported success and wrote no file")
		assert.Positive(t, info.Size(), "the captured tarball is empty")

		require.NoError(t, capturer.RestoreVolume(ctx, cfg, volume, tarball),
			"a tarball this runtime produced was refused by the same runtime")
	})

	t.Run("a volume's size is reported in bytes", func(t *testing.T) {
		rt, cfg := newRuntime(t)
		capturer, ok := rt.(ports.VolumeCapturer)
		require.True(t, ok, "this runtime cannot capture volumes")
		inspector, ok := rt.(ports.VolumeInspector)
		require.True(t, ok, "this runtime cannot report volumes")

		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true}))

		storage, err := inspector.Volumes(ctx, cfg)
		require.NoError(t, err)
		require.NotEmpty(t, storage.Volumes)

		// Not an error and not negative. The space check sums these and
		// refuses a backup that will not fit; a negative would make the
		// sum smaller and turn the refusal into a pass.
		size, err := capturer.VolumeSize(ctx, cfg, storage.Volumes[0].Actual)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, size, int64(0),
			"a negative size would make the space check let any backup through")
	})

	t.Run("the helper image is named and pinned by digest", func(t *testing.T) {
		rt, _ := newRuntime(t)
		capturer, ok := rt.(ports.VolumeCapturer)
		require.True(t, ok, "this runtime cannot capture volumes")

		// `doctor` reports this and tells an operator to pull it. An
		// unpinned reference would make every backup depend on whatever
		// the registry served that night, with the product's data
		// mounted.
		assert.Regexp(t, `^[^\s@]+@sha256:[a-f0-9]{64}$`, capturer.HelperImage(),
			"the volume helper image is not pinned by digest")
	})
}
