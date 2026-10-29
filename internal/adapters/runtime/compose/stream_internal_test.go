package compose

import (
	"bufio"
	"context"
	"io"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// The stream runs whatever argv it is given, which is what makes the whole
// process-group contract testable without Docker: a shell that spawns a child
// into the same group stands in for compose spawning its own.

func startShellStream(t *testing.T, ctx context.Context, script string) *streamCloser {
	t.Helper()
	rc, err := startStream(ctx, []string{"/bin/sh", "-c", script}, ports.RuntimeConfig{WorkingDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatalf("cannot start the stream: %v", err)
	}
	sc, ok := rc.(*streamCloser)
	if !ok {
		t.Fatalf("startStream returned a %T, want *streamCloser", rc)
	}
	return sc
}

// groupGone reports whether the process group has no members left.
func groupGone(pgid int) bool {
	return syscall.Kill(-pgid, 0) == syscall.ESRCH
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Close must reap the leader and take the whole group with it -- a
// `logs --follow` left running after the reader closes would leak a process
// for the life of the session, and its children for longer.
func TestCloseTerminatesTheWholeProcessGroup(t *testing.T) {
	sc := startShellStream(t, context.Background(),
		"sleep 60 & echo ready; wait")

	line, err := bufio.NewReader(sc).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("stream delivered %q (%v), want the child's output", line, err)
	}

	pgid := sc.pgid
	if pgid <= 0 {
		t.Fatal("the stream recorded no process group")
	}
	// Bounded, because a Close that signalled nothing still returns -- once
	// the sleep ends by itself, a minute later, which is the leak.
	started := time.Now()
	if err := sc.Close(); err != nil {
		t.Fatalf("closing the stream: %v", err)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("Close took %s: it waited for the group rather than ending it", took)
	}
	waitFor(t, "the process group to die", func() bool { return groupGone(pgid) })
}

// Context cancellation is the ctx-driven half of the same promise: the group
// TERM from cmd.Cancel must bring the tree down without anyone calling Close
// first. The witness is the *grandchild's* PID, not the group: the unreaped
// leader stays a zombie until someone Waits, which keeps the group ID alive
// even though every member is dead -- while a leader-only kill (the bug this
// pins) would leave the reparented sleep genuinely running.
func TestCancellationTerminatesTheWholeProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sc := startShellStream(t, ctx, "sleep 60 & echo $!; wait")

	line, err := bufio.NewReader(sc).ReadString('\n')
	if err != nil {
		t.Fatalf("stream delivered nothing: %v", err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("the script printed %q, want its child's pid", line)
	}

	cancel()
	waitFor(t, "the group's child to die", func() bool {
		return syscall.Kill(child, 0) == syscall.ESRCH
	})

	if err := sc.Close(); err != nil {
		t.Fatalf("closing after cancellation: %v", err)
	}
}

// The escalation timer disarms only when the group is provably gone: stopping
// it on leader reap alone would spare a child that ignored the TERM.
func TestDisarmKillRequiresTheGroupToBeGone(t *testing.T) {
	alive := startShellStream(t, context.Background(), "echo up; sleep 60")
	defer func() { _ = alive.Close() }()
	if _, err := bufio.NewReader(alive).ReadString('\n'); err != nil {
		t.Fatal(err)
	}

	alive.armKill(alive.pgid)
	alive.disarmKill()
	if !alive.killTimer.Stop() {
		t.Fatal("the timer was stopped while the group still lives")
	}

	// A group that is gone: reuse a dead leader's pgid.
	dead := startShellStream(t, context.Background(), "true")
	_ = dead.Close()
	waitFor(t, "the short-lived group to die", func() bool { return groupGone(dead.pgid) })

	dead.armKill(dead.pgid)
	dead.disarmKill()
	if dead.killTimer.Stop() {
		t.Fatal("the timer stayed armed for a group that is provably gone")
	}
}

// A stream that ends because compose failed is not a stream that ended: the
// daemon being down, or a service name that does not exist, both close stdout
// with nothing on it, and an EOF there made `logs` exit 0 having said nothing.
// The failure carries what compose wrote to stderr, since that is the reason.
func TestAStreamThatTheRuntimeFailedEndsWithItsError(t *testing.T) {
	sc := startShellStream(t, context.Background(),
		"echo partial; echo 'no such service: nosuch' >&2; exit 1")
	defer func() { _ = sc.Close() }()

	out, err := io.ReadAll(sc)
	if string(out) != "partial\n" {
		t.Errorf("stream delivered %q, want what compose wrote before failing", out)
	}
	if err == nil {
		t.Fatal("compose exited 1 and the stream ended cleanly")
	}
	if code := domain.ExitCode(err); code != domain.ExitRuntime {
		t.Errorf("exit %d, want %d", code, domain.ExitRuntime)
	}
	if !strings.Contains(domain.AsError(err).Message, "no such service: nosuch") {
		t.Errorf("the error %q lost compose's own reason", domain.AsError(err).Message)
	}

	// Nothing on stderr still names a reason: the exit status.
	silent := startShellStream(t, context.Background(), "exit 3")
	defer func() { _ = silent.Close() }()
	if _, err := io.ReadAll(silent); err == nil ||
		!strings.Contains(domain.AsError(err).Message, "exit status 3") {
		t.Errorf("a silent failure reported %v, want its exit status", err)
	}
}

// The other half: a stream that ran to its end, or that morzer stopped, is
// still a clean EOF.
func TestAStreamThatEndedOrWasStoppedEndsCleanly(t *testing.T) {
	done := startShellStream(t, context.Background(), "echo one")
	defer func() { _ = done.Close() }()
	if _, err := io.ReadAll(done); err != nil {
		t.Errorf("a stream that ran to its end reported %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := startShellStream(t, ctx, "echo up; sleep 60")
	defer func() { _ = stopped.Close() }()
	reader := bufio.NewReader(stopped)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := io.ReadAll(reader); err != nil {
		t.Errorf("a stream morzer stopped reported %v", err)
	}
}
