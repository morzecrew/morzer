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
		if IsGenerated(cmd) || cmd.Hidden {
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

// TestTheFirstSectionIsInTheOrderYouWouldRunIt.
//
// The order is the entire reason `cobra.EnableCommandSorting` is off, and
// nothing else notices when it comes back: cobra sorts, the help still renders,
// every other test still passes, and "Getting started" reads apply, init,
// status — converge before create.
//
// Asserted on the first group only. It is the one whose order carries a claim;
// pinning all five would turn every deliberate reordering into a test edit for
// no additional guarantee.
func TestTheFirstSectionIsInTheOrderYouWouldRunIt(t *testing.T) {
	want := []string{"init", "apply", "status"}

	var got []string
	for _, cmd := range CommandTree().Commands() {
		if cmd.GroupID == groupStart {
			got = append(got, strings.Fields(cmd.Use)[0])
		}
	}

	if len(got) != len(want) {
		t.Fatalf("the first section holds %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("the first section reads %v; it must read %v — "+
				"cobra.EnableCommandSorting is probably back on", got, want)
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

// TestEveryCommandExplainsItself is RFC 0019 §5.9 rule 2.
//
// `Short` fits a listing line; `Long` is where an operator finds out what the
// command refuses before they run it. A command with no Long shows its Short
// twice — once in the parent's listing and once as the whole of its own help —
// which is the shape that made `installation` read like a place to install
// something.
//
// The generated commands are exempt: cobra writes their help and this project
// does not document them.
func TestEveryCommandExplainsItself(t *testing.T) {
	root := CommandTree()
	if strings.TrimSpace(root.Long) == "" {
		t.Error("`morzer` itself has no Long, so the first page an operator " +
			"opens is a list with nothing above it")
	}

	var walk func(cmd *cobra.Command, path string)
	walk = func(cmd *cobra.Command, path string) {
		for _, sub := range cmd.Commands() {
			name := strings.Fields(sub.Use)[0]
			if sub.Hidden || name == "help" || name == "completion" {
				continue
			}
			if strings.TrimSpace(sub.Long) == "" {
				t.Errorf("`%s%s` has no Long, so its help is its Short repeated",
					path, name)
			}
			walk(sub, path+name+" ")
		}
	}
	walk(root, "morzer ")
}
