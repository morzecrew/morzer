// Package release drives `.github/scripts/release-notes.sh` the way the
// release workflow does.
//
// The script is shell, so it is tested by running it — against fixture
// changelogs rather than the repository's own, so a test does not start
// failing the day somebody cuts a version.
//
// What it replaced published two bad releases. v0.1.0's notes were every
// commit in the repository, because there was no previous tag to diff against.
// v0.1.1's were one subject line and a SHA. Neither failed anything: goreleaser
// generated notes successfully both times, and "successfully" is the word that
// matters — nothing in the pipeline had an opinion about whether the notes said
// anything. These tests are that opinion.
package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func script(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", ".github", "scripts", "release-notes.sh"))
	if err != nil {
		t.Fatalf("resolving the script: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the script is not where the workflow expects it: %v", err)
	}
	return path
}

// run invokes the script against a changelog written for this test.
func run(t *testing.T, changelog, tag string) (stdout, stderr string, code int) {
	t.Helper()

	dir := t.TempDir()
	file := filepath.Join(dir, "CHANGELOG.md")
	if err := os.WriteFile(file, []byte(changelog), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	cmd := exec.Command(script(t), tag)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CHANGELOG_FILE="+file)

	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()

	code = 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("running the script: %v", err)
	}
	return out.String(), errb.String(), code
}

// A changelog shaped like the real one: an Unreleased section on top, then
// versions newest first, with the link references at the bottom.
const changelog = `# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

## [1.2.0] - 2026-03-01

The paragraph a version section opens with.

### Added

- **A thing.** With a sentence about it.

### Fixed

- **Another thing.**

## [1.1.0] - 2026-02-01

### Fixed

- **The older release's entry.**

[unreleased]: https://example.invalid/compare/v1.2.0...HEAD
[1.2.0]: https://example.invalid/compare/v1.1.0...v1.2.0
`

func TestTheNotesAreTheVersionsOwnSection(t *testing.T) {
	out, _, code := run(t, changelog, "v1.2.0")
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}

	for _, want := range []string{
		"The paragraph a version section opens with.",
		"**A thing.** With a sentence about it.",
		"**Another thing.**",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the notes do not carry %q:\n%s", want, out)
		}
	}
}

// The bug that made this script necessary was notes carrying things that
// belong to another release, so the boundaries get their own test rather than
// being implied by the one above.
func TestTheNotesStopAtTheNextVersion(t *testing.T) {
	out, _, code := run(t, changelog, "v1.2.0")
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}

	if strings.Contains(out, "The older release's entry.") {
		t.Errorf("1.1.0's entry reached 1.2.0's notes:\n%s", out)
	}
	if strings.Contains(out, "## [1.1.0]") || strings.Contains(out, "## [1.2.0]") {
		t.Errorf("a version heading reached the notes:\n%s", out)
	}
	if strings.Contains(out, "[unreleased]: https://") {
		t.Errorf("the link references reached the notes:\n%s", out)
	}
	if strings.Contains(out, "All notable changes") {
		t.Errorf("the file's preamble reached the notes:\n%s", out)
	}
}

// Unreleased sits directly above the newest version, so an off-by-one in the
// heading match takes the whole file — including everything not yet released.
func TestUnreleasedIsNotAVersion(t *testing.T) {
	withPending := strings.Replace(changelog,
		"## [Unreleased]\n",
		"## [Unreleased]\n\n### Added\n\n- **Not shipped yet.**\n",
		1)

	out, _, code := run(t, withPending, "v1.2.0")
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if strings.Contains(out, "Not shipped yet.") {
		t.Errorf("an unreleased entry reached the notes:\n%s", out)
	}
}

