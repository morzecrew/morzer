package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The top-level help is the only documentation many operators read, so these
// assert the two properties that make it readable: everything is in a section,
// and no line in those sections wraps.

// generated are the commands cobra adds for itself. They belong in the
// ungrouped section at the bottom and this project does not document them --
// the same two names docs-check excludes from command coverage.
var generated = map[string]bool{"help": true, "completion": true}

// TestEveryCommandIsGrouped.
//
// Cobra has no compile-time hook for this: a command registered without a
// GroupID is not an error, it is silently placed under "Additional Commands" at
// the bottom, beside the generated ones. That is a reasonable place for
// `completion` and the wrong place for anything this project ships, so the
// guarantee is a test rather than a type.
func TestEveryCommandIsGrouped(t *testing.T) {
	root := CommandTree()

	declared := map[string]bool{}
	for _, g := range root.Groups() {
		declared[g.ID] = true
	}
	if len(declared) == 0 {
		t.Fatal("the root command declares no groups at all")
	}

	for _, cmd := range root.Commands() {
		name := strings.Fields(cmd.Use)[0]
		if generated[name] || cmd.Hidden {
			continue
		}
		switch {
		case cmd.GroupID == "":
			t.Errorf("`morzer %s` has no group, so it renders under "+
				"\"Additional Commands\" beside cobra's own", name)
		case !declared[cmd.GroupID]:
			t.Errorf("`morzer %s` names group %q, which the root does not declare",
				name, cmd.GroupID)
		}
	}
}

// TestEveryGroupIsUsed.
//
// The other direction, and the one that rots quietly: a group whose last
// command moved elsewhere renders as a heading with nothing under it, and cobra
// prints it happily.
func TestEveryGroupIsUsed(t *testing.T) {
	root := CommandTree()

	used := map[string]bool{}
	for _, cmd := range root.Commands() {
		used[cmd.GroupID] = true
	}
	for _, g := range root.Groups() {
		if !used[g.ID] {
			t.Errorf("group %q (%q) has no commands in it", g.ID, g.Title)
		}
	}
}

// TestHelpLinesFitEightyColumns.
//
// The listing is `  <name padded> <short>`, and a Short long enough to wrap
// turns the section into a paragraph -- which is the readability this whole
// grouping exists for, undone one command at a time. Eighty because that is the
// width the rest of this project treats as the terminal's floor (`RenderNotes`
// wraps there, and it says why).
func TestHelpLinesFitEightyColumns(t *testing.T) {
	const limit = 80

	var walk func(cmd *cobra.Command, path string)
	walk = func(cmd *cobra.Command, path string) {
		width := 0
		for _, sub := range cmd.Commands() {
			if n := len(strings.Fields(sub.Use)[0]); n > width {
				width = n
			}
		}
		for _, sub := range cmd.Commands() {
			name := strings.Fields(sub.Use)[0]
			if sub.Hidden {
				continue
			}
			// Two spaces of indent, the padded name, one space.
			line := 2 + width + 1 + len(sub.Short)
			if line > limit {
				t.Errorf("`%s%s` renders %d columns wide; its Short needs %d fewer:\n  %s",
					path, name, line, line-limit, sub.Short)
			}
			if sub.Short == "" {
				t.Errorf("`%s%s` has no Short, so it is a blank line in the listing",
					path, name)
			}
			walk(sub, path+name+" ")
		}
	}
	walk(CommandTree(), "morzer ")
}
