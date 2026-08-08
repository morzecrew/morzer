# RFC 0011 — Bundled container images

- **Status:** 📝 Draft — **design locked** 2026-08-08, not yet scheduled. Every
  question in §10 is resolved into §11; the ingest mechanism (§5.3) is verified
  by spike. Ready to execute from P1.
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
  first image of its own) · [0012](0012-packing-images-into-a-bundle.md)

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

`from` takes `registry` (default) or `bundle`. Old manifests keep working with
no edit, which matters because the schema is generated and drift-gated.

The dual shape is not invention: `PortSpec` already accepts an integer or a
string for the same backward-compatibility reason
([`manifest.go:168`](../internal/domain/manifest.go)), and this follows it.

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

This is the load-bearing part. Two mechanisms, tried in order, both ending with
the image resolvable by its manifest reference.

**Primary — an ephemeral local registry.** The manager starts a registry
container bound to `127.0.0.1` on an ephemeral port, serves the bundle's OCI
layout from it, and has the runtime pull each bundled image from
`127.0.0.1:<port>/…@sha256:…`. Because the daemon performs a real V2 pull, it
computes and commits `RepoDigests` itself: afterwards the image answers to its
original reference with no further intervention, and `HasImage` needs no
special case at all.

**Verified end to end**, against a real registry and a real daemon, before this
RFC was locked:

```
oras.Copy  registry -> OCI layout   index.json digest = sha256:40baa8cf…
oras.Copy  OCI layout -> registry   descriptor digest = sha256:40baa8cf…
docker pull <ref>@sha256:40baa8cf…  Status: Downloaded
docker image inspect <ref>@sha256:40baa8cf…  -> resolves
  .RepoDigests contains 127.0.0.1:.../ingested/app@sha256:40baa8cf…
```

The digest the registry reported on push survives the copy into the layout, the
copy back out, and the pull — and the daemon records it as a `RepoDigest`
without being asked. `HasImage` runs exactly that inspect
([`compose.go:516`](../internal/adapters/runtime/compose/compose.go)), so after
ingest a bundled image is indistinguishable from a pulled one. This is the
claim P2 exists to establish, and it holds on an `overlay2` daemon with no
containerd image store.

This is not a novel trick in this repository — the acceptance scenario has run
a throwaway registry since [0005](0005-continuous-integration-and-release.md)
for precisely this reason, recorded at
[`acceptance.sh:158`](../.github/scripts/acceptance.sh): "An image built locally
has no digest until it is pushed -- `RepoDigests` is empty -- and the manifest
requires `name@sha256:…`". `start_registry` there already handles readiness
polling, name collision and teardown ownership.

The registry image itself is a bootstrap dependency, handled the way
[0010](0010-compose-volume-capture.md) handled the volume helper: pinned by
digest in the manager's own source, local-first, and reported by `doctor`.

**Fallback — a verified local index.** Where no registry helper can run, the
manager ingests the layout directly, verifies each blob against the digest
`index.json` claims, records the mapping from manifest reference to local image
ID in installation state, and answers `HasImage` from that mapping when the
daemon does not recognise the reference.

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
sentence was waiting for.

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
other step, and compensating means removing what it loaded.

`apply` is untouched. `stepPullImages` calls `Pull`, `Pull` skips what is
present, and after ingest every bundled image is present. `apply --startup`
continues to skip the pull entirely.

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

