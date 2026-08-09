---
title: Bundled images
icon: lucide/container
summary: Shipping your private container images inside the bundle, so customers need no registry credentials
---

# Bundled images

A release can carry its own container images. The customer installs it without
ever holding credentials for the registry those images came from.

## Why

Shipping a private image otherwise means giving your customer a credential for
your registry. Cloud registries frequently cannot express "this one customer
may read these three repositories" — ECR, Artifact Registry and ACR grant at IAM
or project scope, so the smallest credential you can issue is often far larger
than the access you meant to give. The alternatives are to over-grant, to run a
per-customer registry, or to not ship private images.

## Saying which images travel

Per-image, so a release bundles what is private and keeps pulling what is
public:

```yaml title="manifest.yaml"
images:
  db: postgres@sha256:0000…0002              # still pulled from Docker Hub
  app:
    ref: registry.example/demo/app@sha256:0000…0001
    from: bundle
  api:
    ref: registry.example/demo/api@sha256:0000…0003
    from: bundle
```

That is the design. Bundling everything and bundling nothing are its two
extremes — the mixed case, where two private images ship beside four public
ones, is the one you cannot express without per-image choice, and it is the
common one.

`ref` is unchanged: still pinned by digest, still the reference Compose
receives. Where the bytes came from is metadata about the image, never part of
its name.

## The layout

Bundled images live in a single OCI image layout under `images/`:

```text
my-product/
├── manifest.yaml
├── images/
│   ├── oci-layout
│   ├── index.json          # names each image and its manifest digest
│   └── blobs/sha256/…      # manifests, configs, layers
├── SHA256SUMS
└── SHA256SUMS.minisig
```

An OCI layout rather than `docker save` output, and the reason is not
aesthetic. **`docker save` does not preserve the registry digest.** Saving an
image pulled by digest produces a tarball whose `manifest.json` carries
`RepoTags: null` and no `RepoDigests` at all, so `docker load` restores the
image by content ID and `docker image inspect registry.example/demo/app@sha256:…`
fails afterwards. The digest a manifest pins is the *registry manifest* digest;
save and load deal in config and layer digests, which are different
identifiers. An OCI layout keeps the one the manifest pins.

Every file under `images/` is an ordinary file, so the integrity chain covers
it with no addition: `SHA256SUMS` lists it, the signature covers `SHA256SUMS`,
and a file the list does not name fails verification exactly as a hook would.

## What `release verify` checks

Beyond the ordinary checks, for a bundle that declares any image `from: bundle`:

| Situation | Result |
| --- | --- |
| No `images/index.json` | Refused — the manifest promises images the bundle does not carry. |
| An image marked `from: bundle` that `index.json` does not name by the same digest | Refused, naming the image. |
| An `index.json` entry no manifest image names | Refused. A layout carrying more than the release declares is a bundle whose contents nobody stated. |
| An `index.json` entry whose blob the bundle does not carry | Refused. An index that outruns its blobs passes here and fails at install, on the machine with no registry to fall back to. |

The completeness rule is the same one `SHA256SUMS` already holds, one level up:
a bundle must be exactly what it says it is, in both directions.

## Producing one

```sh
morzer release pack ./my-product
morzer release build ./my-product --version 1.4.0
minisign -Sm ./my-product/SHA256SUMS
morzer release archive ./my-product
```

`pack` copies each `from: bundle` image out of its registry into the layout,
using the credentials your build machine already has. See
[release pack](../reference/release-commands.md#release-pack) for its flags and
refusals.

### By hand

If your build pipeline cannot run morzer, `skopeo` writes the same layout:

```sh
skopeo copy \
    docker://registry.example/demo/app@sha256:0000…0001 \
    oci:./my-product/images:app
```

Then regenerate the checksum list and sign, as always. `release verify` is what
tells you whether the result is well formed.

!!! warning "Put `manifest.yaml` first if you roll your own `tar`"

    A bundle carrying images has its extraction budget read from the manifest
    *before* anything large is extracted, which only works while the manifest is
    the archive's first entry. `release archive` does this for you; a hand-rolled
    `tar` that does not is refused. See
    [publishing](publishing.md#if-you-roll-your-own-tar).

## Size

Measured from real layouts, because the numbers are not what estimating
suggests:

| Image | Layout on disk | Largest single blob |
| --- | --- | --- |
| `postgres` | 115 M | 110 M |
| `minio` | 60 M | 38 M |
| `redis` | 38 M | 33 M |
| `caddy` | 23 M | 16 M |

Two things fall out. A layout is roughly **40% of the uncompressed image size**,
because blobs are stored compressed. And an image is usually **one dominant
blob** rather than an even spread — 110 M of `postgres`'s 115 M is a single
layer.

A bundle that carries everything is a bundle every transport has to move, and a
release that was 2 MB becomes 800 MB. Bundling only what is private is what
keeps that proportionate.

Declare what the bundle expands to, so the manager can size its extraction
budget:

```yaml title="manifest.yaml"
bundle:
  uncompressed_size: 12GiB
```

Without it the default ceiling applies, and a large bundle is refused. It must
cover the whole archive **including the manifest**: a declaration too small for
its own manifest is refused, because a remaining budget of zero would otherwise
read as no budget at all.

!!! info "What the customer's manager trusts, and why"

    A bundled image is trusted because **your signature covers its bytes** —
    not because a registry served it over TLS. That is a different guarantee,
    and arguably a stronger one: the chain is signature → `SHA256SUMS` → blob
    bytes → computed digest, all of it checkable on a machine with no network.

    It also means an unsigned bundle carrying images asks the customer to trust
    the transport alone. Sign your releases.
