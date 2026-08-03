package tty

import (
	"fmt"
	"io"
	"strings"

	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// RenderPlan draws a dry run's step list with its configuration diffs.
//
// Not a Bubble Tea program, for the same reason the doctor table is not: a plan
// is computed and then printed. There is nothing to animate, nothing to own the
// terminal for, and a diff long enough to scroll is a diff the live renderer
// would fight with.
func RenderPlan(w io.Writer, t *theme.Theme, e events.Event, width int) {
	f := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }

	label := e.Description
	if label == "" {
		label = string(e.OpType)
	}
	f("")
	f("  %s", t.Bold("plan: "+label))
	f("")

	for i, step := range e.Plan {
		// The marker is the plan's whole point: which steps will
		// actually do something. A list that shows seven steps when two
		// will run is a list that overstates the change.
		symbol, style := t.Symbols.Active, t.Active
		if !step.WillRun {
			symbol, style = t.Symbols.Skipped, t.Dim
		}

		line := fmt.Sprintf("  %s [%d/%d] %s",
			style(symbol), i+1, len(e.Plan), step.Description)
		if step.Reason != "" {
			line += "  " + t.Dim("("+step.Reason+")")
		}
		f("%s", line)

		if step.Diff != "" {
			f("%s", renderDiff(t, step.Diff, width))
		}
	}

	f("")
	f("  %s", t.Dim("this is a plan; nothing was changed"))
}

// diffLineKind is what a unified-diff line is.
type diffLineKind int

const (
	diffContext diffLineKind = iota
	diffFileHeader
	diffHunkHeader
	diffAdded
	diffRemoved
)

// classifyDiffLine reads a line's prefix.
//
// Only the prefix is inspected. Parsing hunks to do better would mean this
// package understood a format internal/lifecycle/ops produces, and the value of
// that is a slightly prettier header.
//
// The order matters and is the bug this is prone to: "---" and "+++" start with
// the same characters as a removal and an addition, and classifying them as
// such makes every diff look like it deletes a file and adds another.
func classifyDiffLine(line string) diffLineKind {
	switch {
	case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		return diffFileHeader
	case strings.HasPrefix(line, "@@"):
		return diffHunkHeader
	case strings.HasPrefix(line, "+"):
		return diffAdded
	case strings.HasPrefix(line, "-"):
		return diffRemoved
	default:
		return diffContext
	}
}

// renderDiff colours a unified diff.
func renderDiff(t *theme.Theme, diff string, width int) string {
	style := map[diffLineKind]func(string) string{
		diffContext:    t.Dim,
		diffFileHeader: t.Dim,
		diffHunkHeader: t.Highlight,
		diffAdded:      t.Added,
		diffRemoved:    t.Removed,
	}

	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		text := truncate(line, max(width-6, 20))
		b.WriteString("      " + style[classifyDiffLine(line)](text) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