1. **A declared budget read from the stream.** `manifest.yaml` is an entry in
   the same tar; read it before committing to the rest and size the budget from
   what the release says it carries. An archive that then exceeds its own
   declaration is refused. Keeps a bound proportional to a claim, at the cost
   of ordering constraints on the archive.
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
- **Fallback ingest** with the registry helper unavailable, asserting the same
  `HasImage` outcome and that a blob whose bytes do not match its claimed digest
  is refused rather than recorded.
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
- **The ingest mechanism no longer carries design risk, but it carries
  environment risk.** §5.3 is verified end to end on a real daemon, so the
  question is no longer whether it works but whether a customer can run a
  helper container and bind a loopback port. Where they cannot, the fallback
  carries everything, and the fallback is a verified mapping in installation
  state — more surface than it appears.
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
| 5 | Ephemeral registry primary, verified index fallback | A real V2 pull makes the daemon commit `RepoDigests` natively, so nothing downstream needs a special case. **Verified by spike** (§5.3): the digest survives registry → layout → registry → pull, and `docker image inspect` resolves it afterwards on an `overlay2` daemon. The fallback exists for environments that cannot run the helper. |
| 6 | The fallback verifies, never merely records | Recording without recomputing asserts provenance without checking it — the property [0005 §12](0005-continuous-integration-and-release.md) found missing once already, where the digest-pinned `images` map had no effect on what ran. |
| 7 | Daemon metadata is never written directly | Undocumented, root-only, driver-specific, racy — and it forges the guarantee. Recorded as rejected so it is not re-proposed. |
| 8 | Existing verification chain extends unchanged | Image blobs are files; `SHA256SUMS` and the unlisted-file refusal already cover them. No second verification mechanism. |
| 9 | Extraction limits raised, not removed — the mechanism is decision 11 | Extraction precedes verification, so this guard runs on untrusted bytes and the signature cannot back it up. Measured: `postgres` is 115 M as blobs with a 110 M dominant layer, so `MaxFileSize` binds on roughly a whole image. Consequence: a raise taken without a replacement bound is a 25× reduction in what a hostile archive is refused. |
| 10 | Reverses [0004](0004-distribution-and-verification.md)'s "pulling container images … unchanged" non-goal | Stated explicitly rather than quietly. 0004's "the manager does not run a registry" is *narrowed*, not reversed: a loopback helper during install is not hosting for distribution. |
| 11 | The extraction ceiling is raised by a **budget declared in the manifest and read from the tar stream**, plus a **free-space preflight** | Extraction precedes verification, so the guard runs on untrusted bytes and the signature cannot back it up. A budget proportional to a declared claim keeps a real bound; the preflight turns "disk full" into a refusal. Rejected: a flat raise (a 25× weakening with nothing in its place) and pure opt-in (moves a security decision onto an operator with no way to evaluate it). Consequence: `manifest.yaml` must precede the image blobs in the archive, which [0012](0012-packing-images-into-a-bundle.md) controls. |
| 12 | Ingest is an **explicit command** that `init` and `update` call | It has to exist regardless — it is the manual path before 0012 — and an inspectable, re-runnable step costs nothing to also call from the lifecycle. |
| 13 | A bundled image is **preferred** over a registry copy, never compared against one | The digest is the identity: agreeing copies are the same bytes, and differing ones make the bundle authoritative because the vendor's signature covers it. Comparing would add a network round-trip to the path whose purpose is not needing one. |
| 14 | The reference → image ID mapping is **recomputed from the bundle**, never persisted | State that can go stale will. Only the fallback path needs it at all; where the ephemeral registry runs, the daemon holds the truth. |

## 12. Phasing

- **P0 — settle decision 1.** No code. If the answer is a mirror, this RFC is
  marked ❌ and the reasoning stays on file.
- **P1 — format and verification.** The manifest change, the layout, the
  completeness refusals, the checksum extension. Hand-built fixtures; no
  ingest. A bundle can be *validated* before anything can install one.
- **P2 — ingest via the ephemeral registry**, with the container-suite tests
  that assert the original reference resolves afterwards. This is the phase
  that either works or sends the design back.
- **P3 — fallback ingest and `doctor` integration**, including the mismatched
  blob refusal.
- **P4 — acceptance**: a hybrid bundle installed with the registry stopped.

P1 is useful alone: it makes bundles inspectable and gives
[0012](0012-packing-images-into-a-bundle.md) something to target. P2 is the
risk, and nothing after it is worth scheduling until it lands.
