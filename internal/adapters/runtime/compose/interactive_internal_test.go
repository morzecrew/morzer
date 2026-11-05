package compose

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/ports"
)

// standInDocker is a `docker` that prints its argv to stderr, one per line,
// and then does what its last argument says: `seven` exits 7, `wait` sleeps
// until a signal ends it, and anything else copies stdin to stdout.
func standInDocker(t *testing.T) *Runtime {
	t.Helper()
	script := filepath.Join(t.TempDir(), "docker")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
for a in "$@"; do printf '%s\n' "$a" >&2; done
for last in "$@"; do :; done
case "$last" in
seven) exit 7 ;;
wait) exec sleep 30 ;;
esac
cat
`), 0o755))
	r := New(nil)
	r.docker = script
	return r
}

func session(t *testing.T, ctx context.Context, tty bool, argv ...string) (code int, stdout, argvSeen string, err error) {
	t.Helper()
	var out, errs bytes.Buffer
	code, err = standInDocker(t).ExecInteractive(ctx, ports.RuntimeConfig{WorkingDir: t.TempDir()}, "app", argv,
		ports.ExecIO{Stdin: strings.NewReader("typed\n"), Stdout: &out, Stderr: &errs, TTY: tty})
	return code, out.String(), errs.String(), err
}

// Without a terminal, `compose exec` is told not to allocate one, and stdin
// reaches the command.
func TestASessionWithoutATerminalAsksForNoTTY(t *testing.T) {
	code, out, argv, err := session(t, context.Background(), false, "cat")
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "typed\n", out)
	assert.Contains(t, argv, "exec\n--no-TTY\napp\ncat\n")
}

// With one, `compose exec` allocates a TTY, as it does by default.
func TestASessionAtATerminalAsksForATTY(t *testing.T) {
	_, _, argv, err := session(t, context.Background(), true, "cat")
	require.NoError(t, err)
	assert.Contains(t, argv, "exec\napp\ncat\n")
	assert.NotContains(t, argv, "--no-TTY")
}

// A command that exits non-zero is a result, not an error, as for Exec.
func TestASessionReturnsTheCommandsExitCode(t *testing.T) {
	code, _, _, err := session(t, context.Background(), false, "seven")
	require.NoError(t, err)
	assert.Equal(t, 7, code)
}

// Cancelling the session's context sends `compose` SIGTERM, and a session a
// signal ended reports 128 plus the signal, as a shell does.
func TestCancellingASessionSendsSIGTERM(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	started := time.Now()
	code, _, _, err := session(t, ctx, false, "wait")
	require.NoError(t, err)
	assert.Equal(t, 143, code)
	assert.Less(t, time.Since(started), 10*time.Second)
}
