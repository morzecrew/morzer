package contract

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	runInspectionSuite(t, newRuntime)
	RunOptionSuite(t, newRuntime)
}

// runInspectionSuite covers what `morzer logs`, `ps` and `stats` read.
//
// These three are the ones a fake cannot prove on its own, in opposite
// directions. The log framing is a promise about a *format*: the manager parses
// the container name out of every line to attribute a record to a service, so a
// runtime that framed its lines differently would produce a structured stream
// where nothing was attributed to anything -- and no unit test written against
// the fake would notice, because the fake would be emitting whatever shape the
// parser expects. And `docker stats` accepts flags that a scripted runner will
// agree to whatever they are.
func runInspectionSuite(t *testing.T, newRuntime RuntimeFactory) {
	t.Helper()
	ctx := context.Background()

	t.Run("every log line names the container that wrote it", func(t *testing.T) {
		rt, cfg := newRuntime(t)
		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 2 * time.Minute}))

		reader, err := rt.Logs(ctx, cfg, ports.LogOptions{Tail: 100, Timestamps: true})
		require.NoError(t, err)
		defer func() { _ = reader.Close() }()

		body, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.NotEmpty(t, strings.TrimSpace(string(body)),
			"the fixture's services print on start-up, so an empty stream means "+
				"the logs never arrived")

		var framed int
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			prefix, rest, ok := strings.Cut(line, "| ")
			if !ok {
				// The runtime's own narration about the stream
				// carries no frame, and is not what this counts.
				continue
			}
			framed++

			assert.NotEmpty(t, strings.TrimSpace(prefix),
				"a framed line named no container, so nothing can attribute it")

			// The instant, because it was asked for. Without it a
			// `--json` record's `ts` would have to be the moment the
			// manager happened to read the line, which is a
			// different fact wearing the same name.
			stamp, _, split := strings.Cut(rest, " ")
			require.True(t, split, "a framed line carried no text after the prefix: %q", line)
			_, err := time.Parse(time.RFC3339Nano, stamp)
			assert.NoError(t, err,
				"LogOptions.Timestamps was set and %q is not an instant", stamp)
		}
		assert.Positive(t, framed, "no line carried a container prefix:\n%s", body)
	})

	t.Run("stats report a running container's memory", func(t *testing.T) {
		rt, cfg := newRuntime(t)
		require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 2 * time.Minute}))

		stats, err := rt.Stats(ctx, cfg)
		require.NoError(t, err)
		require.NotEmpty(t, stats, "the project is running and nothing was sampled")

		for _, s := range stats {
			assert.NotEmpty(t, s.Container,
				"a sample with no container cannot be told from its own replica")
			// The one figure with no honest zero: a running
			// container uses memory, so 0 means the adapter read
			// the daemon's answer wrongly rather than that the
			// service is frugal.
			assert.Positive(t, s.MemoryBytes,
				"%s reports no memory, which no running container does", s.Container)
		}
	})

	t.Run("stats on a project that was never started report nothing", func(t *testing.T) {
		rt, cfg := newRuntime(t)

		// And do not error: `stats` runs on a machine mid-incident,
		// where "nothing is running" is the answer rather than a fault.
		stats, err := rt.Stats(ctx, cfg)
		require.NoError(t, err)
		assert.Empty(t, stats)
	})
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

		dir := t.TempDir()

		// Known bytes go in through RestoreVolume and come back out
		// through CaptureVolume, because those two are the whole
		// vocabulary: a contract that reached into the volume any other
		// way -- a shell in a container, a path on the host -- would be
		// asserting against one implementation's plumbing rather than
		// against the port.
		known := writeTar(t, filepath.Join(dir, "known.tar"), volumeFixture)
		require.NoError(t, capturer.RestoreVolume(ctx, cfg, volume, known))

		captured := filepath.Join(dir, "captured.tar")
		require.NoError(t, capturer.CaptureVolume(ctx, cfg, volume, captured))
		require.Equal(t, volumeFixture, tarContents(t, captured),
			"the capture does not hold what the volume holds, so every backup "+
				"this runtime takes stores something other than the volume it names")

		// The volume is changed under the capture, and the second
		// capture has to follow it. This is the leg that cannot pass
		// vacuously: a CaptureVolume that wrote a constant, an empty
		// archive, or the previous tarball again would satisfy every
		// other assertion here and fail this one, because the expected
		// contents are no longer the contents that were there before.
		other := writeTar(t, filepath.Join(dir, "other.tar"), replacementFixture)
		require.NoError(t, capturer.RestoreVolume(ctx, cfg, volume, other))

		changed := filepath.Join(dir, "changed.tar")
		require.NoError(t, capturer.CaptureVolume(ctx, cfg, volume, changed))
		require.Equal(t, replacementFixture, tarContents(t, changed),
			"the volume's contents changed and the capture did not: a backup "+
				"taken tonight would hold what the volume held some other night")

		// And back, from the runtime's own tarball -- the artifact a
		// backup encrypts and a restore replays. Anything the capture
		// dropped or the restore mangled shows up as a difference here
		// rather than as a volume nobody looks inside until an incident.
		require.NoError(t, capturer.RestoreVolume(ctx, cfg, volume, captured),
			"a tarball this runtime produced was refused by the same runtime")

		final := filepath.Join(dir, "final.tar")
		require.NoError(t, capturer.CaptureVolume(ctx, cfg, volume, final))
		assert.Equal(t, volumeFixture, tarContents(t, final),
			"a volume restored from this runtime's own capture came back holding "+
				"something else, so a restore returns data the backup never held")
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

	t.Run("a volume's size is an upper bound on the capture it will produce", func(t *testing.T) {
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

		dir := t.TempDir()
		loaded := writeTar(t, filepath.Join(dir, "bound.tar"), blockAlignedFixture())
		require.NoError(t, capturer.RestoreVolume(ctx, cfg, volume, loaded))

		size, err := capturer.VolumeSize(ctx, cfg, volume)
		require.NoError(t, err)

		captured := filepath.Join(dir, "captured.tar")
		require.NoError(t, capturer.CaptureVolume(ctx, cfg, volume, captured))

		info, err := os.Stat(captured)
		require.NoError(t, err)

		// The whole promise, and the only direction that costs anything.
		// A size that reads low passes the space check and then fills the
		// disk during the copy -- which happens after the services have
		// been stopped for it, so the operator meets it as an outage
		// rather than as a refusal.
		assert.GreaterOrEqual(t, size, info.Size(),
			"VolumeSize promised %d bytes and the capture wrote %d: a backup this "+
				"size is admitted onto a disk that cannot hold it, and the copy "+
				"fails with the product already down", size, info.Size())
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

// volumeFixture and replacementFixture are two different volumes' worth of
// contents, as file name to bytes.
//
// Two of them, and deliberately disjoint in both names and contents, because
// "the capture holds this" is only a claim about the capture if a capture that
// ignored the volume entirely would fail it. The second also drops a file the
// first had, so the port's promise that RestoreVolume *replaces* rather than
// merges is asserted rather than assumed.
//
// The bytes carry a newline and a NUL so that "byte for byte" means bytes: the
// helper's tar arrives on a pipe, and a reader that scanned it for lines would
// corrupt exactly this.
var (
	volumeFixture = map[string]string{
		"ledger.csv":       "invoice-0000-4471,4471.00\ninvoice-0000-4472,\x00\x01\x02,end\n",
		"notes/README.txt": "the quarterly report lives here",
	}
	replacementFixture = map[string]string{
		"receipts.csv": "refund-0000-0001,-12.50\n",
	}
)

// blockAlignedFixture is the volume that catches a size which is a measurement
// rather than a bound: many files, each an exact multiple of the filesystem
// block size.
//
// That is the shape with no slack in it. A `du` reading rounds every file up to
// a block, which hides tar's 512-byte-per-entry header behind the rounding for
// almost any other contents -- a volume of 40-byte files is measured at a
// kilobyte apiece and tars to a tenth of that. Files that land exactly on a
// block boundary round up to nothing, so what tar adds is all that is left, and
// a size that forgot to add it comes out about 11% short.
//
// 256 of them, at 4 KiB: enough that the framing exceeds the one directory's
// worth of block rounding that is the only slack left, on any filesystem whose
// block size is 4 KiB or less. On one with larger blocks the reading is
// over-generous instead and the assertion passes without proving anything --
// the promise still holds, it is only this fixture that stops being sharp.
func blockAlignedFixture() map[string]string {
	files := make(map[string]string, 256)
	for i := range 256 {
		files[fmt.Sprintf("block-%03d.dat", i)] = strings.Repeat("x", 4096)
	}
	return files
}

// writeTar builds a tarball holding files and returns its path.
//
// RestoreVolume is the only way into a volume the port offers, and it takes a
// tar -- so the suite has to make one. USTAR with a fixed modification time and
// an explicit entry for every parent directory, because it is read back by
// whatever `tar` the runtime's helper image carries, and the fewer extensions
// and conveniences that has to supply, the fewer implementations this battery
// quietly excludes.
func writeTar(t *testing.T, path string, files map[string]string) string {
	t.Helper()

	modTime := time.Unix(1_700_000_000, 0).UTC()
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)

	for _, dir := range parentDirs(files) {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     dir + "/",
			Mode:     0o755,
			Typeflag: tar.TypeDir,
			ModTime:  modTime,
			Format:   tar.FormatUSTAR,
		}))
	}
	for _, name := range sortedNames(files) {
		body := files[name]
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
			ModTime:  modTime,
			Format:   tar.FormatUSTAR,
		}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}

	require.NoError(t, tw.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
	return path
}

