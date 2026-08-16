package compose

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// The fixture is real output. It was taken from `docker compose config
// --format json` against a project with two services, a shared named volume,
// an external volume that renames itself, and a bind mount -- because the
// shapes that matter here are the ones Compose actually emits, and a
// hand-written approximation of them would agree with the parser and disagree
// with Docker.
const composeConfigJSON = `{
  "name": "demo",
  "services": {
    "app": {
      "image": "busybox",
      "volumes": [
        {"type": "volume", "source": "uploads", "target": "/data", "volume": {}},
        {"type": "bind", "source": "/etc/hosts", "target": "/etc/hosts", "read_only": true, "bind": {}},
        {"type": "volume", "source": "ext", "target": "/ext", "volume": {}},
        {"type": "tmpfs", "target": "/scratch", "tmpfs": {}},
        {"type": "volume", "target": "/anonymous", "volume": {}}
      ]
    },
    "web": {
      "image": "busybox",
      "volumes": [
        {"type": "volume", "source": "uploads", "target": "/shared", "volume": {}}
      ]
    }
  },
  "volumes": {
    "ext": {"name": "my_external", "external": true},
    "uploads": {"name": "demo_uploads"},
    "orphan": {"name": "demo_orphan"}
  }
}`

func TestParseStorageReadsTheProjectsVolumes(t *testing.T) {
	storage, err := parseStorage(composeConfigJSON)
	require.NoError(t, err)

	require.Len(t, storage.Volumes, 3, "expected ext, orphan and uploads and nothing else")

	byName := map[string]int{}
	for i, v := range storage.Volumes {
		byName[v.Name] = i
	}

	uploads := storage.Volumes[byName["uploads"]]
	assert.Equal(t, "demo_uploads", uploads.Actual)
	assert.False(t, uploads.External)
	// Both services, because this is the list a restore refuses against:
	// missing one of them would let a restore write into a volume `web`
	// still has open.
	assert.Equal(t, []string{"app", "web"}, uploads.Services)

	ext := storage.Volumes[byName["ext"]]
	assert.Equal(t, "my_external", ext.Actual,
		"an external volume names itself; using the project prefix would "+
			"capture an empty volume nobody mounts")
	assert.True(t, ext.External)

	// A declared volume nothing mounts is still the project's storage and
	// still holds whatever a previous release put there.
	orphan := storage.Volumes[byName["orphan"]]
	assert.Equal(t, "demo_orphan", orphan.Actual)
	assert.Empty(t, orphan.Services)
}

func TestParseStorageReportsBindMountsSeparately(t *testing.T) {
	storage, err := parseStorage(composeConfigJSON)
	require.NoError(t, err)

	require.Len(t, storage.Binds, 1)
	assert.Equal(t, "/etc/hosts", storage.Binds[0].Source)
	assert.Equal(t, []string{"app"}, storage.Binds[0].Services)

	// And it is emphatically not in Volumes: a bind mount that reached the
	// capture list would have the manager copying an arbitrary host path.
	for _, v := range storage.Volumes {
		assert.NotEqual(t, "/etc/hosts", v.Name)
	}
}

// A tmpfs mount holds nothing that outlives the container, and an anonymous
// volume gets a name that changes when the container is recreated -- so
// capturing either would produce a component no restore could put back.
func TestParseStorageIgnoresTmpfsAndAnonymousVolumes(t *testing.T) {
	storage, err := parseStorage(composeConfigJSON)
	require.NoError(t, err)

	for _, v := range storage.Volumes {
		assert.NotEmpty(t, v.Name, "an anonymous volume reached the capture list")
		assert.NotEqual(t, "/scratch", v.Name)
	}
}

// Compose has not always spelled out the resolved name. Falling back to the
// project prefix is what Compose itself does, and getting it wrong means
// mounting a volume that does not exist -- which Docker creates, empty, so the
// backup would succeed and contain nothing.
func TestParseStorageFallsBackToTheProjectPrefixedName(t *testing.T) {
	storage, err := parseStorage(`{
	  "name": "demo",
	  "services": {"app": {"volumes": [{"type": "volume", "source": "data", "target": "/d"}]}},
	  "volumes": {"data": {}}
	}`)
	require.NoError(t, err)

	require.Len(t, storage.Volumes, 1)
	assert.Equal(t, "demo_data", storage.Volumes[0].Actual)
}

