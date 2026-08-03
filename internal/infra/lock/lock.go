// Package lock implements the deployment lock.
//
// Two concurrent mutating operations on one installation is the race the whole
// design assumes away, so acquiring this is the first step of every mutating
// command. The lock is advisory flock(2) plus an owner record, because
// "resource busy" is a useless thing to tell an operator: they need to know
// which operation holds it and since when.
package lock

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// pollInterval is how often a blocking acquisition retries. Operations run for
// minutes, so a half-second poll costs nothing and keeps cancellation
// responsive.
const pollInterval = 500 * time.Millisecond

// Locker is the flock-backed implementation of ports.Locker.
type Locker struct {
	dir string
}

// New returns a Locker storing lock files in dir.
func New(dir string) *Locker {
	return &Locker{dir: dir}
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
	locked, err := fl.TryLockContext(ctx, pollInterval)
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
// The owner record is only meaningful while the lock is actually held: a
// process killed with SIGKILL releases its flock but leaves the sidecar
// behind. So the flock is probed first, and a stale record is reported as "not
// held" rather than sending an operator after a PID that no longer exists.
func (l *Locker) Owner(ctx context.Context, name string) (ports.LockOwner, bool, error) {
	fl := flock.New(l.path(name))
	free, err := fl.TryLock()
	if err == nil && free {
		// We just took it, so nobody else holds it. Release immediately.
		_ = fl.Unlock()
		return ports.LockOwner{}, false, nil
	}

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
	return owner, true, nil
}
