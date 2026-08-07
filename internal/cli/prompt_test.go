package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

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

// openPTY returns the two ends of a fresh pseudo-terminal, which is what lets
// the no-echo prompt be tested against the promises it actually makes: the
// value arrives, nothing echoes, and the terminal mode survives every exit
// path. Skipped where the environment provides no pty.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("cannot number the pty: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("cannot unlock the pty: %v", err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("cannot open the pty slave: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return m, s
}

// waitForEchoOff polls until the prompt's mode flip has landed on the pty --
// the state is observable, so tests synchronise on it rather than on a sleep
// that a loaded CI box can outrun.
func waitForEchoOff(t *testing.T, f *os.File) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if terminalMode(t, f).Lflag&unix.ECHO == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the prompt never disabled echo")
}

func terminalMode(t *testing.T, f *os.File) unix.Termios {
	t.Helper()
	tio, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("cannot read the terminal mode: %v", err)
	}
	return *tio
}

func TestReadPasswordReadsWithoutEchoAndRestoresTheTerminal(t *testing.T) {
	master, slave := openPTY(t)
	before := terminalMode(t, slave)

	var out strings.Builder
	type result struct {
		value string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		v, err := readPassword(context.Background(), slave, &out, "value: ")
		done <- result{v, err}
	}()

	// The typed bytes must meet echo-off for sure, and the flip is
	// observable state, not a timing bet.
	waitForEchoOff(t, slave)
	if _, err := master.WriteString("hunter2\n"); err != nil {
		t.Fatal(err)
	}

	r := <-done
	require.NoError(t, r.err)
	assert.Equal(t, "hunter2", r.value)
	assert.Equal(t, before, terminalMode(t, slave),
		"the terminal mode must be restored exactly")

	// Nothing may have echoed back to the terminal. The prompt goes to the
	// injected writer, so the master side must stay silent -- probed with
	// poll rather than a read, because on a silent master a read is this
	// test hanging on its own success.
	pfds := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
	ready, perr := unix.Poll(pfds, 200)
	require.NoError(t, perr)
	if ready > 0 {
		buf := make([]byte, 256)
		n, _ := unix.Read(int(master.Fd()), buf)
		assert.NotContains(t, string(buf[:n]), "hunter2",
			"the secret echoed on the terminal")
	}
}

func TestReadPasswordCancelRestoresTheTerminal(t *testing.T) {
	_, slave := openPTY(t)
	before := terminalMode(t, slave)

	ctx, cancel := context.WithCancel(context.Background())
	var out strings.Builder
	done := make(chan error, 1)
	go func() {
		_, err := readPassword(ctx, slave, &out, "value: ")
		done <- err
	}()

	waitForEchoOff(t, slave)
	cancel()

	err := <-done
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInterrupted),
		"a cancelled prompt must classify as interrupted, got %v", err)
	assert.Equal(t, before, terminalMode(t, slave),
		"the cancelled path must restore the terminal mode")
}

func TestReadPasswordAcceptsCtrlDAfterText(t *testing.T) {
	master, slave := openPTY(t)

	var out strings.Builder
	type result struct {
		value string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		v, err := readPassword(context.Background(), slave, &out, "value: ")
		done <- result{v, err}
	}()

	waitForEchoOff(t, slave)
	// Ctrl-D flushes the typed text; the second one delivers the EOF that
	// completes a value typed without a final newline.
	if _, err := master.WriteString("abc\x04\x04"); err != nil {
		t.Fatal(err)
	}

	r := <-done
	require.NoError(t, r.err)
	assert.Equal(t, "abc", r.value)
}
