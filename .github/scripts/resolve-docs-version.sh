#!/usr/bin/env bash
# Decide which docs version a tag publishes, and whether `latest` moves.
#
# Docs are versioned by *minor*: 1.4.0, 1.4.1 and 1.4.2 all publish to `1.4`,
# because a patch release rarely changes the documentation and three near
# identical entries in a version selector help nobody.
#
# `latest` moves only when the tag is the newest released minor. A backport tag
# on an older line refreshes that line in place -- pointing `latest` at 1.3.5
# because it was released after 1.4.0 would send every reader to older docs.
#
# Writes `minor` and `move_latest` to $GITHUB_OUTPUT.
set -euo pipefail

tag="${GITHUB_REF_NAME:?GITHUB_REF_NAME is required}"
version="${tag#v}"

if [[ ! "${version}" =~ ^([0-9]+)\.([0-9]+)\. ]]; then
	echo "::error::tag ${tag} is not a vMAJOR.MINOR.PATCH version" >&2
	exit 1
fi
minor="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}"

# The newest minor across every release tag, this one included.
newest=$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' |
	sed 's/^v//' |
	cut -d. -f1,2 |
	sort -t. -k1,1n -k2,2n |
	tail -1)

move_latest=false
if [ "${minor}" = "${newest}" ]; then
	move_latest=true
fi

echo "minor=${minor}" >> "$GITHUB_OUTPUT"
echo "move_latest=${move_latest}" >> "$GITHUB_OUTPUT"

echo "tag ${tag} publishes docs version ${minor} (newest minor is ${newest}, latest moves: ${move_latest})"
