package compose

import (
	"context"
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

	require.NoError(t, r.CaptureVolume(context.Background(), ports.RuntimeConfig{Project: "demo"},
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

	require.NoError(t, r.RestoreVolume(context.Background(), ports.RuntimeConfig{Project: "demo"},
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

func TestAVolumeIsMeasuredInBytes(t *testing.T) {
	runner := exec.NewScripted()
	runner.On("image inspect", exec.Result{})
	// busybox `du -sk` reports KiB and the path it was given.
	runner.OnOutput("docker run", "2048\t/src\n")
	r := New(runner)

	size, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
	require.NoError(t, err)
	assert.Equal(t, int64(2048*1024), size, "du reports KiB; the space check compares bytes")
}

func TestOutputThatIsNotASizeIsAnErrorRatherThanZero(t *testing.T) {
	runner := exec.NewScripted()
	runner.On("image inspect", exec.Result{})
	runner.OnOutput("docker run", "du: unrecognised option\n")
	r := New(runner)

	// Zero would pass every space check silently, which is worse than a
	// refusal naming what could not be parsed.
	_, err := r.VolumeSize(context.Background(), ports.RuntimeConfig{}, "demo_uploads")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a size")
}

// `compose stop`/`start`, not `down`/`up`: down removes containers and networks,
// and up reconciles against the declared configuration, so resuming a stack
// after a backup could recreate a container whose definition had drifted.
func TestQuiescingUsesStopAndStartRatherThanDownAndUp(t *testing.T) {
	runner := exec.NewScripted()
	r := New(runner)
	cfg := ports.RuntimeConfig{Project: "demo"}

	require.NoError(t, r.Stop(context.Background(), cfg, []string{"app", "worker"}, 30*1e9))
	require.NoError(t, r.Start(context.Background(), cfg, []string{"app", "worker"}))

	lines := runner.CommandLines()
	assert.Contains(t, lines, "compose --project-name demo stop --timeout 30 app worker")
	assert.Contains(t, lines, "compose --project-name demo start app worker")
	assert.NotContains(t, lines, " down ", "a backup tore the project down")
	assert.NotContains(t, lines, " up ", "a backup reconverged the project")
}
