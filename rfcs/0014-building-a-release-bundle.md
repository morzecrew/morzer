# RFC 0014 — Building a release bundle

- **Status:** 📝 Draft — design proposed. The version-scheme constraints in §5.2
  are verified by measurement (§3): semver ordering and `String()`/`Compare()`
  behaviour against the pinned `Masterminds/semver/v3`, OCI tag acceptance
  against `oras-go`'s own validator, and `git describe` output including the
  shallow-clone failure.
- **Scope:** Gives a vendor the two commands that stand between a bundle source
  tree and something publishable: `morzer release build`, which resolves and
  stamps the version (from a flag or from `git describe`), packs any bundled
  images, and regenerates `SHA256SUMS`; and `morzer release archive`, which
  writes the `.tar.zst` the HTTPS and OCI transports already consume, with a
  **locked entry order** that [0011](0011-bundled-container-images.md)'s
  extraction budget depends on. Two commands rather than one because signing
  sits between them and the manager does not sign. Covers version derivation,
  the `+metadata` refusal, sums regeneration, archive ordering and archive
  determinism. Does **not** sign, publish, upload, or build container images,
  and does not change the bundle format — it produces what
  [0004](0004-distribution-and-verification.md) already reads.
- **Related:** [0012](0012-packing-images-into-a-bundle.md) (`pack`, composed in
  as a step) · [0011 decision 11](0011-bundled-container-images.md) (the budget
  read from the tar stream, which needs the ordering this RFC locks) ·
  [0004](0004-distribution-and-verification.md) (the transports that consume the
  archive) · [`internal/cli/release.go`](../internal/cli/release.go) ·
  [`internal/infra/atomicfs/archive.go`](../internal/infra/atomicfs/archive.go)
  (extraction; there is no creation side) ·
  [`internal/release/load.go`](../internal/release/load.go) (`checkVersionFile`) ·
  [`internal/domain/version.go`](../internal/domain/version.go) ·
  [`pages/docs/authoring/publishing.md`](../pages/docs/authoring/publishing.md)
  (the hand-rolled procedure today)

---

## 1. Summary

Nothing in this repository produces a release bundle. [0004](0004-distribution-and-verification.md)
shipped four transports that consume `.tar.zst`; the vendor's side of that is a
`tar --zstd` line and a `find | sha256sum` pipeline in the documentation.

That was a convenience gap until [0011](0011-bundled-container-images.md) locked
decision 11, which reads the extraction budget out of the tar stream and
therefore requires `manifest.yaml` to precede the image blobs. Archive entry
order is now a **correctness requirement of a locked design, owned by no
command**. This RFC gives it an owner and, in the same pass, automates the two
other things a vendor does by hand every release: stamping the version and
regenerating the checksum list.

`build` stamps and sums in place. The vendor signs. `archive` writes the
tarball. The split is not fastidiousness — the signature must be *inside* the
archive, so archiving has to happen after signing, and signing is the boundary
the manager does not cross.

## 2. Motivation

**A locked decision depends on a step nothing owns.**
[0011 decision 11](0011-bundled-container-images.md) raises the extraction
ceiling by reading a declared budget from the tar stream before extracting the
rest, because extraction precedes verification and the signature cannot back
that guard up. Reading it from the stream only works if `manifest.yaml` arrives
first. `tar --zstd -cf … -C ./my-product .`
([`publishing.md:15`](../pages/docs/authoring/publishing.md)) emits entries in
directory order, which no tar implementation specifies and none guarantees to be
stable. [0012 §8](0012-packing-images-into-a-bundle.md) explicitly excludes
archiving. So the guarantee is currently asserted by one RFC, disclaimed by
another, and enforced by nobody.

**The checksum list is produced by a pipeline with a documented failure mode.**
`SHA256SUMS` must name every file — the verifier fails closed on an unlisted one
([`checksum.go:158`](../internal/adapters/verify/checksum/checksum.go)), because
a file the list does not cover is a file the signature does not cover. The
documented procedure is `find … -exec sha256sum {} +`
([`publishing.md:34`](../pages/docs/authoring/publishing.md)) followed by a
manual cross-check comparing `find … | wc -l` against `wc -l < SHA256SUMS`
([`publishing.md:58-59`](../pages/docs/authoring/publishing.md)). A prescribed
manual cross-check is documentation admitting the step is easy to get wrong.
[0012](0012-packing-images-into-a-bundle.md) regenerates the list as a side
effect of copying images — which leaves every bundle *without* bundled images
still doing it by hand.

