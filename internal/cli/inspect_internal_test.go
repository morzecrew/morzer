package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
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

func TestAnOrdinaryFailureIsStillMappedAndStillPrinted(t *testing.T) {
	err := domain.Usage("no")
	if got := exitCodeFor(err); got != domain.ExitUsage {
		t.Errorf("exit %d, want %d", got, domain.ExitUsage)
	}
	if silentFailure(err) {
		t.Error("an ordinary failure would be swallowed")
	}
}
