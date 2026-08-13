package lock

// pidStart reports that this platform cannot answer.
//
// **RFC 0029 decision 12 was left OPEN** — fail-safe stub now, or the real
// `unix.SysctlKinfoProc` immediately, with the RFC asking whoever executes to
// take the real call if they find it trivial and to log that they did. This is
// the log, and it took the stub. The call is available and would be about five
// lines; the reason is not effort.
//
// **The two options fail in opposite directions, and only one of them is
// testable here.** Decision 9 declines a macOS lane, so anything written for
// darwin ships unexecuted. Weigh that against what each does when it is wrong:
//
//   - This stub returns zero, the caller reads zero as "unknown" and falls back
//     to the PID alone, and an unmatched PID is treated as a **live holder**. A
//     wrong answer here refuses to take a lock that is actually free: an
//     operator waits, and runs `morzer` again.
//   - A `SysctlKinfoProc` implementation that is subtly wrong — the wrong field,
//     the wrong units, a `Timeval` compared against Linux's clock ticks — yields
//     a start time that never matches what was recorded. Every lock then looks
//     stale, and the next operation **steals** it. That is a concurrent
//     deployment against one installation, which is the single thing this guard
//     exists to prevent.
//
// An untested guard that fails unsafe is worse than an acknowledged absence
// that fails safe, and macOS is a development host (decision 1) where two
// concurrent deployments are a likelier accident than a stale lock is a
// nuisance. P2 takes the real call, with something on a Mac to run it.
//
// Nothing else changes: `startTimeContradicts` needs *both* sides known, so a
// zero from here is silence rather than a mismatch — the same path a record
// written before this field existed already takes.
func pidStart(int) uint64 { return 0 }