// An external volume names itself, and Compose does not always resolve that
// name into the document: `external: true` with no `name:` key comes back with
// the key alone in the shapes below.
const externalVolumeConfigJSON = `{
  "name": "shop",
  "services": {
    "api": {
      "image": "busybox",
      "volumes": [
        {"type": "volume", "source": "shared_media", "target": "/media", "volume": {}}
      ]
    }
  },
  "volumes": {
    "shared_media": {"external": true},
    "legacy_archive": {"external": true}
  }
}`

// The project prefix belongs to volumes the project owns and creates. An
// external one is neither: prefixing it names a volume that does not exist, and
// `docker run --volume` creates a missing volume rather than refusing -- so the
// capture would succeed, hold a tar of an empty directory, and be discovered
// only by the restore that brought back nothing.
func TestAnExternalVolumeWithoutAResolvedNameKeepsItsOwnName(t *testing.T) {
	storage, err := parseStorage(externalVolumeConfigJSON)
	require.NoError(t, err)

	require.Len(t, storage.Volumes, 2)
	byName := map[string]ports.NamedVolume{}
	for _, v := range storage.Volumes {
		byName[v.Name] = v
	}

	mounted := byName["shared_media"]
	assert.True(t, mounted.External)
	assert.Equal(t, "shared_media", mounted.Actual,
		"a mounted external volume was project-prefixed, so the backup would "+
			"capture an empty volume Docker created on the spot")

	// The declared-but-unmounted branch resolves the name separately, and
	// got it wrong the same way.
	unmounted := byName["legacy_archive"]
	assert.True(t, unmounted.External)
	assert.Equal(t, "legacy_archive", unmounted.Actual,
		"an unmounted external volume was project-prefixed, so the backup "+
			"would capture an empty volume Docker created on the spot")
}

func TestParseStorageRejectsOutputItCannotRead(t *testing.T) {
	_, err := parseStorage("not json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot parse the merged compose configuration")
}

// The helper image is pinned by digest for the same reason every release image
// is: an unpinned helper makes each backup depend on whatever the registry
// served that night, and this one runs with the product's data mounted.
func TestTheHelperImageIsPinnedByDigest(t *testing.T) {
	assert.Regexp(t, `^[^\s@]+@sha256:[a-f0-9]{64}$`, DefaultHelperImage)
	assert.Equal(t, DefaultHelperImage, New(nil).HelperImage())
}

func TestTheHelperImageCanBeOverriddenButNotErased(t *testing.T) {
	r := New(nil, WithHelperImage("registry.internal/busybox@sha256:"+
		"0000000000000000000000000000000000000000000000000000000000000000"))
	assert.Contains(t, r.HelperImage(), "registry.internal/busybox")

	// An operator who sets the key to nothing gets the default rather than
	// a `docker run ""`, which fails with a message about no such image.
	assert.Equal(t, DefaultHelperImage, New(nil, WithHelperImage("  ")).HelperImage())
}

// A tag is not an override this manager can honour: it resolves to whatever the
// registry served last, and the image it resolves to runs with the product's
// data mounted. The refusal is late -- an Option cannot fail -- but it is a
// refusal, because using the default instead would back the deployment up
// through an image the operator did not choose and never hear about it.
func TestAHelperImageThatIsNotPinnedByDigestIsRefused(t *testing.T) {
	runner := exec.NewScripted()
	// The tag is present locally: the case that would otherwise run.
	runner.On("image inspect", exec.Result{})
	r := New(runner, WithHelperImage("busybox:latest"))

	dir := t.TempDir()
	src := filepath.Join(dir, "in.tar")
	require.NoError(t, os.WriteFile(src, []byte("tar"), 0o600))

	ctx := context.Background()
	cfg := ports.RuntimeConfig{Product: "demo"}
	calls := map[string]func() error{
		"capture": func() error {
			return r.CaptureVolume(ctx, cfg, "demo_uploads", filepath.Join(dir, "out.tar"))
		},
		"restore": func() error { return r.RestoreVolume(ctx, cfg, "demo_uploads", src) },
		"size": func() error {
			_, err := r.VolumeSize(ctx, cfg, "demo_uploads")
			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			require.Error(t, err, "a volume was read through an image that can be repointed between backups")
			assert.Contains(t, err.Error(), "not pinned by digest")
			assert.Contains(t, err.Error(), "busybox:latest",
				"the message does not name the value the operator set, "+
					"so they cannot tell which of their settings is wrong")

			// The remedy: the variable to change, and the command
			// that turns the tag they have into the digest it wants.
			hint := domain.AsError(err).Hint
			assert.Contains(t, hint, "MORZER_VOLUME_HELPER_IMAGE")
			assert.Contains(t, hint, "sha256")
		})
	}

	assert.False(t, runner.Ran("docker run"),
		"the unpinned helper was started anyway:\n"+runner.CommandLines())
	assert.Equal(t, "busybox:latest", r.HelperImage(),
		"the operator's configured image was swapped for the default, so a "+
			"`doctor` run would report an image the backup does not use")
}

