# RFC 0011 — Bundled container images

- **Status:** 🚧 In progress — **P1 shipped 2026-08-09**: the manifest's dual
  image spelling, the layout's completeness refusals in both directions, and the
  extraction budget with its hard caps, its manifest-first requirement and its
  free-space preflight. A bundle carrying images is now *inspectable*; nothing
  installs one yet. **P2 (ingest via the ephemeral registry) is the phase that
  either works or sends the design back**, and nothing after it is worth
  scheduling until it lands. §5.4's `doctor` vocabulary rides with P3, since a
  check distinguishing "present in the bundle, not yet loaded" needs the loading
  to exist. **Design locked** 2026-08-08. Every
  question in §10 is resolved into §11; the ingest mechanism (§5.3) is verified
  by spike. Ready to execute from P1. **Amended 2026-08-08** (decision 15):
  decision 11's archive-ordering requirement is owned by
  [0014](0014-building-a-release-bundle.md) rather than
  [0012](0012-packing-images-into-a-bundle.md), and the budget read now fails
  closed when the ordering is not honoured — so P1 gains a dependency on
  0014 P1. **Amended 2026-08-09 (decisions 18–21): P2 sent the design back,
  which is what P2 was for.** Measured against a real daemon, a bundled image
  cannot answer to the reference the manifest pins — no supported command
  creates a digest reference for a repository the daemon did not pull from — so
  decisions 5, 17 and half of §5.3 do not survive. What replaces them: the
  registry moves *into the manager's process*, and Compose receives a
  manager-created alias while `ref` stays the identity. §5.3 carries the
  measurements.
- **Scope:** Lets a release bundle carry its own container images, so installing
  it needs no credentials for the vendor's registry. Per-image: the manifest
  says which images travel in the bundle and which are still pulled, so a
  release can bundle its two private images and keep pulling `postgres` from
  Docker Hub. Covers the manifest change, the bundle layout, the verification
  chain, the ingest that makes a bundled image satisfy a digest-pinned
  reference, and the `apply` / `doctor` integration. Does **not** cover how a
  vendor produces such a bundle — that is [0012](0012-packing-images-into-a-bundle.md) —
  and does not change how pulled images are pulled. Reverses one non-goal of
  [0004](0004-distribution-and-verification.md); decision 1 is settled in favour
  of building it.
- **Related:** [`internal/domain/manifest.go`](../internal/domain/manifest.go)
  (`Images`, `ImageRefs`) ·
  [`internal/adapters/runtime/compose/compose.go`](../internal/adapters/runtime/compose/compose.go)
  (`Pull`, `HasImage`) ·
  [`internal/lifecycle/ops/apply.go`](../internal/lifecycle/ops/apply.go)
  (`stepPullImages`) ·
  [`internal/lifecycle/ops/doctor.go`](../internal/lifecycle/ops/doctor.go)
  (`checkImagesLocal`, `checkRegistryReachable`) ·
  [`internal/ports/runtime.go`](../internal/ports/runtime.go) (`ImageInspector`) ·
  [`internal/adapters/verify/checksum/checksum.go`](../internal/adapters/verify/checksum/checksum.go) ·
  [0004](0004-distribution-and-verification.md) (transports, verification, the
  non-goal this reverses) · [0010](0010-compose-volume-capture.md) (the manager's
  first image of its own) · [0012](0012-packing-images-into-a-bundle.md) ·
  [0014](0014-building-a-release-bundle.md) (which owns the archive ordering
  decision 11 depends on)

---

## 1. Summary

A release manifest may mark an image as travelling in the bundle rather than
being pulled. Such images ship as an OCI image layout under `images/` in the
bundle, covered by the same `SHA256SUMS` and minisign signature as every other
file. At install time the manager ingests them into the local image store and
`apply` proceeds unchanged, because `Pull` already skips images that are
present. The result: a customer installs a release containing private images
without ever holding credentials for the registry those images came from.

Per-image, not per-bundle. A release bundles what is private and keeps pulling
what is public, which is what keeps bundles from growing by a gigabyte of
Postgres nobody needed to ship.

## 2. Motivation

**Today, shipping a private image requires giving the customer registry
credentials.** The manager has no credential handling of its own: every pull
goes through the runtime with whatever ambient `docker login` the host has.
The documentation says so plainly —
[`publishing.md:128`](../pages/docs/authoring/publishing.md): "…has run
`docker login` for the registry your *images* come from can already…", and the
`doctor` hint at [`doctor.go:679`](../internal/lifecycle/ops/doctor.go) tells an
operator to "check network access and registry credentials (`docker login`)".

That is a workable answer when the images are public. It is not when they are
not. Cloud registries frequently cannot express "this one customer may read
these three repositories": ECR, Artifact Registry and ACR grant at IAM or
project scope, so the smallest credential a vendor can issue is often far
larger than the access intended. The vendor's options today are to over-grant,
to run a per-customer registry, or to not ship private images.

**The manager already advertises the capability it cannot deliver.** `doctor`
ships `runtime.images-local` — "release images are available offline"
([`doctor.go:705`](../internal/lifecycle/ops/doctor.go)) — which warns when a
release's images are not in the local store. Its own doc comment says it exists
for "the question an operator has to answer *before* losing network access".
There is a check for the condition and no supported way to satisfy it. The only
remedy is a `docker pull` against the registry whose credentials are the
problem.

