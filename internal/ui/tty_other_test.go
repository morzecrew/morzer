//go:build !linux

package ui_test

import (
	"os"
	"testing"
)

// tty needs a pseudo-terminal, opened here only on Linux, where the suite runs.
func tty(t *testing.T) *os.File {
	t.Skip("no pseudo-terminal helper on this platform")
	return nil
}