// helperLab returns a runtime whose docker invocations are recorded rather than
// run, with the helper image reported as already present.
func helperLab(t *testing.T) (*Runtime, *exec.Scripted) {
	t.Helper()
	runner := exec.NewScripted()
	// `image inspect` succeeding is how HasImage answers "it is here".
	runner.On("image inspect", exec.Result{})
	return New(runner), runner
}

func helperArgv(t *testing.T, runner *exec.Scripted) string {
	t.Helper()
	for _, c := range runner.Calls() {
		line := strings.Join(c.Argv, " ")
		if strings.Contains(line, "docker run") {
			return line
		}
	}
	t.Fatalf("no `docker run` was issued; the runtime ran:\n%s", runner.CommandLines())
	return ""
}

// Decision 3, asserted where it is decided. A read-only source is the reason it
// is safe to run somebody else's image with the product's data mounted, and the
// flag that enforces it is one word in an argv nobody looks at again.
func TestAVolumeIsReadThroughAReadOnlyMount(t *testing.T) {
	r, runner := helperLab(t)

	require.NoError(t, r.CaptureVolume(context.Background(), ports.RuntimeConfig{Product: "demo"},
		"demo_uploads", filepath.Join(t.TempDir(), "uploads.tar")))

	argv := helperArgv(t, runner)
	assert.Contains(t, argv, "--volume demo_uploads:/src:ro",
		"the source is not mounted read-only, so a helper that misbehaves "+
			"can write into the product's data")
	assert.Contains(t, argv, "--network none",
		"the helper can reach a network it has no reason to reach")
	assert.Contains(t, argv, "--rm")
	assert.Contains(t, argv, DefaultHelperImage)
	assert.Contains(t, argv, "tar -C /src -cf - .")
}

// The other direction has to be writable, and must not also be readable as the
// capture mount -- writing into /src would silently produce a restore that
// changed nothing.
func TestAVolumeIsRestoredThroughAWritableMount(t *testing.T) {
	r, runner := helperLab(t)

	src := filepath.Join(t.TempDir(), "uploads.tar")
	require.NoError(t, os.WriteFile(src, []byte("tar"), 0o600))

	require.NoError(t, r.RestoreVolume(context.Background(), ports.RuntimeConfig{Product: "demo"},
		"demo_uploads", src))

	argv := helperArgv(t, runner)
	assert.Contains(t, argv, "--volume demo_uploads:/dst")
	assert.NotContains(t, argv, ":ro", "the restore mounted the volume read-only")
	assert.Contains(t, argv, "rm -rf",
		"the volume is not emptied first, so a restore merges rather than replaces")
	assert.Contains(t, argv, "tar -C /dst -xf -")
}

