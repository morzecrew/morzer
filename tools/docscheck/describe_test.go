package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The exclusion check is a detection branch: it only speaks when the published
// table and the code disagree, which on a healthy tree is never. So it is
// driven here against tables that must be objected to, rather than only against
// the real page where there is nothing to object to.
//
// The heading case is the one worth having. A parser that cannot find its table
// and reports nothing is indistinguishable from one that found a correct table,
// and renaming a heading is an ordinary editorial act.

// describeProblems writes a page into a fresh root and runs the check over it.
func describeProblems(t *testing.T, table string) []string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, docsDir, "reference")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# installation commands\n\n" + describeExclusionsAnchor + "\n\n" +
		"| Not in the document | Why |\n| --- | --- |\n" + table +
		"\nProse after the table.\n"
	path := filepath.Join(root, docsDir, filepath.FromSlash(describePage))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var rep report
	checkDescribeExclusions(&rep, root)
	return rep.problems["describe exclusions"]
}

// theRealTable is what the page carries when it agrees with the code. Built
// from the code rather than typed out, so this file does not become a fourth
// hand-maintained copy of the same list.
func theRealTable(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, name := range sorted(excludedSerialisedNames()) {
		b.WriteString("| `" + name + "` | A reason long enough to be a reason. |\n")
	}
	return b.String()
}

func TestTheRealExclusionTableIsAccepted(t *testing.T) {
	if got := describeProblems(t, theRealTable(t)); len(got) != 0 {
		t.Errorf("the table the code implies was rejected: %v", got)
	}
}

func TestAnExclusionMissingFromThePageIsReported(t *testing.T) {
	rows := strings.SplitAfter(theRealTable(t), "\n")
	dropped := strings.TrimSpace(strings.Trim(rows[0], "| \n"))
	got := describeProblems(t, strings.Join(rows[1:], ""))

	if len(got) != 1 || !strings.Contains(got[0], "does not list it") {
		t.Fatalf("dropping %s was not reported as missing: %v", dropped, got)
	}
}

func TestAPageListingSomethingNobodyExcludesIsReported(t *testing.T) {
	got := describeProblems(t, theRealTable(t)+"| `parameters` | Invented. |\n")

	if len(got) != 1 || !strings.Contains(got[0], "has moved on") {
		t.Fatalf("an invented exclusion was not reported: %v", got)
	}
}

// A renamed heading must fail loudly. Reporting nothing here would be the
// check passing because it looked nowhere.
func TestARenamedHeadingIsNotSilence(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, docsDir, "reference")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, docsDir, filepath.FromSlash(describePage))
	if err := os.WriteFile(path, []byte("# page\n\n#### Omissions\n\n| a | b |\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var rep report
	checkDescribeExclusions(&rep, root)
	got := rep.problems["describe exclusions"]

	if len(got) != 1 || !strings.Contains(got[0], "no table under") {
		t.Fatalf("a renamed heading was not reported: %v", got)
	}
}

func TestAnUnreadablePageIsReported(t *testing.T) {
	var rep report
	checkDescribeExclusions(&rep, t.TempDir())

	got := rep.problems["describe exclusions"]
	if len(got) != 1 || !strings.Contains(got[0], "cannot be read") {
		t.Fatalf("a missing page was not reported: %v", got)
	}
}
