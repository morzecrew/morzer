package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The release-asset check reads the pipeline's own configuration, and every way
// of reading it wrong is silent: a pattern built from the wrong `name_template`
// or the wrong `formats` still reports something, just about names nobody
// publishes. These tests are what makes the reading itself checkable.

// withConfig writes a .goreleaser.yaml into a fresh root and returns it.
func withConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, goreleaserConfig), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// problems runs the check over one synthetic page and returns what it said.
func problems(t *testing.T, root, raw string) []string {
	t.Helper()
	var rep report
	checkReleaseAssets(&rep, root, []page{{Rel: "page.md", Raw: raw}})
	return rep.problems["release-assets"]
}

// TestTheArchiveTemplateIsTheOneUnderArchives.
//
// A goreleaser config carries `name_template` under `checksum`, `snapshot`,
// `release` and `nfpms` as well as under `archives` — this repository's already
// has a second one under `checksum`. Taking the first match in the file built
// the archive pattern out of whichever key happened to be written first, so the
// check's correctness rested on the order of two unrelated blocks.
func TestTheArchiveTemplateIsTheOneUnderArchives(t *testing.T) {
	root := withConfig(t, `
project_name: morzer

checksum:
  name_template: SHA256SUMS

archives:
  - formats: [tar.zst]
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
`)

	if got := problems(t, root, "morzer_1.0.0_linux_amd64.tar.zst"); len(got) != 0 {
		t.Errorf("the archive template rejected a name it produces: %v", got)
	}
	if got := problems(t, root, "morzer_linux_amd64.tar.zst"); len(got) != 1 {
		t.Errorf("a name with no version was not reported: %v", got)
	}
}

// TestFormatsAreReadInEitherYAMLShape.
//
// `formats: [tar.zst]` and a block sequence are the same declaration. Read by
// pattern, the block form matched nothing and fell through to a default — so a
// pipeline switched to `zip` would keep having its documentation validated
// against the extension it no longer builds.
func TestFormatsAreReadInEitherYAMLShape(t *testing.T) {
	root := withConfig(t, `
project_name: morzer
archives:
  - name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    formats:
      - zip
`)

	if got := problems(t, root, "morzer_1.0.0_linux_amd64.zip"); len(got) != 0 {
		t.Errorf("the declared format was rejected: %v", got)
	}
	got := problems(t, root, "morzer_1.0.0_linux_amd64.tar.zst")
	if len(got) != 1 {
		t.Fatalf("a format the pipeline no longer builds was accepted: %v", got)
	}
	if !strings.Contains(got[0], "tar.zst") {
		t.Errorf("the report does not name what it rejected: %q", got[0])
	}
}

// TestAnArchiveWithNoFormatsGetsGoreleasersDefault.
//
// Not this repository's. Defaulting to the extension that happens to be
// configured here would make a deleted `formats:` line invisible: the check
// would go on validating `.tar.zst` while the pipeline started emitting
// `.tar.gz`.
func TestAnArchiveWithNoFormatsGetsGoreleasersDefault(t *testing.T) {
	root := withConfig(t, `
project_name: morzer
archives:
  - name_template: "{{ .ProjectName }}_{{ .Os }}"
`)

	if got := problems(t, root, "morzer_linux.tar.gz"); len(got) != 0 {
		t.Errorf("goreleaser's default format was rejected: %v", got)
	}
	if got := problems(t, root, "morzer_linux.tar.zst"); len(got) != 1 {
		t.Errorf("this repository's format was accepted as the default: %v", got)
	}
}

// TestSeveralArchivesAreAllPublished. More than one archives entry is legal, and
// a name matching any of them is a name the pipeline produces.
func TestSeveralArchivesAreAllPublished(t *testing.T) {
	root := withConfig(t, `
project_name: morzer
archives:
  - id: unix
    name_template: "{{ .ProjectName }}_{{ .Os }}"
    formats: [tar.zst]
  - id: windows
    name_template: "{{ .ProjectName }}_{{ .Os }}"
    formats: [zip]
`)

	for _, name := range []string{"morzer_linux.tar.zst", "morzer_windows.zip"} {
		if got := problems(t, root, name); len(got) != 0 {
			t.Errorf("%s is published and was reported: %v", name, got)
		}
	}
}