// The offline refusal, at the point it is made: before `docker run`, so an
// air-gapped machine gets the pull command rather than a registry error from
// inside a backup.
func TestAnAbsentHelperImageIsRefusedBeforeAnythingRuns(t *testing.T) {
	runner := exec.NewScripted()
	runner.OnExit("image inspect", 1, "Error: No such image: busybox")
	r := New(runner)

	err := r.CaptureVolume(context.Background(), ports.RuntimeConfig{},
		"demo_uploads", filepath.Join(t.TempDir(), "uploads.tar"))

	require.Error(t, err)
	assert.False(t, runner.Ran("docker run"),
		"the helper was started despite its image being absent")
	assert.Contains(t, err.Error(), "is not on this machine")

	// The remedy lives in the hint, which is what the CLI prints under the
	// message. It carries the full digest-pinned reference, so an operator
	// can paste it rather than go looking for which busybox.
	hint := domain.AsError(err).Hint
	assert.Contains(t, hint, "docker pull")
	assert.Contains(t, hint, DefaultHelperImage)
}

// A failed capture must not leave a zero-length tarball that a checksum would
// happily record as this volume's contents.
func TestAFailedCaptureRemovesTheFileItStarted(t *testing.T) {
	runner := exec.NewScripted()
	runner.On("image inspect", exec.Result{})
	runner.OnExit("docker run", 2, "tar: /src: Permission denied")
	r := New(runner)

	dest := filepath.Join(t.TempDir(), "uploads.tar")
	err := r.CaptureVolume(context.Background(), ports.RuntimeConfig{}, "demo_uploads", dest)

	require.Error(t, err)
	_, statErr := os.Stat(dest)
	assert.Error(t, statErr, "a truncated tarball was left where the backup expects a volume")
}

// closingRunner runs the script and then makes the destination file
// unclosable, which is the shape the last write of a capture takes when it
// fails: buffered bytes that only reach the disk at close, and a close that
// reports the failure -- a full filesystem, an NFS server that went away.
type closingRunner struct {
	*exec.Scripted
}

func (r closingRunner) Run(ctx context.Context, cmd exec.Command) (exec.Result, error) {
	res, err := r.Scripted.Run(ctx, cmd)
	if f, ok := cmd.Stdout.(*os.File); ok {
		_ = f.Close()
	}
	return res, err
}

// The tar on disk is the product's data in the clear until the backup engine
// encrypts it, and a capture that reported an error is one the engine will
// never encrypt or delete. So the failure that arrives last -- at close, after
// the helper exited zero -- has to clean up like every other one.
func TestACaptureThatCannotBeFinishedLeavesNothingBehind(t *testing.T) {
	scripted := exec.NewScripted()
	scripted.On("image inspect", exec.Result{})
	r := New(closingRunner{scripted})

	dest := filepath.Join(t.TempDir(), "uploads.tar")
	err := r.CaptureVolume(context.Background(), ports.RuntimeConfig{}, "demo_uploads", dest)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot finish writing")
	_, statErr := os.Stat(dest)
	assert.Error(t, statErr,
		"a plaintext copy of the volume was left behind by a capture that failed, "+
			"where nothing downstream will encrypt or remove it")
}

// helperMeasurement is what the measure script prints: the entry count and the
// bytes its paths occupy, then whatever `du` readings the helper managed.
//
// busybox `wc -lc` pads its columns, and `printf` passes the padding through --
// so the fixtures carry it, because a parser that only worked on tidy output
// would work on nothing this helper produces.
func helperMeasurement(entries, pathBytes int, du string) string {
	return fmt.Sprintf("entries    %d    %d\n%s", entries, pathBytes, du)
}

func TestAVolumeIsMeasuredInBytes(t *testing.T) {
	runner := exec.NewScripted()
	runner.On("image inspect", exec.Result{})
	// busybox `du -sk` reports KiB and the path it was given.
	runner.OnOutput("docker run", helperMeasurement(1, 5, "2048\t/src\n"))
	r := New(runner)

	size, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
	require.NoError(t, err)
	assert.Equal(t, int64(2048*1024)+tarEntryFraming+5+tarArchiveFraming, size,
		"du reports KiB; the space check compares bytes")
}

func TestOutputThatIsNotASizeIsAnErrorRatherThanZero(t *testing.T) {
	runner := exec.NewScripted()
	runner.On("image inspect", exec.Result{})
	runner.OnOutput("docker run", helperMeasurement(1, 5, "du: unrecognised option\n"))
	r := New(runner)

	// Zero would pass every space check silently, which is worse than a
	// refusal naming what could not be parsed.
	_, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a size")
}