**The version is hand-edited, and the manager refuses to forgive that.** A
version already installed with different content is refused in three places
([`release.go:306`](../internal/cli/release.go),
[`update.go:257`](../internal/lifecycle/ops/update.go), and again as
defence-in-depth at [`update.go:585`](../internal/lifecycle/ops/update.go)), with
no `--force` bypass. That refusal is correct — the release store is keyed by
version string ([`paths.go:122`](../internal/domain/paths.go)) and `current` /
`previous` are symlinks into it, so overwriting a directory would silently
change what `rollback` returns to. But it means a vendor iterating against a
sandbox must hand-bump `metadata.version` **and** `VERSION` on every single
build, or watch the install fail. The tooling should produce the version, not
ask the human to remember it.

## 3. Current state

Verified against `86da400`.

| Fact | Where |
| --- | --- |
| `release` has `list`, `show`, `verify`, `fetch`, `prune`. No `build`, no `archive`, no `pack` | [`release.go`](../internal/cli/release.go) |
| `atomicfs` extracts `tar.zst` and has no creation side | [`archive.go:68`](../internal/infra/atomicfs/archive.go) (`ExtractTarZst`) |
| **Nothing in the shipped binary writes a tar at all.** `tar.NewWriter` appears only in test helpers and two test files — the archives this project reads are all built by hand or by the suite | [`test/contract/runtime.go:453`](../test/contract/runtime.go), `archive_test.go` |
| `klauspost/compress` is a **direct** dependency already, used for reading | [`go.mod:17`](../go.mod) |
| `SHA256SUMS` / `SHA256SUMS.minisig` are contract constants, not adapter detail | [`source.go:150`](../internal/ports/source.go) |
| A bundle may contain any file; `Load` requires only `manifest.yaml` and checks that *declared* paths exist | [`load.go`](../internal/release/load.go) (`Load`, `checkReferencedFiles`) |
| `VERSION`, when present, must agree with the manifest or the bundle fails to load | [`load.go:201`](../internal/release/load.go) (`checkVersionFile`) |
| The store is keyed by `version.String()`, and `--to` resolves by the same string | [`paths.go:122`](../internal/domain/paths.go), [`ops.go:542`](../internal/lifecycle/ops/ops.go) |
| The bundle digest covers contents, paths and the executable bit — not mtimes | [`update.go`](../internal/lifecycle/ops/update.go) (staging step comment), `atomicfs.DigestTree` |

Four facts about version handling, **verified empirically** against
`Masterminds/semver/v3 v3.5.0` (the pinned version), because each one changes a
design choice below:

| Input | `String()` | Note |
| --- | --- | --- |
| `v1.4.0` | `1.4.0` | The `v` prefix is dropped, so git tags work unchanged |
| `1.4.1-dev.7+gabc1234` | `1.4.1-dev.7+gabc1234` | Build metadata is **retained** by `String()` |
| `1.4.0-dev.7` vs `1.4.0` | `-1` | A prerelease sorts **below** its own release |
| `1.4.1-dev.7` vs `1.4.0` | `+1` | Guessing the next patch is what makes a dev build sort forward |
| `1.4.1-dev.7+gaaa` vs `1.4.1-dev.7+gbbb` | `0` | Metadata is **ignored** by comparison |

The last two rows together are a live hazard, not a curiosity: `String()`
retains metadata and `ReleaseDir` is built from `String()`, so two builds
differing only in metadata land in **different directories** — which means the
"already installed with a different digest" check, which compares the release
loaded from `staged.Root`, never compares them at all. Build metadata is a
silent bypass of the guard. Nothing uses it today, which is why closing it is
cheap.

Two more facts, also measured rather than assumed, because §5.2 rests on them:

