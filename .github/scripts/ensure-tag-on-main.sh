#!/usr/bin/env bash
# Refuse to release from a commit that is not on main.
#
# A tag can point at anything: a side branch, a commit that was force-pushed
# away, a local experiment. A release built from one is a release nobody can
# reconstruct later -- "what was in 1.4.0" becomes unanswerable the moment the
# branch is deleted.
#
# Run from the release workflow, after a checkout with full history.
set -euo pipefail

tag="${GITHUB_REF_NAME:?GITHUB_REF_NAME is required}"
branch="${RELEASE_BRANCH:-main}"

# The tag is what is checked out; main may not be fetched at all.
git fetch --quiet --no-tags origin "+refs/heads/${branch}:refs/remotes/origin/${branch}"

if git merge-base --is-ancestor HEAD "refs/remotes/origin/${branch}"; then
	echo "tag ${tag} is on ${branch}"
	exit 0
fi

commit=$(git rev-parse --short HEAD)
cat >&2 <<EOF
::error::tag ${tag} points at ${commit}, which is not on ${branch}
EOF
echo >&2
echo "A release has to be reconstructable from the default branch. Merge the" >&2
echo "commit to ${branch} first, then move the tag:" >&2
echo >&2
echo "    git tag -d ${tag} && git push --delete origin ${tag}" >&2
echo "    git tag ${tag} <the merged commit> && git push origin ${tag}" >&2
exit 1
