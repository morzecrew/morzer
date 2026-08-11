#!/usr/bin/env bash
# Does the published installer still install the published release?
#
# This is the one check no offline test can make, and it is the exact class of
# failure RFC 0022 exists to fix: the documented download URL named an asset the
# pipeline does not produce, and nothing caught it because nothing ever ran it.
#
# Two questions:
#
#   1. Run the script for real against the newest release. That exercises the
#      asset name, the checksum file, the signature against the key embedded in
#      the script, and the binary itself -- end to end, on a machine that has
#      none of it.
#   2. Are the published copies the same bytes? The comparison is anchored at
#      the release tag rather than at main, because main legitimately moves on
#      after a release: what must agree is the asset, the site copy, and
#      `install.sh` as it was at that tag.
#
# Nightly rather than per-pull-request: it depends on GitHub being up, and a
# network check on every change is a flake generator.
set -euo pipefail

repo="${REPO:-morzecrew/morzer}"
site="${SITE:-https://morzecrew.github.io/morzer}"

fail=0
problem() {
	echo "::error::$*"
	fail=1
}

tag="$(gh api "repos/${repo}/releases/latest" --jq .tag_name 2>/dev/null || true)"
if [ -z "$tag" ] || [ "$tag" = "null" ]; then
	# Nothing to check yet, and that is a fact about the repository rather
	# than a failure of this job. It stops being true at the first release,
	# and then everything below runs.
	echo "no published release yet; nothing for this job to check"
	exit 0
fi
version="${tag#v}"
echo "newest release: ${tag}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# --- 1. the script installs what the release published ---------------------

# --require-signature, because this is the run that can afford to demand it:
# the key is embedded in the script and a rotation that shipped without a new
# script is exactly what this should catch.
echo "::group::install.sh --version ${version} --require-signature"
if sh install.sh \
	--version "$version" \
	--dir "${work}/bin" \
	--require-signature \
	--no-modify-path \
	--no-completions; then
	reported="$("${work}/bin/morzer" version | head -n 1)"
	echo "installed: ${reported}"
	case "$reported" in
	*"$version"*) ;;
	*) problem "the installed binary reports '${reported}', not ${version}" ;;
	esac
else
	problem "install.sh could not install ${version} from the published release"
fi
echo "::endgroup::"

# --- 2. the published copies are the same bytes ----------------------------

digest_of_file() { sha256sum "$1" | awk '{print $1}'; }

if ! git cat-file -e "${tag}:install.sh" 2>/dev/null; then
	echo "notice: ${tag} predates install.sh; skipping the copy comparison"
	exit "$fail"
fi
git show "${tag}:install.sh" >"${work}/at-tag.sh"
tagged="$(digest_of_file "${work}/at-tag.sh")"
echo "install.sh at ${tag}: ${tagged}"

if curl -fsSL -o "${work}/asset.sh" \
	"https://github.com/${repo}/releases/download/${tag}/install.sh"; then
	got="$(digest_of_file "${work}/asset.sh")"
	if [ "$got" != "$tagged" ]; then
		problem "the release asset (${got}) is not install.sh at ${tag} (${tagged})"
	fi
else
	problem "no install.sh asset on release ${tag}"
fi

if curl -fsSL -o "${work}/site.sh" "${site}/install.sh"; then
	got="$(digest_of_file "${work}/site.sh")"
	if [ "$got" != "$tagged" ]; then
		# The likeliest cause is a docs deploy that rewrote the gh-pages
		# root, which is the property the publication step exists to
		# defend and the reason this is a test rather than an assumption.
		problem "the site copy (${got}) is not install.sh at ${tag} (${tagged})"
	fi
else
	problem "${site}/install.sh is not published"
fi

if [ "$fail" -eq 0 ]; then
	echo "the installer installs ${tag}, and every published copy is the same file"
fi
exit "$fail"
