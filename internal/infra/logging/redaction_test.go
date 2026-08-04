package logging_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/logging"
)

// "Secrets never reach a log" is the boldest claim this project makes, and
// `redactingHandler` is the whole of its enforcement. Before these tests it was
// the least-covered code in the package: redactAttr at 21%, WithAttrs and
// WithGroup at 0%.
//
// Every test here names a route a secret could take to a log line, and fails if
// that route stops being scrubbed.

const secret = "s3cr3t-p4ssw0rd-value"

func newLogger(t *testing.T) (*slog.Logger, *logging.Redactor, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger, redactor := logging.New(logging.Options{
		Writer: &buf, Format: logging.FormatText, Level: slog.LevelDebug,
	})
	return logger, redactor, &buf
}

// assertScrubbed is the assertion every test in this file makes: the value is
// gone, and something marks where it was.
func assertScrubbed(t *testing.T, buf *bytes.Buffer, route string) {
	t.Helper()
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Errorf("a secret reached the log via %s:\n%s", route, out)
	}
	if !strings.Contains(out, "REDACTED") && !strings.Contains(out, "redacted") {
		t.Errorf("%s produced no redaction marker, so the value may simply "+
			"have been dropped rather than scrubbed:\n%s", route, out)
	}
}

func TestASecretIsScrubbedFromEveryRoute(t *testing.T) {
	routes := map[string]func(l *slog.Logger){
		"the message itself": func(l *slog.Logger) {
			l.Info("connecting with " + secret)
		},
		"a string attribute": func(l *slog.Logger) {
			l.Info("connecting", "password", secret)
		},
		"an attribute inside a group": func(l *slog.Logger) {
			l.Info("connecting", slog.Group("db", "password", secret))
		},
		"an error's message": func(l *slog.Logger) {
			l.Error("failed", "error", errors.New("bad password: "+secret))
		},
		"a value that only stringifies": func(l *slog.Logger) {
			l.Info("connecting", "conn", stringer{secret})
		},
		"a struct with no String method at all": func(l *slog.Logger) {
			// Neither a string, an error, nor a Stringer -- the
			// handler renders it, and the rendering carries the
			// field. This is why the KindAny branch ends in a
			// scrub of %v rather than passing the value through.
			l.Info("connecting", "conn", struct{ Password string }{secret})
		},
		"a recovered panic value": func(l *slog.Logger) {
			// The one place this codebase logs a bare `any`: a
			// presenter that panicked, whose value could be
			// anything at all.
			l.Error("a presenter panicked", "panic", any(secret+" in a panic"))
		},
		"a nested group": func(l *slog.Logger) {
			l.Info("connecting",
				slog.Group("outer", slog.Group("inner", "password", secret)))
		},
	}

	for name, emit := range routes {
		t.Run(name, func(t *testing.T) {
			logger, redactor, buf := newLogger(t)
			redactor.Register(secret)
			emit(logger)
			assertScrubbed(t, buf, name)
		})
	}
}

// TestASecretCapturedByWithIsScrubbed is the WithAttrs path, which had no test
// at all. A logger built with `.With(...)` carries its attributes into every
// subsequent line, so a miss here leaks repeatedly rather than once.
func TestASecretCapturedByWithIsScrubbed(t *testing.T) {
	logger, redactor, buf := newLogger(t)
	redactor.Register(secret)

	child := logger.With("password", secret)
	child.Info("first")
	child.Info("second")

	assertScrubbed(t, buf, "logger.With")
	if strings.Count(strings.ToLower(buf.String()), "redacted") < 2 {
		t.Errorf("the captured attribute was not scrubbed on every line:\n%s", buf.String())
	}
}

func TestASecretUnderWithGroupIsScrubbed(t *testing.T) {
	logger, redactor, buf := newLogger(t)
	redactor.Register(secret)

	logger.WithGroup("db").Info("connecting", "password", secret)
	assertScrubbed(t, buf, "logger.WithGroup")
}

// TestRegisteringAfterWithIsAKnownLimit records what the redactor cannot do.
//
// WithAttrs scrubs when `With` is called, so a value captured before its secret
// is registered is written in the clear. It is not reachable today -- the only
// call sites pass operation ids -- and this test exists so that if it ever
// becomes reachable, somebody has already written down that it is a hazard
// rather than discovering it in a log.
//
// If the eager behaviour is ever replaced by scrubbing at write time, this test
// fails, and that is the correct outcome: delete it and keep the improvement.
func TestRegisteringAfterWithIsAKnownLimit(t *testing.T) {
	logger, redactor, buf := newLogger(t)

	child := logger.With("token", secret) // captured first
	redactor.Register(secret)             // registered second
	child.Info("using it")

	if !strings.Contains(buf.String(), secret) {
		t.Log("redaction is no longer eager, which is an improvement: " +
			"delete this test and the hazard note beside it")
		t.Fail()
	}
}

