package ops

import (
	"fmt"
	"strings"
)

// unifiedDiff renders a small unified diff for the plan view.
//
// It is hand-rolled rather than pulled from a dependency because the only
// consumer is `--dry-run` showing what a configuration file would become, and
// these files are tens of lines. A full LCS implementation would be more code
// than the feature warrants; this reports the changed region, which is what an
// operator is looking at the plan to see.
func unifiedDiff(name, before, after string) string {
	if before == after {
		return ""
	}

	oldLines := splitLines(before)
	newLines := splitLines(after)

	// Trim the common prefix and suffix so the diff shows the change
	// rather than the file.
	start := 0
	for start < len(oldLines) && start < len(newLines) && oldLines[start] == newLines[start] {
		start++
	}
	endOld, endNew := len(oldLines), len(newLines)
	for endOld > start && endNew > start && oldLines[endOld-1] == newLines[endNew-1] {
		endOld--
		endNew--
	}

	const context = 2
	ctxStart := max(0, start-context)

	var b strings.Builder
	if before == "" {
		fmt.Fprintf(&b, "--- %s (new file)\n", name)
	} else {
		fmt.Fprintf(&b, "--- %s\n", name)
	}
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n",
		ctxStart+1, endOld-ctxStart, ctxStart+1, endNew-ctxStart)

	for i := ctxStart; i < start; i++ {
		fmt.Fprintf(&b, "  %s\n", oldLines[i])
	}
	for i := start; i < endOld; i++ {
		fmt.Fprintf(&b, "- %s\n", oldLines[i])
	}
	for i := start; i < endNew; i++ {
		fmt.Fprintf(&b, "+ %s\n", newLines[i])
	}
	for i := endOld; i < min(len(oldLines), endOld+context); i++ {
		fmt.Fprintf(&b, "  %s\n", oldLines[i])
	}

	return b.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
