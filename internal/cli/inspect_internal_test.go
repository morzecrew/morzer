package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
)

// `--since` is the one flag here with a parse worth testing on its own: it
// takes two forms, refuses a third, and the refusal is the interesting one.

func TestSinceTakesADurationBackFromNow(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	got, err := parseSince("15m", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-15 * time.Minute); !got.Equal(want) {
		t.Errorf("--since 15m resolved to %s, want %s", got, want)
	}
}

func TestSinceTakesAnAbsoluteInstant(t *testing.T) {
	got, err := parseSince("2026-08-10T09:12:33Z", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 8, 10, 9, 12, 33, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestSinceRefusesATimestampWithNoZone(t *testing.T) {
	// Not assumed local, and not assumed UTC. "Which midnight" is exactly
	// the question a log query must not guess, and the machine's zone is
	// rarely the operator's -- so the refusal names the missing part rather
	// than saying the value is invalid, which would send them looking at
	// the date.
	_, err := parseSince("2026-08-10T09:12:33", time.Now())
	if err == nil {
		t.Fatal("a timestamp with no zone was accepted")
	}
	if code := domain.ExitCode(err); code != domain.ExitUsage {
		t.Errorf("exit %d, want %d", code, domain.ExitUsage)
	}
	if hint := domain.AsError(err).Hint; hint == "" {
		t.Error("the refusal offers no remedy")
	}
	if msg := domain.AsError(err).Message; msg == "" || !strings.Contains(msg, "time zone") {
		t.Errorf("the refusal does not name the missing zone: %q", msg)
	}
}

func TestSinceRefusesSomethingThatIsNeither(t *testing.T) {
	_, err := parseSince("yesterday", time.Now())
	if err == nil {
		t.Fatal("`yesterday` was accepted")
	}
	// Both accepted forms are named. A usage error that says only "invalid"
	// is a second guess an operator has to make.
	hint := domain.AsError(err).Hint
	if !strings.Contains(hint, "duration") || !strings.Contains(hint, "RFC 3339") {
		t.Errorf("the hint names neither form: %q", hint)
	}
}

func TestSinceUnsetIsZero(t *testing.T) {
	got, err := parseSince("", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Errorf("an unset --since resolved to %s, so the runtime would be given a "+
			"cutoff nobody asked for", got)
	}
}

func TestSinceRefusesANegativeDuration(t *testing.T) {
	// `--since -10m` parses as a duration and would ask for lines from ten
	// minutes in the future, which is a silently empty stream.
	if _, err := parseSince("-10m", time.Now()); err == nil {
		t.Fatal("a negative duration was accepted")
	}
}

// TestAContainersExitCodeOutranksTheMappingTable is the other half of
// `exec`'s contract: the codes inside a container are not this program's.
func TestAContainersExitCodeOutranksTheMappingTable(t *testing.T) {
	err := exitStatus{
		code:  3,
		cause: domain.RuntimeError(domain.ErrRuntime, "the command in db exited 3"),
	}

	if got := exitCodeFor(err); got != 3 {
		t.Errorf("exit %d, want the container's own 3", got)
	}
	// And the envelope still sees an ordinary runtime error underneath, so
	// a --json consumer gets what it gets for every other failure.
	if !errors.Is(err, domain.ErrRuntime) {
		t.Error("the envelope cannot classify this failure")
	}
	if domain.AsError(err).Message == "" {
		t.Error("the envelope would carry no message")
	}
	// Nothing is printed for it: the command already said whatever it had
	// to say on its own streams.
	if !silentFailure(err) {
		t.Error("a container's own failure would be reported twice")
	}
}

// TestCtrlCDuringAFollowIsNotAFailure is the difference between an operator who
// finished reading and a runtime that died.
func TestCtrlCDuringAFollowIsNotAFailure(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if err := endOfStream(cancelled, errors.New("signal: terminated")); err != nil {
		t.Errorf("an interrupted follow reported %v; ctrl-C means the operator "+
			"has finished reading", err)
	}

	// EOF is the ordinary end of a stream that was not following.
	if err := endOfStream(context.Background(), io.EOF); err != nil {
		t.Errorf("a stream that ended reported %v", err)
	}

	// Anything else is the runtime's own failure, and it must not be
	// swallowed: a `logs` that exited 0 having printed nothing would be
	// read as a deployment that said nothing.
	err := endOfStream(context.Background(), errors.New("the daemon went away"))
	if err == nil {
		t.Fatal("a runtime that died mid-stream reported success")
	}
	if code := domain.ExitCode(err); code != domain.ExitRuntime {
		t.Errorf("exit %d, want %d", code, domain.ExitRuntime)
	}
}

// TestAWatchGivesUpAfterTwoConsecutiveFailures drives the plain `--watch` loop,
// which is the form a pipe and a journal get.
func TestAWatchGivesUpAfterTwoConsecutiveFailures(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{
		Stream: ui.Streams{Out: &out, Err: &errOut},
		// No runtime wired, so every sample fails the same way a daemon
		// that has gone would. What is under test is the counting, not
		// the reason.
		Deps: &ops.Deps{},
	}

	start := time.Now()
	err := app.appendStats(context.Background(), 10*time.Millisecond)
	if err == nil {
		t.Fatal("a watch against a runtime that never answers ran forever and exited 0")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the watch took %s to give up", elapsed)
	}

	// The first failure is reported and the watch carries on: a daemon
	// hiccup must not end something an operator set running and walked
	// away from.
	if !strings.Contains(errOut.String(), "cannot read statistics") {
		t.Errorf("the first failure was not reported:\n%s", errOut.String())
	}
	if n := strings.Count(errOut.String(), "cannot read statistics"); n != 1 {
		t.Errorf("reported %d failures before giving up, want 1 then the error", n)
	}
}

// TestAWatchThatIsInterruptedEndsCleanly: ctrl-C during a watch is how one ends.
func TestAWatchThatIsInterruptedEndsCleanly(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Stream: ui.Streams{Out: &out, Err: &errOut}, Deps: &ops.Deps{}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := app.appendStats(ctx, time.Hour); err != nil {
		t.Errorf("an interrupted watch reported %v", err)
	}
	// And says nothing about it. The in-flight sample fails because the
	// operator's ctrl-C reached the runtime, and "cannot read statistics"
	// under it would be the manager blaming the daemon for a keystroke.
	if errOut.Len() != 0 {
		t.Errorf("an interrupted watch complained on the way out:\n%s", errOut.String())
	}
}

func TestExecOutputGoesThroughUnframed(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Stream: ui.Streams{Out: &out, Err: &errOut}}

	// Byte for byte. An operator piping `morzer exec db -- pg_dump` into a
	// file must get the dump, so nothing wraps, pads or colours it.
	const dump = "COPY users (id, name) FROM stdin;\n1\tada\n\\.\n"
	app.passThrough(dump)
	if out.String() != dump {
		t.Errorf("the command's output was changed:\n got %q\nwant %q", out.String(), dump)
	}
}

func TestAnOrdinaryFailureIsStillMappedAndStillPrinted(t *testing.T) {
	err := domain.Usage("no")
	if got := exitCodeFor(err); got != domain.ExitUsage {
		t.Errorf("exit %d, want %d", got, domain.ExitUsage)
	}
	if silentFailure(err) {
		t.Error("an ordinary failure would be swallowed")
	}
}
