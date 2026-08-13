package atomicfs

import "syscall"

// availableBytes is space_linux.go's counterpart: on darwin `Statfs_t.Bsize` is
// `uint32`, so the product needs both operands widened. See that file for why
// this is two files rather than one expression.
//
//nolint:gosec // Bavail and Bsize are non-negative in practice.
func availableBytes(stat *syscall.Statfs_t) int64 {
	return int64(stat.Bavail) * int64(stat.Bsize)
}
