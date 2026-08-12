#!/usr/bin/env bash
# Does the published installer still install the published release?
#
# This is the one check no offline test can make, and it is the exact class of
# failure RFC 0022 exists to fix: the documented download URL named an asset the
# pipeline does not produce, and nothing caught it because nothing ever ran it.
#
# Two questions:
#
#   1. Are the published copies the same bytes? The comparison is anchored at
#      the release tag rather than at main, because main legitimately moves on
#      after a release: what must agree is the asset, the site copy, and
#      `install.sh` as it was at that tag.
#   2. Does the script an operator actually gets install the release? Run the
#      *published* copy -- the site one by preference, since that is what
#      `curl -fsSL .../install.sh | sh` fetches. Running the checkout's copy
#      instead would report success while the published one was broken, which
#      is precisely the drift this job exists to catch.
#
# Nightly rather than per-pull-request: it depends on GitHub being up, and a
# network check on every change is a flake generator.
set -euo pipefail

repo="${REPO:-morzecrew/morzer}"
site_base="${SITE:-https://morzecrew.github.io/morzer}"

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

# --- 1. the published copies are the same bytes ----------------------------

digest_of_file() { sha256sum "$1" | awk '{print $1}'; }

tagged=""
if git cat-file -e "${tag}:install.sh" 2>/dev/null; then
	git show "${tag}:install.sh" >"${work}/at-tag.sh"
	tagged="$(digest_of_file "${work}/at-tag.sh")"
	echo "install.sh at ${tag}: ${tagged}"
else
	echo "notice: ${tag} predates install.sh; there is nothing to compare"
fi

asset=""
if curl -fsSL -o "${work}/asset.sh" \
	"https://github.com/${repo}/releases/download/${tag}/install.sh"; then
	asset="$(digest_of_file "${work}/asset.sh")"
	if [ -n "$tagged" ] && [ "$asset" != "$tagged" ]; then
		problem "the release asset (${asset}) is not install.sh at ${tag} (${tagged})"
	fi
elif [ -n "$tagged" ]; then
	problem "no install.sh asset on release ${tag}"
fi

site=""
if curl -fsSL -o "${work}/site.sh" "${site_base}/install.sh"; then
	site="$(digest_of_file "${work}/site.sh")"
	if [ -n "$tagged" ] && [ "$site" != "$tagged" ]; then
		# The likeliest cause is a docs deploy that rewrote the gh-pages
		# root, which is the property the publication step exists to
		# defend and the reason this is a test rather than an assumption.
		problem "the site copy (${site}) is not install.sh at ${tag} (${tagged})"
	fi
elif [ -n "$tagged" ]; then
	problem "${site_base}/install.sh is not published"
fi

# --- 2. the published script installs the published release ----------------

# Whichever copy an operator would actually get, in the order they would get
# it. The checkout's own install.sh is the last resort and is announced as
# such: it is the one copy whose success proves nothing about what is
# published.
if [ -n "$site" ]; then
	runner="${work}/site.sh"
	echo "running the site copy (${site_base}/install.sh)"
elif [ -n "$asset" ]; then
	runner="${work}/asset.sh"
	echo "running the release asset from ${tag}"
else
	runner="install.sh"
	echo "notice: nothing is published yet; running this checkout's install.sh,"
	echo "        which says nothing about what an operator would download"
fi

# --require-signature, because this is the run that can afford to demand it:
# the key is embedded in the script and a rotation that shipped without a new
# script is exactly what this should catch.
echo "::group::${runner} --version ${version} --require-signature"
if sh "$runner" \
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
	problem "${runner} could not install ${version} from the published release"
fi
echo "::endgroup::"

if [ "$fail" -eq 0 ]; then
	echo "the published installer installs ${tag}, and every copy is the same file"
fi
exit "$fail"
