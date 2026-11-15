package dockerdesktop

import (
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// The user who runs Docker Desktop is the owner of the socket the link names,
// and a link that names nothing, a file, or a socket root owns is refused
// (decision 19).
func TestTheEngineUserIsTheSocketsOwner(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "docker.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	link := filepath.Join(dir, "default.sock")
	require.NoError(t, os.Symlink(sock, link))

	// The socket's owner, through the link. Run as root, the socket is
	// root's and refused below, so the owner is stood in for.
	if os.Getuid() != 0 {
		uid, gid, err := socketOwner(link, os.Stat)
		require.NoError(t, err)
		assert.Equal(t, [2]int{os.Getuid(), os.Getgid()}, [2]int{uid, gid})
	}
	userOwned := func(string) (fs.FileInfo, error) { return socketOf{uid: 501, gid: 20}, nil }
	uid, gid, err := socketOwner(link, userOwned)
	require.NoError(t, err)
	assert.Equal(t, [2]int{501, 20}, [2]int{uid, gid})

	_, _, err = socketOwner(filepath.Join(dir, "missing"), os.Stat)
	require.ErrorIs(t, err, domain.ErrRuntime)
	assert.Contains(t, domain.AsError(err).Hint, "default Docker socket")

	file := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	_, _, err = socketOwner(file, os.Stat)
	require.ErrorContains(t, err, "names no socket")

	rootOwned := func(string) (fs.FileInfo, error) { return socketOf{}, nil }
	_, _, err = socketOwner(link, rootOwned)
	require.ErrorContains(t, err, "names a socket root owns")
}

// socketOf is a socket uid:gid owns.
type socketOf struct{ uid, gid uint32 }

func (socketOf) Name() string       { return "docker.sock" }
func (socketOf) Size() int64        { return 0 }
func (socketOf) Mode() fs.FileMode  { return fs.ModeSocket | 0o660 }
func (socketOf) ModTime() time.Time { return time.Time{} }
func (socketOf) IsDir() bool        { return false }
func (s socketOf) Sys() any         { return &syscall.Stat_t{Uid: s.uid, Gid: s.gid} }
