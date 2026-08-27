package release

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/morzecrew/morzer/internal/domain"
)

// maxNotesBytes bounds what is read.
//
// Release notes are vendor-supplied text that reaches a terminal, a
// notification body and an operator's screen. A bundle shipping a gigabyte of
// them must not be able to exhaust the machine deciding whether to install it,
// so this much is read and the rest is refused with the cut said out loud --
// 256 KiB is around forty pages, which is more changelog than anyone reads
// before accepting downtime.
const maxNotesBytes = 256 * 1024

// Notes reads a release's notes, or "" when it ships none.
//
// A missing file is not an error: nothing about a release requires notes, and a
// bundle without them renders nothing, exactly as `release show` behaves. What
// *is* an error is a manifest declaring a notes file that is absent, and Load
// already refuses that -- so by the time anything calls this, a declaration and
// a file agree or the release never loaded.
//
// Read through the release root: a bundle is untrusted input, and a notes path
// that is a symlink to /etc/shadow must fail to open rather than be printed to
// the operator and posted to their Slack channel.
func Notes(rel domain.Release) string {
	// The declaration and nothing else. There is no fallback to finding
	// RELEASE.md by convention: `Metadata.ReleaseNotes` says so in the one
	// place a vendor reads about the field, and a convention layered under a
	// declaration reintroduces exactly the ambiguity the declaration
	// removes -- an undeclared file, which nothing validates and which
	// `release verify` never checks the existence of, printed to an operator
	// and posted to their chat channel as the vendor's release notes.
	//
	// `release new` scaffolds RELEASE.md *and* declares it, so a bundle
	// authored by this manager has notes without needing the fallback.
	name := rel.Manifest.Metadata.ReleaseNotes
	if name == "" {
		return ""
	}

	root, err := os.OpenRoot(rel.Root)
	if err != nil {
		return ""
	}
	defer func() { _ = root.Close() }()

	f, err := root.Open(filepath.ToSlash(filepath.Clean(name)))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	// One byte past the bound, so an oversized file is *known* to be
	// oversized rather than silently becoming a changelog that stops
	// mid-sentence. What the reader gets is the bound's worth plus a line
	// saying the rest was not read -- discarding it entirely would hide a
	// vendor's actual notes over their formatting, and saying nothing would
	// have an operator decide on text they cannot tell is incomplete.
	raw, err := io.ReadAll(io.LimitReader(f, maxNotesBytes+1))
	if err != nil {
		return ""
	}
	if len(raw) > maxNotesBytes {
		return stripTerminalControls(string(raw[:maxNotesBytes])) +
			"\n\n_(release notes truncated at 256 KiB)_\n"
	}
	return stripTerminalControls(string(raw))
}

// stripTerminalControls removes what a terminal acts on rather than prints.
//
// These bytes are the vendor's, and every path out of here ends somewhere that
// interprets them: `update --check` and `update --stage` write them to stderr,
// and NotesSummary puts the first line into a notification body that leaves the
// machine. A release note is prose, so nothing legitimate is lost -- and this
// project already withholds raw vendor output from notifiers for exactly this
// reason (ops.forwardedKinds refuses KindStepOutput), which the notes path was
// quietly bypassing.
//
// Two passes, because they catch different things. ansi.Strip removes whole
// escape sequences -- OSC (a window title, or OSC 52 writing the operator's
// clipboard), CSI, SGR, DCS -- which is what a naive control-rune filter cannot
// do: dropping the ESC alone leaves `]0;...` on screen as garbage. The rune pass
// then removes the bare C0 and C1 controls ansi.Strip leaves behind, where the
// ones that matter are CR and backspace: both let a vendor overwrite a line the
// operator has already read, which is how text that looked safe stops being what
// is there. Newline and tab are the two a note legitimately contains.
func stripTerminalControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}

// maxSummaryRunes bounds the one-liner.
//
// The file is already bounded, but a bound of 256 KiB is no bound at all for the
// place this goes: a notification body. Nothing stops a vendor writing their
// whole changelog as one line, and the first thing that happens to it is being
// posted to somebody's chat channel.
const maxSummaryRunes = 200

// NotesSummary is the first line worth reading, for somewhere a paragraph does
// not fit -- a notification body, a status line.
//
// Markdown headings are skipped rather than returned: `# demo 1.4.0` is the
// version, which whatever is displaying this has already said. What the reader
// wants is the first sentence underneath.
func NotesSummary(notes string) string {
	for line := range strings.Lines(notes) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return truncateRunes(line, maxSummaryRunes)
	}
	return ""
}

// truncateRunes cuts on a rune boundary, so a multi-byte character is never
// split into the mojibake that a byte-wise cut produces.
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}
