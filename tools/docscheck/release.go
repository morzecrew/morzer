package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
)

// The release pipeline's own configuration, read rather than restated.
const goreleaserConfig = ".goreleaser.yaml"

// goreleaserDefaultFormat is what an archive with no declared `formats` gets.
//
// goreleaser's default, not this repository's choice -- the two differ, and
// defaulting to what this repository happens to use would make a deleted
// `formats:` line silently keep validating the old extension.
const goreleaserDefaultFormat = "tar.gz"

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

// archiveExtensions are the endings that make a bare name in the prose an
// archive name worth checking.
//
// Every archive ending goreleaser can produce, not the one this pipeline is
// configured for. That looseness is the point and it must not be derived from
// `formats`: a page naming a `.zip` while the pipeline builds `.tar.zst` is
// precisely the drift this check exists to report, and an extension list built
// from the configuration would stop recognising it as an archive name at all.
//
// The `binary` format is the one ending not listed, because it has none -- a
// bare name with no extension is indistinguishable from prose, so an archives
// block using it is checked through the release URLs only.
var archiveExtensions = regexp.MustCompile(`\.(?:tar(?:\.[a-z0-9]+)?|t[gbx]z|zip|gz)\b`)

// A release URL that names an asset, in either of GitHub's two forms:
// `/releases/download/<tag>/<asset>` and `/releases/latest/download/<asset>`.
//
// The tag segment is required in the first form rather than optional. Optional,
// it swallows a *base* URL -- `…/releases/download/v${VERSION}`, which the
// documented instructions build the asset onto -- and reports the tag as an
// asset the pipeline does not publish. That is not a hypothetical: it is what
// the first draft of this file did.
var releaseURLRe = regexp.MustCompile(
	`https://github\.com/morzecrew/morzer/releases/(?:latest/download|download/[^/\s]+)/([^\s'"()\x60]+)`)

// sentencePunctuation is what a URL in running prose picks up: a full stop, a
// list separator, the closing half of an angle-bracket autolink. None of it can
// end an asset name, so trimming it costs nothing and stops a correctly written
// page from being reported as drift.
const sentencePunctuation = ".,;:>"

// archiveNames is how the pipeline names its archives, in the two shapes this
// check needs: one loose enough to find a name in prose whether or not it is
// right, one exact enough to say whether it is.
type archiveNames struct {
	mention *regexp.Regexp
	exact   *regexp.Regexp
}

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

	names, err := archivePattern(root)
	if err != nil {
		rep.add("release-assets", "%v", err)
		return
	}

	for _, p := range pages {
		for _, name := range names.mention.FindAllString(p.Raw, -1) {
			// A shell variable standing in for the version is the
			// documented form: the instructions set VERSION once and
			// build the name from it, which is what keeps a runbook
			// pinned.
			if names.exact.MatchString(name) {
				continue
			}
			rep.add("release-assets",
				"%s names %q, which the archive template cannot produce", p.Rel, name)
		}

		for _, m := range releaseURLRe.FindAllStringSubmatch(p.Raw, -1) {
			asset := strings.TrimRight(m[1], sentencePunctuation)
			if asset == "" || nonArchiveAssets[asset] || names.exact.MatchString(asset) {
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
//
// Parsed rather than pattern-matched, and the difference is not stylistic: a
// goreleaser config carries `name_template` under `checksum`, `snapshot`,
// `release` and `nfpms` as well as under `archives` -- this one already has a
// second under `checksum` -- so a regular expression taking the first match in
// the file builds the archive pattern out of whichever key happens to be
// written first. `formats` has the same problem from the other direction: it is
// equally valid as a block sequence, which a flow-sequence pattern reads as
// absent and quietly replaces with a default. The parser is free here, the
// manager already depends on it, and neither failure survives it.
func archivePattern(root string) (archiveNames, error) {
	path := filepath.Join(root, goreleaserConfig)
	data, err := os.ReadFile(path)
	if err != nil {
		return archiveNames{}, fmt.Errorf("cannot read %s: %w", goreleaserConfig, err)
	}

	// The three fields this check needs. Everything else in the file is
	// somebody else's business, and yaml ignores what it is not asked for.
	var config struct {
		ProjectName string `yaml:"project_name"`
		Archives    []struct {
			NameTemplate string   `yaml:"name_template"`
			Formats      []string `yaml:"formats"`
		} `yaml:"archives"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return archiveNames{}, fmt.Errorf("cannot parse %s: %w", goreleaserConfig, err)
	}
	if config.ProjectName == "" {
		return archiveNames{}, fmt.Errorf("%s declares no project_name", goreleaserConfig)
	}
	if len(config.Archives) == 0 {
		return archiveNames{}, fmt.Errorf("%s declares no archives", goreleaserConfig)
	}

	// One alternative per archive: several are legal, and a name matching
	// any of them is a name the pipeline publishes.
	alternatives := make([]string, 0, len(config.Archives))
	for i, archive := range config.Archives {
		if archive.NameTemplate == "" {
			return archiveNames{}, fmt.Errorf(
				"%s: archive %d declares no name_template", goreleaserConfig, i)
		}
		pattern, err := templatePattern(archive.NameTemplate, config.ProjectName)
		if err != nil {
			return archiveNames{}, err
		}

		formats := archive.Formats
		if len(formats) == 0 {
			formats = []string{goreleaserDefaultFormat}
		}
		exts := make([]string, 0, len(formats))
		for _, f := range formats {
			if f = strings.TrimSpace(f); f != "" {
				exts = append(exts, regexp.QuoteMeta(f))
			}
		}
		if len(exts) == 0 {
			return archiveNames{}, fmt.Errorf(
				"%s: archive %d declares an empty formats list", goreleaserConfig, i)
		}

		alternatives = append(alternatives, pattern+`\.(?:`+strings.Join(exts, "|")+`)`)
	}

	exact, err := regexp.Compile(`^(?:` + strings.Join(alternatives, "|") + `)$`)
	if err != nil {
		return archiveNames{}, fmt.Errorf("%s: %w", goreleaserConfig, err)
	}

	// The loose half. A name is a candidate when it opens with the project
	// name and ends in an archive extension; everything between is
	// deliberately unconstrained, because a name this fails to recognise is
	// a name it fails to check.
	mention, err := regexp.Compile(
		`\b` + regexp.QuoteMeta(config.ProjectName) + `[_-][^\s'"()\x60]*` + archiveExtensions.String())
	if err != nil {
		return archiveNames{}, fmt.Errorf("%s: %w", goreleaserConfig, err)
	}

	return archiveNames{mention: mention, exact: exact}, nil
}

// templatePattern turns one goreleaser name template into a regular expression.
func templatePattern(template, project string) (string, error) {
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
		return "", fmt.Errorf(
			"%s: name_template %q carries a field this check does not know", goreleaserConfig, template)
	}
	return pattern, nil
}