- **A `+` cannot be an OCI tag.** Checked against `oras-go/v2 v2.6.2`'s own
  validator — the library this repository already uses to talk to registries:
  `ValidateReferenceAsTag` accepts `1.4.1-dev.7.gabc1234` and
  `1.4.1-dev.7.gabc1234.dirty`, and rejects `1.4.1-dev.7+gabc1234` with
  `invalid tag`. So a version carrying build metadata could never be published
  as an OCI tag at all.
- **`git describe --tags --long` produces `v<tag>-<distance>-g<sha>`.**
  Confirmed in a scratch repository seven commits past `v1.4.0`:
  `v1.4.0-7-g3be286c`, and `v1.4.0-7-g3be286c-dirty` with `--dirty`. A shallow
  clone with no tags fails with `fatal: No names found, cannot describe
  anything` — the failure decision 8 requires be surfaced rather than defaulted
  around.

## 4. Goals / Non-goals

**Goals**

- Give `manifest.yaml`-first archive ordering an owner, so
  [0011 decision 11](0011-bundled-container-images.md) rests on a guarantee
  rather than a convention.
- Produce `SHA256SUMS` by construction for every bundle, not only those with
  bundled images.
- Let a version be derived from the VCS, so a sandbox iteration loop does not
  depend on a human bumping two files.
- Add no new dependency.

**Non-goals**

- **Signing.** Unchanged from [0012](0012-packing-images-into-a-bundle.md):
  `minisign -Sm SHA256SUMS` with the vendor's key. A manager that signs is a
  manager that handles signing keys.
- **Publishing or uploading.** `archive` writes a file. What happens to it is
  the vendor's pipeline.
- **Building container images.** The vendor's CI builds and pushes; `pack`
  copies what is already published.
- **Changing the bundle format.** Everything here produces what
  [0004](0004-distribution-and-verification.md) already reads.
- **Being a VCS integration.** §5.2 draws this line explicitly: the manager
  supports a *scheme*, and shells out to `git` only as documented sugar.

## 5. Design

### 5.1 Two commands, and why they are two

```
morzer release build   <bundle-dir> [--version V | --version-from-git] [--allow-dirty]
morzer release archive <bundle-dir> [-o FILE]
```

`build` resolves the version, stamps it into `manifest.yaml` and `VERSION`,
invokes [0012](0012-packing-images-into-a-bundle.md)'s pack step for any image
marked `from: bundle`, regenerates `SHA256SUMS`, and verifies the result by
reusing `release verify` — the same reuse [0012 decision 8](0012-packing-images-into-a-bundle.md)
locks, for the same reason: a broken bundle should fail on the vendor's machine.

The vendor then signs: `minisign -Sm <bundle-dir>/SHA256SUMS`.

`archive` writes `<bundle-dir>/../<name>-<version>.tar.zst` (or `-o`).

**These cannot be one command.** The signature is a detached file over
`SHA256SUMS` that travels *inside* the bundle
([`source.go:150`](../internal/ports/source.go) records why: a sibling file would
not survive packing and unpacking). So the order is forced — sums, then
signature, then archive — and the middle step is the one the manager does not
perform. A single `build` that also archived would either exclude the signature
or require the manager to hold a signing key. This is the same boundary
[0012 decision 1](0012-packing-images-into-a-bundle.md) draws, arrived at from
the other side.

`build` writes **in place**, matching
[0012 decision 7](0012-packing-images-into-a-bundle.md). `--out` is not offered:
copying a tree whose `images/` layout may be tens of gigabytes to guard against
a stamped version field is a poor trade. The consequence is real and belongs in
the documentation: **`build` leaves the working tree modified**, because
stamping is a write. A vendor's source manifest carries a placeholder version
that `build` overwrites; CI checks out fresh, so it never notices.

### 5.2 Version resolution

Three ways to arrive at a version, in precedence order:

1. `--version 1.4.0` — explicit. This is the plumbing; everything else is
   convenience on top of it.
2. `--version-from-git` — runs `git describe --tags --long --dirty` and renders
   it through the scheme below.
3. Neither — the manifest's existing `metadata.version` is used as-is, and
   `build` only sums and packs. This keeps `build` usable by a vendor who
   manages versions their own way.

