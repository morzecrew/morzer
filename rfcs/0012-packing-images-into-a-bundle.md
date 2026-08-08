# RFC 0012 — Packing images into a bundle

- **Status:** 📝 Draft — **design locked** 2026-08-08, gated on
  [0011](0011-bundled-container-images.md) P1. Every question in §10 is resolved
  into §11; the copy mechanism (§5.2) is verified by spike. **Amended
  2026-08-08** (decision 10): archiving moves from "excluded" to "delegated to
  [0014](0014-building-a-release-bundle.md)", which also takes the
  reproducibility requirement §8 deferred to whoever owned that step.
- **Scope:** Gives a vendor a supported way to produce the bundles
  [0011](0011-bundled-container-images.md) defines: a `morzer release pack`
  that copies the images a manifest marks `from: bundle` out of the vendor's
  registry into an OCI layout inside the bundle, writes `SHA256SUMS` over the
  result, and leaves signing to `minisign` as today. Covers the command, the
  copy mechanism, platform selection, and what the manager must refuse. Does
  **not** cover the bundle format or how images are ingested on the customer's
  machine — that is 0011 — and does not sign, publish or upload anything. Makes
  the manager *produce* an artefact for the first time, which is a direction
  change worth deciding explicitly.
- **Related:** [0011](0011-bundled-container-images.md) (the format this
  targets) · [0014](0014-building-a-release-bundle.md) (which took archiving,
  reproducibility, and the sums routine this describes) ·
  [`internal/cli/release.go`](../internal/cli/release.go) (the
  command group) ·
  [`internal/adapters/source/oci/oci.go`](../internal/adapters/source/oci/oci.go)
  (`oras-go` usage and ambient credentials) ·
  [`pages/docs/authoring/publishing.md`](../pages/docs/authoring/publishing.md)
  (the manual procedure today) ·
  [`internal/adapters/verify/checksum/checksum.go`](../internal/adapters/verify/checksum/checksum.go)

---

## 1. Summary

`morzer release pack ./my-product` reads the manifest, copies every image
marked `from: bundle` from its registry into `images/` as an OCI layout,
regenerates `SHA256SUMS` over the whole tree, and stops. Signing stays
`minisign -Sm SHA256SUMS`, and archiving stays `tar --zstd`, because both
already work and neither needs the manager.

The copy uses `oras-go/v2`, already a direct dependency, so this adds a command
and no new supply-chain surface.

## 2. Motivation

[0011](0011-bundled-container-images.md) defines a bundle layout a vendor
currently has no supported way to build. The manual procedure would be
`skopeo copy docker://… oci:images:…` per image, then the existing `find | sha256sum`
incantation from
[`publishing.md:33`](../pages/docs/authoring/publishing.md) — which is workable
for one image and error-prone for several, in a specific way that matters:
`SHA256SUMS` must list **every** file, and the checksum verifier fails closed on
an unlisted one ([`checksum.go:158`](../internal/adapters/verify/checksum/checksum.go)).
An OCI layout is dozens of blob files with content-addressed names. A vendor
who adds an image and forgets to regenerate the sums ships a bundle that fails
verification on the customer's machine, and the error names a blob hash rather
than the mistake.

The existing documentation already anticipates this class of error and
prescribes a manual cross-check —
[`publishing.md:54-59`](../pages/docs/authoring/publishing.md) tells vendors to
compare `find … | wc -l` against `wc -l < SHA256SUMS`. That is a workaround for
the absence of tooling, and it scales badly when the file count goes from a
dozen to a few hundred.

There is also a correctness argument the manual path cannot satisfy: the
manifest is the authority on what a release consists of, and only something
that reads the manifest can guarantee the layout matches it. 0011 makes a
mismatch a verification failure; this RFC makes it impossible to produce.

## 3. Current state

- `release` has `list`, `show`, `verify`, `fetch`, `prune` — no `pack`. The
  manager consumes bundles and has never produced one
  ([`release.go`](../internal/cli/release.go)).
- Vendors pack by hand: `tar --zstd`, a `find`/`sha256sum` pipeline, then
  `minisign -Sm` ([`publishing.md:12-35`](../pages/docs/authoring/publishing.md)).
