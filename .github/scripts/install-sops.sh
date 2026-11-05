#!/usr/bin/env bash
#
# Install sops, verified against its published checksums.
#
# Shared by every job that runs the test suite. Without sops the real
# SecretStore contract suite skips itself and the run stays green having
# exercised only the fake -- and for the coverage job it also silently drops the
# adapter's statements out of the total.
set -euo pipefail

version="${1:-${SOPS_VERSION:?SOPS_VERSION is required}}"
base="https://github.com/getsops/sops/releases/download/v${version}"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Retried: the release download answers 504 for minutes at a time, and every
# job that runs the suite starts here.
fetch() { curl -fsSL --retry 5 --retry-delay 10 -o "$1" "$2"; }
fetch "$work/sops" "${base}/sops-v${version}.linux.amd64"
fetch "$work/checksums.txt" "${base}/sops-v${version}.checksums.txt"

expected=$(grep "sops-v${version}.linux.amd64\$" "$work/checksums.txt" | awk '{print $1}')
if [ -z "$expected" ]; then
    echo "error: no checksum published for sops-v${version}.linux.amd64" >&2
    exit 1
fi
( cd "$work" && echo "${expected}  sops" | sha256sum -c - )

sudo install -m 0755 "$work/sops" /usr/local/bin/sops
sops --version --disable-version-check
