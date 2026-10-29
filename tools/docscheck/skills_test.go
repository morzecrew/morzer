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

// The name is read from the frontmatter alone: a `name:` in the body, in an
// example, says nothing about what the installer will call the skill.
func TestASkillIsNamedForItsDirectory(t *testing.T) {
	cases := map[string]bool{
		"---\nname: a\ndescription: d\n---\nbody\n":           true,
		"---\nname: b\n---\nbody\n":                           false,
		"no frontmatter\n\n```yaml\nname: a\n```\n":           false,
		"---\ndescription: d\n---\n\n```yaml\nname: a\n```\n": false,
	}
	for raw, ok := range cases {
		var rep report
		checkSkillName(&rep, "skills/a/SKILL.md", "a", raw)
		if got := len(rep.problems["skills"]) == 0; got != ok {
			t.Errorf("%q: passed %v, want %v", raw, got, ok)
		}
	}
}

// A directory under skills/ with no SKILL.md is one the installer cannot load.
func TestASkillDirectoryWithoutASkillFileIsReported(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, skillsDir, "half-written")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var rep report
	checkSkills(&rep, root)
	if len(rep.problems["skills"]) != 1 {
		t.Errorf("a directory with no SKILL.md passed: %v", rep.problems["skills"])
	}
}
