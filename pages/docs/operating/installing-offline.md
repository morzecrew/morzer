---
title: Installing without a network
icon: lucide/wifi-off
summary: Preparing a machine that cannot reach a registry, and checking beforehand that it will come up
---

# Installing without a network

An air-gapped host, a machine behind a proxy that will not pass a registry, a
site whose uplink is unreliable — the manager can install and converge on all of
them, but only if the two things it would otherwise fetch are already there:
the **release bundle** and the **container images**.

!!! warning "Print this, or keep it where you can read it offline"

    A procedure for a machine with no network access is a procedure you cannot
    look up from that machine. It is also in the repository, in
    `pages/docs/operating/installing-offline.md`.

## Check first, while there is still a network

```sh
morzer doctor
```

Three checks answer the question:

```text
[ok]   container registry is reachable
[warn] release images are available offline
       2 of 2 image(s) are not local: registry.example/demo/app, registry.example/demo/db
       → run `morzer apply` while online, or preload with
         `docker load < images.tar`, if this machine has to come up without
         network access
[warn] the volume helper image is available offline
       busybox is not on this machine, so a backup cannot capture volumes
       → run `docker pull busybox@sha256:…` — do it now rather than during a
         backup on a machine that has lost its network
```

The last two are the ones that matter here. Both are warnings rather than
failures because needing the network is the normal case — but on a machine that
is about to lose it, a warning is the difference between a planned preload and a
failed boot at 3am.

The helper image is the manager's own, not the release's: volumes are read
through a container, so a backup on a disconnected machine that never pulled it
captures nothing. It is the one image that is not in the release manifest, which
is exactly why it is easy to miss.

**Preload the reference `doctor` prints, not `busybox`.** If this deployment sets
`MORZER_VOLUME_HELPER_IMAGE`, that is the image volume capture will run, and the
default busybox is not pulled at all. `doctor` reports whichever one is
configured, so its output is the reference to copy — checking it beats assuming.

## Prepare the artifacts

On a machine that *does* have a network:

```sh
# 1. The bundle, as a single file.
morzer release fetch https://releases.example/demo-1.3.0.tar.zst
# or download it directly; the file is what matters, not how it arrived.

# 2. The images the manifest pins, by digest.
morzer release show 1.3.0 | grep -A20 images
docker pull registry.example/demo/app@sha256:…
docker pull registry.example/demo/db@sha256:…
# 3. The manager's volume helper image, which is not in the manifest.
#    Take the exact reference from `morzer doctor` -- it is busybox unless
#    MORZER_VOLUME_HELPER_IMAGE names another, and then it is that one.
HELPER=$(morzer doctor --json |
    jq -r '.data.results[] | select(.id == "backup.volume-helper") | .detail' |
    grep -o '[^ ]*@sha256:[a-f0-9]*')
docker pull "$HELPER"

docker save -o images.tar \
    registry.example/demo/app@sha256:… \
    registry.example/demo/db@sha256:… \
    "$HELPER"
```

Copy `demo-1.3.0.tar.zst` and `images.tar` to the target machine, along with
the vendor's signing key if the installation requires signatures.

## Install on the disconnected machine

```sh
# 1. Load the images into the local store.
docker load < images.tar

# 2. Install the manager's own state. No network needed: the bundle is a file.
morzer init --release ./demo-1.3.0.tar.zst \
    --profile embedded \
    --recovery-recipient age1… \
    --signing-key RWQ…

# 3. Converge, without pulling.
morzer apply --startup

# 4. Confirm.
morzer doctor
```

`--startup` is the flag that makes this work. It was written for boot-time
convergence — a machine restarting in a datacentre that has lost its uplink —
and skips pulls when the images are already local, and migrations when the
schema is already current. Those are the same two conditions an offline install
has.

Without it, `apply` pulls every image, which on a disconnected machine is a
timeout rather than an error you can act on.

## Updating offline

The same shape, with `update` instead of `init`:

```sh
docker load < images-1.4.0.tar
morzer update ./demo-1.4.0.tar.zst
morzer apply --startup
```

Take the pre-update backup seriously here: the recovery path for a failed
offline update is a restore, and a restore that needs to download anything is
not a recovery.

## What still needs a network

- **`release fetch` from `https://` or `oci://`.** By definition. Fetch on a
  connected machine and copy the archive.
- **The registry-reachable check in `doctor`.** It will warn; that is correct
  and expected on a disconnected host.
- **Pulling an image the manifest pins that you did not preload.** The manifest
  pins by digest, so `docker save`/`load` carries exactly the right bytes —
  there is no tag to resolve and nothing to get wrong.
- **Capturing volumes without the helper image.** The backup **fails**, with the
  `docker pull` command rather than a registry error. It does not quietly
  produce a backup missing the volumes, because a backup that silently covers
  less than it claims is the failure the whole component exists to prevent. If
  you deliberately want one anyway, scope it:
  `morzer backup --component database --component config --component secrets`.

## Verifying without the network either

Nothing in verification needs one. The bundle's `SHA256SUMS` and its
`SHA256SUMS.minisig` travel inside the archive, and the signing key is
configuration on the machine. An offline install is verified exactly as
thoroughly as a connected one:

```sh
morzer release verify ./demo-1.3.0.tar.zst --signing-key RWQ…
```

See [Verification](../reference/release-commands.md#verification).
