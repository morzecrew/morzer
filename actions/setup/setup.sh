#!/bin/sh
# Install a published morzer release on a GitHub Actions runner; what
# action.yml runs.
#
# install.sh does the work -- this repository's copy, at the ref the action was
# called at -- with the flags a job wants: the version it was given, a
# signature that must verify, a directory the job owns, and no startup file or
# completion touched. A CI job that installs whatever is newest, or installs
# unsigned, is one nobody can reproduce or trust.
#
# --require-signature refuses without minisign, and a hosted runner has none,
# so minisign is installed first when it is missing: from its release archive,
# checked against the digest pinned below rather than a checksum fetched beside
# it.
#
# Environment, set by action.yml:
#   VERSION      the release to install, X.Y.Z
#   DIR          where the binaries go; empty is $RUNNER_TEMP/morzer
#   ACTION_PATH  this directory
set -eu

MINISIGN=0.12
MINISIGN_SHA256=9a599b48ba6eb7b1e80f12f36b94ceca7c00b7a5173c95c3efc88d9822957e73

die() {
	echo "::error::setup-morzer: $*" >&2
	exit 1
}

[ "${RUNNER_OS:-}" = Linux ] || die "morzer runs on Linux, and this runner is ${RUNNER_OS:-unknown}"
case "${VERSION:-}" in
'') die "the version input is required, for example 0.9.0" ;;
v*) die "version '${VERSION}' takes no v: 0.9.0, not v0.9.0" ;;
esac

dir=${DIR:-${RUNNER_TEMP:?}/morzer}
mkdir -p "$dir"
PATH="${dir}:${PATH}"

if ! command -v minisign >/dev/null 2>&1; then
	case "$(uname -m)" in
	x86_64 | amd64) arch=x86_64 ;;
	aarch64 | arm64) arch=aarch64 ;;
	*) die "minisign ${MINISIGN} has no Linux build for $(uname -m)" ;;
	esac
	command -v curl >/dev/null 2>&1 || die "curl is needed to fetch minisign"
	work=$(mktemp -d)
	archive="minisign-${MINISIGN}-linux.tar.gz"
	curl -fsSL -o "${work}/${archive}" \
		"https://github.com/jedisct1/minisign/releases/download/${MINISIGN}/${archive}"
	echo "${MINISIGN_SHA256}  ${work}/${archive}" | sha256sum -c - >/dev/null \
		|| die "${archive} does not match the digest this action pins"
	tar -xzf "${work}/${archive}" -C "$work" "minisign-linux/${arch}/minisign" 2>/dev/null
	install -m 0755 "${work}/minisign-linux/${arch}/minisign" "${dir}/minisign"
	rm -rf "$work"
fi

sh "${ACTION_PATH:?}/../../install.sh" --version "$VERSION" --dir "$dir" \
	--require-signature --no-modify-path --no-completions

# `morzer version` prints `morzer X.Y.Z` first; anything else installed the
# wrong thing, which is worth failing on before a later step relies on it.
installed=$("${dir}/morzer" version | sed -n '1s/^morzer //p')
[ "$installed" = "$VERSION" ] || die "the installed morzer reports '${installed}', not ${VERSION}"

echo "$dir" >>"${GITHUB_PATH:?}"
{
	echo "path=${dir}"
	echo "version=${installed}"
} >>"${GITHUB_OUTPUT:?}"