- `oras-go/v2 v2.6.2` is a direct dependency ([`go.mod:29`](../go.mod)), used
  today by the OCI *source* adapter for fetching bundles. The packages already
  imported include `registry/remote`, `registry/remote/auth`,
  `registry/remote/credentials` and `registry/remote/retry`.
- The module also ships `content/oci`, an on-disk OCI layout store
  (`oci.New(root)`), which is exactly the write target 0011's layout needs. It
  is present in the module cache and not currently imported.
- The OCI source adapter already resolves ambient Docker credentials —
  [`oci.go:15`](../internal/adapters/source/oci/oci.go) records that an operator
  "who has run `docker login` for the registry their images come from should not
  have to" configure anything further. The same mechanism serves packing, where
  the credentials belong to the *vendor* and never leave their build machine.
- `release verify` already computes a content digest and checks `SHA256SUMS`
  ([`release.go:207`](../internal/cli/release.go)), so a packed bundle can be
  validated by an existing command.

## 4. Goals / Non-goals

**Goals**

- Produce a bundle whose `images/` layout matches what the manifest declares,
  by construction rather than by discipline.
- Regenerate `SHA256SUMS` over the whole tree, so the completeness rule cannot
  be violated by forgetting a step.
- Use the credentials the vendor's build machine already has, the way the OCI
  source adapter does.
- Add no new dependency.

**Non-goals**

- **Signing.** `minisign -Sm SHA256SUMS` stays a separate step with the vendor's
  key. A manager that signs is a manager that handles signing keys, which is a
  different threat model and a different RFC.
- **Archiving and publishing.** Packing writes a directory. Publishing stays the
  vendor's pipeline; archiving was left as `tar --zstd` here and is now
  [0014](0014-building-a-release-bundle.md)'s — see decision 10 for why the two
  stopped being one exclusion.
- **Building images.** The vendor's CI builds and pushes; this copies what is
  already published.
- **Editing the manifest.** `pack` reads it. A manifest that declares an image
  it cannot copy is an error, not something to rewrite.

## 5. Design

### 5.1 The command

```
morzer release pack <bundle-dir> [--platform linux/amd64]
```

Reads `<bundle-dir>/manifest.yaml`, and for every image with `from: bundle`
copies it from its registry into `<bundle-dir>/images/` as an OCI layout keyed
by the reference the manifest pins. Then regenerates
`<bundle-dir>/SHA256SUMS` over every file except `SHA256SUMS` and
`SHA256SUMS.minisig`, matching the rule the verifier enforces.

Idempotent: running it twice produces the same layout, because the content is
addressed by digest. Re-running after adding an image adds only that image's
blobs.

**Failure semantics, all fail-closed:**

| Situation | Behaviour |
| --- | --- |
| An image marked `from: bundle` cannot be resolved or pulled | Refuse. A partially-packed bundle must not exist. |
| The registry digest does not match what the manifest pins | Refuse, naming both. This is the check that makes the bundle's provenance meaningful. |
| An existing `SHA256SUMS.minisig` is present | Refuse unless `--force`: the signature covers a `SHA256SUMS` that packing is about to invalidate, and leaving a stale signature beside a changed tree is worse than refusing. |
| `images/` contains entries no manifest names | Refuse. 0011 makes this a verification failure downstream; catching it here names the cause. |

The refusal on a stale signature is the one an implementer is most likely to
soften. It should not be softened: a bundle whose signature verifies against a
`SHA256SUMS` that no longer matches the tree is precisely the failure the chain
exists to prevent.

### 5.2 Copying, with `oras-go`

`oras.Copy` from a `registry/remote.Repository` to a `content/oci.Store` rooted
at `<bundle-dir>/images/`. Both sides are already in the dependency tree; the
source side is the same construction the OCI source adapter uses, with the same
ambient credential resolution.

**Verified by spike** against a real registry: the copy produces a layout whose
`index.json` carries the registry's own manifest digest, byte for byte —

```json
{ "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
  "digest": "sha256:40baa8cf…", "size": 527,
  "annotations": { "org.opencontainers.image.ref.name": "v1" } }
```

— matching what the registry reported on push. That is the property the whole
design rests on, and it is the one `docker save` does not have.

Using the library rather than shelling out to `skopeo` or `crane` keeps packing
available to any vendor who has the manager, which is the point of shipping it
as a command at all. It also keeps the digest check in Go, next to the manifest
that pins it, rather than in a shell pipeline comparing strings.

