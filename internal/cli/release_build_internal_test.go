package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/ui"
)

// TestAnUnreadableSignatureIsNotTreatedAsAbsent.
//
// `os.Stat` fails for reasons other than absence, and reading every one of them
// as "there is no signature" makes this guard produce the artifact it exists to
// prevent: the build regenerates SHA256SUMS and leaves a signature that no
// longer covers it, without the --force refusal ever firing.
//
// Tested here rather than through the command, because `release.Load` digests
// the whole tree first and refuses the non-regular file this uses to provoke
// the error -- so the guard is unreachable from outside with a fixture like
// this one, and a test that went through the CLI would be asserting Load's
// behaviour instead.
func TestAnUnreadableSignatureIsNotTreatedAsAbsent(t *testing.T) {
	dir := t.TempDir()

	// A symlink loop: os.Stat resolves it and fails with ELOOP rather than
	// with "does not exist", which is exactly the distinction under test.
	signature := filepath.Join(dir, ports.SignatureFileName)
	if err := os.Symlink(signature, signature); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	if _, err := os.Stat(signature); err == nil {
		t.Fatal("the fixture resolved, so it does not provoke a stat error")
	}

	app := &App{Stream: ui.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}}
	err := clearStaleSignature(app, dir, false)
	if err == nil {
		t.Fatal("an unreadable signature was treated as no signature, so a build " +
			"would regenerate the checksum list and leave it in place")
	}
	if !strings.Contains(err.Error(), ports.SignatureFileName) {
		t.Errorf("the refusal does not name the file it could not read: %v", err)
	}

	// And --force does not paper over it: forcing means "discard a
	// signature I know about", not "proceed past a file I cannot read".
	if err := clearStaleSignature(app, dir, true); err == nil {
		t.Error("--force proceeded past a signature that could not be read")
	}
}

// TestAnAbsentSignatureIsStillAbsent is the other half: the common case must
// not become a refusal.
func TestAnAbsentSignatureIsStillAbsent(t *testing.T) {
	app := &App{Stream: ui.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}}

	if err := clearStaleSignature(app, t.TempDir(), false); err != nil {
		t.Fatalf("a bundle with no signature was refused: %v", err)
	}
}
