package atomicfs

import "golang.org/x/sys/unix"

// SwapDirs exchanges two paths in one step, so neither is missing at any
// moment, a crash included.
func SwapDirs(a, b string) error {
	return unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
}
