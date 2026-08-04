package lock_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/lock"
	"github.com/morzecrew/morzer/internal/ports"
)

// Two concurrent mutating operations on one installation is the race the whole
// design assumes away, so this lock is the first step of every mutating
// command. What it has to get right is not just exclusion -- flock does that --
// but the refusal: "resource busy" is a useless thing to tell an operator, and
// a stale record pointing at a dead PID is worse than none.

func locker(t *testing.T) *lock.Locker {
	t.Helper()
	return lock.New(filepath.Join(t.TempDir(), "locks"))
}

func owner(id string) ports.LockOwner {
	return ports.LockOwner{OperationID: id, Type: "update"}
}

func TestALockIsExclusiveAndReleasable(t *testing.T) {
	l := locker(t)
	ctx := context.Background()

	release, err := l.Acquire(ctx, "deployment", ports.LockOptions{Owner: owner("op-1")})
	if err != nil {
		t.Fatalf("taking a free lock: %v", err)
	}

	if _, err := l.Acquire(ctx, "deployment", ports.LockOptions{Owner: owner("op-2")}); err == nil {
		t.Fatal("a second operation took a lock the first was holding")
	}

	// A different name is a different lock: `backup` and `deployment` are
	// separate, or a scheduled backup would block every update.
	other, err := l.Acquire(ctx, "backup", ports.LockOptions{Owner: owner("op-3")})
	if err != nil {
		t.Fatalf("an unrelated lock was blocked: %v", err)
	}
	if err := other(); err != nil {
		t.Fatal(err)
	}

	if err := release(); err != nil {
		t.Fatalf("releasing: %v", err)
	}

	again, err := l.Acquire(ctx, "deployment", ports.LockOptions{Owner: owner("op-4")})
	if err != nil {
		t.Fatalf("a released lock could not be retaken: %v", err)
	}
	if err := again(); err != nil {
		t.Fatal(err)
	}
}

// TestReleaseIsIdempotent: the release function is deferred, and some paths
// also call it explicitly. A second call must not report an error the caller
// would then log as a failure.
func TestReleaseIsIdempotent(t *testing.T) {
	l := locker(t)
	release, err := l.Acquire(context.Background(), "deployment", ports.LockOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Errorf("a second release reported an error: %v", err)
	}
}

