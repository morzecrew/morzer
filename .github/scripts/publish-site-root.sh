#!/usr/bin/env bash
# Publish install.sh and schemas/ at the gh-pages root, beside mike's version
# directories.
#
# The site is versioned: mike deploys each minor into its own subdirectory and
# owns the root, where it writes index.html and versions.json. A URL under a
# version directory is the wrong home for an installer — .../1.4/install.sh pins
# the script to a docs version that has nothing to do with the release being
# installed, and .../latest/install.sh moves when the docs move. The root is the
# only place with the right lifetime.
#
# The schemas have the same problem. Every scaffolded manifest names
# .../schemas/selfhost-v1alpha1-manifest.json in its yaml-language-server
# modeline, a bundle outlives any docs version, and the file name already
# carries the API version that decides which schema applies.
#
# mike touches only the version directory, index.html and versions.json, so a
# sibling file at the root survives a deploy. That is a property of mike's
# behaviour rather than a promise it makes, which is why this runs on every docs
# release rather than once, and why the nightly job fetches the published file
# and compares it.
set -euo pipefail

branch="${1:-gh-pages}"

for path in install.sh schemas; do
	if [ ! -e "$path" ]; then
		echo "error: $path is not here; run this from the repository root" >&2
		exit 1
	fi
done

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

cp install.sh "${work}/install.sh"
chmod 0755 "${work}/install.sh"
# Replaced whole rather than copied over, so a schema the repository stops
# publishing stops answering at its URL.
rm -rf "${work}/schemas"
cp -R schemas "${work}/schemas"

cd "$work"

# Staged first, and compared against HEAD rather than against the working
# tree. `git diff` reports nothing at all for an untracked path, so on the one
# run that matters -- the first, when the root has no install.sh yet -- the
# working-tree comparison said "already these bytes" and exited 0. The step
# passed, the release published, and the URL the README tells people to curl
# returned 404 until somebody tried it.
#
# Staging makes all three cases the same question: absent stages as a new file
# and differs, changed differs, unchanged does not.
git add -A install.sh schemas
if git diff --cached --quiet -- install.sh schemas; then
	echo "install.sh and schemas/ at the site root are already these bytes"
	exit 0
fi

git commit -m "🔧 chore(docs): publish install.sh and schemas at the site root"
git push origin "HEAD:${branch}"
echo "published install.sh and schemas/ to the ${branch} root"