// tarContents reads a captured tarball back into file name to bytes.
//
// Regular files only, and names normalised: a helper that runs `tar -C /src -cf
// - .` reports its entries as `./ledger.csv` beside directory entries the
// volume's data does not depend on. Comparing the files rather than the archive
// bytes is deliberate -- two tars of identical contents differ in entry order
// and in the timestamps an extraction stamped on them, so byte equality would
// be a flake rather than a check.
func tarContents(t *testing.T, path string) map[string]string {
	t.Helper()

	f, err := os.Open(path)
	require.NoError(t, err, "CaptureVolume reported success and wrote no file")
	defer func() { _ = f.Close() }()

	out := map[string]string{}
	tr := tar.NewReader(f)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err,
			"%s is not a readable tar, so nothing downstream could restore it", path)

		if header.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(tr)
		require.NoError(t, err)
		out[strings.TrimPrefix(header.Name, "./")] = string(body)
	}
	return out
}

// parentDirs lists every directory the files sit in, shallowest first, so a tar
// never names a directory before it has created it.
func parentDirs(files map[string]string) []string {
	set := map[string]bool{}
	for name := range files {
		parts := strings.Split(name, "/")
		for i := 1; i < len(parts); i++ {
			set[strings.Join(parts[:i], "/")] = true
		}
	}

	out := make([]string, 0, len(set))
	for dir := range set {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

// sortedNames keeps the archive deterministic: a tarball that differed between
// runs would make a failure here impossible to compare against the last one.
func sortedNames(files map[string]string) []string {
	out := make([]string, 0, len(files))
	for name := range files {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// RunOptionSuite exercises ports.OptionResolver.
//
// It exists because the manager decides whether to refuse a release on what
// this returns (RFC 0023 decision 16), and there are two implementations of the
// rule: the compose adapter, and the fake that stands in for it in every unit
// test of the comparison. A fake resolving differently from the adapter would
// make those tests agree with a manager that refuses the wrong releases -- and
// the layering forbids the lifecycle package from importing the adapter, so
// this battery is the only place both can be asked the same questions.
//
// A runtime that declines the capability is a supported answer, and is reported
// rather than skipped silently: "no implementation" and "an implementation
// nobody ran" look identical in a pass list otherwise.
//
// Exported separately from RunRuntimeSuite because resolving options is pure:
// it needs no daemon, while the rest of the battery does. A caller that has a
// real adapter but no Docker can run this half, which is what keeps the shared
// rule enforced in the lane that runs on every commit rather than only in the
// tagged one.
func RunOptionSuite(t *testing.T, newRuntime RuntimeFactory) {
	t.Helper()

	rt, _ := newRuntime(t)
	resolver, ok := rt.(ports.OptionResolver)
	if !ok {
		t.Log("this runtime declines ports.OptionResolver; the manager compares declared options")
		return
	}

	t.Run("every declared key survives resolution", func(t *testing.T) {
		_, cfg := newRuntime(t)
		cfg.Options = map[string]string{"a_key_no_runtime_knows": "kept"}

		resolved := resolver.ResolveOptions(cfg)
		assert.Equal(t, "kept", resolved["a_key_no_runtime_knows"],
			"a resolver that drops what it does not understand hides a change "+
				"the manager treats as durable; refusing an unknown key is Validate's job")
	})

	t.Run("resolution does not mutate what it was given", func(t *testing.T) {
		_, cfg := newRuntime(t)
		cfg.Options = map[string]string{"kept": "value"}

		before := maps.Clone(cfg.Options)
		resolver.ResolveOptions(cfg)
		assert.Equal(t, before, cfg.Options,
			"the declared map is the installation's record; resolving must copy, not edit")
	})

	t.Run("resolution is stable", func(t *testing.T) {
		_, cfg := newRuntime(t)

		first := resolver.ResolveOptions(cfg)
		assert.Equal(t, first, resolver.ResolveOptions(cfg),
			"the comparison runs on every converge; a resolver that answers "+
				"differently twice would refuse a release at random")
	})

	t.Run("a resolved value is already in force", func(t *testing.T) {
		_, cfg := newRuntime(t)
		cfg.Options = nil

		defaults := resolver.ResolveOptions(cfg)
		if len(defaults) == 0 {
			t.Log("this runtime fills in no defaults; nothing to compare against")
			return
		}

		// The whole point of the capability: declaring the value the
		// runtime would have used anyway must resolve to the same thing,
		// or the manager still refuses what it was taught to allow.
		cfg.Options = maps.Clone(defaults)
		assert.Equal(t, defaults, resolver.ResolveOptions(cfg),
			"writing out a default must resolve identically to omitting it")
	})
}
