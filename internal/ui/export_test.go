package ui

import (
	"os"
	"testing"
)

// StubTerminalSize makes every file look like a terminal of the given width,
// for the duration of one test.
//
// The seam exists because the rule under test is *which* stream is asked, and a
// `go test` process has no terminal on any of its streams — so without it the
// correct implementation and one that probes stderr regardless of the
// destination are indistinguishable.
func StubTerminalSize(t *testing.T, width int) {
	t.Helper()

	previous := terminalSize
	terminalSize = func(*os.File) (int, bool) { return width, true }
	t.Cleanup(func() { terminalSize = previous })
}