**The scheme.** `git describe --tags --long` on a tree seven commits past
`v1.4.0` at `abc1234` produces `v1.4.0-7-gabc1234`, which renders as:

```
1.4.1-dev.7.gabc1234
```

Three properties, each load-bearing:

- **The patch is bumped.** `1.4.0-dev.7` sorts *below* `1.4.0` (verified, §3), so
  a dev build named after the tag it follows would sort behind the release it
  comes after. Guessing the next patch is what makes it sort forward. This is
  precisely `setuptools-scm`'s `guess-next-dev` default, arrived at from the
  same constraint.
- **The sha is not decoration.** Commit distance is not unique across branches:
  two branches each seven commits past `v1.4.0` both produce `dev.7` with
  different content, which is exactly the collision the never-republish refusal
  exists to catch. The sha is what makes the version unique.
- **The sha is a prerelease identifier, not build metadata.** `+gabc1234` fails
  twice, and both failures are measured in §3: `oras-go`'s own tag validator
  rejects it, so such a version could never be a registry tag — and the OCI
  transport is how these bundles are distributed — while metadata is retained by
  `String()` yet ignored by comparison, so two metadata-differing builds occupy
  different release directories that the digest-conflict check never compares.
  `-dev.7.gabc1234` is a legal semver prerelease, an accepted OCI tag, and
  participates in ordering: the numeric `7` dominates and the sha only breaks
  ties between builds at the same commit count.

Exactly on a tag (distance `0`), the version is the tag verbatim — `1.4.0`, no
suffix. That is the release build, and it is the only shape that produces a
non-prerelease version.

A dirty tree is **refused** by default. `--allow-dirty` appends a fourth
identifier, `1.4.1-dev.7.gabc1234.dirty`, again as a prerelease rather than
metadata and for the same two reasons. It sorts after the clean build at the
same commit, which is the correct reading.

**`build` stamps both files.** `metadata.version` and `VERSION`, or the bundle
fails to load — `checkVersionFile` refuses a disagreement
([`load.go:201`](../internal/release/load.go)). Stamping one and not the other
is the single most likely implementation defect here and gets a test.

**Build metadata is refused in `metadata.version`.** Not in constraints —
`upgrade_from: ">=1.0.0+build.7"` stays legal and is already tested
([`compatibility_test.go`](../internal/domain/compatibility_test.go) asserts
metadata decides nothing there) — but a bundle's own identity may not carry a
`+`, because §3 shows it silently defeats the guard that makes version identity
mean anything. Nothing uses it today.

#### Alternatives considered

**Build `git` into the manager as a first-class version source.** Rejected. It
adds a VCS dependency to a tool that has none, and inherits two failure modes
that bite `setuptools-scm` users constantly: `actions/checkout` defaults to
`fetch-depth: 1` and fetches **no tags**, so `git describe` fails or silently
describes nothing; and in a monorepo "the latest tag" is ambiguous. The
mitigation is the design: `--version` is the real interface, `--version-from-git`
is documented sugar, and it **fails loudly** rather than falling back to a
default when no tag is reachable. A silent `0.0.0` would produce a bundle that
installs, collides, and confuses.

**A `version: dynamic` manifest field, resolved at load time.** Rejected, and it
cannot work: the customer's machine has no repository, and the bundle digest
covers `manifest.yaml`, so the version must be concrete before the tree is
hashed. `setuptools-scm` resolves at build time for the same reason — it writes
the version into the distribution, not into the installer.

### 5.3 `SHA256SUMS`, once

Regeneration covers every file except `SHA256SUMS` and `SHA256SUMS.minisig`,
matching the rule [`checksum.go:158`](../internal/adapters/verify/checksum/checksum.go)
enforces. This is the same routine
[0012 §5.1](0012-packing-images-into-a-bundle.md) describes; it becomes one
internal function that both `pack` and `build` call, so `pack` stays usable
standalone and a bundle with no images stops being the case nothing covers.