// The defect this whole measurement exists to avoid, at the size where it bites.
//
// `du` measures contents. `tar` writes contents *plus* a 512-byte header per
// entry, a trailer, and padding -- and a volume whose files are exact multiples
// of the block size has no rounding slack left to hide that in. A million
// four-kilobyte files read as 4 GiB of contents and tar to about 4.5 GiB, so a
// size that stopped at the readings admits a backup 12% larger than the disk it
// was checked against, and the copy fails with the services already stopped.
func TestASizeCoversTheFramingTarAddsToTheContentsItMeasured(t *testing.T) {
	const (
		entries   = 1_000_000
		pathBytes = 20_000_000
		contents  = int64(4) << 30
	)

	runner := exec.NewScripted()
	runner.On("image inspect", exec.Result{})
	runner.OnOutput("docker run",
		helperMeasurement(entries, pathBytes, fmt.Sprintf("%d\t/src\n", contents/1024)))
	r := New(runner)

	size, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
	require.NoError(t, err)

	// What busybox and GNU tar both write for that volume: a header per
	// entry, the contents, the 1024-byte trailer.
	realTar := contents + 512*entries + 1024
	assert.GreaterOrEqual(t, size, realTar,
		"a volume of %d files measured %d bytes and tars to at least %d, so the "+
			"space check budgets less than the copy writes", entries, size, realTar)
}

// A helper that cannot count entries has to refuse, not answer.
//
// The number it could still produce -- the readings alone -- is exactly the
// under-estimate this exists to prevent, and it would be indistinguishable from
// a correct one. The space check treats a volume it cannot measure as one it
// need not check, so refusing costs the check; answering costs the disk.
func TestAMeasurementWithoutAnEntryCountIsRefused(t *testing.T) {
	cases := map[string]string{
		"no entries line": "2048\t/src\n2048\t/src\n",
		"truncated":       "entries\n2048\t/src\n",
		"not a number":    "entries wc: not found\n2048\t/src\n",
		"negative":        "entries -1 -1\n2048\t/src\n",
		// What a helper image without `find` actually prints, and the
		// only one of these that parses: `wc` counts the nothing that
		// a missing command wrote and reports it perfectly. The count
		// includes the mount point itself, so even an empty volume is
		// one entry -- zero is a helper that never walked it.
		"find missing": "entries 0 0\n2048\t/src\n",
	}

	for name, stdout := range cases {
		t.Run(name, func(t *testing.T) {
			runner := exec.NewScripted()
			runner.On("image inspect", exec.Result{})
			runner.OnOutput("docker run", stdout)
			r := New(runner)

			size, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
			require.Error(t, err, "a bound was reported that nothing bounded the framing of")
			assert.Zero(t, size)
		})
	}
}

// The framing is added to a reading that is already near the ceiling, so it is
// the addition that overflows. A total that wrapped would come out negative,
// and every space check reads a negative as smaller than the free space --
// which is how a refusal became a pass once already.
func TestAFramingThatCannotBeAddedSaturatesRatherThanWrapping(t *testing.T) {
	runner := exec.NewScripted()
	runner.On("image inspect", exec.Result{})
	runner.OnOutput("docker run",
		helperMeasurement(math.MaxInt64/2, math.MaxInt64/2,
			fmt.Sprintf("%d\t/src\n", int64(maxMeasurableKiB))))
	r := New(runner)

	size, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
	require.NoError(t, err)
	assert.Equal(t, int64(math.MaxInt64), size,
		"a requirement no disk can satisfy must compare as larger than the free "+
			"space, not as negative")
}

// The entry count travels in the same stream as the sizes and must never be
// read as one. largestSize takes the largest number it is shown, so an unlabelled
// count of ten million files would be budgeted as ten gigabytes of contents.
func TestTheEntryCountIsNotMistakenForASize(t *testing.T) {
	runner := exec.NewScripted()
	runner.On("image inspect", exec.Result{})
	runner.OnOutput("docker run", helperMeasurement(10_000_000, 100, "16\t/src\n"))
	r := New(runner)

	size, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
	require.NoError(t, err)
	assert.Equal(t, int64(16*1024)+10_000_000*tarEntryFraming+100+tarArchiveFraming, size,
		"the entry count was read as a KiB figure, so a volume of many small "+
			"files is refused for being thousands of times its own size")
}

