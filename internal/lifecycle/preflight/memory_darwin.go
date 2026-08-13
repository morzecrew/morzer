package preflight

import "golang.org/x/sys/unix"

// totalMemory is memory_linux.go's counterpart.
//
// `hw.memsize` through `sysctl(3)` rather than `sysinfo(2)`, which darwin does
// not have. `x/sys/unix` exposes it as a typed call, so this needs no
// subprocess and no parsing -- which matters because the check that reads it
// runs before every operation, and a preflight that shells out is a preflight
// that can hang.
//
// The value is bytes already, where Linux returns a count and a unit to
// multiply. That asymmetry is the whole reason these are two functions rather
// than one with a branch in it.
func totalMemory() (int64, error) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, err
	}
	//nolint:gosec // hw.memsize is a physical quantity and fits in an int64.
	return int64(total), nil
}
