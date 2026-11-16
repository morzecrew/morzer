package compose

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/test/fakes"
)

// shellContainer runs the probe's script with /bin/sh, each bind's target
// replaced by its source, so the script that reaches a container is the one
// tested.
type shellContainer struct{ fakes.Scripted }

func (s *shellContainer) Run(ctx context.Context, cmd exec.Command) (exec.Result, error) {
	var pairs []string
	for i, arg := range cmd.Argv {
		if arg != "--mount" {
			continue
		}
		fields := map[string]string{}
		for _, kv := range strings.Split(cmd.Argv[i+1], ",") {
			k, v, _ := strings.Cut(kv, "=")
			fields[k] = v
		}
		pairs = append(pairs, fields["target"], fields["source"])
	}
	script := strings.NewReplacer(pairs...).Replace(cmd.Argv[len(cmd.Argv)-1])
	out, err := osexec.CommandContext(ctx, "/bin/sh", "-c", script).Output()
	return exec.Result{Stdout: string(out)}, err
}

func TestTheProbeListsWhatItReadsAndWritesWhatItWrites(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root lists and writes a directory whatever its mode")
	}
	dir := t.TempDir()
	etc, sealed, data, locked := filepath.Join(dir, "etc"), filepath.Join(dir, "sealed"),
		filepath.Join(dir, "data"), filepath.Join(dir, "locked")
	for _, d := range []string{etc, sealed, data, locked} {
		require.NoError(t, os.Mkdir(d, 0o755))
	}
	require.NoError(t, os.Chmod(sealed, 0o311))
	require.NoError(t, os.Chmod(locked, 0o555))
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755); _ = os.Chmod(locked, 0o755) })
	r := New(&shellContainer{})

	require.NoError(t, r.ProbeFileSharing(context.Background(), []string{etc}, []string{data}))
	entries, err := os.ReadDir(data)
	require.NoError(t, err)
	assert.Empty(t, entries, "the probe's file was left in the data directory")

	err = r.ProbeFileSharing(context.Background(), []string{etc, sealed}, []string{data, locked})
	require.ErrorIs(t, err, domain.ErrPreflight)
	assert.Equal(t, "a container cannot list "+sealed+", or write "+locked, domain.AsError(err).Message)
}

func TestTheProbeBindsWhatItWritesAloneWritable(t *testing.T) {
	scripted := fakes.NewScripted()
	scripted.OnOutput("docker run", "probed\n")
	r := New(scripted)

	require.NoError(t, r.ProbeFileSharing(context.Background(), []string{"/etc/demo"}, []string{"/var/lib/demo/data"}))

	line := scripted.CommandLines()
	assert.Contains(t, line, "--network none")
	assert.Contains(t, line, "--mount type=bind,source=/etc/demo,target=/probe/read/0,readonly ")
	assert.Contains(t, line, "--mount type=bind,source=/var/lib/demo/data,target=/probe/write/0 ")
	assert.Contains(t, line, DefaultHelperImage+" sh -c")
}

func TestTheProbeSaysWhichDirectoryAContainerCannotReach(t *testing.T) {
	for name, tc := range map[string]struct {
		stderr, message, hint string
	}{
		"outside the engine's file sharing": {
			stderr: "docker: Error response from daemon: mounts denied: \nThe path /opt/demo is not shared " +
				"from the host and is not known to Docker.\nYou can configure shared paths from Docker -> " +
				"Preferences... -> Resources -> File Sharing.",
			message: "the engine does not share /opt/demo with containers",
			hint:    "add /opt/demo to the engine's file sharing",
		},
		"under a directory the engine's user cannot list": {
			stderr: `docker: Error response from daemon: invalid mount config for type "bind": ` +
				`stat /host_mnt/private/var/lib/demo: permission denied`,
			message: "a container cannot reach /private/var/lib/demo: the user who runs the engine cannot " +
				"list it, or a directory above it",
			hint: "every directory from / down to /private/var/lib/demo",
		},
	} {
		t.Run(name, func(t *testing.T) {
			scripted := fakes.NewScripted()
			scripted.OnExit("docker run", 125, tc.stderr)

			err := New(scripted).ProbeFileSharing(context.Background(), []string{"/opt/demo"}, nil)

			require.ErrorIs(t, err, domain.ErrPreflight)
			assert.Equal(t, tc.message, domain.AsError(err).Message)
			assert.Contains(t, domain.AsError(err).Hint, tc.hint)
		})
	}
}

// Only what a container could not reach refuses; a probe that did not run is
// told apart from one that found nothing.
func TestAProbeThatDidNotRunIsNotARefusal(t *testing.T) {
	for name, tc := range map[string]struct {
		arrange func(*fakes.Scripted)
		says    string
	}{
		"the helper image cannot be pulled": {
			arrange: func(s *fakes.Scripted) {
				s.OnExit("docker run", 125, "Unable to find image 'busybox@sha256:9db7' locally\n"+
					"docker: Error response from daemon: Get \"https://registry-1.docker.io/v2/\": dial tcp: "+
					"lookup registry-1.docker.io: no such host\nSee 'docker run --help'.")
			},
			says: "busybox: docker: Error response from daemon: Get",
		},
		"the helper printed nothing": {
			arrange: func(s *fakes.Scripted) { s.OnOutput("docker run", "") },
			says:    "printed no result",
		},
	} {
		t.Run(name, func(t *testing.T) {
			scripted := fakes.NewScripted()
			tc.arrange(scripted)

			err := New(scripted).ProbeFileSharing(context.Background(), []string{"/etc/demo"}, nil)

			require.Error(t, err)
			assert.False(t, errors.Is(err, domain.ErrPreflight), "a probe that did not run refused: %v", err)
			assert.Contains(t, domain.AsError(err).Message, tc.says)
		})
	}
}

// An override that names no digest runs nothing with the data directory bound.
func TestAnUnpinnedHelperProbesNothing(t *testing.T) {
	scripted := fakes.NewScripted()
	r := New(scripted, WithHelperImage("busybox:latest"))

	err := r.ProbeFileSharing(context.Background(), nil, []string{"/var/lib/demo/data"})

	require.ErrorIs(t, err, domain.ErrValidation)
	assert.False(t, scripted.Ran("docker run"))
}