#### Alternatives considered

**Shell out to `skopeo`.** Rejected: it makes the manager's own tooling depend
on a binary the vendor may not have, and moves the digest comparison into a
place where a typo silently passes. It stays the documented manual path until
this command exists, which is a different thing from being the implementation.

**`docker save` and repack.** Rejected for the reason 0011 records: the save
format discards the registry digest, so the resulting layout could not carry
the identity the manifest pins.

**Pull with the daemon, then export.** Rejected: it requires a running Docker on
the build machine and inherits the daemon's storage backend behaviour, when the
operation is a registry-to-disk copy that needs neither.

### 5.3 Platform selection

A registry reference may resolve to a multi-platform index. `--platform`
selects one, defaulting to the platform `pack` runs on.

The default is a deliberate compromise and worth naming: it is the least
surprising for a vendor building on the architecture they ship, and wrong for a
vendor cross-building on arm64 for amd64 customers. The refusal that protects
them is that `pack` records the selected platform in the layout and
[0011](0011-bundled-container-images.md)'s ingest refuses an image whose
platform does not match the host — so a mistake fails at install with a
readable message rather than at runtime with `exec format error`.

Packing several platforms into one bundle is out of scope in
[0011 §8](0011-bundled-container-images.md); per-platform bundles are the
answer until someone needs otherwise.

## 6. Tests

- **Against a real registry**, in the container suites, which already run one:
  pack a manifest with two bundled images, assert the layout carries both by
  the pinned digest and that `release verify` passes on the result.
- **The digest mismatch refusal**, by pinning a manifest to a digest the
  registry does not serve. This is the check that gives the bundle its meaning;
  a test asserting only that packing succeeds would not notice it was removed.
- **The stale signature refusal**, and that `--force` overrides it.
- **Completeness**: after packing, every file under `images/` appears in
  `SHA256SUMS`, asserted by counting rather than by sampling — the manual
  cross-check from `publishing.md` turned into a test.
- **Idempotence**: packing twice leaves the tree byte-identical.
- **A hybrid manifest**: images marked `from: registry` are not copied, and
  `images/` does not mention them.

## 7. Docs

- `authoring/publishing.md` gains `pack` as the supported path, with the manual
  `skopeo` procedure kept for vendors who cannot run the manager on their build
  machine. Both must state that signing follows packing, and why the order
  matters.
- `reference/release-commands.md` documents the command, its flags and its
  refusals — the docs-drift gate requires it, and the refusals are the part a
  vendor needs before they hit one.

## 8. Out of scope

- **Signing and publishing.** Named in Non-goals; repeated here because a `pack`
  that did all four would be the obvious next request. What would change it:
  nothing in this design — signing keys are the boundary.
- **Archiving.** ~~Also excluded with no successor.~~ **Amended 2026-08-08:**
  [0014](0014-building-a-release-bundle.md) now owns it, because the exclusion
  turned out to have a cost this RFC did not see.
  [0011 decision 11](0011-bundled-container-images.md) reads the extraction
  budget from the tar stream and therefore requires `manifest.yaml` to be the
  archive's first entry — a guarantee that was asserted by 0011, disclaimed
  here, and enforced nowhere. See decision 10 below.
- **Multi-platform bundles.** Follows [0011 §8](0011-bundled-container-images.md).
- **Pruning a layout when an image is removed from the manifest.** `pack` adds
  and refuses; it does not garbage-collect. Repacking into a clean directory is
  the workaround, and a `--prune` flag is the escape hatch if that proves
  annoying.
- **Reproducible layouts across machines.** The blobs are content-addressed and
  therefore identical, but file ordering and timestamps inside the eventual
  `tar` are not this command's concern. If bit-identical bundles become a
  requirement it belongs with the archiving step, which this RFC does not own —
  and **as of 2026-08-08 that step has an owner**, which took the requirement:
  [0014 decision 4](0014-building-a-release-bundle.md) makes archives
  deterministic.

## 9. Risks

- **Gated on a design that may not be built.** If
  [0011 decision 1](0011-bundled-container-images.md) resolves toward a
  distribution mirror, this RFC is moot and should be marked ❌ with it.