// A sparse file occupies far fewer blocks than it tars to. Budgeting the block
// count is how a space check passes and the copy then fills the disk -- after
// the services have already been stopped for it.
func TestAVolumeIsMeasuredByWhicheverReadingIsLarger(t *testing.T) {
	// Apparent size is printed first and allocated blocks second, and which
	// of them is the larger depends on what the volume holds.
	cases := map[string]struct {
		stdout string
		want   int64
	}{
		// A 40 MiB sparse image occupying a megabyte of blocks: the
		// tar carries the holes as zeroes, so the blocks are a lie.
		"a sparse file": {helperMeasurement(2, 12, "40960\t/src\n1024\t/src\n"), 40960 * 1024},
		// Thousands of tiny files, each rounded up to a filesystem
		// block, plus a tar header apiece.
		"a directory of tiny files": {helperMeasurement(2, 12, "128\t/src\n4096\t/src\n"), 4096 * 1024},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			runner := exec.NewScripted()
			runner.On("image inspect", exec.Result{})
			runner.OnOutput("docker run", tc.stdout)
			r := New(runner)

			size, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
			require.NoError(t, err)
			assert.Equal(t, tc.want+2*tarEntryFraming+12+tarArchiveFraming, size,
				"the smaller reading was budgeted, so a volume is allowed "+
					"onto a disk it does not fit on and the copy fails "+
					"with the services already stopped")

			argv := helperArgv(t, runner)
			assert.Contains(t, argv, "--apparent-size",
				"nothing asks for the size tar will write, so a sparse volume measures small")
			assert.Contains(t, argv, "find /src | wc -lc",
				"nothing counts the entries, so the framing tar adds to them is "+
					"unbounded and a volume of block-aligned files measures short")
			assert.Contains(t, argv, "du -skb",
				"only the GNU spelling of apparent size is tried, so a busybox "+
					"helper -- which is the default one -- measures blocks "+
					"and a sparse volume still reads small")
			assert.Regexp(t, `;\s*du -sk /src`, argv,
				"the plain measurement is not last, so the shell's exit status "+
					"comes from an optional one and a helper lacking it "+
					"reports a failure instead of the size it can measure")
		})
	}
}

// The order of the two apparent-size spellings is load-bearing, and getting it
// backwards is silent.
//
// GNU spells it `--apparent-size`; busybox rejects that and spells it `-b`. But
// GNU accepts `-b` too, where it *also* means `--block-size=1` -- so `du -skb`
// reports KiB on busybox and bytes on GNU. Trying `-b` first would therefore
// read 1024x high on a GNU helper and refuse every backup, with a figure that
// looks plausible. GNU has to win on the first form so it never reaches the
// second. Verified against both implementations.
func TestTheGNUSpellingOfApparentSizeIsTriedFirst(t *testing.T) {
	runner := exec.NewScripted()
	runner.On("image inspect", exec.Result{})
	runner.OnOutput("docker run", helperMeasurement(1, 5, "1\t/src\n1\t/src\n"))
	r := New(runner)

	_, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
	require.NoError(t, err)

	argv := helperArgv(t, runner)
	gnu := strings.Index(argv, "--apparent-size")
	busybox := strings.Index(argv, "du -skb")
	require.NotEqual(t, -1, gnu)
	require.NotEqual(t, -1, busybox)
	assert.Less(t, gnu, busybox,
		"`-b` is tried before `--apparent-size`, so a GNU helper takes it and "+
			"reports bytes where KiB are expected -- a thousandfold "+
			"over-estimate that refuses every backup")
}

// The port promises bytes and the reading is KiB, so the conversion multiplies
// by 1024. A figure that wraps comes out negative, and a negative size reads as
// *smaller* than the free space -- turning the refusal this exists for into a
// pass.
func TestASizeThatCannotBecomeAByteCountIsRefused(t *testing.T) {
	cases := map[string]struct{ stdout, message string }{
		"negative":  {"-4\t/src\n", "negative"},
		"too large": {"9223372036854775807\t/src\n", "more than a byte count can hold"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			runner := exec.NewScripted()
			runner.On("image inspect", exec.Result{})
			runner.OnOutput("docker run", tc.stdout)
			r := New(runner)

			size, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
			require.Error(t, err, "a size no byte count can hold was passed to the space check")
			assert.Contains(t, err.Error(), tc.message)
			assert.Zero(t, size)
		})
	}
}