An existing `SHA256SUMS.minisig` blocks `build` unless `--force`, identical to
[0012 decision 4](0012-packing-images-into-a-bundle.md) and for the identical
reason: a signature over a tree that has changed is the exact failure the chain
exists to prevent.

### 5.4 The archive, and the order that is now a contract

`archive` writes a `tar` stream through `klauspost/compress/zstd` — already a
direct dependency ([`go.mod:17`](../go.mod)), currently used only for reading —
in this order:

| # | Entry | Why here |
| --- | --- | --- |
| 1 | `manifest.yaml` | [0011 decision 11](0011-bundled-container-images.md) reads the extraction budget from it before extracting anything else |
| 2 | `VERSION` | Tiny, and lets a reader confirm identity from the first two entries |
| 3 | `SHA256SUMS`, `SHA256SUMS.minisig` | The integrity evidence, available before the bytes it covers |
| 4 | Everything else, `images/` last | The large content, extracted under a budget that is by now known |

**This is a property of a morzer release archive, not an implementation
detail.** An extractor depends on it, so it has to be stated where a
reimplementer would look — which is why it is a decision row and a documented
part of the format rather than a comment in the writer.

And because a guarantee nothing checks is a guarantee that decays: **0011's
budget read must fail closed when the first entry is not `manifest.yaml`.** An
archive rolled by hand, or by a future writer that reorders, must be refused
rather than silently fall back to the default ceiling. Stated here because this
RFC creates the dependency; recorded against 0011 as a new decision row so the
consuming side carries it too.

**Determinism.** Tar headers are normalised — uid/gid `0`, owner names empty,
mtime from `SOURCE_DATE_EPOCH` when set and `0` otherwise, no atime/ctime — so
two archives of the same tree are byte-identical. This is cheap here and cannot
be bolted on later by a vendor's `tar` invocation. It is also free of
consequence for bundle identity: the content digest covers contents, paths and
the executable bit, not timestamps, so normalising mtimes changes the archive
bytes and not the bundle's digest. [0012 §8](0012-packing-images-into-a-bundle.md)
deferred reproducibility to "the archiving step, which this RFC does not own" —
this is that owner, deciding it.

**Refusals:**

| Situation | Behaviour |
| --- | --- |
| No `SHA256SUMS` | Refuse. Archiving an unsummed tree produces a bundle the verifier's completeness rule cannot be applied to. |
| `SHA256SUMS` does not match the tree | Refuse, reusing `release verify`. |
| `SHA256SUMS.minisig` older than `SHA256SUMS` | Refuse. Same stale-signature rule as `build` and as [0012 decision 4](0012-packing-images-into-a-bundle.md). |
| No signature at all | **Warn, do not refuse.** Signing is an operator-side policy (`require_signature`); an unsigned bundle is legitimate for a vendor whose customers do not require one. |

### 5.5 What this does to the documented manual path

`publishing.md`'s hand-rolled procedure stays documented and correct — a vendor
whose CI cannot run the manager still needs it, which is
[0012 decision 6](0012-packing-images-into-a-bundle.md)'s reasoning applied
again. It gains one paragraph it did not previously need: that a hand-rolled
`tar` must place `manifest.yaml` first if the bundle carries images, with the
`tar` invocation that does so.

## 6. Tests

- **The ordering guarantee**, asserted by reading the produced archive and
  checking that entry 0 is `manifest.yaml`. This is the test that protects
  [0011 decision 11](0011-bundled-container-images.md), and it is the one whose
  removal would be least noticed.
- **The ordering guard on the consuming side**: an archive whose first entry is
  not the manifest is refused by ingest rather than falling back to the default
  ceiling. Verified-red by hand-rolling such an archive.
- **Round trip**: `build` → sign → `archive` → `ExtractTarZst` → `release.Load`
  yields the stamped version, driven by `testdata/bundle`.
- **Determinism**: two archives of the same tree are byte-identical, under a
  changed `SOURCE_DATE_EPOCH` and an unchanged one.
- **The version scheme as a table** — distance 0, distance N, dirty, a tag with
  a `v` prefix, and no reachable tag (which must fail, not default).
- **Both files stamped**: a test that asserts `VERSION` and `metadata.version`
  agree after `build`, because stamping one is the likely defect.
