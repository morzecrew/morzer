package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/cli"
)

// The skills check is what keeps an agent from being told to run a command that
// no longer exists, or sent to a heading that was renamed. Each way of reading a
// skill wrong passes silently, so each is pinned here.

func TestAnInvocationResolvesAgainstTheCommandTree(t *testing.T) {
	tree := cli.CommandTree()
	cases := map[string]string{
		"release new ./my-product --vendor example":  "",
		"release verify ./my-product --render-check": "",
		"release verify --signing-key=RWQ ./b":       "",
		"task run seed -- --size s":                  "",
		"release build ./b | tee log --not-a-flag":   "",
		"--version":                                "",
		"release verfy ./my-product":               "`morzer release verfy` is not a command",
		"release verify ./b --render-chek":         "`--render-chek` is not a flag of `morzer release verify`",
		"--json release verify ./b --render-check": "",
		"--config /etc/x.yaml release verify ./b":  "",
		"--jsn release verify ./b":                 "`--jsn` is not a flag of `morzer`",
	}
	for line, want := range cases {
		if got := resolveInvocation(tree, strings.Fields(line)); got != want {
			t.Errorf("morzer %s: got %q, want %q", line, got, want)
		}
	}
}

// The anchors the site generator writes, as the pages already link them.
func TestASlugIsTheAnchorTheSiteWrites(t *testing.T) {
	cases := map[string]string{
		"What `--render-check` checks, and what it does not":                        "what-render-check-checks-and-what-it-does-not",
		"`database_schema_produces` is what opts a release into unattended updates": "database_schema_produces-is-what-opts-a-release-into-unattended-updates",
		"release verify":    "release-verify",
		"The secret schema": "the-secret-schema",
	}
	for heading, want := range cases {
		if got := slug(heading); got != want {
			t.Errorf("slug(%q) = %q, want %q", heading, got, want)
		}
	}
}

func TestASkillLinkResolvesToAPageAndAHeading(t *testing.T) {
	root := t.TempDir()
	page := filepath.Join(root, docsDir, "reference", "manifest.md")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# Release manifest\n\n```sh\n# not a heading\n```\n\n## The secret schema\n"
	if err := os.WriteFile(page, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := map[string]bool{
		docsSite + "reference/manifest/":                   true,
		docsSite + "reference/manifest/#the-secret-schema": true,
		docsSite + "reference/manifest/#not-a-heading":     false,
		docsSite + "reference/manifest/#missing":           false,
		docsSite + "reference/hooks/":                      false,
	}
	for link, ok := range cases {
		var rep report
		checkSkillLinks(&rep, root, "SKILL.md", "[x]("+link+")")
		if got := len(rep.problems["skills"]) == 0; got != ok {
			t.Errorf("%s: resolved %v, want %v (%v)", link, got, ok, rep.problems["skills"])
		}
	}
}

// A command shown as prose names nothing, and one in code is checked whether it
// is in a fenced block or an inline span.
func TestOnlyCodeIsReadForCommands(t *testing.T) {
	raw := "the morzer frobnicate tool\n\n```sh\nmorzer release verfy ./b\n```\n\nthen `morzer frobnicate`\n"
	var rep report
	checkSkillCommands(&rep, cli.CommandTree(), "SKILL.md", raw)
	if got := len(rep.problems["skills"]); got != 2 {
		t.Errorf("got %d problems, want the fenced and the inline one: %v", got, rep.problems["skills"])
	}
}

func TestASkillIsNamedForItsDirectory(t *testing.T) {
	var rep report
	checkSkillName(&rep, "skills/a/SKILL.md", "a", "---\nname: a\n---\n")
	checkSkillName(&rep, "skills/b/SKILL.md", "b", "---\nname: a\n---\n")
	if got := len(rep.problems["skills"]); got != 1 {
		t.Errorf("got %d problems, want one for the mismatched name: %v", got, rep.problems["skills"])
	}
}