// The heading has to start the line, not merely appear on it.
//
// Found by sabotage: relaxing the match from "starts with" to "contains"
// passed every other test here, because no fixture mentioned a heading inside
// an entry. Prose does — a changelog is a document about releases, and this
// project's own entries quote things like `## [Unreleased]` — and a mention
// sitting *above* the real heading starts the extraction early, so the notes
// open with somebody else's paragraph and the heading line lands in the middle.
func TestAHeadingMentionedInProseIsNotTheHeading(t *testing.T) {
	quoted := strings.Replace(changelog,
		"## [Unreleased]\n",
		"## [Unreleased]\n\n- Nothing here shipped; it moves to `## [1.2.0]` at the cut.\n"+
			"- A second unreleased entry, which is what the mention drags in.\n",
		1)

	out, _, code := run(t, quoted, "v1.2.0")
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if strings.Contains(out, "A second unreleased entry") {
		t.Errorf("extraction started at a mention rather than the heading:\n%s", out)
	}
	if strings.Contains(out, "## [1.2.0] -") {
		t.Errorf("the real heading landed inside the notes:\n%s", out)
	}
}

// The whole point: a release whose notes would be empty does not happen.
func TestAVersionWithNoSectionIsRefused(t *testing.T) {
	_, errOut, code := run(t, changelog, "v9.9.9")
	if code == 0 {
		t.Fatal("a tag with no changelog section produced notes and exit 0")
	}
	if !strings.Contains(errOut, "no entries under '## [9.9.9]'") {
		t.Errorf("the refusal does not name what is missing:\n%s", errOut)
	}
	// An error that lists what the file does have is the difference between
	// re-running and reading the script.
	if !strings.Contains(errOut, "## [1.2.0]") {
		t.Errorf("the refusal does not name the versions that exist:\n%s", errOut)
	}
}

// A heading with nothing under it is the same failure wearing a section
// heading, and it is what a half-finished cut leaves behind.
func TestAnEmptySectionIsRefused(t *testing.T) {
	empty := "# Changelog\n\n## [2.0.0] - 2026-04-01\n\n## [1.2.0] - 2026-03-01\n\n- kept\n"

	_, errOut, code := run(t, empty, "v2.0.0")
	if code == 0 {
		t.Fatal("an empty section produced notes and exit 0")
	}
	if !strings.Contains(errOut, "no entries under '## [2.0.0]'") {
		t.Errorf("the refusal does not name what is missing:\n%s", errOut)
	}
}

// The footer moved out of `.goreleaser.yaml` because `--release-notes` drops
// it, and it carries the version in three places. A release whose install
// command names the previous version is worse than one with no footer.
func TestTheFooterNamesThisVersion(t *testing.T) {
	out, _, code := run(t, changelog, "v1.2.0")
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}

	for _, want := range []string{
		"sh -s -- --version 1.2.0",
		"ARCHIVE=morzer_1.2.0_linux_amd64.tar.zst",
		"minisign -Vm SHA256SUMS -p morzer.pub",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the footer does not carry %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "{{") {
		t.Errorf("an untemplated goreleaser variable reached the notes:\n%s", out)
	}
}

// `${ARCHIVE}` and `$SHA256SUMS` are shell in the *reader's* terminal, not in
// the script writing the notes. A heredoc that expanded them would publish a
// verification recipe with the variable already gone.
func TestTheVerificationRecipeSurvivesTheHeredoc(t *testing.T) {
	out, _, code := run(t, changelog, "v1.2.0")
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}

	const line = "grep \" ${ARCHIVE}$\" SHA256SUMS | sha256sum -c -"
	if !strings.Contains(out, line) {
		t.Errorf("the verification line was mangled, want %q:\n%s", line, out)
	}
}

// The tag is what the workflow has; the changelog is written in versions.
func TestTheTagsPrefixIsNotPartOfTheVersion(t *testing.T) {
	withV := strings.Replace(changelog, "## [1.2.0] -", "## [v1.2.0] -", 1)

	_, _, code := run(t, withV, "v1.2.0")
	if code == 0 {
		t.Error("a changelog heading spelled `[v1.2.0]` was accepted; " +
			"Keep a Changelog headings carry the version, not the tag")
	}
}
