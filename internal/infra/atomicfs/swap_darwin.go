package atomicfs

import "golang.org/x/sys/unix"

// SwapDirs exchanges two paths in one step, so neither is missing at any
// moment, a crash included.
func SwapDirs(a, b string) error {
	return unix.RenamexNp(a, b, unix.RENAME_SWAP)
}
