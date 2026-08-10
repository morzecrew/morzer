package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/release"
)

func releaseAt(t *testing.T, files map[string]string) domain.Release {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rel := domain.Release{Root: root}
	rel.Manifest.Metadata.Name = "demo"
	return rel
}

// TestNotesAreReadByDeclarationAndNotByConvention.
//
// `Metadata.ReleaseNotes` says there is deliberately no fallback to finding
// RELEASE.md, and this is that sentence as a test. An undeclared file is one
// nothing validates and `release verify` never checks the existence of, so
// reading it would put unvalidated bundle content in front of an operator
// deciding on an update -- and would make a typo'd declaration succeed against
// the wrong file rather than fail.
//
// The second half is why the first costs nothing: `release new` writes
// RELEASE.md *and* declares it.
func TestNotesAreReadByDeclarationAndNotByConvention(t *testing.T) {
	declared := releaseAt(t, map[string]string{"CHANGES.md": "declared\n"})
	declared.Manifest.Metadata.ReleaseNotes = "CHANGES.md"
	if got := release.Notes(declared); !strings.Contains(got, "declared") {
		t.Errorf("the declared notes file was not read: %q", got)
	}

	undeclared := releaseAt(t, map[string]string{"RELEASE.md": "by convention\n"})
	if got := release.Notes(undeclared); got != "" {
		t.Errorf("an undeclared RELEASE.md was read as release notes: %q", got)
	}
}

// TestABundleWithoutNotesIsNotAFailure.
//
// Nothing about a release requires notes. The caller prints what comes back, so
// "" is what keeps an empty section out of the output rather than an error out
// of an update check.
func TestABundleWithoutNotesIsNotAFailure(t *testing.T) {
	if got := release.Notes(releaseAt(t, nil)); got != "" {
		t.Errorf("a bundle with no notes produced %q", got)
	}
}

// TestNotesAreReadThroughTheReleaseRoot.
//
// A bundle is untrusted input. A notes path that is a symlink out of the bundle
// must fail to open rather than be printed to the operator and posted to their
// chat channel -- which is where the first line of this goes.
func TestNotesAreReadThroughTheReleaseRoot(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "host-secret")
	if err := os.WriteFile(secret, []byte("not yours\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rel := releaseAt(t, map[string]string{"placeholder": "x"})
	rel.Manifest.Metadata.ReleaseNotes = "RELEASE.md"
	if err := os.Symlink(secret, filepath.Join(rel.Root, "RELEASE.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if got := release.Notes(rel); got != "" {
		t.Errorf("a symlink out of the bundle was read as release notes: %q", got)
	}
}

// TestNotesAreBoundedAndSayWhereTheyWereCut.
//
// Vendor-supplied text that reaches a terminal, a notification body and an
// operator's screen, so a bundle shipping a gigabyte of it must not be able to
// exhaust the machine deciding whether to install it.
//
// The second assertion is the one worth having. A bound that silently returns
// the first 256 KiB hands an operator a changelog that stops mid-sentence and
// looks complete -- they cannot tell the difference between "that is all the
// vendor wrote" and "the rest was dropped", and the decision they are making is
// whether to accept downtime.
func TestNotesAreBoundedAndSayWhereTheyWereCut(t *testing.T) {
	huge := strings.Repeat("a", 2*256*1024)
	rel := releaseAt(t, map[string]string{"RELEASE.md": huge})
	rel.Manifest.Metadata.ReleaseNotes = "RELEASE.md"

	got := release.Notes(rel)
	if len(got) >= len(huge) {
		t.Errorf("read %d bytes of notes; nothing bounded the file", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("the notes were cut without saying so:\n%q", got[max(0, len(got)-120):])
	}

	// The bound is not a trap for an ordinary file: one byte under it is
	// returned whole and unannotated.
	exact := releaseAt(t, map[string]string{"RELEASE.md": strings.Repeat("b", 256*1024)})
	exact.Manifest.Metadata.ReleaseNotes = "RELEASE.md"
	if got := release.Notes(exact); strings.Contains(got, "truncated") {
		t.Errorf("a file exactly at the bound was reported as truncated")
	}
}

// TestTheSummarySkipsHeadingsAndIsShortEnoughToSend.
//
// It goes into a notification body. `# demo 1.4.0` is the version, which the
// message has already said; what the reader wants is the sentence underneath.
// And a vendor's whole changelog on one line is a chat message nobody can read.
func TestTheSummarySkipsHeadingsAndIsShortEnoughToSend(t *testing.T) {
	got := release.NotesSummary("# demo 1.4.0\n\n## What changed\n\nFixes the crash.\n")
	if got != "Fixes the crash." {
		t.Errorf("summary is %q, want the first line that is not a heading", got)
	}

	long := release.NotesSummary(strings.Repeat("word ", 500))
	if len([]rune(long)) > 220 {
		t.Errorf("the summary is %d runes; a notification body is not a changelog",
			len([]rune(long)))
	}

	if got := release.NotesSummary("# only a heading\n"); got != "" {
		t.Errorf("a file with nothing but headings summarised as %q", got)
	}
}
