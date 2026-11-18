package compose

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
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
// tested. It reads a --mount as docker does, as one CSV record, and quotes
// each source for the shell, as a container's own path needs none. A path
// stands for an image's PATH.
type shellContainer struct {
	fakes.Scripted
	path string
}

func (s *shellContainer) Run(ctx context.Context, cmd exec.Command) (exec.Result, error) {
	var pairs []string
	for i, arg := range cmd.Argv {
		if arg != "--mount" {
			continue
		}
		record, err := csv.NewReader(strings.NewReader(cmd.Argv[i+1])).Read()
		if err != nil {
			return exec.Result{}, err
		}
		fields := map[string]string{}
		for _, kv := range record {
			k, v, _ := strings.Cut(kv, "=")
			fields[k] = v
		}
		pairs = append(pairs, fields["target"], "'"+strings.ReplaceAll(fields["source"], "'", `'\''`)+"'")
	}
	script := strings.NewReplacer(pairs...).Replace(cmd.Argv[len(cmd.Argv)-1])
	sh := osexec.CommandContext(ctx, "/bin/sh", "-c", script)
	if s.path != "" {
		sh.Env = []string{"PATH=" + s.path}
	}
	out, err := sh.Output()
	return exec.Result{Stdout: string(out)}, err
}

func TestTheProbeListsWhatItReadsAndWritesWhatItWrites(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root lists and writes a directory whatever its mode")
	}
	dir := t.TempDir()
	// A comma in a name, which docker reads a --mount's fields apart by, and
	// a space, which a shell splits words at.
	etc, sealed, data, locked := filepath.Join(dir, "etc,local"), filepath.Join(dir, "sealed"),
		filepath.Join(dir, "data dir"), filepath.Join(dir, "locked")
	for _, d := range []string{etc, sealed, data, locked} {
		require.NoError(t, os.Mkdir(d, 0o755))
	}
	require.NoError(t, os.Chmod(sealed, 0o311))
	require.NoError(t, os.Chmod(locked, 0o555))
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755); _ = os.Chmod(locked, 0o755) })
	// Whatever the data directory already holds, a name the probe could pick
	// among it.
	kept := filepath.Join(data, fmt.Sprintf(".file-sharing-probe-%d", os.Getpid()))
	require.NoError(t, os.WriteFile(kept, []byte("a service's"), 0o600))
	r := New(&shellContainer{})

	require.NoError(t, r.ProbeFileSharing(context.Background(), []string{etc}, []string{data}))
	entries, err := os.ReadDir(data)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the probe's file was left in the data directory")
	assert.FileExists(t, kept, "the probe removed a file it did not create")

	err = r.ProbeFileSharing(context.Background(), []string{etc, sealed}, []string{data, locked})
	require.ErrorIs(t, err, domain.ErrPreflight)
	assert.Equal(t, "a container cannot list "+sealed+", or write "+locked, domain.AsError(err).Message)
}

// busybox's ls stats each entry it lists, and through Docker Desktop's file
// sharing an entry the engine's user cannot open does not exist, as a Mac
// measured (logs/macos-host.md, 2026-11-18T21:21:47Z). So ls fails in each directory
// that holds one of root's, which a container lists all the same. The ls
// on the PATH here does what that one did; GNU's stats nothing it lists.
func TestTheProbeListsADirectoryHoldingAnEntryAContainerCannotOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens an entry whatever its mode")
	}
	bin := t.TempDir()
	for _, tool := range []string{"mktemp", "rm"} {
		found, err := osexec.LookPath(tool)
		require.NoError(t, err)
		require.NoError(t, os.Symlink(found, filepath.Join(bin, tool)))
	}
	ls, err := osexec.LookPath("ls")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ls"), []byte(`#!/bin/sh
for dir; do :; done
names=$('`+ls+`' -a "$dir") || exit $?
status=0
for name in $names; do
	if [ -r "$dir/$name" ]; then
		echo "$name"
	else
		echo "ls: $dir/$name: No such file or directory" >&2
		status=1
	fi
done
exit $status
`), 0o755)) //nolint:gosec // a tool the probe runs
	dir := t.TempDir()
	etc, sealed := filepath.Join(dir, "etc"), filepath.Join(dir, "sealed")
	for _, d := range []string{etc, sealed} {
		require.NoError(t, os.Mkdir(d, 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(etc, "secrets.sops.yaml"), nil, 0o000))
	require.NoError(t, os.Chmod(sealed, 0o311))
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })
	r := New(&shellContainer{path: bin})

	require.NoError(t, r.ProbeFileSharing(context.Background(), []string{etc}, nil))

	err = r.ProbeFileSharing(context.Background(), []string{etc, sealed}, nil)
	require.ErrorIs(t, err, domain.ErrPreflight)
	assert.Equal(t, "a container cannot list "+sealed, domain.AsError(err).Message)
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
		"outside it, with a space in its name": {
			stderr: "docker: Error response from daemon: mounts denied: \nThe path /Volumes/Work Disk/demo " +
				"is not shared from the host and is not known to Docker.",
			message: "the engine does not share /Volumes/Work Disk/demo with containers",
			hint:    "add /Volumes/Work Disk/demo to the engine's file sharing",
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

// An image with a shell and no mktemp, which is not POSIX's, is a probe that
// did not run rather than a directory a container cannot write.
func TestAHelperWithoutTheProbesToolsIsNotARefusal(t *testing.T) {
	bin := t.TempDir()
	for _, tool := range []string{"ls", "rm"} {
		found, err := osexec.LookPath(tool)
		require.NoError(t, err)
		require.NoError(t, os.Symlink(found, filepath.Join(bin, tool)))
	}
	r := New(&shellContainer{path: bin})

	err := r.ProbeFileSharing(context.Background(), []string{t.TempDir()}, []string{t.TempDir()})

	require.Error(t, err)
	assert.False(t, errors.Is(err, domain.ErrPreflight), "a probe that did not run refused: %v", err)
	assert.Contains(t, domain.AsError(err).Message, "has no mktemp")
	assert.Contains(t, domain.AsError(err).Hint, "`ls`, `mktemp` and `rm`")
}

// An override that names no digest runs nothing with the data directory bound.
func TestAnUnpinnedHelperProbesNothing(t *testing.T) {
	scripted := fakes.NewScripted()
	r := New(scripted, WithHelperImage("busybox:latest"))

	err := r.ProbeFileSharing(context.Background(), nil, []string{"/var/lib/demo/data"})

	require.ErrorIs(t, err, domain.ErrValidation)
	assert.False(t, scripted.Ran("docker run"))
}
