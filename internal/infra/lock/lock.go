// Package lock implements the deployment lock.
//
// Two concurrent mutating operations on one installation is the race the whole
// design assumes away, so acquiring this is the first step of every mutating
// command. The lock is advisory flock(2) plus an owner record, because
// "resource busy" is a useless thing to tell an operator: they need to know
// which operation holds it and since when.
package lock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// defaultPollInterval is how often a blocking acquisition retries. Operations
// run for minutes, so a half-second poll costs nothing and keeps cancellation
// responsive.
const defaultPollInterval = 500 * time.Millisecond

// Locker is the flock-backed implementation of ports.Locker.
type Locker struct {
	dir string

	// pollInterval is the gap between attempts while waiting. A test that
	// has to observe a waiter actually waiting would otherwise pay the
	// production interval in real seconds on every run.
	pollInterval time.Duration
}

// New returns a Locker storing lock files in dir.
func New(dir string) *Locker {
	return &Locker{dir: dir, pollInterval: defaultPollInterval}
}

// WithPollInterval overrides the wait poll interval. Tests use it to avoid
// real waits, as health.Waiter.WithInterval does.
func (l *Locker) WithPollInterval(d time.Duration) *Locker {
	l.pollInterval = d
	return l
}

var _ ports.Locker = (*Locker)(nil)

func (l *Locker) path(name string) string {
	return filepath.Join(l.dir, name+".lock")
}

// ownerPath is a sidecar rather than the lock file's own contents.
//
// Writing owner metadata into the flocked file itself would mean truncating a
// file another process may be reading, and would make the "who holds this"
// query require taking the very lock it is asking about.
func (l *Locker) ownerPath(name string) string {
	return filepath.Join(l.dir, name+".owner.json")
}

// Acquire takes the named lock, returning a release function that must be
// deferred by the caller.
func (l *Locker) Acquire(ctx context.Context, name string, opts ports.LockOptions) (func() error, error) {
	if err := atomicfs.MkdirAll(l.dir, 0o750); err != nil {
		return nil, err
	}

	fl := flock.New(l.path(name))

	locked, err := l.tryAcquire(ctx, fl, opts)
	if err != nil {
		return nil, err
	}
	if !locked {
		owner, found, _ := l.Owner(ctx, name)
		return nil, lockedError(name, owner, found)
	}

	// The owner record is best-effort: failing to write it must not fail an
	// operation that legitimately holds the lock. A missing record degrades
	// diagnostics, nothing more.
	l.writeOwner(name, opts.Owner)

	released := false
	return func() error {
		if released {
			return nil
		}
		released = true
		_ = os.Remove(l.ownerPath(name))
		if err := fl.Unlock(); err != nil {
			return domain.Internal(err, "cannot release the %s lock", name)
		}
		return nil
	}, nil
}

func (l *Locker) tryAcquire(ctx context.Context, fl *flock.Flock, opts ports.LockOptions) (bool, error) {
	if !opts.Wait {
		locked, err := fl.TryLock()
		if err != nil {
			return false, domain.Internal(err, "cannot acquire the deployment lock")
		}
		return locked, nil
	}

	// TryLockContext polls rather than blocking in the kernel, which is
	// what makes ctrl-c work while waiting for a lock.
	interval := l.pollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	locked, err := fl.TryLockContext(ctx, interval)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, domain.Interrupted("gave up waiting for the deployment lock")
		}
		return false, domain.Internal(err, "cannot acquire the deployment lock")
	}
	return locked, nil
}

func lockedError(name string, owner ports.LockOwner, found bool) error {
	if !found {
		return domain.Locked("the %s lock is held by another process", name).
			WithHint("wait for the running operation to finish, or re-run with --wait")
	}
	held := ""
	if !owner.StartedAt.IsZero() {
		held = " (running for " + time.Since(owner.StartedAt.Time).Round(time.Second).String() + ")"
	}
	return domain.Locked("the %s lock is held by %s operation %s, pid %d%s",
		name, owner.Type, owner.OperationID, owner.PID, held).
		WithHint("wait for it to finish, or re-run with --wait. " +
			"If that process is gone, the lock is released automatically when it exits.")
}

func (l *Locker) writeOwner(name string, owner ports.LockOwner) {
	if owner.PID == 0 {
		owner.PID = os.Getpid()
	}
	if owner.PIDStart == 0 {
		owner.PIDStart = pidStart(owner.PID)
	}
	if owner.StartedAt.IsZero() {
		owner.StartedAt = domain.NewTime(time.Now())
	}
	if owner.Host == "" {
		if h, err := os.Hostname(); err == nil {
			owner.Host = h
		}
	}
	data, err := json.MarshalIndent(owner, "", "  ")
	if err != nil {
		return
	}
	_ = atomicfs.WriteFile(l.ownerPath(name), append(data, '\n'), 0o640)
}