**The deployment path is already shaped for this.** `Pull` skips any image that
is already present, with the reason recorded at
[`compose.go:183`](../internal/adapters/runtime/compose/compose.go): "Already
here is already correct: a digest-pinned reference cannot mean different bytes
on a second pull, and skipping is what lets a boot-time apply work without a
network." `apply --startup` skips the pull step entirely. Nothing in the
converge path needs to change — only the getting-images-here part is missing.

**Precedent exists at one-image scale.** [0010](0010-compose-volume-capture.md)
shipped "the manager's first image of its own (busybox, pinned, local-first,
reported by `doctor`)", and the same offline problem followed: the volume helper
must be pulled ahead of time or backups refuse. This RFC generalises a problem
the codebase has already met once.

## 3. Current state

Verified against the tree at `d81d1c1`.

| Fact | Where |
| --- | --- |
| Images are a flat `map[string]string` of name → reference | [`manifest.go:59`](../internal/domain/manifest.go) |
| References must be `name@sha256:…`; the manifest is the authority on what a release consists of | [`compose.go:167-176`](../internal/adapters/runtime/compose/compose.go) |
| Each image is exported to Compose as `<PRODUCT>_IMAGE_<NAME>` | [`ops.go:387`](../internal/lifecycle/ops/ops.go) |
| `Pull` skips images already present, one reference at a time | [`compose.go:183-188`](../internal/adapters/runtime/compose/compose.go) |
| `HasImage` decides presence with `docker image inspect <ref>` | [`compose.go:516`](../internal/adapters/runtime/compose/compose.go) |
| `ImageInspector` is optional and type-asserted; callers degrade | [`runtime.go:268-282`](../internal/ports/runtime.go) |
| Verification chain is signature → `SHA256SUMS` → every file, and an unlisted file fails closed | [`checksum.go:158-161`](../internal/adapters/verify/checksum/checksum.go) |
| Extraction limits: 20 000 entries, 2 GiB total, 1 GiB per file | [`copy.go:49-51`](../internal/infra/atomicfs/copy.go) |
| `oras-go/v2` is already a direct dependency, including an on-disk OCI layout store (`content/oci.Store`) | [`go.mod:29`](../go.mod) |
| Vendors build bundles with `tar`, `sha256sum` and `minisign`; the manager ships no packing tooling | [`publishing.md:12-35`](../pages/docs/authoring/publishing.md) |
| `release` has `list`, `show`, `verify`, `fetch`, `prune` — no `pack` | [`release.go`](../internal/cli/release.go) |

**The surprising fact that shapes the whole design.** `docker save` does not
preserve the registry digest. Saving an image pulled by digest produces a
tarball whose `manifest.json` reads:

```json
[{ "Config": "blobs/sha256/db287c…", "RepoTags": null, "Layers": ["blobs/sha256/412f8a…"] }]
```

`RepoTags: null`, and no `RepoDigests` anywhere. `docker load` therefore
restores the image by content ID only — the `name@sha256:…` reference is not
reconstructed, and `docker image inspect registry.example/demo/app@sha256:…`
fails afterwards. The digest in a manifest is the *registry manifest* digest;
save/load deals in config and layer digests, which are different identifiers.

Consequence: the obvious implementation — `docker save` at pack time,
`docker load` at install time — **does not work**. `HasImage` returns false for
every bundled image, `Pull` tries the registry the customer has no credentials
for, and the install fails at exactly the step the bundle existed to remove.
Everything in §5.3 exists to answer this.

## 4. Goals / Non-goals

**Goals**

- Install a release whose private images never leave the vendor, without the
  customer holding credentials for the vendor's registry.
- Per-image choice, so a bundle carries what is private and pulls what is
  public.
- A bundled image is verified by the same chain as the rest of the bundle:
  signature → `SHA256SUMS` → bytes → digest.
- `apply`, `update` and `rollback` work unchanged once images are present.
- An operator can tell, before losing network access, which images are already
  here — the question `runtime.images-local` already asks.

**Non-goals**

- **Changing how pulled images are pulled.** Ambient `docker login` stays the
  mechanism for everything not bundled. This RFC adds a second source; it does
  not replace the first.
- **Credential management.** The manager still holds no registry credentials of
  its own, for bundled images or otherwise. Avoiding the need for them is the
  point; storing them is a different design.
- **A runtime other than Compose/Docker.** Ingest is runtime-specific by
  nature. The port stays optional and type-asserted, like `ImageInspector`, so
  a runtime with no local store is not made to lie.
- **Producing bundles.** Packing is [0012](0012-packing-images-into-a-bundle.md).
  This RFC defines the format; a vendor can hand-build one with `skopeo` until
  0012 ships.
- **Multi-architecture selection at install time.** §8.

## 5. Design

### 5.1 The manifest says where an image comes from

