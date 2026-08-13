package cli

import (
	"context"
	"errors"
	"fmt"
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

// The no-echo prompt against a real pseudo-terminal.
//
// Linux-only, and by its plumbing rather than by its subject: allocating a pty
// here is `/dev/ptmx` plus `TIOCGPTN` and `TIOCSPTLCK`, and darwin has neither
// ioctl nor `/dev/pts/<n>`. The prompt itself is portable -- RFC 0029 site 4
// left it with one per-OS ioctl pair -- so what this file costs on a Mac is the
// pty coverage, not the prompt.
//
// Split out rather than left in prompt_test.go because that file also holds the
// tests that need no terminal at all, and those must keep compiling and running
// wherever the package does. RFC 0029 §3.1 counted six sites by what `go build`
// reaches; a test file is a seventh that only `go vet` sees.

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

// TestTheLiveViewReadsFromARealTerminal is the positive half of
// root_internal_test.go's TestTheLiveViewReadsOnlyFromATerminal, which keeps
// the three negative cases because they need no pty and must run everywhere.
//
// An embedder that supplied its own pty must have its keys read from there,
// and nothing but a terminal is ever handed to the live view.
func TestTheLiveViewReadsFromARealTerminal(t *testing.T) {
	_, slave := openPTY(t)

	app := &App{Stream: ui.Streams{In: slave}}
	if got := app.terminalInput(); got != slave {
		t.Errorf("terminalInput = %v, want the injected terminal", got)
	}
}
