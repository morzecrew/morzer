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
curl -fsSLO https://github.com/morzecrew/morzer/releases/latest/download/morzer_linux_amd64.tar.zst
curl -fsSLO https://github.com/morzecrew/morzer/releases/latest/download/SHA256SUMS
curl -fsSLO https://github.com/morzecrew/morzer/releases/latest/download/SHA256SUMS.minisig
curl -fsSLO https://raw.githubusercontent.com/morzecrew/morzer/main/morzer.pub

minisign -Vm SHA256SUMS -p morzer.pub    # who published it
sha256sum -c SHA256SUMS --ignore-missing # that this is what they published

tar --zstd -xf morzer_linux_amd64.tar.zst
sudo install -m 0755 morzer /usr/local/bin/morzer
```

Both checks matter and they answer different questions. The signature says a key
you trust produced the checksum file; the checksum says the archive you have is
the one that file describes.

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

## Running as root

The manager writes to `/etc`, `/var/lib`, `/run` and `/opt`, installs systemd
units, and runs release hooks. It expects to be root.

To try it without being root, or without touching those paths at all, see
[Your first deployment](first-deployment.md) — every managed path derives from a
single prefix, and there is a flag that moves all of them.
