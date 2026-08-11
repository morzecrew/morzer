#!/usr/bin/env bash
# Publish install.sh at the gh-pages root, beside mike's version directories.
#
# The site is versioned: mike deploys each minor into its own subdirectory and
# owns the root, where it writes index.html and versions.json. A URL under a
# version directory is the wrong home for an installer — .../1.4/install.sh pins
# the script to a docs version that has nothing to do with the release being
# installed, and .../latest/install.sh moves when the docs move. The root is the
# only place with the right lifetime.
#
# mike touches only the version directory, index.html and versions.json, so a
# sibling file at the root survives a deploy. That is a property of mike's
# behaviour rather than a promise it makes, which is why this runs on every docs
# release rather than once, and why the nightly job fetches the published file
# and compares it.
set -euo pipefail

branch="${1:-gh-pages}"
source_file="${2:-install.sh}"

if [ ! -f "$source_file" ]; then
	echo "error: $source_file is not here; run this from the repository root" >&2
	exit 1
fi

if ! git ls-remote --exit-code --heads origin "$branch" >/dev/null 2>&1; then
	# Before the first docs deploy there is no branch to publish into. That
	# is not a failure of this step: the next deploy creates the branch and
	# runs this again.
	echo "no $branch branch yet; nothing to publish into"
	exit 0
fi

work="$(mktemp -d)"
cleanup() {
	git worktree remove --force "$work" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

git fetch --no-tags --depth 1 origin "$branch"
git worktree add --force "$work" "origin/${branch}" >/dev/null

cp "$source_file" "${work}/install.sh"
chmod 0755 "${work}/install.sh"

cd "$work"
if git diff --quiet -- install.sh; then
	echo "install.sh at the site root is already these bytes"
	exit 0
fi

git add install.sh
git commit -m "🔧 chore(docs): publish install.sh at the site root"
git push origin "HEAD:${branch}"
echo "published install.sh to the ${branch} root"