- **The `+metadata` refusal** in `metadata.version`, and that a constraint
  carrying metadata still parses — the pair, so the refusal cannot be widened
  into the constraint path without a failure.
- **Sums completeness by counting**, not sampling: the manual cross-check from
  `publishing.md:58-59` turned into an assertion.
- **The stale-signature refusals**, on both commands, and that `--force`
  overrides `build`'s.

## 7. Docs

- `authoring/publishing.md`: `build` and `archive` as the supported path, the
  signing step between them and **why** it is between them, and the
  manifest-first requirement for anyone still rolling `tar` by hand.
- A new `authoring/versioning.md`, or a section in `publishing.md`: the scheme,
  the CI recipe, and — the gap this closes — what a vendor should do instead of
  republishing a version. The never-republish rule is currently stated at
  [`publishing.md:158`](../pages/docs/authoring/publishing.md) with no
  alternative offered, which is why it reads as an obstacle rather than as
  "use a prerelease".
- The hint on the "already installed with a different digest" error gains a
  pointer to prereleases. Three call sites share the wording.
- `reference/release-commands.md`: both commands, their flags and their
  refusals — the docs-drift gate requires it.

## 8. Out of scope

- **Publishing.** `archive` writes a file; `oras push` or an HTTPS upload is the
  vendor's pipeline. What would change it: nothing here — but note that
  [0012](0012-packing-images-into-a-bundle.md) already imports the `oras-go`
  push side for images, so the temptation will exist.
- **A `release release` that chains build/sign/archive.** It would have to
  invoke `minisign` or hold a key. If a vendor wants one step, it is a shell
  script in their CI, and the docs should show it.
- **Multi-platform archives.** Follows [0011 §8](0011-bundled-container-images.md).
- **Version schemes other than the one in §5.2** — calendar versioning, build
  numbers from CI rather than commit distance. `--version` accepts any valid
  semver, so these all work; what is not offered is a second `--version-from-*`
  flag for each.
- **Verifying that the vendor actually signed.** `archive` warns. Making it
  refuse would be the manager deciding an operator-side policy.

## 9. Risks

- **`build` modifies the working tree.** Stamping is a write, and a vendor who
  runs it locally will find `manifest.yaml` and `VERSION` changed. Documented,
  and the placeholder-version convention makes it expected — but it will
  surprise someone once, and it interacts awkwardly with the dirty-tree refusal
  (the check runs *before* stamping; a second `build` in the same tree would
  otherwise refuse itself).
- **The ordering guarantee is invisible in the artifact.** Nothing in the
  archive declares "entries are ordered". A reimplementer who does not read this
  RFC produces archives that fail 0011's budget read — which is the argument for
  making that read fail closed rather than degrade, and it is why the refusal is
  specified here and not left to the implementer.
- **Refusing `+metadata` is a validation change.** A bundle that is valid today
  becomes invalid. Nothing in the corpus uses it and the manifest schema will
  say so, but it is a behaviour change to a shipped contract and should be
  called out in the changelog rather than slipped in.
- **`--version-from-git` will produce a wrong version in someone's CI.** Shallow
  checkouts are the default in the most popular CI system in the world. The
  design's answer is to fail loudly; the residual risk is a vendor who adds
  `fetch-depth: 0` after their first confusing failure rather than before it.
- **Two commands invite a wrapper that skips the signature.** The likely shape
  is a vendor script that runs `build && archive` and never signs, producing
  unsigned bundles that `archive` only warns about. The mitigation is the
  documentation showing the three-step recipe, which is weaker than a refusal
  and deliberately so.

## 10. Unresolved questions

1. Should `build` refuse to run when `metadata.version` is a placeholder and no
   `--version` was given? Refusing catches the vendor who forgot the flag;
   accepting keeps `build` usable as a sums-only command. Leaning toward
   accepting, because §5.2's third path is a real use.
2. Is `SOURCE_DATE_EPOCH` the right knob, or should the archive mtime be pinned
   to the commit date under `--version-from-git`? The commit date is more
   meaningful and couples the archive to git; the environment variable is the
   reproducible-builds convention and couples it to nothing.
