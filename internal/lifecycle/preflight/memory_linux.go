package preflight

import "syscall"

// totalMemory is the machine's installed RAM in bytes.
//
// `sysinfo(2)`, which is Linux's own answer and needs no subprocess. The
// darwin counterpart is in memory_darwin.go; the split is per-OS files rather
// than a runtime branch so that the Linux path is unchanged by construction
// rather than by a test somebody has to remember to write (RFC 0029 decision
// 8).
func totalMemory() (int64, error) {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0, err
	}
	//nolint:gosec // Totalram and Unit are non-negative.
	return int64(info.Totalram) * int64(info.Unit), nil
}
