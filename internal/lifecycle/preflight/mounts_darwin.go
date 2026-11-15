package preflight

import (
	"errors"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// FilesystemType is mounts_linux.go's counterpart. A Mac has no /proc/mounts,
// and statfs(2) names the filesystem itself in f_fstypename, so no mount table
// is read (RFC 0055 §5.4). A path not created yet answers for the nearest
// directory above it that is, as the Linux reading answers for the mount it
// would be under.
func FilesystemType(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	for {
		var st unix.Statfs_t
		err := unix.Statfs(abs, &st)
		if err == nil {
			return unix.ByteSliceToString(st.Fstypename[:])
		}
		// Only a path that does not exist answers for its parent; any
		// other failure is "cannot tell", as /proc missing is on Linux.
		if !errors.Is(err, unix.ENOENT) {
			return ""
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return ""
		}
		abs = parent
	}
}
