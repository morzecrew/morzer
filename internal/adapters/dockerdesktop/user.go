// Package dockerdesktop is what a manager on a Mac needs from Docker Desktop
// (RFC 0055): who runs it, since its file sharing reads the Mac's files as that
// user whatever uid a container runs as.
package dockerdesktop

import (
	"errors"
	"io/fs"
	"os"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

// DefaultSocket is the link Docker Desktop's "default Docker socket" setting
// keeps, root's, naming the socket in the home of the user who runs it.
const DefaultSocket = "/var/run/docker.sock"

// User is who runs the engine DefaultSocket names: the owner of the socket
// the link names (decision 19). Read at every call, so a different user
// starting the engine later is followed.
func User() (uid, gid int, err error) {
	return socketOwner(DefaultSocket, os.Stat)
}

// socketOwner is User for any link; stat is the filesystem, so a test can
// stand one in.
func socketOwner(link string, stat func(string) (fs.FileInfo, error)) (uid, gid int, err error) {
	info, err := stat(link)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, 0, domain.RuntimeError(err,
			"no engine answers at %s, so who runs Docker Desktop is not known", link).
			WithHint("start Docker Desktop, and allow its default Docker socket in its advanced settings")
	}
	if err != nil {
		return 0, 0, domain.RuntimeError(err, "cannot read %s", link)
	}
	if info.Mode()&fs.ModeSocket == 0 {
		return 0, 0, domain.RuntimeError(nil, "%s names no socket", link).
			WithHint("Docker Desktop keeps it as a link to the socket in the home of the user who runs it")
	}
	uid, gid = atomicfs.Owner(info)
	if uid <= 0 {
		return 0, 0, domain.RuntimeError(nil,
			"%s names a socket root owns, and what a container reads through an engine run as root is not measured", link).
			WithHint("run Docker Desktop as the user who uses this Mac")
	}
	return uid, gid, nil
}