// TestTheRefusalNamesWhoHoldsItAndForHowLong is the whole reason there is an
// owner sidecar at all.
func TestTheRefusalNamesWhoHoldsItAndForHowLong(t *testing.T) {
	l := locker(t)
	ctx := context.Background()

	release, err := l.Acquire(ctx, "deployment", ports.LockOptions{
		Owner: ports.LockOwner{
			OperationID: "op-abc123",
			Type:        "update",
			StartedAt:   domain.NewTime(time.Now().Add(-90 * time.Second)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	_, err = l.Acquire(ctx, "deployment", ports.LockOptions{Owner: owner("op-second")})
	if err == nil {
		t.Fatal("the lock was granted twice")
	}

	de := domain.AsError(err)
	if de.Code != domain.CodeLocked {
		t.Errorf("code = %v, want the locked code: a script retrying on a busy "+
			"lock needs to tell it from a real failure", de.Code)
	}
	for _, want := range []string{"op-abc123", "update", strconv.Itoa(os.Getpid())} {
		if !strings.Contains(de.Message, want) {
			t.Errorf("the refusal drops %q, so the operator cannot go and look at "+
				"the process holding it:\n%s", want, de.Message)
		}
	}
	// "1m", not the exact seconds: the elapsed time is measured when the
	// refusal is built, so pinning it to the second would make this fail
	// whenever the machine was busy for an extra half-second.
	if !strings.Contains(de.Message, "running for 1m") {
		t.Errorf("the refusal does not say how long it has been held, which is "+
			"how an operator tells a hung operation from a busy one:\n%s", de.Message)
	}
	if !strings.Contains(de.Hint, "--wait") {
		t.Errorf("the hint does not offer the flag that would have avoided the "+
			"refusal: %q", de.Hint)
	}
}

// TestARefusalWithoutARecordStillSaysSomethingUseful. A holder that never
// wrote a sidecar -- an older manager, or one that crashed between flock and
// write -- must still produce an actionable message.
func TestARefusalWithoutARecordStillSaysSomethingUseful(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "locks")
	l := lock.New(dir)
	ctx := context.Background()

	release, err := l.Acquire(ctx, "deployment", ports.LockOptions{Owner: owner("op-1")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	// Remove the sidecar behind the holder's back.
	if err := os.Remove(filepath.Join(dir, "deployment.owner.json")); err != nil {
		t.Fatal(err)
	}

	_, err = l.Acquire(ctx, "deployment", ports.LockOptions{})
	if err == nil {
		t.Fatal("the lock was granted twice")
	}
	de := domain.AsError(err)
	if de.Code != domain.CodeLocked {
		t.Errorf("code = %v, want the locked code", de.Code)
	}
	if !strings.Contains(de.Message, "another process") {
		t.Errorf("message %q says nothing at all", de.Message)
	}
	if !strings.Contains(de.Hint, "--wait") {
		t.Errorf("hint %q offers nothing", de.Hint)
	}
}

func TestOwnerOnALockNobodyHolds(t *testing.T) {
	l := locker(t)

	got, found, err := l.Owner(context.Background(), "deployment")
	if err != nil {
		t.Fatalf("asking about a free lock failed: %v", err)
	}
	if found {
		t.Errorf("a lock nobody holds reported an owner: %+v", got)
	}
}

func TestOwnerReportsTheHolderWithoutTakingTheLock(t *testing.T) {
	l := locker(t)
	ctx := context.Background()

	release, err := l.Acquire(ctx, "deployment", ports.LockOptions{
		Owner: ports.LockOwner{OperationID: "op-xyz", Type: "backup"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	got, found, err := l.Owner(ctx, "deployment")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("the holder was not reported, so `status` shows nothing while an " +
			"operation is running")
	}
	if got.OperationID != "op-xyz" || got.Type != "backup" {
		t.Errorf("owner = %+v, want the recorded operation", got)
	}
	if got.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d: the record has to be fillable in without "+
			"the caller supplying it", got.PID, os.Getpid())
	}
	if got.StartedAt.IsZero() {
		t.Error("no start time was recorded, so the refusal cannot say how long")
	}
	if got.Host == "" {
		t.Error("no host was recorded")
	}

	// Asking must not have disturbed the lock.
	if _, err := l.Acquire(ctx, "deployment", ports.LockOptions{}); err == nil {
		t.Error("Owner released the lock it was only supposed to look at")
	}
}

// TestAStaleRecordIsNotReportedAsAHolder is the case a SIGKILL leaves behind:
// the flock is gone with the process, the sidecar is not. Sending an operator
// after a PID that no longer exists is worse than saying nothing.
func TestAStaleRecordIsNotReportedAsAHolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "locks")
	l := lock.New(dir)
	ctx := context.Background()

	release, err := l.Acquire(ctx, "deployment", ports.LockOptions{Owner: owner("op-killed")})
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}

	// Put the record back, the way a killed process would have left it.
	record := `{"pid":999999,"operation_id":"op-killed","type":"update"}`
	if err := os.WriteFile(filepath.Join(dir, "deployment.owner.json"),
		[]byte(record), 0o640); err != nil {
		t.Fatal(err)
	}

	_, found, err := l.Owner(ctx, "deployment")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("a stale record was reported as a live holder, which sends the " +
			"operator after a PID that no longer exists")
	}

	// And the lock is genuinely takeable, record or no record.
	again, err := l.Acquire(ctx, "deployment", ports.LockOptions{})
	if err != nil {
		t.Fatalf("a lock whose holder was killed could not be retaken: %v", err)
	}
	_ = again()
}

// TestAnUnreadableRecordIsNotAFailure: diagnostics degrading is acceptable,
// an operation refusing to start because a sidecar is corrupt is not.
func TestAnUnreadableRecordIsNotAFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "locks")
	l := lock.New(dir)
	ctx := context.Background()

	release, err := l.Acquire(ctx, "deployment", ports.LockOptions{Owner: owner("op-1")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	if err := os.WriteFile(filepath.Join(dir, "deployment.owner.json"),
		[]byte("{this is not json"), 0o640); err != nil {
		t.Fatal(err)
	}

	_, found, err := l.Owner(ctx, "deployment")
	if err != nil {
		t.Errorf("a corrupt sidecar produced an error: %v", err)
	}
	if found {
		t.Error("a corrupt sidecar was decoded into an owner")
	}

	// The refusal still fires -- exclusion does not depend on the record.
	if _, err := l.Acquire(ctx, "deployment", ports.LockOptions{}); err == nil {
		t.Error("a corrupt sidecar let a second operation through")
	}
}

// TestWaitingForALockSucceedsWhenItIsReleased is the --wait path.
func TestWaitingForALockSucceedsWhenItIsReleased(t *testing.T) {
	l := locker(t)
	ctx := context.Background()

	release, err := l.Acquire(ctx, "deployment", ports.LockOptions{Owner: owner("op-holder")})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		second, err := l.Acquire(waitCtx, "deployment", ports.LockOptions{
			Wait: true, Owner: owner("op-waiter"),
		})
		if err == nil {
			_ = second()
		}
		done <- err
	}()

	// Long enough that the waiter has certainly polled at least once.
	time.Sleep(750 * time.Millisecond)
	if err := release(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("--wait did not get the lock after it was released: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("--wait never returned after the lock was released")
	}
}

// TestWaitingIsInterruptible is what makes ctrl-c work while an operator is
// waiting behind a long update.
func TestWaitingIsInterruptible(t *testing.T) {
	l := locker(t)
	ctx := context.Background()

	release, err := l.Acquire(ctx, "deployment", ports.LockOptions{Owner: owner("op-holder")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = l.Acquire(waitCtx, "deployment", ports.LockOptions{Wait: true})
	if err == nil {
		t.Fatal("waiting returned a lock that was never free")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("cancellation took %s, so ctrl-c does not work while waiting", elapsed)
	}

	de := domain.AsError(err)
	if de.Code != domain.CodeInterrupted {
		t.Errorf("code = %v, want interrupted: giving up on a lock is not the "+
			"same failure as being refused one, and a script reads the "+
			"difference", de.Code)
	}
}

// TestAcquireCreatesTheLockDirectory: the first mutating command on a fresh
// machine runs before anything has created it.
func TestAcquireCreatesTheLockDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deep", "not", "created", "locks")
	l := lock.New(dir)

	release, err := l.Acquire(context.Background(), "deployment", ports.LockOptions{})
	if err != nil {
		t.Fatalf("the lock directory was not created: %v", err)
	}
	defer func() { _ = release() }()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Errorf("the lock directory is mode %04o, want 0750", got)
	}
}

// TestAcquireRefusesADirectoryItCannotCreate is the fault-injection case: a
// read-only parent, which is what a machine with a full or remounted /var
// looks like.
func TestAcquireRefusesADirectoryItCannotCreate(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so this proves nothing as root")
	}

	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	_, err := lock.New(filepath.Join(parent, "locks")).
		Acquire(context.Background(), "deployment", ports.LockOptions{})
	if err == nil {
		t.Fatal("a lock was taken in a directory that could not be created")
	}
}