3. Should `archive`'s output name be derived (`<name>-<version>.tar.zst`) or
   required? Derivation is convenient and guessable; a required `-o` is
   explicit. Implementation may settle this.

## 11. Decisions

| # | Decision | Rationale and consequence |
| --- | --- | --- |
| 1 | `build` and `archive` are **two commands**, not one | The signature lives inside the archive, so archiving must follow signing, and signing is the boundary the manager does not cross ([0012 decision 1](0012-packing-images-into-a-bundle.md)). Consequence: the documented recipe is three steps and always will be. |
| 2 | The archive's entry order is **locked**: `manifest.yaml`, `VERSION`, sums and signature, then everything else with `images/` last | [0011 decision 11](0011-bundled-container-images.md) reads the extraction budget from the stream and cannot do so otherwise. Consequence: this is a format property a reimplementer must honour, not an implementation detail. |
| 3 | 0011's budget read **fails closed** when the first entry is not `manifest.yaml` | A guarantee nothing checks decays. Consequence: hand-rolled archives carrying bundled images are refused unless the vendor orders them, which `publishing.md` must show. |
| 4 | Archives are **deterministic** — normalised uid/gid/mtime, `SOURCE_DATE_EPOCH` honoured | Closes the item [0012 §8](0012-packing-images-into-a-bundle.md) deferred to "the archiving step, which this RFC does not own". Free of consequence for identity: the content digest covers contents, paths and the executable bit, not timestamps. |
| 5 | The VCS scheme is `<next-patch>-dev.<distance>.g<sha>`, with a bare tag producing the tag verbatim | A prerelease sorts below its own release (verified), so the patch must be bumped for a dev build to sort forward. Same constraint and same answer as `setuptools-scm`'s `guess-next-dev`. |
| 6 | The sha is a **prerelease identifier**, never build metadata | OCI tag grammar excludes `+`, and metadata is retained by `String()` while ignored by `Compare` — so metadata-differing builds get different release directories that the digest-conflict check never compares. Consequence: version strings are longer and uglier, and they are correct. |
| 7 | `metadata.version` **refuses build metadata**; constraints keep accepting it | Closes the guard bypass in decision 6's rationale at the source. Consequence: a validation change to a shipped contract; nothing uses it today. |
| 8 | `--version` is the interface; `--version-from-git` is sugar that **fails loudly** with no reachable tag | Keeps a VCS out of the manager's core, and refuses the shallow-clone failure that would otherwise produce a plausible wrong version. |
| 9 | A dirty tree is refused unless `--allow-dirty`, which appends `.dirty` as a prerelease identifier | A build stamping a version that names a commit it is not is a lie the digest cannot catch. |
| 10 | `build` writes **in place**; there is no `--out` | Matches [0012 decision 7](0012-packing-images-into-a-bundle.md); copying a multi-gigabyte `images/` layout to protect a stamped field is a poor trade. Consequence: `build` leaves the working tree modified, which must be documented. |
| 11 | Sums regeneration is **one routine** called by both `pack` and `build` | A bundle without bundled images is otherwise the case nothing covers. |
| 12 | `archive` **warns** on a missing signature rather than refusing | `require_signature` is an operator-side policy; an unsigned bundle is legitimate for some vendors. Consequence: the "vendor forgot to sign" failure is caught by the operator's policy, not by the vendor's tooling. |

## 12. Phasing

- **P1 — `archive`, with the ordering lock and its two tests.** This is the part
  [0011](0011-bundled-container-images.md) depends on, so it should land with or
  before 0011 P1 rather than after. It needs neither `build` nor
  [0012](0012-packing-images-into-a-bundle.md).
- **P2 — `build`: sums regeneration and verification.** Independent of the
  version work; useful to any vendor immediately.
- **P3 — version stamping**, `--version` first and `--version-from-git` after
  it, plus the `+metadata` refusal and the documentation that finally tells a
  vendor what to do instead of republishing.

P1 is the phase with a deadline attached, because a locked decision in another
RFC rests on it. P3 is the one that makes a sandbox iteration loop pleasant, and
is otherwise optional.
