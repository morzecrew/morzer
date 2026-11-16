package lock

import "golang.org/x/sys/unix"

// pidStart reads the kernel's start time for a PID, in microseconds since the
// epoch: `p_starttime`, which the kernel sets as the process forks and never
// changes.
//
// Not converted to ticks since boot, the unit Linux reports. That takes
// `kern.boottime`, which the kernel moves when the clock is stepped, and a
// start converted after a step would never match the one recorded before it.
// Every lock would then look stale and the next operation would steal it: two
// deployments against one installation, which is the single thing this guard
// exists to prevent. A record is read only on the host that wrote it, so the
// two platforms' units never meet (logs/macos-host.md).
//
// Zero when it cannot be read -- a PID that has already gone. Callers treat
// zero as "unknown" and fall back.
func pidStart(pid int) uint64 {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0
	}
	start := kp.Proc.P_starttime
	//nolint:gosec // a process's start is after the epoch, so both are non-negative.
	return uint64(start.Sec)*1e6 + uint64(start.Usec)
}
