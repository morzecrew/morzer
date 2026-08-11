package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/cli"
)

// The generator's own tests. The drift check is what runs in CI, and it only
// says "regenerate"; these say what the generator is supposed to do, and one of
// them is the assertion that fails the day somebody adds a top-level command.

func loadDocs(t *testing.T) []page {
	t.Helper()

	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	pages, err := loadPages(filepath.Join(root, docsDir))
	if err != nil {
		t.Fatal(err)
	}
	return pages
}

// TestEveryCommandResolvesToARealSection is the check the whole page rests on.
//
// A row whose reference column pointed at a heading no page has would be an
// index that sends a reader nowhere — worse than no index, because they would
// stop looking. A new top-level command fails here until somebody decides which
// page documents it.
func TestEveryCommandResolvesToARealSection(t *testing.T) {
	pages := loadDocs(t)

	entries, problems := commandEntries(cli.CommandTree(), pages)
	for _, p := range problems {
		t.Errorf("%s", p)
	}
	if len(entries) == 0 {
		t.Fatal("the tree produced no commands at all")
	}

	anchors := map[string]map[string]bool{}
	for _, p := range pages {
		anchors[p.Rel] = anchorsIn(p.Prose)
	}
	for _, e := range entries {
		if !anchors[e.Page][e.Anchor] {
			t.Errorf("`morzer %s` points at %s#%s, which is not a heading",
				e.Path, e.Page, e.Anchor)
		}
	}
}

// TestTheIndexCoversCobrasOwnCommandsExactlyAsCoverageDoes.
//
// The two used to share a list of names, and a name check would have dropped
// `completion install` from both the moment it existed — the index missing a
// command *and* the coverage check no longer requiring it to be documented, in
// one silent step.
func TestTheIndexCoversCobrasOwnCommandsExactlyAsCoverageDoes(t *testing.T) {
	entries, _ := commandEntries(cli.CommandTree(), loadDocs(t))

	listed := map[string]bool{}
	for _, e := range entries {
		listed[e.Path] = true
	}

	if !listed["completion install"] {
		t.Error("`completion install` is this project's command and is not in the index")
	}
	for _, generated := range []string{
		"completion", "completion bash", "completion zsh",
		"completion fish", "completion powershell", "help",
	} {
		if listed[generated] {
			t.Errorf("`%s` is cobra's and should not be documented here", generated)
		}
	}
}

func TestACommandWithNoPageFailsRatherThanVanishing(t *testing.T) {
	// The failure has to be loud. An index that silently omitted a command
	// nobody had assigned a page to would be an index that agreed with
	// itself while the command went undocumented.
	root := cli.CommandTree()
	root.AddCommand(&cobra.Command{
		Use:         "teleport",
		Short:       "Move the deployment somewhere else",
		Run:         func(*cobra.Command, []string) {},
		Annotations: map[string]string{"morzer.scope": "machine"},
	})

	entries, problems := commandEntries(root, loadDocs(t))
	if len(problems) == 0 {
		t.Fatal("a command with no reference page was accepted")
	}
	if !strings.Contains(problems[0], "teleport") {
		t.Errorf("the problem does not name the command: %q", problems[0])
	}
	for _, e := range entries {
		if e.Path == "teleport" {
			t.Error("the unroutable command was written into the index anyway")
		}
	}
}

func TestSlugMatchesWhatAMarkdownRendererProduces(t *testing.T) {
	for heading, want := range map[string]string{
		"secret recipients generate-recovery-key": "secret-recipients-generate-recovery-key",
		"Changing one after install":              "changing-one-after-install",
		"`morzer secret`":                         "morzer-secret",
		"What it refuses":                         "what-it-refuses",
		"Entry order is part of the format":       "entry-order-is-part-of-the-format",
	} {
		if got := slug(heading); got != want {
			t.Errorf("slug(%q) = %q, want %q", heading, got, want)
		}
	}
}

func TestAliasesShareTheirCommandsRow(t *testing.T) {
	// `config ls` is not a second command, and a table that gave it a row
	// would report a surface larger than the one that exists.
	entries, _ := commandEntries(cli.CommandTree(), loadDocs(t))

	var found bool
	for _, e := range entries {
		if e.Path == "config list" {
			found = true
			if len(e.Aliases) == 0 {
				t.Error("`config list` lost its `ls` alias")
			}
		}
		if e.Path == "config ls" {
			t.Error("an alias was given a row of its own")
		}
	}
	if !found {
		t.Fatal("`config list` is missing, so this proves nothing")
	}

	rendered := renderIndex(entries)
	if !strings.Contains(rendered, "(also `ls`)") {
		t.Error("the alias is not named on its command's row")
	}
}
