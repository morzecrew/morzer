#!/usr/bin/env bash
# Build a release's notes from CHANGELOG.md, and refuse if there are none.
#
# The notes used to be assembled from commit subjects, and the two releases made
# that way are the argument for this script. v0.1.0 had no previous tag to
# compare against, so it listed *every commit in the repository* -- a hundred
# and eighty lines including `fix(backup): improve volume reporting`, twice, and
# commits about agent skills that ship in nothing. v0.1.1 listed one line, a
# subject and a full SHA, for a release whose whole point needed a paragraph to
# explain. Both were published while CHANGELOG.md held a written account of
# exactly what shipped.
#
# So the changelog is the source and the commit log is not. A commit subject is
# written for whoever reads `git log`; a changelog entry is written for whoever
# is deciding whether to upgrade, and those are different readers.
#
# **A missing section is an error, not an empty release.** This is the failure
# the whole script exists to prevent: notes that are silently wrong get
# published, and a release is the one artifact you cannot quietly fix later.
# Running before anything is built means a forgotten changelog section costs a
# re-run rather than a re-release.
#
# Usage: release-notes.sh [tag]     (defaults to $GITHUB_REF_NAME)
# Writes the notes to stdout.
set -euo pipefail

tag="${1:-${GITHUB_REF_NAME:?a tag is required, as an argument or GITHUB_REF_NAME}}"
changelog="${CHANGELOG_FILE:-CHANGELOG.md}"
version="${tag#v}"

if [ ! -r "${changelog}" ]; then
	echo "::error::${changelog} is not readable from $(pwd)" >&2
	exit 1
fi

# `## [0.2.0]` and nothing else -- matched as a literal prefix rather than a
# regex, because a version is full of dots and `0.1.0` would otherwise also
# match a hypothetical `0x1y0`. The heading carries a date after it, which is
# why this is a prefix test and not equality.
heading="## [${version}]"

section=$(
	awk -v heading="${heading}" '
		index($0, heading) == 1 { found = 1; next }
		found && /^## / { exit }
		found { print }
	' "${changelog}"
)

# Trim leading and trailing blank lines, so the notes neither open with a gap
# nor carry the separator before the next version's heading.
section=$(printf '%s\n' "${section}" | sed -e '/./,$!d' -e :a -e '/^\n*$/{$d;N;ba' -e '}')

if [ -z "${section}" ]; then
	echo "::error::${changelog} has no entries under '${heading}'" >&2
	echo >&2
	echo "A release is published from what the changelog says shipped. Add the" >&2
	echo "section, or cut it from Unreleased, then move the tag:" >&2
	echo >&2
	echo "    ## [${version}] - $(date -u +%Y-%m-%d)" >&2
	echo >&2
	echo "Versions this file does have:" >&2
	grep -o '^## \[[^]]*\]' "${changelog}" | sed 's/^/    /' >&2
	exit 1
fi

# The footer lives here rather than in `.goreleaser.yaml` because
# `--release-notes` replaces the whole body: goreleaser's `release.header` and
# `release.footer` are dropped the moment a notes file is passed, so a footer
# left in that file would be configuration that reads as live and never runs.
# One place builds the release body.
cat <<EOF
## Changelog

${section}

## Installing this release

\`\`\`sh
curl -fsSL https://morzecrew.github.io/morzer/install.sh | sh -s -- --version ${version}
\`\`\`

The script verifies the checksum always and the signature whenever
\`minisign\` is present; \`--require-signature\` makes a missing minisign fatal,
which is what a production runbook sets. \`--print-only\` shows everything it
detected and would do, and changes nothing. \`install.sh\` is an asset of this
release and is covered by \`SHA256SUMS\` below.

## Verifying this release by hand

\`\`\`sh
ARCHIVE=morzer_${version}_linux_amd64.tar.zst   # the one you downloaded

curl -fsSLO https://raw.githubusercontent.com/morzecrew/morzer/main/morzer.pub
minisign -Vm SHA256SUMS -p morzer.pub
grep " \${ARCHIVE}\$" SHA256SUMS | sha256sum -c -
\`\`\`

\`SHA256SUMS\` covers every architecture, so checking it whole fails on the
archives you did not download — and \`--ignore-missing\`, the usual way
around that, reports \`OK\` when *no* archive is present at all. Pulling out
the line for the file you have fails when there is nothing to check.

The signing key is the same for every release. If you have verified a
morzer release before, use the copy you already have rather than fetching
it again — a key that changes between releases is the thing worth noticing.

Release bundles are a separate artifact with their own signature and their
own key; see
https://morzecrew.github.io/morzer/reference/release-commands/#verification
EOF
