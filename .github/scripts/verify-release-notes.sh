#!/usr/bin/env bash
# Refuse a release whose body is not the notes this run built.
#
# v0.3.0 published with an empty body while every job reported success. The
# notes were built correctly, printed into the job log, and passed as
# `--release-notes` -- and then discarded, because `changelog.disable: true`
# skips the GoReleaser pipe that is the only reader of that flag. Nothing
# errored, because from GoReleaser's side nothing went wrong: it was asked to
# skip the step and it skipped it.
#
# `release-notes.sh` already refuses to *produce* empty notes. That guards its
# own output and stops one line short of the thing anyone cares about, which is
# whether the notes reached the release. This script closes that gap by
# checking the artifact instead of the intent.
#
# **It compares, rather than checking for emptiness**, because the near miss is
# not empty. With `disable` gone, a run that loses the `--release-notes` flag
# does not produce an empty body -- it produces a changelog generated from
# commit subjects and full SHAs, which is exactly what v0.1.0 shipped and what
# building notes from CHANGELOG.md exists to prevent. A non-empty check would
# pass that happily. Only "the body equals the file we built" catches both.
#
# Runs after GoReleaser, while the release is still a draft, so a mismatch
# costs a re-run rather than a re-release.
#
# Usage: verify-release-notes.sh [tag]   (defaults to $GITHUB_REF_NAME)
# Needs: gh authenticated, and RELEASE_NOTES naming the file that was passed.
set -euo pipefail

tag="${1:-${GITHUB_REF_NAME:?a tag is required, as an argument or GITHUB_REF_NAME}}"
notes="${RELEASE_NOTES:?RELEASE_NOTES must name the notes file passed to goreleaser}"

if [ ! -r "${notes}" ]; then
	echo "::error::${notes} is not readable" >&2
	exit 1
fi

# GitHub stores the body with CRLF line endings. Normalise both sides and drop
# trailing blank lines, so the comparison is about content rather than about
# how two systems spell the end of a line.
normalise() {
	tr -d '\r' < "$1" | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}'
}

published="$(mktemp)"
expected="$(mktemp)"
raw="$(mktemp)"
trap 'rm -f "${published}" "${expected}" "${raw}"' EXIT

# `gh release view` resolves drafts by tag, which is what this is at this point.
if ! gh release view "${tag}" --json body --jq .body > "${raw}" 2>/dev/null; then
	echo "::error::no release found for ${tag}" >&2
	echo >&2
	echo "GoReleaser reported success, so a release should exist. Check the" >&2
	echo "publishing step's output before re-running." >&2
	exit 1
fi

normalise "${raw}" > "${published}"
normalise "${notes}" > "${expected}"

if cmp -s "${published}" "${expected}"; then
	echo "release ${tag} carries the notes built from CHANGELOG.md ($(wc -c < "${expected}") bytes)"
	exit 0
fi

echo "::error::release ${tag} does not carry the notes this run built" >&2
echo >&2

if [ ! -s "${published}" ]; then
	echo "The published body is EMPTY. The notes were built and then dropped" >&2
	echo "on the way to the release -- check that the changelog pipe is not" >&2
	echo "disabled in .goreleaser.yaml, since that pipe is what reads" >&2
	echo "--release-notes." >&2
else
	echo "Published body: $(wc -c < "${published}") bytes" >&2
	echo "Expected:       $(wc -c < "${expected}") bytes" >&2
	echo >&2
	echo "First difference:" >&2
	diff -u "${expected}" "${published}" 2>&1 | sed -n '1,20p' >&2
fi

echo >&2
echo "The release is still a draft. Fix the cause and re-run this workflow" >&2
echo "rather than publishing it." >&2
exit 1
