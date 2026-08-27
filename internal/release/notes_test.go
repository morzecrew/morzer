package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

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

// TestNotesCannotDriveTheTerminalOrTheClipboard.
//
// The notes are the vendor's bytes and every path out of `Notes` ends somewhere
// that interprets them: stderr on `update --check` and `update --stage`, and a
// notification body through NotesSummary. A bundle that ships an OSC 52 in its
// RELEASE.md would otherwise write the operator's clipboard the moment they
// asked what an update changes.
//
// Carriage return and backspace are in here for the reason that is easy to miss:
// they are not escape sequences, so an ANSI stripper leaves them, and either one
// lets a vendor overwrite a line the operator has already read.
func TestNotesCannotDriveTheTerminalOrTheClipboard(t *testing.T) {
	// wantGone is the sequence's payload, not just its ESC. Asserting only
	// that no control rune survives is too weak: dropping the ESC alone
	// satisfies it and leaves `]52;c;cHduZWQ=` on the operator's screen as
	// text. That version of this test passed against a stripper that had
	// been removed, which is how this column got here.
	for _, tc := range []struct {
		name, body, wantKept, wantGone string
	}{
		{"OSC 52 clipboard write", "\x1b]52;c;cHduZWQ=\x07keep", "keep", "cHduZWQ="},
		{"OSC window title, BEL-terminated", "\x1b]0;owned\x07keep", "keep", "owned"},
		{"OSC window title, ST-terminated", "\x1b]0;owned\x1b\\keep", "keep", "owned"},
		{"SGR colour", "\x1b[31mkeep\x1b[0m", "keep", "31m"},
		{"cursor movement", "\x1b[2Akeep", "keep", "[2A"},
		{"DCS", "\x1bP0;1|x\x1b\\keep", "keep", "0;1|x"},
		{"carriage return overwrite", "hidden\rkeep", "keep", ""},
		{"backspace overwrite", "x\bkeep", "keep", ""},
		{"bell", "\x07keep", "keep", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rel := releaseAt(t, map[string]string{"RELEASE.md": tc.body})
			rel.Manifest.Metadata.ReleaseNotes = "RELEASE.md"

			got := release.Notes(rel)

			if strings.ContainsFunc(got, func(r rune) bool {
				return r != '\n' && r != '\t' && unicode.IsControl(r)
			}) {
				t.Errorf("a control character survived into what reaches a terminal: %q", got)
			}
			if !strings.Contains(got, tc.wantKept) {
				t.Errorf("the visible text was lost: %q", got)
			}
			if tc.wantGone != "" && strings.Contains(got, tc.wantGone) {
				t.Errorf("the sequence lost its ESC but kept its payload %q: %q",
					tc.wantGone, got)
			}
		})
	}
}

// TestNotesKeepEveryMarkdownConstruct. The filter above must not be a renderer:
// what a vendor wrote is what an operator reads, and the fenced command in here
// is the one they copy.
func TestNotesKeepEveryMarkdownConstruct(t *testing.T) {
	body := "# demo 1.4.0\n\n- one\n- two\n\n```sh\nmorzer apply --wait-for-health\n```\n\n" +
		"| setting | old | new |\n| --- | --- | --- |\n| pool | 10 | 40 |\n\n" +
		"See https://example.com/a/b?c=d&e=f\n\n\tindented\n"
	rel := releaseAt(t, map[string]string{"RELEASE.md": body})
	rel.Manifest.Metadata.ReleaseNotes = "RELEASE.md"

	if got := release.Notes(rel); got != body {
		t.Errorf("the notes were rewritten:\nwant %q\ngot  %q", body, got)
	}
}
