package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The release pipeline's own configuration, read rather than restated.
const goreleaserConfig = ".goreleaser.yaml"

// nonArchiveAssets are the published files that are not archives, so their
// names are not produced by the archive template.
//
// Listed rather than pattern-matched: the point of this check is that a name in
// the documentation matches something the pipeline emits, and a pattern loose
// enough to accept any extra file would accept the typo it exists to catch.
var nonArchiveAssets = map[string]bool{
	"SHA256SUMS":         true,
	"SHA256SUMS.minisig": true,
}

var (
	// The three values this check needs out of .goreleaser.yaml. A YAML
	// parser would be a dependency for a build-time tool that reads three
	// scalars; the nav check reads zensical.toml the same way.
	projectNameRe  = regexp.MustCompile(`(?m)^project_name:\s*(\S+)`)
	nameTemplateRe = regexp.MustCompile(
		`(?m)^\s*name_template:\s*["']?([^"'\n]+?)["']?\s*$`)
	formatsRe = regexp.MustCompile(`(?m)^\s*formats:\s*\[([^\]]+)\]`)

	// An archive named in the docs, whatever shape it is in. Deliberately
	// loose on the middle: this must match the *wrong* names too, because a
	// name it fails to recognise is a name it fails to check.
	archiveMentionRe = regexp.MustCompile(`\bmorzer[_-][^\s'"()\x60]*\.tar\.[a-z]+`)

	// A release URL that names an asset, in either of GitHub's two forms:
	// `/releases/download/<tag>/<asset>` and `/releases/latest/download/<asset>`.
	//
	// The tag segment is required in the first form rather than optional.
	// Optional, it swallows a *base* URL -- `…/releases/download/v${VERSION}`,
	// which the documented instructions build the asset onto -- and reports
	// the tag as an asset the pipeline does not publish. That is not a
	// hypothetical: it is what the first draft of this file did.
	releaseURLRe = regexp.MustCompile(
		`https://github\.com/morzecrew/morzer/releases/(?:latest/download|download/[^/\s]+)/([^\s'"()\x60]+)`)
)

// checkReleaseAssets fails when the documentation names a release asset the
// pipeline does not produce.
//
// This check exists because of a specific defect: every published installation
// instruction downloaded `morzer_linux_amd64.tar.zst`, and goreleaser's template
// is `{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}` -- so the URL was
// a 404 and nothing noticed. Link checking cannot: these are external URLs, and
// resolving them would make the build depend on GitHub being up and on a release
// having been cut. Comparing against the template needs neither.
//
// Unlike every other check here, it reads the raw page rather than its prose. A
// URL an operator copy-pastes lives inside a fenced code block by definition,
// and the fence-stripping that keeps an example from counting as documentation
// would hide the only thing this check is about.
func checkReleaseAssets(rep *report, root string, pages []page) {
	rep.checks++

	pattern, err := archivePattern(root)
	if err != nil {
		rep.add("release-assets", "%v", err)
		return
	}

	for _, p := range pages {
		for _, name := range archiveMentionRe.FindAllString(p.Raw, -1) {
			// A shell variable standing in for the version is the
			// documented form: the instructions set VERSION once and
			// build the name from it, which is what keeps a runbook
			// pinned.
			if pattern.MatchString(name) {
				continue
			}
			rep.add("release-assets",
				"%s names %q, which the archive template cannot produce", p.Rel, name)
		}

		for _, m := range releaseURLRe.FindAllStringSubmatch(p.Raw, -1) {
			asset := m[1]
			if nonArchiveAssets[asset] || pattern.MatchString(asset) {
				continue
			}
			rep.add("release-assets",
				"%s links to release asset %q, which the pipeline does not publish", p.Rel, asset)
		}
	}
}

// archivePattern builds the set of names the archive template can produce.
//
// Derived from the pipeline's configuration rather than restated here, because a
// check that carried its own copy of the naming scheme would agree with the
// documentation and disagree with the release on the day somebody changed the
// template.
func archivePattern(root string) (*regexp.Regexp, error) {
	path := filepath.Join(root, goreleaserConfig)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", goreleaserConfig, err)
	}
	text := string(data)

	project := firstSubmatch(projectNameRe, text)
	template := firstSubmatch(nameTemplateRe, text)
	if project == "" || template == "" {
		return nil, fmt.Errorf("cannot read project_name and name_template from %s", goreleaserConfig)
	}

	// Every field the template may carry, as the pattern that field can
	// take. A template growing a field this does not know produces a
	// leftover `{{`, which fails below by name rather than silently
	// matching everything.
	replacements := []struct{ field, pattern string }{
		{".ProjectName", regexp.QuoteMeta(project)},
		// A literal semantic version, or the shell variable the
		// documented instructions build the name from.
		{".Version", `(?:[0-9][0-9A-Za-z.+-]*|\$\{?VERSION\}?)`},
		{".Os", `[a-z0-9]+`},
		{".Arch", `[a-z0-9]+`},
	}

	pattern := regexp.QuoteMeta(template)
	for _, r := range replacements {
		// QuoteMeta has escaped the braces and the dots; match the
		// escaped form with whatever spacing the template used.
		field := regexp.MustCompile(`\\\{\\\{\s*` + regexp.QuoteMeta(regexp.QuoteMeta(r.field)) + `\s*\\\}\\\}`)
		pattern = field.ReplaceAllString(pattern, r.pattern)
	}
	if strings.Contains(pattern, `\{\{`) {
		return nil, fmt.Errorf(
			"%s: name_template %q carries a field this check does not know", goreleaserConfig, template)
	}

	formats := firstSubmatch(formatsRe, text)
	if formats == "" {
		formats = "tar.zst"
	}
	var exts []string
	for _, f := range strings.Split(formats, ",") {
		if f = strings.Trim(strings.TrimSpace(f), `"'`); f != "" {
			exts = append(exts, regexp.QuoteMeta(f))
		}
	}

	return regexp.Compile(`^` + pattern + `\.(?:` + strings.Join(exts, "|") + `)$`)
}

func firstSubmatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}
