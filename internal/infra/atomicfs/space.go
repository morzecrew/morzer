package atomicfs

import (
	"fmt"
	"strings"
	"syscall"
)

// FreeSpace returns the bytes available to an unprivileged process on the
// filesystem holding a path.
//
// Bavail rather than Bfree: the difference is the reserved blocks only root
// may use, and reporting those as available would let a non-root operation
// pass its space check and then fail on ENOSPC -- which for a backup means
// failing partway through writing the thing that was supposed to be the safety
// net.
//
// It lives here rather than with the preflight checks that were its first
// caller because an adapter needs it too: a volume capture measures before it
// copies, and an adapter cannot import the lifecycle layer.
func FreeSpace(path string) (int64, error) {
	var stat syscall.Statfs_t
	target := path
	for {
		if err := syscall.Statfs(target, &stat); err == nil {
			break
		}
		// The path may not exist yet on a fresh install; walk up to the
		// nearest existing ancestor, which is on the same filesystem
		// the directory will be created on.
		parent := parentDir(target)
		if parent == target {
			return 0, fmt.Errorf("cannot stat any ancestor of %s", path)
		}
		target = parent
	}
	//nolint:gosec // Bavail and Bsize are non-negative in practice.
	return int64(stat.Bavail) * stat.Bsize, nil
}

func parentDir(p string) string {
	if i := strings.LastIndexByte(strings.TrimSuffix(p, "/"), '/'); i > 0 {
		return p[:i]
	}
	return "/"
}
