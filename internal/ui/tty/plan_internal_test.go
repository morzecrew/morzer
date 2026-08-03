package tty

import "testing"

// TestDiffHeadersAreNotMistakenForChanges is the ordering bug this classifier
// is prone to: "---" and "+++" start with the same characters as a removal and
// an addition, and getting it wrong makes every diff look like it deletes one
// file and adds another.
//
// Tested at the classifier rather than through the rendered bytes, because
// lipgloss strips colour when it cannot see a terminal -- so an assertion over
// the output would be testing the renderer's environment detection, and would
// pass whatever this function returned.
func TestDiffHeadersAreNotMistakenForChanges(t *testing.T) {
	for line, want := range map[string]diffLineKind{
		"--- a/app.env":   diffFileHeader,
		"+++ b/app.env":   diffFileHeader,
		"@@ -1,3 +1,3 @@": diffHunkHeader,
		"-WORKERS=2":      diffRemoved,
		"+WORKERS=4":      diffAdded,
		" LOG_LEVEL=info": diffContext,
		"":                diffContext,
		// A removal that happens to be three dashes of content. The
		// prefix rule cannot tell it from a header, and a YAML document
		// separator is exactly that. Pinned so the behaviour is a
		// recorded limit rather than a surprise.
		"---": diffFileHeader,
	} {
		if got := classifyDiffLine(line); got != want {
			t.Errorf("classifyDiffLine(%q) = %d, want %d", line, got, want)
		}
	}
}