// Owner reports who holds the lock without attempting to take it.
//
// Without attempting is load-bearing. This used to answer by taking the real
// lock and releasing it again, which makes a *reader* an acquirer: `status
// --watch` refreshes every two seconds, and a mutating command whose
// non-waiting acquisition landed in that window was told the deployment was
// busy by the thing that was only looking at it.
//
// So the answer comes from the sidecar plus the liveness of the process it
// names. That is a report rather than a decision -- exclusion is still the
// flock's job, and Acquire is the only thing that takes one -- which is what
// makes a heuristic acceptable here: a stale record whose PID has been reused
// shows the wrong operation in a status table, where taking the lock to find
// out shows a spurious failure on a command that should have run.
func (l *Locker) Owner(ctx context.Context, name string) (ports.LockOwner, bool, error) {
	data, err := os.ReadFile(l.ownerPath(name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Held, but by a process that did not record itself.
			return ports.LockOwner{}, false, nil
		}
		return ports.LockOwner{}, false, domain.Internal(err, "cannot read lock owner for %s", name)
	}

	var owner ports.LockOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return ports.LockOwner{}, false, nil
	}
	if !ownerAlive(owner) {
		// A process killed with SIGKILL releases its flock and leaves
		// the sidecar behind. Reporting that as held would send an
		// operator after a PID that no longer exists.
		return ports.LockOwner{}, false, nil
	}
	return owner, true, nil
}

// ownerAlive reports whether the recorded holder could still be running.
//
// EPERM counts as alive: the process exists and belongs to another user, which
// is what a root-run operation looks like to an unprivileged `status`.
//
// A record naming another host is reported as held, because nothing here can
// check it. The layout is machine-local by design, so this only arises when
// /var/lib is on shared storage -- where a lock file is not doing what its
// owner thinks anyway, and the honest answer is the record itself.
func ownerAlive(o ports.LockOwner) bool {
	if o.PID <= 0 {
		return false
	}
	if host, err := os.Hostname(); err == nil && o.Host != "" && o.Host != host {
		return true
	}
	if err := syscall.Kill(o.PID, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	// Something answers to that PID. Whether it is the process that took the
	// lock is a different question, and the one that matters after a SIGKILL:
	// the flock is already gone, this record is not, and the kernel may have
	// handed the number to somebody else. A start time that does not match is
	// a different process wearing the same PID.
	//
	// Only decides when both sides are known. A record written before this
	// field existed, or a platform that cannot report the start time, falls
	// back to the PID alone.
	if o.PIDStart != 0 {
		if live := pidStart(o.PID); live != 0 && live != o.PIDStart {
			return false
		}
	}
	return true
}

// pidStart reads the kernel's start time for a PID, in clock ticks since boot.
//
// Field 22 of /proc/<pid>/stat, counted after the comm field rather than by
// splitting the whole line: comm is the executable name in parentheses and may
// contain spaces, which is what breaks the naive split.
//
// Zero when it cannot be read -- a kernel without procfs, a PID that has
// already gone. Callers treat zero as "unknown" and fall back.
func pidStart(pid int) uint64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	return parsePIDStart(data)
}

// parsePIDStart pulls field 22 out of a /proc/<pid>/stat line.
//
// Split from the read so the parsing is testable without a process to point
// at: the shapes that break a naive reader -- a comm with a space in it, a
// comm with a parenthesis in it, a truncated line -- are the ones no live
// process on the machine running the tests is likely to have.
//
// Zero for anything it cannot read, which callers treat as "unknown".
func parsePIDStart(data []byte) uint64 {
	// Last ')' rather than first: comm is the executable name in parentheses
	// and may itself contain one, so scanning forward finds the wrong end.
	commEnd := bytes.LastIndexByte(data, ')')
	if commEnd < 0 || commEnd+2 >= len(data) {
		return 0
	}
	// After "(comm) " the fields are state (3) onward, so the start time --
	// field 22 -- is the 20th of what remains.
	fields := strings.Fields(string(data[commEnd+2:]))
	const startTimeOffset = 19
	if len(fields) <= startTimeOffset {
		return 0
	}
	v, err := strconv.ParseUint(fields[startTimeOffset], 10, 64)
	if err != nil {
		return 0
	}
	return v
}
