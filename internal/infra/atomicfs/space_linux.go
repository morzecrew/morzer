package atomicfs

import "syscall"

// availableBytes multiplies the two fields of a statfs whose widths differ by
// platform.
//
// One line per OS rather than one portable expression, and the reason is the
// linter rather than the compiler. `int64(stat.Bavail) * int64(stat.Bsize)`
// compiles everywhere, but `Bsize` is already `int64` on Linux, so `unconvert`
// -- which this repository enables -- rejects the conversion that darwin needs.
// A build that is portable and unlintable is not portable.
//
// RFC 0029 §5.1 offered exactly these two shapes and left the choice to
// whichever the code demanded; decision 8 wants platform differences in per-OS
// files anyway, so the answer costs nothing it was not already asking for.
//
//nolint:gosec // Bavail and Bsize are non-negative in practice.
func availableBytes(stat *syscall.Statfs_t) int64 {
	return int64(stat.Bavail) * stat.Bsize
}