// TestAConfigThisCheckCannotReadIsReported.
//
// Each of these leaves the check with no way to know what the pipeline names its
// archives, and the failure has to be loud: a check that quietly passes when it
// cannot do its job is worse than no check, because the next reader believes it.
func TestAConfigThisCheckCannotReadIsReported(t *testing.T) {
	for name, body := range map[string]string{
		"no project_name": "archives:\n  - name_template: x\n",
		"no archives":     "project_name: morzer\n",
		"no name_template": "project_name: morzer\n" +
			"archives:\n  - formats: [tar.zst]\n",
		"unknown template field": "project_name: morzer\n" +
			"archives:\n  - name_template: \"{{ .Runtime }}\"\n",
		"not yaml": "project_name: [morzer\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := problems(t, withConfig(t, body), "nothing here"); len(got) != 1 {
				t.Errorf("an unreadable config was not reported: %v", got)
			}
		})
	}

	t.Run("no config at all", func(t *testing.T) {
		if got := problems(t, t.TempDir(), "nothing here"); len(got) != 1 {
			t.Errorf("a missing config was not reported: %v", got)
		}
	})
}

// TestAnArchiveNameIsFoundWhateverShapeItIsIn.
//
// The half of this check that has to be loose. A name it does not recognise is a
// name it does not check, so the extensions it looks for are every ending
// goreleaser can produce rather than the one configured here — the wrong
// extension is exactly the drift worth reporting, and deriving the list from the
// configuration would make it invisible.
func TestAnArchiveNameIsFoundWhateverShapeItIsIn(t *testing.T) {
	root := withConfig(t, `
project_name: morzer
archives:
  - name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    formats: [tar.zst]
`)

	for _, name := range []string{
		"morzer_linux_amd64.zip",
		"morzer_linux_amd64.tar.gz",
		"morzer-1.0.0.tgz",
		"morzer_1.0.0_linux_amd64.tar.bz2",
		// A markdown link, where the name is followed immediately by
		// the target it links to. The greedy middle must stop at the
		// name rather than swallowing the URL and reporting the pair.
		"[morzer_linux_amd64.tar.zst](https://example.invalid/morzer_1.0.0_linux_amd64.tar.zst)",
	} {
		if got := problems(t, root, name); len(got) != 1 {
			t.Errorf("%s is not a name this pipeline produces and was not reported: %v", name, got)
		}
	}

	// The documented form: the instructions set VERSION once and build the
	// name from it, which is what keeps a runbook pinned to a release.
	for _, name := range []string{
		"morzer_${VERSION}_linux_amd64.tar.zst",
		"morzer_1.0.0_linux_arm64.tar.zst",
	} {
		if got := problems(t, root, name); len(got) != 0 {
			t.Errorf("%s is published and was reported: %v", name, got)
		}
	}
}

// TestAURLInRunningProseKeepsItsSentence.
//
// The asset capture stops at whitespace, so a URL at the end of a sentence
// carries the full stop into the name — and a name with a full stop on it
// matches neither the published list nor the template. The check would report
// drift on a page that is correct, which is the failure that gets a check turned
// off rather than fixed.
func TestAURLInRunningProseKeepsItsSentence(t *testing.T) {
	root := withConfig(t, `
project_name: morzer
archives:
  - name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    formats: [tar.zst]
`)

	const base = "https://github.com/morzecrew/morzer/releases/download/v1.0.0/"
	for _, raw := range []string{
		"Verify it against " + base + "SHA256SUMS.",
		"Fetch " + base + "SHA256SUMS, then check it.",
		"See <" + base + "morzer_1.0.0_linux_amd64.tar.zst>",
	} {
		if got := problems(t, root, raw); len(got) != 0 {
			t.Errorf("a correctly written page was reported as drift: %q -> %v", raw, got)
		}
	}

	// And the punctuation trimming does not swallow a real miss.
	raw := "Fetch " + base + "morzer_linux_amd64.tar.zst."
	if got := problems(t, root, raw); len(got) == 0 {
		t.Error("a name the pipeline cannot produce was accepted because it ended a sentence")
	}
}

// TestTheBaseURLIsNotAnAsset.
//
// The tag segment is required rather than optional in the download form.
// Optional, the expression swallows the base URL the documented instructions
// build a name onto and reports the tag itself as a missing asset — which is
// what the first draft of this file did.
func TestTheBaseURLIsNotAnAsset(t *testing.T) {
	root := withConfig(t, `
project_name: morzer
archives:
  - name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    formats: [tar.zst]
`)

	raw := "BASE=https://github.com/morzecrew/morzer/releases/download/v${VERSION}\n" +
		"curl -LO \"${BASE}/${ARCHIVE}\"\n"
	if got := problems(t, root, raw); len(got) != 0 {
		t.Errorf("a base URL was read as an asset: %v", got)
	}
}
