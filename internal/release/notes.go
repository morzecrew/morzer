package release

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
)

// maxNotesBytes bounds what is read.
//
// Release notes are vendor-supplied text that reaches a terminal, a
// notification body and an operator's screen. A bundle shipping a gigabyte of
// them should render nothing rather than exhaust the machine deciding whether to
// install it -- and 256 KiB is around forty pages, which is more changelog than
// anyone reads before accepting downtime.
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
	name := rel.Manifest.Metadata.ReleaseNotes
	if name == "" {
		// The convention, layered under the declaration: a bundle that
		// ships RELEASE.md without declaring it still has notes worth
		// reading, and `release new` declares it precisely so this
		// fallback is not the only path.
		name = ReleaseNotesFileName
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

	raw, err := io.ReadAll(io.LimitReader(f, maxNotesBytes))
	if err != nil {
		return ""
	}
	return string(raw)
}

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
		return line
	}
	return ""
}
