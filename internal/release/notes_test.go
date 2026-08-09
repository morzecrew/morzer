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

// TestNotesAreReadByDeclarationAndByConvention.
//
// The declaration is what RFC 0013 added so a scaffolded RELEASE.md is pointed
// at rather than merely dropped beside the manifest. The convention still works,
// because a bundle that ships the file without declaring it has notes worth
// reading and nothing is gained by refusing to read them.
func TestNotesAreReadByDeclarationAndByConvention(t *testing.T) {
	declared := releaseAt(t, map[string]string{"CHANGES.md": "declared\n"})
	declared.Manifest.Metadata.ReleaseNotes = "CHANGES.md"
	if got := release.Notes(declared); !strings.Contains(got, "declared") {
		t.Errorf("the declared notes file was not read: %q", got)
	}

	conventional := releaseAt(t, map[string]string{"RELEASE.md": "by convention\n"})
	if got := release.Notes(conventional); !strings.Contains(got, "by convention") {
		t.Errorf("RELEASE.md was not read: %q", got)
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
	if err := os.Symlink(secret, filepath.Join(rel.Root, "RELEASE.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if got := release.Notes(rel); got != "" {
		t.Errorf("a symlink out of the bundle was read as release notes: %q", got)
	}
}

// TestNotesAreBounded.
//
// Vendor-supplied text that reaches a terminal, a notification body and an
// operator's screen. A bundle shipping a gigabyte of it should render nothing
// rather than exhaust the machine deciding whether to install it.
func TestNotesAreBounded(t *testing.T) {
	huge := strings.Repeat("a", 2*256*1024)
	rel := releaseAt(t, map[string]string{"RELEASE.md": huge})

	if got := len(release.Notes(rel)); got >= len(huge) {
		t.Errorf("read %d bytes of notes; nothing bounded the file", got)
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