`Images` becomes `map[string]ImageSpec`, where an `ImageSpec` decodes from
either a bare string (today's spelling — pull it) or a mapping:

```yaml
images:
  db: postgres@sha256:…                       # unchanged: pulled
  app:
    ref: registry.example/demo/app@sha256:…   # the identity, unchanged
    from: bundle                              # where the bytes come from
```

`from` takes `registry` (default) or `bundle`. A manifest that does not care
says nothing — which is the point of the default, not a compatibility
concession: nothing has been released, so there are no old manifests to keep
working. The reason to accept both the scalar and the mapping form is that most
images will never be bundled, and making every one of them carry a `from:` key
to say so is noise in the file a vendor reads most.

The dual shape is not invention either: `PortSpec` already accepts an integer or
a string ([`manifest.go:168`](../internal/domain/manifest.go)), so the parsing
pattern and its tests exist.

**Three delivery modes fall out of one mechanism**, and none of them is a
separate feature:

| Mode | Manifest | Result |
| --- | --- | --- |
| Everything pulled | no `from: bundle` anywhere | today's behaviour, byte for byte |
| Mixed | `from: bundle` on the private images | bundle carries those; `postgres` and friends still pulled |
| Nothing pulled | `from: bundle` on every image | installs with no registry contact at all |

The mixed case is the design. The other two are its empty and full extremes,
which is why per-image granularity is decision 2 rather than an enhancement:
build only the endpoints and the middle case — the common one, where a vendor's
two private images ship beside four public ones — is the one you cannot express.

**`ref` stays a real image reference in both spellings.** It is interpolated
into Compose as `<PRODUCT>_IMAGE_<NAME>`
([`ops.go:387`](../internal/lifecycle/ops/ops.go)), so it must remain something
the daemon can resolve. Where the bytes came from is metadata about the image,
never part of its name.

#### Alternatives considered — the manifest shape

**Scheme dispatch on the reference** — `bundle://demo/app@sha256:…`, mirroring
the source registry ([0004](0004-distribution-and-verification.md)) and the
target registry ([0009](0009-backup-targets.md)). Rejected: the reference
reaches Compose, and a scheme prefix would reach the daemon and fail. The
house pattern is right for *transports*, where the manager consumes the
reference; it is wrong here, where something else does.

**A separate `bundled: [app, api]` list** naming which of `images` travel.
Rejected as a second place to forget: adding an image would mean editing two
blocks, and the schema could not express that a name in one must exist in the
other.

### 5.2 Bundle layout

Bundled images live in a single OCI image layout, a documented standard:

```
bundle/
├── manifest.yaml
├── images/
│   ├── oci-layout
│   ├── index.json          ← names each image and its manifest digest
│   └── blobs/sha256/…      ← manifests, configs, layers
├── SHA256SUMS
└── SHA256SUMS.minisig
```

An OCI layout rather than `docker save` output because it preserves the
registry manifest and therefore its digest — the identifier `manifest.yaml`
pins and `HasImage` asks about. `docker save` discards it (§3).

Every file under `images/` is an ordinary file, so the existing chain covers it
with no change: `SHA256SUMS` lists it, the signature covers `SHA256SUMS`, and
the SEC-1 rule that an unlisted file fails the verification
([`checksum.go:158`](../internal/adapters/verify/checksum/checksum.go)) applies
to image blobs exactly as to hooks.

`index.json` must name every image the manifest marks `from: bundle`, by the
same digest. A bundled image the layout does not carry, or a layout entry no
manifest names, fails verification — the same completeness rule `SHA256SUMS`
already holds, one level up.

### 5.3 Ingest: making a bundled image satisfy its digest

This is the load-bearing part, and **the part P2 sent back**. What follows is
the amended design (decisions 18–21); the original is kept below it, because
what was believed and why it was wrong is the most useful thing this section
carries.

#### What P2 measured

Docker 29.6.2, `overlay2`, no containerd image store — the configuration the
original spike also ran on. Three results:

| Attempt | Result |
| --- | --- |
| pull `127.0.0.1:<port>/demo/app@sha256:X`, then `docker image inspect registry.example/demo/app@sha256:X` | **`No such image`.** `RepoDigests` holds `127.0.0.1:<port>/demo/app@sha256:X` and nothing else |
| `docker tag <src> registry.example/demo/app@sha256:X` | **`refusing to create a tag with a digest reference`** |
| `docker load` of an OCI image layout | **`invalid archive: does not contain a manifest.json`** |

The first kills the primary path's central claim. A digest reference resolves
through the daemon's reference store, which records the name that was *pulled*;
a repository the daemon never contacted has no entry, and nothing puts one
there. The second kills decision 17's repair: a tag names `repo:tag`, never
`repo@digest`, so the fallback could not have tagged an image with the
reference the manifest pins either. The third means the fallback's load step
needed a format conversion nobody designed.

**How the original spike passed.** It inspected `<ref>@sha256:40baa8cf…` where
`<ref>` was the *loopback* repository — the same name it had just pulled. The
evidence line below records exactly that: `.RepoDigests contains
127.0.0.1:.../ingested/app@sha256:…`. The one inspect that mattered, of the
vendor's own reference, was never run. A spike that verifies the step you
doubted and skips the step you assumed is a spike that confirms the assumption
it was built to test.

#### What replaces it

**One mechanism, and the registry is in-process.** The manager serves the
bundle's OCI layout over the distribution API from its own process, on
`127.0.0.1:0`, read-only, for the duration of one ingest; the runtime pulls
each bundled image from `127.0.0.1:<port>/<repo>@sha256:…`; the manager then
tags the result under the alias of decision 19 and drops the loopback
reference. Verified by a cold pull against a real daemon — the requests the
daemon made were `GET /v2/`, one manifest fetch by digest, and one blob fetch
per layer.

The container form is withdrawn (decision 18) for a reason the original
understated. It called `registry:2` "a bootstrap dependency, handled the way
[0010](0010-compose-volume-capture.md) handled the volume helper" — but busybox
is needed for a *backup*, on a machine that has already installed something,
while this is needed for the *install*. A customer who cannot reach a registry
cannot obtain the registry image, so the primary path would have been
unavailable in precisely the case the feature exists for, and every such
install would have fallen to the path with the least testing.

**Verification is not lost with the fallback.** The daemon performs a real V2
pull, which verifies every blob against the digest the manifest names — so a
corrupted blob is refused by the puller rather than recorded, which is
decision 6's requirement met by the primary path instead of by a second one.
§6 tests it by corrupting a blob and watching the ingest refuse, against a real
daemon rather than a fake that would agree with whatever it was told.

#### The original design, superseded

**Primary — an ephemeral local registry.** The manager starts a registry
container bound to `127.0.0.1` on an ephemeral port, serves the bundle's OCI
layout from it, and has the runtime pull each bundled image from
`127.0.0.1:<port>/…@sha256:…`. Because the daemon performs a real V2 pull, it
computes and commits `RepoDigests` itself: afterwards the image answers to its
original reference with no further intervention, and `HasImage` needs no
special case at all.

~~**Verified end to end**~~ — the claim below is false, and the table above
says how. Kept verbatim, against a real registry and a real daemon:

```
oras.Copy  registry -> OCI layout   index.json digest = sha256:40baa8cf…
oras.Copy  OCI layout -> registry   descriptor digest = sha256:40baa8cf…
docker pull <ref>@sha256:40baa8cf…  Status: Downloaded
docker image inspect <ref>@sha256:40baa8cf…  -> resolves
  .RepoDigests contains 127.0.0.1:.../ingested/app@sha256:40baa8cf…
```

The digest the registry reported on push survives the copy into the layout, the
copy back out, and the pull — that much is true, and the amended design still
rests on it. What does not follow is the next sentence: the daemon records the
`RepoDigest` of the name it pulled, which is the loopback one, so a bundled
image is **not** indistinguishable from a pulled one and `HasImage` on the
manifest's reference returns false. Decision 19 exists because of this line.

This is not a novel trick in this repository — the acceptance scenario has run
a throwaway registry since [0005](0005-continuous-integration-and-release.md)
for precisely this reason, recorded at
[`acceptance.sh:158`](../.github/scripts/acceptance.sh): "An image built locally
has no digest until it is pushed -- `RepoDigests` is empty -- and the manifest
requires `name@sha256:…`". `start_registry` there already handles readiness
polling, name collision and teardown ownership.

The registry image itself is a bootstrap dependency, handled the way
[0010](0010-compose-volume-capture.md) handled the volume helper: pinned by
digest in the manager's own source, local-first, and reported by `doctor`. —
*Withdrawn by decision 18: an install that needs an image from a registry is
not an install that works without one.*

**Fallback — a verified local index.** Where no registry helper can run, the
manager ingests the layout directly, verifies each blob against the digest
`index.json` claims, loads it into the daemon, and **tags the loaded image with
the reference the manifest pins** so the daemon answers to it. The mapping from
manifest reference to local image ID is recomputed from the bundle each time it
is needed and never written to installation state (decision 14).

Both halves of that sentence are corrections. An earlier draft said the mapping
was *recorded in installation state*, contradicting decision 14 in the same
document — and recording it would not have been enough anyway: `Pull` and the
Compose project both work from the reference, so a mapping the manager consults
privately leaves the daemon unable to create the service. The image has to
answer to the name, which means tagging it, not remembering it.

The fallback is deliberately *verified*, not merely recorded. The trust chain
is minisign signature → `SHA256SUMS` → blob bytes → computed digest → local
image ID — which is stronger than the pulled path, where the guarantee rests on
a registry's TLS. A mapping trusted without recomputing the digest would assert
"these bytes came from that digest" without checking, which is the property
[0005 §12](0005-continuous-integration-and-release.md) records the acceptance
run finding absent — "**The manifest's images decided nothing.**", where a
digest-pinned `images` map "had **no effect on what actually ran**", and "Every
fake-backed test passed because the fake Runtime records the images it is handed
rather than resolving a Compose file". That must not be reintroduced, and the
last sentence is why §6 tests ingest against a real daemon.

`ImageInspector` is the seam. It is already optional and type-asserted, and its
doc comment already frames it as answering "will this deployment come up on a
host that cannot reach a registry"
([`runtime.go:268`](../internal/ports/runtime.go)) — this design is what that
sentence was waiting for. It asks about the alias of decision 19 rather than
about `ref`, which is the one adjustment the amendment forces on it.

*Withdrawn by decision 21.* With the registry in the manager's own process
there is no helper left to be unable to run, so the fallback answers a question
the amended design does not ask. Its verification obligation is not withdrawn
with it: see decision 6, now discharged by the daemon's own pull.

#### Alternatives considered — ingest

**`docker load` of a save tarball.** Rejected on evidence: it does not preserve
the registry digest (§3), so every bundled image reads as absent.

**Writing to the daemon's own metadata**, appending the target repository and
digest to the files under `/var/lib/docker/image/<driver>/distribution/` so the
engine reports the image as present by digest. Rejected, and worth recording
why: it is undocumented and storage-driver-specific, requires root, races a
daemon that caches that state in memory, and breaks entirely under the
containerd image store. The fatal objection is not fragility — it forges the
claim digest pinning exists to make, asserting provenance without verifying it.
It is the fallback above with the verification removed.

**`ctr -n moby images import`**, reaching into the daemon's containerd
namespace. Rejected as unsupported and version-coupled. `docker image import`
is sometimes suggested for this and does not do it at all — it creates a new
filesystem image from a rootfs tarball and assigns a new identity, which is the
opposite of preserving one.

**`docker load` on a containerd-backed daemon**, which does preserve digests
from an OCI archive. Not rejected — recorded as an optimisation the ingest may
detect and prefer, skipping the registry helper. It is not the foundation
because the image store backend is opt-in: the machine this RFC was written on
runs `overlay2`.

### 5.4 Install and converge

Ingest happens once, in `init` and in `update` — the operations that stage a
release — before the converge that uses the images. It is journaled like any
other step.

**It does not compensate**, which reverses the sentence this paragraph used to
end with. An ingested image is content-addressed and shared by digest, so the
alias a failed update would remove is the *same* alias a previous release
carrying the same image resolves through — and undoing the load would take the
deployment being rolled back to down with it. What compensation was protecting
against is disk, which [§8](#8-out-of-scope) already assigns to `docker`'s own
pruning.

**`apply` is not untouched**, which is the other thing decision 19 costs.
Three changes, all of them consequences of a bundled image answering to an
alias rather than to `ref`:

- `<PRODUCT>_IMAGE_<NAME>` carries the alias for a bundled image and `ref` for
  every other. Compose has to receive something the daemon can resolve, and for
  a bundled image that is the only such name there is.
- `Pull` receives the images whose source is a registry, not all of them.
  Passing a bundled image to `Pull` would send the deployment to the vendor's
  registry for bytes it already has — the exact contact the bundle exists to
  avoid — and it would do it under a reference no longer resolvable locally.
- A bundled image that is absent locally is a **refusal** (decision 20), raised
  in preflight, before anything mutates. Not a pull: the alias is a tag, and a
  tag the vendor's registry happens to serve would let a digest-pinned
  deployment converge on bytes nobody verified.

`apply --startup` continues to skip the pull entirely, and the refusal above
still runs — a boot-time apply on a machine whose image store was pruned should
say so rather than fail inside Compose.

`doctor` gains the vocabulary it lacks: `runtime.images-local` can distinguish
"absent" from "present in the bundle, not yet loaded" and name the command that
fixes it, instead of reporting a condition with no remedy.
`checkRegistryReachable` narrows to the images actually pulled — a release that
bundles everything should not warn about a registry it never contacts.

### 5.5 Size, and the limit that guards untrusted bytes

Measured, from real OCI layouts rather than estimated:

| image | layout on disk | largest single blob |
| --- | --- | --- |
| `postgres` | 115 M | 110 M |
| `minio` | 60 M | 38 M |
| `redis` | 38 M | 33 M |
| `caddy` | 23 M | 16 M |
| all four together | 234 M | |

Two facts fall out that the current limits were not set against. A layout is
roughly **40% of the uncompressed image size** (`postgres` is 289 M unpacked,
115 M as blobs) because blobs are stored compressed. And an image is usually
**one dominant blob**, not an even spread — 110 M of `postgres`'s 115 M is a
single layer. So `MaxFileSize` binds on approximately a whole image, not on
image-divided-by-layers.

A self-contained stack — frontend, two databases, a handful of services — lands
in the low gigabytes; one carrying a JVM, CUDA or model weights reaches tens.
The current ceilings (20 000 entries, **2 GiB total, 1 GiB per file**,
[`copy.go:49-51`](../internal/infra/atomicfs/copy.go)) refuse all of that.
`MaxEntries` is not a constraint at any size — an OCI layout is a handful of
files per image.

**The ordering makes raising them a security trade, not a config change.**
Extraction happens in `Fetch`
([`local.go:98`](../internal/adapters/source/local/local.go)) and verification
in a later step
([`update.go:322`](../internal/lifecycle/ops/update.go), `stepVerifyBundle`) —
so a bundle is written to disk **before** its signature is checked, and
`ExtractLimits` is the only thing standing between a hostile archive and the
disk. Raising the total 25× weakens that by 25×, and the signature cannot be
the mitigation because it is checked afterwards.

Three ways to raise the ceiling without simply removing the guard. **Decision
11 takes the first two together**; the third is recorded as rejected:

1. **A declared budget read from the stream, under a hard cap.** `manifest.yaml`
   is an entry in the same tar; read it before committing to the rest and size
   the budget from what the release says it carries. An archive that then
   exceeds its own declaration is refused.

   **The declaration is untrusted, so it may only ever lower the ceiling.** The
   effective limit is `min(declared, hard_cap)` with `hard_cap = 50 GiB` total
   and `5 GiB` per file, and `MaxEntries` unchanged. A first draft of this
   section said the budget "keeps a bound proportional to a claim" — but the
   claim is made by the same unverified bytes the guard exists to bound, so an
   attacker declaring 500 GiB would simply have raised it. The free-space
   preflight does not substitute: on a machine with a large disk it passes.

   A bundle needing more than the cap is refused and says so, which is the
   correct answer for a limit that exists to bound *unverified* input. Raising
   it is a change to the manager, made once, in the open — not something a
   bundle can ask for.

   **Amended 2026-08-08 (decision 15):** those ordering constraints now have an
   owner — [0014 decision 2](0014-building-a-release-bundle.md) makes
   `manifest.yaml` the archive's first entry as a property of the format — and
   this read **fails closed** when it is not. An archive whose first entry is
   something else is refused rather than falling back to the default ceiling,
   because a fallback would make the whole mechanism opt-out for anyone rolling
   their own `tar`, which is alternative 3 arriving by the back door.
2. **A free-space preflight before extraction.** Refuse when the ceiling
   exceeds what the disk has, so the failure is a clean refusal rather than a
   full filesystem. [0010](0010-compose-volume-capture.md) already shipped "a
   space check that refuses before anything is written or stopped" — the same
   shape.
3. ~~**Opt-in.**~~ Rejected: it moves a security decision onto an operator who
   has no way to evaluate it, and a bundle that will not install without a flag
   is a bundle whose format does not carry what it claims.

What this RFC will not do is raise the numbers and say nothing. `DigestTree`
also hashes every file ([`copy.go:301`](../internal/infra/atomicfs/copy.go)), so
fetch, extract and digest all become proportional to image size — a 20 GiB
bundle is written once, read once more to digest, and that cost is real.

Per-image selection remains the mitigation that matters most: bundling only
what is private keeps the common case to the vendor's own layers.

## 6. Tests

- **Manifest decode**, both spellings, at the layer that decodes: a bare string
  and a mapping, and a `from` value nobody defined refused. The scalar suite's
  gap that let an unquoted mode decode wrongly for a whole remediation cycle is
  recent enough to be worth not repeating — decode tests go through YAML.
- **Layout completeness**, as a refusal: a manifest marking an image `bundle`
  with no matching `index.json` entry, and a layout entry no manifest names.
- **Verification**, extending the existing checksum suite: a tampered blob
  under `images/` fails; an unlisted blob fails; a bundle whose signature covers
  a `SHA256SUMS` that omits an image blob fails.
- **Ingest, against real Docker**, in the container suites — the only place the
  claim can be tested, because the claim is about what the daemon believes. The
  assertion that matters: after ingest, `HasImage` returns true for the
  *original* reference, and `Pull` performs no network operation. A test that
  asserted only "the image is present" would pass against the `docker load`
  implementation this RFC rejects.
- **A blob whose bytes do not match its claimed digest is refused** rather than
  recorded — against a real daemon, since decision 21 makes the daemon's own
  pull the thing that checks it. ~~Fallback ingest with the registry helper
  unavailable~~ has no subject left to test.
- **The alias resolves and `ref` does not**, asserted in the same test. Half of
  that is the regression guard: an implementation that quietly went back to
  handing Compose the manifest's reference would pass every test that only
  checked the alias.
- **Acceptance**, extending the scenario that already builds and pushes stub
  images: install a hybrid bundle with the registry stopped afterwards, proving
  the deployment converges with no reachable registry.

## 7. Docs

- `authoring/publishing.md` gains the bundled-image layout and the `from` field.
  Until [0012](0012-packing-images-into-a-bundle.md) ships, the procedure is
  manual (`skopeo copy` into `images/`) and must be documented as such rather
  than implied to be automatic.
- `reference/manifest.md` documents `images` in both spellings. The `mode` row
  in that file taught an unquoted octal for months; a field with two shapes
  deserves both spelled out.
- `operating/` gains the offline install story and what `doctor` now reports.
- The threat model needs care: a bundled image is trusted because the vendor's
  signature covers its bytes, *not* because a registry served it over TLS. That
  is a different guarantee — arguably stronger, and worth saying precisely
  rather than implying equivalence.

## 8. Out of scope

- **Multi-architecture selection.** An OCI layout can carry a multi-arch index,
  and this design does not choose between platforms at install time. A bundle
  targets the architecture it was packed for. Multi-arch bundles would multiply
  size by the number of platforms, which argues for per-platform bundles
  instead. Reopens if a vendor needs one artefact for mixed fleets.
- **Layer deduplication across releases.** A bundle is self-contained, so an
  update ships layers the machine may already have. Fixing that means either a
  content-addressed store the manager owns or a delta format — a larger design,
  and the mirror alternative in decision 1 avoids the problem entirely.
- **Bundled images for the manager's own helpers.** The busybox volume helper
  has the same offline problem, and this mechanism could subsume it. Left out
  to keep the first version to one concern; named because it is the obvious
  second customer.
- **Garbage collection of ingested images.** Images loaded for a release that
  was later rolled back stay in the local store. Existing `docker` pruning
  applies; a manager-owned lifecycle is a separate design.

## 9. Risks

- **The mirror alternative stays cheaper for every customer who can reach a
  registry.** Decision 1 chose bundling because some cannot, not because the
  mirror is worse where it applies. If the isolated deployments go away, this
  machinery outlives its reason — and the honest response then is to retire it,
  not to keep it because it exists.
- **The ingest mechanism carried design risk after all, and P2 found it.** The
  amended §5.3 is verified by a cold pull rather than by the reading of a spike
  that tested the adjacent thing. What remains is environment risk with a
  narrower shape: the manager serves the layout on its own loopback, so a
  daemon that does not share it — a remote `DOCKER_HOST`, and only that —
  cannot reach the ingest at all. There is no fallback behind it any more, so
  that case has to be a clear refusal rather than a quiet failure.
- **The alias is a tag, and tags are mutable.** Decision 19 buys local
  resolution with a name a registry could also serve. Decision 20 is what keeps
  that from mattering — absent means refuse, never pull — and if that refusal
  is ever softened to a pull "for convenience", digest pinning stops meaning
  anything for bundled images. It is one `if` away at all times.
- **Raising the extraction ceiling weakens a guard that runs on untrusted
  bytes.** Extraction precedes verification (§5.5), so `ExtractLimits` is the
  only defence against a hostile archive, and the signature cannot substitute
  for it. A 25× raise taken without one of §5.5's three mitigations is a real
  reduction in what the manager refuses.
- **Bundle size is a customer-visible regression.** A release that was 2 MB
  becomes 800 MB. Every transport in [0004](0004-distribution-and-verification.md)
  carries it, and HTTPS fetch of a large bundle over a poor link is a support
  case the current design has never had.
- **Verification could be silently skipped.** The fallback's value is entirely
  in recomputing digests. An implementation that records the mapping without
  verifying would pass every test that only asserts presence, and would be
  indistinguishable from the rejected metadata-forging alternative. The test in
  §6 asserting refusal of a mismatched blob is the guard, and it must exist
  before the fallback ships.
- **The manifest change touches a generated schema and a drift gate.** Two
  spellings of one field is exactly where a schema and its documentation
  disagree.

## 10. Unresolved questions

All five are resolved as of 2026-08-08 and recorded as decisions 1 and 11–14.
Kept here because what was open, and for how long, is part of the record.

1. ~~Build this, or run a distribution mirror?~~ → decision 1. **Build.** A
   mirror is practically available but does not deliver the capability: it
   still requires the customer to reach a registry, and customers exist for
   whom that is not true.
2. ~~Ingest in `init`/`update`, or an explicit command?~~ → decision 12. Both:
   an explicit command that the lifecycle calls.
3. ~~Bundled image also reachable from a registry — prefer, or compare?~~ →
   decision 13. Prefer the bundle.
4. ~~Where does the reference → image ID mapping live?~~ → decision 14.
   Recomputed, never persisted.
5. ~~How is the extraction ceiling raised?~~ → decision 11. A declared budget
   read from the tar stream, plus a free-space preflight.

What implementation is still free to settle: the ephemeral registry's port
selection and readiness timeout, the on-disk arrangement of the fallback's
recomputation, and the wording of every refusal.

## 11. Decisions

| # | Decision | Rationale and consequence |
| --- | --- | --- |
| 1 | **Build bundled images** rather than run a distribution mirror *(settled 2026-08-08)* | A mirror is practically available and solves the credential problem, but not the capability: it still requires the customer to reach a registry. Customers exist for whom that does not hold. Consequence: everything below is live, and 0004's non-goal is reversed in earnest. |
| 2 | Per-image, not per-bundle | A release bundles what is private and pulls what is public. Without it every bundle carries Postgres. Consequence: the manifest grows a second spelling, and both must be documented and tested. |
| 3 | OCI image layout, not `docker save` | Only the layout preserves the registry manifest digest, which is the identity `manifest.yaml` pins and `HasImage` asks about. Verified: `docker save` writes `RepoTags: null` and no digest. |
| 4 | `ref` stays a resolvable image reference; `from` is separate | The reference is interpolated into Compose. Consequence: scheme dispatch, the house pattern for transports, is unavailable here. |
| 5 | ~~Ephemeral registry primary, verified index fallback~~ — **superseded by decisions 18 and 21** | The mechanism survives and both its halves changed: the registry moved into the manager's process, and the fallback is gone. What the spike actually established was that the digest survives registry → layout → registry → pull; what it was read as establishing — that `docker image inspect` then resolves the *manifest's* reference — it never tested. |
| 6 | The fallback verifies, never merely records | Recording without recomputing asserts provenance without checking it — the property [0005 §12](0005-continuous-integration-and-release.md) found missing once already, where the digest-pinned `images` map had no effect on what ran. |
| 7 | Daemon metadata is never written directly | Undocumented, root-only, driver-specific, racy — and it forges the guarantee. Recorded as rejected so it is not re-proposed. |
| 8 | Existing verification chain extends unchanged | Image blobs are files; `SHA256SUMS` and the unlisted-file refusal already cover them. No second verification mechanism. |
| 9 | Extraction limits raised, not removed — the mechanism is decision 11 | Extraction precedes verification, so this guard runs on untrusted bytes and the signature cannot back it up. Measured: `postgres` is 115 M as blobs with a 110 M dominant layer, so `MaxFileSize` binds on roughly a whole image. Consequence: a raise taken without a replacement bound is a 25× reduction in what a hostile archive is refused. |
| 10 | Reverses [0004](0004-distribution-and-verification.md)'s "pulling container images … unchanged" non-goal | Stated explicitly rather than quietly. 0004's "the manager does not run a registry" is *narrowed*, not reversed: a loopback helper during install is not hosting for distribution. |
| 11 | The extraction ceiling is raised by a **budget declared in the manifest and read from the tar stream**, plus a **free-space preflight** — **refined by decisions 15 and 16** | Extraction precedes verification, so the guard runs on untrusted bytes and the signature cannot back it up. A budget proportional to a declared claim keeps a real bound; the preflight turns "disk full" into a refusal. Rejected: a flat raise (a 25× weakening with nothing in its place) and pure opt-in (moves a security decision onto an operator with no way to evaluate it). Consequence: `manifest.yaml` must precede the image blobs in the archive, which [0012](0012-packing-images-into-a-bundle.md) controls. |
| 12 | Ingest is an **explicit command** that `init` and `update` call | It has to exist regardless — it is the manual path before 0012 — and an inspectable, re-runnable step costs nothing to also call from the lifecycle. |
| 13 | A bundled image is **preferred** over a registry copy, never compared against one | The digest is the identity: agreeing copies are the same bytes, and differing ones make the bundle authoritative because the vendor's signature covers it. Comparing would add a network round-trip to the path whose purpose is not needing one. |
| 14 | The reference → image ID mapping is **recomputed from the bundle**, never persisted | State that can go stale will. Only the fallback path needs it at all; where the ephemeral registry runs, the daemon holds the truth. |
| 15 | **Refines decision 11** (2026-08-08): the manifest-first ordering it requires is owned by [0014](0014-building-a-release-bundle.md), and the budget read **fails closed** when the first tar entry is not `manifest.yaml` | Decision 11 named [0012](0012-packing-images-into-a-bundle.md) as controlling the ordering; 0012 §8 excluded archiving, so the guarantee was asserted here and enforced nowhere. 0014 decision 2 locks the order as a property of the archive format, and this row adds the consuming half: an archive that does not honour it is **refused**, not silently given the default ceiling. Consequence: a hand-rolled `tar` over a bundle carrying images is refused unless the vendor orders it, which `publishing.md` must show — a real cost, accepted because a guarantee nothing checks decays into a comment. |
| 16 | **Refines decision 11** (2026-08-09): the declared budget may only ever **lower** the ceiling — the effective limit is `min(declared, hard_cap)`, `hard_cap` being 50 GiB total and 5 GiB per file | The declaration is read from the same unverified bytes the guard bounds, so a budget that could *raise* the ceiling is one an attacker sets. Decision 11's "a bound proportional to a claim" was the error; the free-space preflight does not cover it, since a large disk simply passes. Consequence: a bundle above the cap is refused, and raising the cap is a change to the manager rather than something a bundle can request. |
| 17 | ~~**Refines decision 14** (2026-08-09): the fallback **tags** the loaded image with the manifest's reference~~ — **withdrawn by decision 19** | Not expressible: `docker tag` answers `refusing to create a tag with a digest reference`. A tag names `repo:tag` and never `repo@digest`, so no amount of tagging makes an image answer to the reference a manifest pins. The half that survives is that the mapping is recomputed and never persisted (decision 14). |
| 18 | **The registry is in the manager's process**, not a container: `127.0.0.1:0`, read-only, serving the bundle's layout, closed when the ingest ends *(2026-08-09, replaces the container half of decision 5)* | The container form needs `registry:2` on the machine, and the only way to get an image is from a registry — so the primary path was unavailable in exactly the case this RFC exists for, and those installs would all have fallen through to the least-tested path. Verified by a cold `docker pull` against a Go HTTP server serving the layout. Consequence: [0004](0004-distribution-and-verification.md)'s "the manager does not run a registry" narrows further than decision 10 narrowed it — the manager now *contains* one, read-only, on loopback, for the length of one ingest. |
| 19 | **A bundled image is deployed under a manager-created alias**, `<repo>:morzer-sha256-<hex>`, derived from the digest the manifest pins *(2026-08-09, refines decision 4)* | Measured: no supported command creates a digest reference for a repository the daemon did not pull from, so `ref` cannot be what Compose receives for a bundled image. `ref` stays the identity — pinned, verified, recorded, reported by `release show` — and the alias is what resolves. Derived from the digest rather than invented, so it is identical on every apply and the rendered configuration does not move; a random or timestamped alias would make `diff` and `status` report a change on every run. Consequence: an operator reading `docker images` sees a tag the vendor never published, which the docs have to explain. |
| 20 | **A bundled image that is not present locally is a refusal, never a pull** *(2026-08-09)* | The alias is a tag and a tag is mutable, so an absent image plus a registry that serves that tag equals a digest-pinned deployment converging on unverified bytes. Refusing keeps the guarantee the digest was for. Consequence: `apply` gains a failure mode on a machine whose image store was pruned — the correct one, and `morzer release ingest` is the remedy it names. |
| 21 | **The fallback ingest is withdrawn; there is one mechanism** *(2026-08-09)* | It existed for hosts that cannot run the registry helper, and decision 18 leaves no helper to fail. Decision 6's obligation stands and moves: the daemon's own V2 pull verifies every blob against the digest the manifest names, so a corrupted blob is refused rather than recorded — tested by corrupting one against a real daemon. Consequence: P3 keeps the `doctor` vocabulary and loses its second code path, and a daemon the manager cannot reach over loopback (a remote `DOCKER_HOST`) now has no ingest at all, which it must say plainly rather than fail obscurely. |

## 12. Phasing

- **P0 — settle decision 1.** No code. If the answer is a mirror, this RFC is
  marked ❌ and the reasoning stays on file.
- **P1 — format and verification.** The manifest change, the layout, the
  completeness refusals, the checksum extension. Hand-built fixtures; no
  ingest. A bundle can be *validated* before anything can install one.
- **P2 — ingest via the ephemeral registry**, with the container-suite tests
  that assert the original reference resolves afterwards. This is the phase
  that either works or sends the design back. — **It sent it back.** The
  original reference does not resolve and cannot be made to; P2 as shipped is
  the in-process registry of decision 18 and the alias of decision 19, and the
  container-suite test asserts the *alias* resolves, with the manifest's own
  reference asserted absent so the distinction cannot quietly rot back.
- **P3 — ~~fallback ingest and~~ `doctor` integration**, including the
  mismatched blob refusal — which decision 21 moves onto the primary path
  rather than deleting.
- **P4 — acceptance**: a hybrid bundle installed with the registry stopped.

P1 is useful alone: it makes bundles inspectable and gives
[0012](0012-packing-images-into-a-bundle.md) something to target. P2 is the
risk, and nothing after it is worth scheduling until it lands.

**Amended 2026-08-08:** P1 now carries a dependency it did not when it was
written. The budget read in decision 11 needs an archive it can trust to put
`manifest.yaml` first, and decision 15 makes the absence of that a refusal — so
[0014](0014-building-a-release-bundle.md) P1 (`release archive`, with its
ordering lock) has to land with or before this P1. It is a small phase and
independent of everything else here, but it is no longer optional.
