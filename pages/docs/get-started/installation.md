---
title: Installing morzer
icon: lucide/download
summary: Getting the binary onto a machine, and what it needs from that machine
---

# Installing morzer

morzer is a single static binary with no runtime dependency on the machine's
libc. Getting it is a download and a checksum.

## The script

```sh
curl -fsSL https://morzecrew.github.io/morzer/install.sh | sh -s -- --version 0.2.0
```

It resolves the archive for this machine's architecture, checks the checksum
against the release's own `SHA256SUMS`, checks the signature when `minisign` is
installed, installs one file, and puts the prefix on `PATH` if it is not there
already. Then it tells you what it did.

!!! warning "Unpacking needs `zstd` on the machine"

    Releases are `tar.zst`, and GNU tar's `--zstd` runs the `zstd` binary as a
    filter rather than decompressing on its own — so a machine without it cannot
    unpack a release however new its tar is. `apt install zstd`, `apk add zstd`
    or `dnf install zstd`.

    An installed server usually has it — on Debian and Ubuntu the initramfs
    tooling pulls it in. Container images mostly do not: of
    `debian:bookworm-slim`, `ubuntu:24.04`, `rockylinux:9`, `alpine:3.20` and
    `fedora:41`, only Fedora ships it.

    `--print-only` reports whether this machine has it, and reports it before
    anything is downloaded. Without it the script still downloads, checksums and
    verifies the archive, then refuses at the last step and names this.

```sh
# See everything it detected and would do, and change nothing.
curl -fsSL https://morzecrew.github.io/morzer/install.sh | sh -s -- --print-only

# A production runbook: pin the version, and refuse to install unsigned.
curl -fsSL https://morzecrew.github.io/morzer/install.sh \
  | sh -s -- --version 0.2.0 --require-signature

# Somewhere else, without touching a startup file.
curl -fsSL https://morzecrew.github.io/morzer/install.sh \
  | sh -s -- --version 0.2.0 --dir /opt/morzer/bin --no-modify-path
```

`sh -s --` is how arguments reach a script that arrived on stdin. `MORZER_VERSION`
and `MORZER_INSTALL_DIR` do the same job for a runbook that would rather set
environment variables than get that incantation right.

| Option | |
| --- | --- |
| `--version X.Y.Z` | The release to install. Without it, the newest published release — resolved once, then printed. A prerelease is installed when you name it and never when it is inferred. |
| `--dir PATH` | Where to install. Default: `/usr/local/bin` when writable, otherwise `$HOME/.local/bin`. |
| `--digest sha256:…` | Bytes you are asserting. Checked before `SHA256SUMS`, and a mismatch is fatal. |
| `--require-signature` | Refuse to install when `minisign` is missing or the signature does not verify. |
| `--no-verify-signature` | Skip the signature check, and say so. Refused together with `--require-signature`. |
| `--no-modify-path` | Never edit a startup file; print the block instead. |
| `--completions` / `--no-completions` | Force shell completions on or off. On by default at a terminal, off in a build. |
| `--shell NAME` | Override the shell detected from `$SHELL`. |
| `--print-only` | Print what it detected and would do. Changes nothing. |

What it never does: run `sudo` on its own initiative — if the prefix needs root
it prints the command to re-run and exits non-zero — install anything but
`<dir>/morzer`, or leave a partial download behind.

### Reading it first

Piping a script into a shell means running what a server chose to send. The
criticism is correct in general, and the answer here is that everything the
script does is also documented below, and that you can read it before running
it:

```sh
curl -fsSLO https://morzecrew.github.io/morzer/install.sh
less install.sh
sh install.sh --print-only
sh install.sh --version 0.2.0
```

The same file is at
[`raw.githubusercontent.com`](https://raw.githubusercontent.com/morzecrew/morzer/main/install.sh)
— swap `main` for a tag to pin the script itself — and it ships as an asset of
every release, covered by that release's `SHA256SUMS`. So the script that
verifies the archive is itself verifiable against the file it teaches you to
check.

## By hand

The script is doing this. It stays documented because an operator who wants to
check its work needs to be able to, and because a machine that cannot reach the
site can still install from a release it already has.

```sh
VERSION=0.2.0                            # the release you mean, not "whatever is newest"
ARCHIVE=morzer_${VERSION}_linux_amd64.tar.zst
BASE=https://github.com/morzecrew/morzer/releases/download/v${VERSION}

curl -fsSLO ${BASE}/${ARCHIVE}
curl -fsSLO ${BASE}/SHA256SUMS
curl -fsSLO ${BASE}/SHA256SUMS.minisig
curl -fsSLO https://raw.githubusercontent.com/morzecrew/morzer/main/morzer.pub

minisign -Vm SHA256SUMS -p morzer.pub    # who published it
grep " ${ARCHIVE}\$" SHA256SUMS | sha256sum -c -  # that this is what they published

tar --zstd -xf ${ARCHIVE}                # runs zstd as a filter; install it first
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

The install script above runs exactly this command when the install is
interactive and the shell is one of the three, which is why there is no second
copy of these paths written in `sh`: a completion in the wrong directory
produces no error at all, just a Tab key that does nothing.

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
