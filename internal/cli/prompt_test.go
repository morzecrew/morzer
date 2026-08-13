package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ui"
)

// There is no --value flag, and there never will be: argv is world-readable
// through /proc, so a credential passed that way is a credential published.
// stdin is the only channel, which makes these two functions the whole of how
// a secret enters this program.

func readerApp(in io.Reader) (*App, *strings.Builder) {
	var out strings.Builder
	return &App{Stream: ui.Streams{Out: &out, Err: &out, In: in}}, &out
}

func TestAPipedSecretIsTakenWhole(t *testing.T) {
	cases := map[string]struct {
		piped string
		want  string
	}{
		// What `echo secret | morzer secret set x` produces. Exactly one
		// trailing newline goes, because echo adds exactly one.
		"one trailing newline":      {"hunter2\n", "hunter2"},
		"no trailing newline":       {"hunter2", "hunter2"},
		"two trailing newlines":     {"hunter2\n\n", "hunter2\n"},
		"internal newlines survive": {"-----BEGIN KEY-----\nabc\n", "-----BEGIN KEY-----\nabc"},
		// Whitespace is part of a password. Trimming it would silently
		// set a different secret than the operator piped.
		"leading and trailing spaces": {"  spaced  \n", "  spaced  "},
		"a value that is only spaces": {"   \n", "   "},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			app, _ := readerApp(strings.NewReader(tc.piped))

			got, err := app.readSecretValue(context.Background(), "value for x: ")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Reveal())
		})
	}
}

func TestAnEmptyPipeIsAnEmptySecret(t *testing.T) {
	app, _ := readerApp(strings.NewReader(""))

	got, err := app.readSecretValue(context.Background(), "value for x: ")
	require.NoError(t, err, "an empty pipe is a value the caller rejects, not a read failure")
	assert.True(t, got.IsEmpty(),
		"an empty pipe produced a non-empty secret, so `secret set` would store it")
}

// TestNothingIsPromptedWhenThereIsNoTerminal is the refusal a cron job or a
// systemd unit hits, and it has to say what to do instead.
func TestNothingIsPromptedWhenThereIsNoTerminal(t *testing.T) {
	// A pipe that is not a terminal takes the piped path, so the refusal
	// below is reached by calling the prompt directly -- which is what
	// happens when stdin *is* a terminal and reading the password fails.
	var out strings.Builder

	_, err := readPassword(context.Background(), strings.NewReader("ignored"), &out, "value for x: ")
	require.Error(t, err, "a value was read from something that is not a terminal")

	de := domain.AsError(err)
	assert.Equal(t, domain.CodeUsage, de.Code)
	assert.Contains(t, de.Hint, "printf",
		"the refusal does not show the piped form, which is the only thing a "+
			"script can do")
}

// TestAFileThatIsNotATerminalIsNotOneEither. The check is a type assertion
// followed by isatty, and an *os.File that happens to be a regular file must
// fail the second half.
func TestAFileThatIsNotATerminalIsNotOneEither(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "in")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	assert.False(t, ui.IsTerminal(f))
	assert.False(t, ui.IsTerminal(strings.NewReader("")))
	assert.False(t, ui.IsTerminal(nil))
}

// TestAnUnreasonablyLargeValueIsRefused. Without the bound, `secret set x <
// /dev/zero` is the manager filling its own memory.
//
// This pins the refusal. It does not pin the LimitReader that makes the
// refusal memory-safe -- a test for that would have to exhaust memory to
// prove it, so the two are separate and only one is asserted here.
func TestAnUnreasonablyLargeValueIsRefused(t *testing.T) {
	app, _ := readerApp(strings.NewReader(strings.Repeat("x", (1<<20)+1)))

	_, err := app.readSecretValue(context.Background(), "value for x: ")
	require.Error(t, err, "a value larger than a megabyte was accepted as a secret")
	assert.Equal(t, domain.CodeUsage, domain.AsError(err).Code)
	assert.Contains(t, err.Error(), "unreasonably large")
}

func TestAValueExactlyAtTheLimitIsAccepted(t *testing.T) {
	// The bound is inclusive, so a value at exactly the limit is a value,
	// not a refusal.
	app, _ := readerApp(strings.NewReader(strings.Repeat("x", 1<<20)))

	got, err := app.readSecretValue(context.Background(), "value for x: ")
	require.NoError(t, err)
	assert.Len(t, got.Reveal(), 1<<20)
}

// TestAReadThatFailsIsReportedNotSilentlyTruncated. A short read that ends in
// an error must not become a secret made of whatever arrived first.
func TestAReadThatFailsIsReportedNotSilentlyTruncated(t *testing.T) {
	app, _ := readerApp(&failingReader{after: []byte("partial-")})

	_, err := app.readSecretValue(context.Background(), "value for x: ")
	require.Error(t, err, "a failed read produced a truncated secret, which would be "+
		"stored and then never work")
	assert.Equal(t, domain.CodeInternal, domain.AsError(err).Code)
}

// failingReader delivers some bytes and then breaks, the way a closed pipe
// does.
type failingReader struct {
	after []byte
	done  bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, assert.AnError
	}
	r.done = true
	return copy(p, r.after), nil
}

func TestParseComponents(t *testing.T) {
	got, err := parseComponents([]string{"database", "CONFIG", " secrets "})
	require.NoError(t, err, "case and surrounding whitespace are not typos")
	assert.Len(t, got, 3)

	none, err := parseComponents(nil)
	require.NoError(t, err)
	assert.Nil(t, none, "no --component means everything, not nothing")

	_, err = parseComponents([]string{"database", "databsae"})
	require.Error(t, err, "a mistyped component was silently ignored, so the backup "+
		"would quietly cover less than the operator asked for")

	de := domain.AsError(err)
	assert.Equal(t, domain.CodeUsage, de.Code)
	assert.Contains(t, de.Message, "databsae")
	assert.Contains(t, de.Hint, "database",
		"the refusal does not list what is valid")
	// Sorted, so two runs of the same mistake read the same.
	assert.Less(t, strings.Index(de.Hint, "config"), strings.Index(de.Hint, "secrets"))
}