func TestRegisterIgnoresWhatIsNotWorthScrubbing(t *testing.T) {
	logger, redactor, buf := newLogger(t)

	// Empty and very short values would turn every log line into confetti:
	// registering "a" would scrub the letter a everywhere.
	redactor.Register("", "a", "ab")
	logger.Info("a cat sat on a mat")

	if strings.Contains(strings.ToLower(buf.String()), "redacted") {
		t.Errorf("a trivially short value was registered and wrecked the output:\n%s", buf.String())
	}
}

func TestLongestValuesAreScrubbedFirst(t *testing.T) {
	logger, redactor, buf := newLogger(t)

	// A short secret that is a substring of a longer one must not leave the
	// tail of the longer one visible.
	redactor.Register("password123", "password123456")
	logger.Info("value", "v", "password123456")

	if strings.Contains(buf.String(), "456") {
		t.Errorf("a longer secret was partially scrubbed, leaving its tail:\n%s", buf.String())
	}
}

func TestRegisterSetTakesEveryValueInASecretSet(t *testing.T) {
	logger, redactor, buf := newLogger(t)

	set := domain.NewSecretSet(map[string]domain.Secret{
		"db_password": domain.NewSecret(secret),
		"session_key": domain.NewSecret("another-secret-value-here"),
	})
	redactor.RegisterSet(set)

	logger.Info("both", "a", secret, "b", "another-secret-value-here")
	assertScrubbed(t, buf, "RegisterSet")
	if strings.Contains(buf.String(), "another-secret-value-here") {
		t.Errorf("only one of the set's values was scrubbed:\n%s", buf.String())
	}
}

// TestValuesReturnsWhatIsRegistered pins the accessor the exec runner uses to
// scrub subprocess output — the same list, so a secret cannot be redacted from
// a log line and printed by a tool in the next.
func TestValuesReturnsWhatIsRegistered(t *testing.T) {
	_, redactor, _ := newLogger(t)
	redactor.Register(secret)

	var found bool
	for _, v := range redactor.Values() {
		if v == secret {
			found = true
		}
	}
	if !found {
		t.Error("a registered secret is not in Values(), so subprocess output would not be scrubbed")
	}
}

// TestTheEventSinkScrubsAsWell covers the other route into a log: the event
// bus. Presenters and the log handler subscribe to the same stream.
func TestTheEventSinkScrubsAsWell(t *testing.T) {
	logger, redactor, buf := newLogger(t)
	redactor.Register(secret)

	sink := logging.EventSink(logger)
	sink.Handle(events.Message(events.LevelWarn, "leaking %s", secret))
	sink.Handle(events.StepOutput("op", "step", "tool printed "+secret))

	assertScrubbed(t, buf, "the event sink")
}

func TestTheHandlerRespectsTheLevel(t *testing.T) {
	var buf bytes.Buffer
	logger, _ := logging.New(logging.Options{
		Writer: &buf, Format: logging.FormatJSON, Level: slog.LevelWarn,
	})

	logger.Debug("quiet")
	logger.Info("also quiet")
	logger.Warn("loud")

	if strings.Contains(buf.String(), "quiet") {
		t.Errorf("a below-level line was written:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "loud") {
		t.Errorf("an at-level line was dropped:\n%s", buf.String())
	}
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		verbose, quiet bool
		want           slog.Level
	}{
		{false, false, slog.LevelInfo},
		{true, false, slog.LevelDebug},
		{false, true, slog.LevelError},
		// Both: quiet wins, because an operator who asked for silence is
		// more likely to mean it than one who left -v in a script.
		{true, true, slog.LevelError},
	}
	for _, tc := range cases {
		if got := logging.ParseLevel(tc.verbose, tc.quiet); got != tc.want {
			t.Errorf("ParseLevel(verbose=%v, quiet=%v) = %v, want %v",
				tc.verbose, tc.quiet, got, tc.want)
		}
	}
}

func TestTheLoggerTravelsInAContext(t *testing.T) {
	logger, _, buf := newLogger(t)

	ctx := logging.WithLogger(context.Background(), logger)
	logging.FromContext(ctx).Info("carried")

	if !strings.Contains(buf.String(), "carried") {
		t.Error("the logger did not survive the context round trip")
	}

	// And a context without one still logs somewhere rather than panicking.
	logging.FromContext(context.Background()).Info("no logger here")
}

type stringer struct{ v string }

func (s stringer) String() string { return "conn=" + s.v }
