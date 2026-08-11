---
title: Installing morzer
icon: lucide/download
summary: Getting the binary onto a machine, and what it needs from that machine
---

# Installing morzer

morzer is a single static binary with no runtime dependency on the machine's
libc. Getting it is a download and a checksum.

## From a release

```sh
VERSION=1.0.0                            # the release you mean, not "whatever is newest"
ARCHIVE=morzer_${VERSION}_linux_amd64.tar.zst
BASE=https://github.com/morzecrew/morzer/releases/download/v${VERSION}

curl -fsSLO ${BASE}/${ARCHIVE}
curl -fsSLO ${BASE}/SHA256SUMS
curl -fsSLO ${BASE}/SHA256SUMS.minisig
curl -fsSLO https://raw.githubusercontent.com/morzecrew/morzer/main/morzer.pub

minisign -Vm SHA256SUMS -p morzer.pub    # who published it
grep " ${ARCHIVE}\$" SHA256SUMS | sha256sum -c -  # that this is what they published

tar --zstd -xf ${ARCHIVE}
sudo install -m 0755 morzer /usr/local/bin/morzer
```

Both checks matter and they answer different questions. The signature says a key
you trust produced the checksum file; the checksum says the archive you have is
the one that file describes.

!!! warning "Not `sha256sum -c --ignore-missing`"

    `SHA256SUMS` covers every architecture's archive, so a plain
    `sha256sum -c SHA256SUMS` fails on the ones you did not download and
    `--ignore-missing` is the usual way around that. It is also the way to make
    the check meaningless: with **no** archive present — a download that failed,
    a typo in the name — `--ignore-missing` skips every line and reports `OK`.

    Pulling this archive's own line out of the file first is one more pipe and
    fails when there is nothing to check.

The version is written out rather than using `/releases/latest/download/…`.
`latest` is a moving target: a runbook written today installs something else
tomorrow, which is the same reason `update.channel` exists and is a decision an
operator makes rather than a default. If you do want the newest release, take
the version from the [releases page](https://github.com/morzecrew/morzer/releases)
and put it in the runbook.

!!! tip "Keep your copy of the public key"

    The key is published in the same repository as the releases it signs, so on
    its own it proves only that the repository was self-consistent. Its value is
    continuity: a release signed by a *different* key than the one you verified
    last time is worth stopping over. Save it the first time.

`linux/arm64` is published alongside `amd64`.

## From source

```sh
git clone https://github.com/morzecrew/morzer
cd morzer
just build          # ./morzer
```

Needs Go 1.25 or newer. `just build-all` cross-compiles both architectures with
a `SHA256SUMS`; the flags match what the release pipeline uses, so a binary you
build from a tag is byte-identical to the published one.

## Shell completion

```sh
morzer completion install
```

Puts the completion script where your shell reads completions from — bash, zsh
and fish — creating the directory if it is missing, and printing anything else
that shell needs. It defaults to `$SHELL`; name one to override.

A shell it cannot place a file for gets the script on stdout instead, and exits
0. The paths are in [`completion install`](../reference/commands.md#completion-install).

## What the machine needs

| Tool | Why | Checked by |
| --- | --- | --- |
| `docker` and `docker compose` | The runtime the manager coordinates. | preflight, `doctor` |
| `sops` | Encrypting and decrypting the secret state. | preflight, `doctor` |
| A tmpfs at `/run` | Decrypted secrets are written there. Standard on systemd hosts. | `doctor` |
| systemd | Optional. Boot-time convergence and scheduled backups. | `doctor` |

The versions a release requires are declared in its manifest, not by the
manager, so a bundle that needs `docker >= 24` says so and preflight enforces
it:

```sh
morzer doctor
```

On a machine with no installation yet, `doctor` still reports whether the tools
are present — which is the question you have before running `init`, not after.

One machine may hold more than one installation: every path, unit and lock is
keyed by the product name. `morzer ls` says what a host is already running,
which is worth asking on a machine somebody else set up — see
[Several installations](../operating/several-installations.md).

## Running as root

The manager writes to `/etc`, `/var/lib`, `/run` and `/opt`, installs systemd
units, and runs release hooks. It expects to be root.

To try it without being root, or without touching those paths at all, see
[Your first deployment](first-deployment.md) — every managed path derives from a
single prefix, and there is a flag that moves all of them.
