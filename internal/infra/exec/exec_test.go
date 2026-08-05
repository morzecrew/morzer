package exec_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/infra/exec"
)

// Command.Stdout is the raw byte path, added for output that is not text: a
// volume's contents arrive as a tar stream, and the line scanner would both
// corrupt it and hold the whole thing in memory to do it.

func TestStdoutReceivesBytesTheLineScannerWouldHaveMangled(t *testing.T) {
	var out bytes.Buffer

	// Bytes a scanner would eat: no trailing newline, an embedded NUL, and
	// 0x0a bytes that are data rather than line endings. A tar stream is
	// full of all three.
	_, err := exec.New().Run(context.Background(), exec.Command{
		Argv:   []string{"printf", `a\nb\0c`},
		Stdout: &out,
	})
	require.NoError(t, err)

	assert.Equal(t, "a\nb\x00c", out.String(),
		"the raw path went through the line scanner, so a tar stream would "+
			"come out re-terminated and NUL-stripped")
}

func TestStdoutIsNotAlsoCapturedIntoTheResult(t *testing.T) {
	var out bytes.Buffer

	res, err := exec.New().Run(context.Background(), exec.Command{
		Argv:          []string{"printf", "payload"},
		Stdout:        &out,
		CaptureOutput: true,
	})
	require.NoError(t, err)

	assert.Equal(t, "payload", out.String())
	assert.Empty(t, res.Stdout,
		"the stream was buffered in the Result as well, which for a volume "+
			"capture means holding the whole volume in memory")
}

// Stderr keeps working, because a command that fails while streaming still has
// to say why.
func TestStderrIsStillReportedWhileStdoutStreams(t *testing.T) {
	var out bytes.Buffer

	_, err := exec.New().Run(context.Background(), exec.Command{
		Argv:   []string{"sh", "-c", "printf data; echo 'it went wrong' >&2; exit 3"},
		Stdout: &out,
	})

	require.Error(t, err)
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr))
	assert.Equal(t, 3, exitErr.ExitCode)
	assert.Contains(t, exitErr.Stderr, "it went wrong")
}

// failingWriter is a disk that fills up partway through.
type failingWriter struct {
	written int
	limit   int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.written += len(p)
	if w.written > w.limit {
		return 0, errors.New("no space left on device")
	}
	return len(p), nil
}

// A process that succeeded while its output could not be stored is a failure.
// Reporting it as success is how a full disk produces a truncated volume
// tarball that verifies against a checksum taken of the truncation.
func TestAWriteFailureFailsTheCommandThatSucceeded(t *testing.T) {
	w := &failingWriter{limit: 16}

	_, err := exec.New().Run(context.Background(), exec.Command{
		// Enough output to outrun the limit several times over.
		Argv:   []string{"sh", "-c", "yes morzer | head -c 200000"},
		Stdout: w,
	})

	require.Error(t, err, "the command reported success while its output was lost")
	assert.Contains(t, err.Error(), "cannot store the output")

	// And it stopped rather than reading the rest into nothing: the child
	// gets a closed pipe, so a failure at byte 16 does not read the
	// remaining two hundred kilobytes -- or, for a volume, the remaining
	// hundred gigabytes.
	assert.Less(t, w.written, 200000,
		"the whole stream was consumed after the write had already failed")
}

func TestRedactionStillAppliesToTheScannedPath(t *testing.T) {
	res, err := exec.New().Run(context.Background(), exec.Command{
		Argv:          []string{"sh", "-c", "echo 'token=hunter2'"},
		CaptureOutput: true,
		Redact:        []string{"hunter2"},
	})
	require.NoError(t, err)

	assert.NotContains(t, res.Stdout, "hunter2")
	assert.True(t, strings.Contains(res.Stdout, "token="),
		"redaction removed more than the secret")
}
