package main

import (
	"os"
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

// TestTheGeneratedPageDoesNotCountAsCoverage.
//
// The index names every command by construction. If it counted as prose, the
// coverage check would be satisfied for every command there will ever be — and
// it would go on passing while the hand-written pages it exists to gate said
// nothing at all. A generated page that disarms the gate it was added beside is
// worse than no page.
//
// Asserted by taking the hand-written pages away: with only the generated index
// left, every command must be reported as unmentioned.
func TestTheGeneratedPageDoesNotCountAsCoverage(t *testing.T) {
	var index page
	for _, p := range loadDocs(t) {
		if p.Rel == indexPage {
			index = p
		}
	}
	if index.Rel == "" {
		t.Fatalf("%s is missing, so this proves nothing", indexPage)
	}
	if !strings.Contains(index.Prose, "release prune") {
		t.Fatal("the generated page does not name the commands, so this proves nothing")
	}

	var rep report
	checkCommands(&rep, []page{index})

	if !rep.failed() {
		t.Fatal("with only the generated index, every command still counted as documented")
	}
	var reported int
	for _, item := range rep.problems["commands"] {
		if strings.Contains(item, "is not mentioned by any page") {
			reported++
		}
	}
	if reported < 50 {
		t.Errorf("only %d command(s) reported as unmentioned; the generated page is "+
			"being counted as documentation", reported)
	}
}

// TestASubcommandPointsAtTheSectionThatDocumentsIt.
//
// The anchor is the most specific heading that exists for the command or one of
// its ancestors, and "most specific" is the whole value: a page-title anchor
// would resolve, would pass every other assertion here, and would send a reader
// to the top of a five-hundred-line page.
func TestASubcommandPointsAtTheSectionThatDocumentsIt(t *testing.T) {
	entries, _ := commandEntries(cli.CommandTree(), loadDocs(t))

	anchors := map[string]string{}
	for _, e := range entries {
		anchors[e.Path] = e.Anchor
	}

	for path, want := range map[string]string{
		// Its own section.
		"release prune": "release-prune",
		"secret recipients generate-recovery-key": "secret-recipients-generate-recovery-key",
		// No section of its own: the parent's, which documents it.
		"backup target add": "backup-target",
		// No heading anywhere for the noun, so the declared fragment.
		"config set": "changing-one-after-install",
	} {
		if got := anchors[path]; got != want {
			t.Errorf("`morzer %s` points at #%s, want #%s", path, got, want)
		}
	}
}

// TestTheParentNounsAreInTheIndexToo.
//
// `morzer release` and `morzer installation` are commands an operator types,
// and they are the two that declare no scope of their own — every child does,
// because the subtree holds commands of both kinds. Nothing but the delegating
// marker distinguishes them from cobra's `completion`, so this is the assertion
// that fails if that marker is ever read as "not ours".
func TestTheParentNounsAreInTheIndexToo(t *testing.T) {
	entries, _ := commandEntries(cli.CommandTree(), loadDocs(t))

	listed := map[string]bool{}
	for _, e := range entries {
		listed[e.Path] = true
	}
	for _, noun := range []string{"release", "installation", "secret", "backup", "config"} {
		if !listed[noun] {
			t.Errorf("`morzer %s` is missing from the index", noun)
		}
	}
}

// TestTheCommittedPageIsWhatTheTreeProducesNow is the drift gate, as a test.
//
// `just docs-check` runs it too, and that is what fails CI. Having it here as
// well means a contributor who adds a command and runs `go test` is told to
// regenerate at the moment they would otherwise push.
func TestTheCommittedPageIsWhatTheTreeProducesNow(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}

	var rep report
	checkCommandIndex(&rep, root, loadDocs(t))
	if rep.failed() {
		t.Errorf("%v — run `just docs-index`", rep.problems)
	}
}

func TestADriftedPageIsReported(t *testing.T) {
	pages := loadDocs(t)

	// A whole temporary tree, so the assertion is about the check and not
	// about the repository's own file.
	root := t.TempDir()
	dir := filepath.Join(root, docsDir, "reference")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Missing entirely: the case a contributor meets when they add the
	// generator's output to .gitignore by accident.
	var missing report
	checkCommandIndex(&missing, root, pages)
	if !missing.failed() {
		t.Error("an absent index passed the drift check")
	}

	// Present and stale, which is the case that matters: a command was
	// added and nobody regenerated.
	stale := filepath.Join(dir, "index.md")
	if err := os.WriteFile(stale, []byte("# Every command\n\nnothing here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var drifted report
	checkCommandIndex(&drifted, root, pages)
	if !drifted.failed() {
		t.Error("a stale index passed the drift check")
	}
	if !strings.Contains(strings.Join(drifted.problems["command index"], " "), "just docs-index") {
		t.Errorf("the failure does not name the remedy: %v", drifted.problems)
	}

	// And writing it makes the check pass, which is the pair: a gate whose
	// remedy does not satisfy it is a gate nobody can get past.
	if err := writeCommandIndex(root, pages); err != nil {
		t.Fatal(err)
	}
	var after report
	checkCommandIndex(&after, root, pages)
	if after.failed() {
		t.Errorf("the page the generator wrote does not satisfy the check: %v", after.problems)
	}
}