- **The manager becomes a build tool.** Today it consumes bundles; `pack` makes
  it produce one. That is a genuine direction change: it invites "and sign
  them", "and upload them", "and build the images too", each individually
  reasonable. The Non-goals exist to hold that line and will be tested.
- **Ambient credentials on a build machine are broader than on a host.** A
  vendor's CI often holds push credentials for everything. `pack` only reads,
  but it reads with whatever it is given, and a misconfigured manifest could
  copy an image the vendor did not intend to ship. The digest check limits this
  to images the manifest names.
- **Vendors will not use it if their CI has no morzer binary.** The manual path
  must stay documented and correct, not left to rot as the unsupported
  alternative.
- **The copy is verified; the command is not.** The spike proves
  `oras.Copy` produces the right layout. Everything around it — reading the
  manifest, the four refusals in §5.1, regenerating `SHA256SUMS` — is
  unwritten, and the refusals are where the value is.

## 10. Unresolved questions

All three are resolved as of 2026-08-08 and recorded as decisions 7–9. Kept
here because what was open is part of the record.

1. ~~In place, or produce a copy?~~ → decision 7. In place.
2. ~~Should `pack` verify the whole bundle afterwards?~~ → decision 8. Yes.
3. ~~One `--platform`, or per-image?~~ → decision 9. One, for now.

What implementation is still free to settle: how much of the copy's progress is
reported, and whether the four refusals in §5.1 share one error type.

## 11. Decisions

| # | Decision | Rationale and consequence |
| --- | --- | --- |
| 1 | `pack` writes a layout and sums; it does not sign, archive or publish | Signing keys are a boundary the manager does not cross. Consequence: publishing stays a multi-step procedure, and the docs must keep the order explicit. |
| 2 | `oras-go`, not `skopeo`/`crane`, not the daemon | Already a dependency; keeps the digest comparison in Go beside the manifest that pins it; needs no Docker on the build machine. |
| 3 | A digest mismatch between manifest and registry is a refusal | This is what makes a bundled image's provenance mean anything. Removing it would leave a command that packs whatever the registry currently serves. |
| 4 | A stale `SHA256SUMS.minisig` blocks packing unless `--force` | A signature over a tree that has changed is the exact failure the chain prevents. |
| 5 | `--platform` selects one platform, defaulting to the host's | Per-platform bundles rather than multi-platform ones ([0011 §8](0011-bundled-container-images.md)). Consequence: a cross-building vendor must pass the flag, and ingest refuses a mismatch rather than failing at runtime. |
| 6 | The manual `skopeo` procedure stays documented | Vendors whose CI cannot run the manager must retain a supported path. |
| 7 | `pack` writes **in place**, not into a copy | Matches how vendors already work, and §5.1's refusals are designed to make a half-written layout unreachable. A copy would double disk for a multi-gigabyte bundle to guard a case already covered. |
| 8 | `pack` **verifies the whole bundle afterwards**, reusing `release verify`'s code | The cheapest defect prevention in either RFC: a manifest/layout mismatch is caught on the vendor's machine instead of the customer's, for one function call and no second checker. Consequence: `pack` fails on a bundle that was already broken before it ran, which is correct and will surprise someone once. |
| 9 | One `--platform` flag; per-image selection deferred | True of every release today, and addable later without changing the single-flag spelling. Reopens for a release mixing an app with a sidecar built for another architecture. |
| 10 | **Amends decision 1** (2026-08-08): archiving is no longer merely excluded, it is **delegated** to [0014](0014-building-a-release-bundle.md) | Decision 1 lumped signing, archiving and publishing together as one boundary. They are not one boundary: signing is a key-custody question the manager must never cross, while archiving is a file-format question with a locked correctness requirement — [0011 decision 11](0011-bundled-container-images.md)'s budget read needs `manifest.yaml` first, which no `tar` invocation guarantees. Consequence: decision 1's *signing* half stands unchanged and is the reason 0014 is two commands rather than one; the archiving half moves rather than disappearing, and `pack` remains an images-and-sums command. |

## 12. Phasing

Single phase, gated on [0011](0011-bundled-container-images.md) P1 having
defined and validated the layout — there is nothing to target before that.

Worth landing before 0011 P2: a vendor-produced fixture makes the ingest work
testable against something other than a hand-built directory.