// The space check refuses on a measurement it cannot read and steps past one
// that never ran, and it can only tell them apart if this adapter marks them
// apart.
//
// The mark is on the permissive side deliberately. A helper that ran and printed
// something unusable will print the same thing tomorrow -- it is the image or
// the override, and it is a refusal the operator can fix -- whereas a `docker
// run` that did not complete has said nothing about the volume at all, and
// refusing every backup of a deployment over it is its own failure.
func TestAMeasurementThatNeverRanIsMarkedApartFromOneThatRanAndCouldNotBeRead(t *testing.T) {
	t.Run("the measurement never ran", func(t *testing.T) {
		runner := exec.NewScripted()
		runner.On("image inspect", exec.Result{})
		runner.OnExit("docker run", 1, "du: /src: Operation not permitted")
		r := New(runner)

		_, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrMeasureIncomplete,
			"a helper that exited non-zero on one awkward volume refuses every "+
				"backup of the deployment, including the ones that fit")
	})

	// Every reading that came back and could not be used. Each is a fixed
	// property of the helper image, so marking any of them would put the
	// space check back where it was: disabled by exactly the failures that
	// leave the *capture* working.
	unreadable := map[string]string{
		"no entry count":        "2048\t/src\n2048\t/src\n",
		"a helper without find": "entries 0 0\n2048\t/src\n",
		"du printed no size":    helperMeasurement(1, 5, "du: unrecognised option\n"),
		"du printed nothing":    helperMeasurement(1, 5, ""),
		"a figure too large to express": helperMeasurement(1, 5,
			"9223372036854775807\t/src\n"),
	}
	for name, stdout := range unreadable {
		t.Run(name, func(t *testing.T) {
			runner := exec.NewScripted()
			runner.On("image inspect", exec.Result{})
			runner.OnOutput("docker run", stdout)
			r := New(runner)

			_, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
			require.Error(t, err)
			assert.NotErrorIs(t, err, domain.ErrMeasureIncomplete,
				"the space check will step past this and admit the backup with "+
					"the volume unbudgeted, which is what it did before")
			assert.NotEmpty(t, domain.AsError(err).Hint,
				"this refuses a backup, so it has to say what to do about it")
		})
	}
}

// A cancelled backup is not a volume that cannot be measured.
//
// Marking it would have the space check stepping past the cancellation and
// answering on behalf of an operation that is already over -- and the mark
// would travel out to `morzer backup` as a runtime failure rather than an
// interruption, which is a different exit code.
func TestACancelledMeasurementIsAnInterruptionRatherThanAnUnmeasuredVolume(t *testing.T) {
	runner := exec.NewScripted()
	runner.On("image inspect", exec.Result{})
	runner.OnError("docker run", domain.Interrupted("docker run was cancelled"))
	r := New(runner)

	_, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrMeasureIncomplete)
	assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(err))
}

// `compose stop`/`start`, not `down`/`up`: down removes containers and networks,
// and up reconciles against the declared configuration, so resuming a stack
// after a backup could recreate a container whose definition had drifted.
func TestQuiescingUsesStopAndStartRatherThanDownAndUp(t *testing.T) {
	runner := exec.NewScripted()
	r := New(runner)
	cfg := ports.RuntimeConfig{Product: "demo"}

	require.NoError(t, r.Stop(context.Background(), cfg, []string{"app", "worker"}, 30*1e9))
	require.NoError(t, r.Start(context.Background(), cfg, []string{"app", "worker"}))

	lines := runner.CommandLines()
	assert.Contains(t, lines, "compose --project-name demo stop --timeout 30 app worker")
	assert.Contains(t, lines, "compose --project-name demo start app worker")
	assert.NotContains(t, lines, " down ", "a backup tore the project down")
	assert.NotContains(t, lines, " up ", "a backup reconverged the project")
}
