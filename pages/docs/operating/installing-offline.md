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

!!! warning "`docker save` and `docker load` do not carry an image's digest"

    Earlier versions of this page told you to move images with
    `docker save`/`docker load`. That does not work, and it fails in the way
    that is hardest to see: the load succeeds and the image is *there*, but it
    no longer answers to the `name@sha256:…` the manifest pins, so the manager
    sees it as absent and tries to pull.

    Measured on Docker 29.6.2 — saving an image pulled by digest produces a
    tarball whose `manifest.json` reads `"RepoTags": null` with no
    `RepoDigests` anywhere, because a registry *manifest* digest is not
    something save and load deal in.

    The supported answer is [bundled images](../authoring/bundled-images.md):
    the vendor packs them into the release, and the whole problem stops being
    yours. What to do when the release you have does not bundle them is
    [below](#a-release-that-does-not-bundle-its-images).

!!! warning "Print this, or keep it where you can read it offline"

    A procedure for a machine with no network access is a procedure you cannot
    look up from that machine. It is also in the repository, in
    `pages/docs/operating/installing-offline.md`.

## Check first, while there is still a network

```sh
morzer doctor
```

Four checks answer the question:

```text
[ok]   container registry is reachable
[warn] release images are available offline
       2 of 2 pulled image(s) are not local: registry.example/demo/app, registry.example/demo/db
       → run `morzer apply` while online; if this machine has to come up
         without network access, the durable answer is a release that marks
         these images `from: bundle` so they travel with it
[ok]   images the bundle carries are loaded: all 1 bundled image(s) are loaded
[warn] the volume helper image is available offline
       busybox is not on this machine, so a backup cannot capture volumes
       → run `docker pull busybox@sha256:…` — do it now rather than during a
         backup on a machine that has lost its network
```

The two warnings are the ones that matter here. Both are warnings rather than
failures because needing the network is the normal case — but on a machine that
is about to lose it, a warning is the difference between a planned preload and a
failed boot at 3am.

`images the bundle carries are loaded` is not a warning. It is fatal, and it is
the only one of the four that `apply` refuses on: an image that travels in the
bundle is either loaded or the deployment does not start. `morzer release
ingest` loads it, out of the bundle, with no network.

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
# 1. The bundle, as a single file. Its images travel inside it.
morzer release fetch https://releases.example/demo-1.3.0.tar.zst
# or download it directly; the file is what matters, not how it arrived.

# 2. The manager's volume helper image, which is not in the manifest and does
#    not travel in any bundle. Take the exact reference from `morzer doctor` --
#    it is busybox unless MORZER_VOLUME_HELPER_IMAGE names another.
HELPER=$(morzer doctor --json |
    jq -r '.data.results[] | select(.id == "backup.volume-helper")
           | .message, .remedy' |
    grep -o '[^ ]*@sha256:[a-f0-9]\{64\}' | head -1)
test -n "$HELPER" || { echo "doctor did not report a helper image" >&2; exit 1; }
docker pull "$HELPER"
docker save -o helper.tar "$HELPER"
```

`docker save` is fine for the helper and wrong for the release's images, and the
difference is not arbitrary. The manager finds the helper **by tag**, which a
save tarball carries; it finds a release image **by digest**, which a save
tarball does not.

Copy `demo-1.3.0.tar.zst` and `helper.tar` to the target machine, along with the
vendor's signing key if the installation requires signatures.

## Install on the disconnected machine

```sh
# 1. The volume helper, so backups can capture volumes later.
docker load < helper.tar

# 2. Install the manager's own state. No network needed: the bundle is a file,
#    and staging it loads the images it carries.
morzer init --release ./demo-1.3.0.tar.zst \
    --profile embedded \
    --recovery-recipient age1… \
    --signing-key RWQ…

# 3. Converge.
morzer apply

# 4. Confirm.
morzer doctor
```

There is no preload step for the release's own images and no `--startup` needed
to skip pulling them. `init` serves the bundle's `images/` layout to the
container runtime over loopback and has it load each one — so by the time
`apply` runs, they are already in the local store and the pull step has nothing
left to fetch.

`apply --startup` still exists and still matters, for the machine that reboots
into a datacentre that has lost its uplink: it skips pulls when images are
local, and migrations when the schema is current. An install whose images all
travel in the bundle simply does not need it.

## A release that does not bundle its images

If the manifest marks nothing `from: bundle`, its images come from a registry,
and there is no supported way to carry them to a disconnected machine by hand.
`docker save` and `docker load` will not do it — see the warning at the top of
this page — and no `docker tag` can repair the result, because a tag cannot name
a digest.

What works:

- **Ask the vendor to bundle them.** One line per image in their manifest, and
  the problem is theirs to solve once rather than yours to solve per machine.
  Point them at [bundled images](../authoring/bundled-images.md).
- **Pull on the machine while it still has a network.** A real pull records the
  digest, so `morzer apply` while connected — or plain `docker pull` of each
  reference `morzer release show` lists — leaves images that survive losing the
  uplink afterwards. This is what `doctor`'s "release images are available
  offline" warning is telling you to do, and it is why it asks *before* the
  network goes.
- **Run a registry the machine can reach.** A mirror on the same network is not
  an air-gapped install, but it is a real answer for a site that has no route to
  the internet and does have one to the next rack.

None of these is a workaround for a machine that is already disconnected and
already missing an image. That case needs the bundle to have carried it.

## Updating offline

The same shape, with `update` instead of `init`:

```sh
morzer update ./demo-1.4.0.tar.zst
```

`update` stages the bundle and loads the images it carries, then converges — so
for a release that bundles its images there is nothing to copy alongside the
archive.

Take the pre-update backup seriously here: the recovery path for a failed
offline update is a restore, and a restore that needs to download anything is
not a recovery.

## What still needs a network

- **`release fetch` from `https://` or `oci://`.** By definition. Fetch on a
  connected machine and copy the archive.
- **The registry-reachable check in `doctor`.** It will warn; that is correct
  and expected on a disconnected host.
- **Pulling an image the manifest pins that does not travel in the bundle.**
  There is no offline path for one, and `docker save`/`load` is not it: the
  digest the manifest pins does not survive the round trip, so the image loads
  and then reads as absent. See [above](#a-release-that-does-not-bundle-its-images).
- **Capturing volumes without the helper image.** The backup **fails**, with the
  `docker pull` command rather than a registry error. It does not quietly
  produce a backup missing the volumes, because a backup that silently covers
  less than it claims is the failure the whole component exists to prevent. If
  you deliberately want one anyway, scope the volumes out:
  `morzer backup --component database --component config --component secrets`.
  That only helps a release **with** a backup hook. One without a hook has
  nothing left to put in a backup once volumes are excluded, so it is refused
  rather than given an empty one — for such a release the helper image is not
  optional.

## Verifying without the network either

Nothing in verification needs one. The bundle's `SHA256SUMS` and its
`SHA256SUMS.minisig` travel inside the archive, and the signing key is
configuration on the machine. An offline install is verified exactly as
thoroughly as a connected one:

```sh
morzer release verify ./demo-1.3.0.tar.zst --signing-key RWQ…
```

See [Verification](../reference/release-commands.md#verification).
