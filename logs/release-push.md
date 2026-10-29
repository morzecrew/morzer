# Release push · RFC 0039 P1

On branch `release-push` from `f039960`, executing
[RFC 0039](../rfcs/0039-publishing-a-bundle-to-an-oci-registry.md) P1:
`morzer release push <archive> oci://<repo>[:<version>]`, decisions 1–7 and
9–11, the docs in §7. Decisions 1–3 are `LOCKED`, the rest `ASSUMED`; decision 8
(`--tag-images`) is out of P1.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

### Shape

No new port. Push is an authoring command acting on a file named on the
command line, like `release pack`, which already calls `oci.OpenRepository`
from the CLI (`internal/cli/release_pack.go`). The registry work lives in the
OCI adapter beside the pull path it reuses; the archive checks live in the CLI
beside `release verify`'s, which they share.

### Files

| File | Change |
|---|---|
| `internal/adapters/source/oci/oci.go` | `ArtifactType` constant beside `MediaType` (D-5); annotation key constant `AnnotationContentDigest` (D-6). |
| `internal/adapters/source/oci/push.go` (new) | `(*Source).Push(ctx, PushRequest) (PushResult, error)`: the §5.3 existence table, layer upload, `oras.PackManifest` (v1.1, `ArtifactType`), version tag, re-resolve, channel moves one at a time (D-3, D-5, D-6, D-7, D-9). |
| `internal/cli/release_push.go` (new) | The command: reference and channel validation, extraction through the local source, `checkBundleIntegrity`, the signature check or the missing-signature warning, the call into the adapter, output (D-2, D-4, D-10, D-11). |
| `internal/cli/release.go` | Register `machineScope(newReleasePushCommand(app))`; the group's `Long` says it also publishes. |
| `internal/ui/views/release.go` | `Pushed` view: the §5.4 text, and the JSON keys `version`, `reference`, `manifest_digest`, `content_digest`, `published`, `dry_run`, `channels[{name, moved, error}]` (D-10). |
| `test/suite/oci_push_test.go` (new) | A writable in-process registry and the §6 table test at the adapter, plus the round trip through `Source.Resolve` and `Source.List`. |
| `test/clitest/release_push_test.go` (new) | The binary against the same registry on loopback: corrupt archive refused before any request, tag mismatch refused, `--dry-run` writes nothing, output names both digests, missing-signature warning. |
| `pages/docs/authoring/publishing.md` | "Publish it" and "Moving a channel" lead with `release push`; the `oras` recipe stays as the manual route with `--artifact-type`; the §9 race stated with the CI concurrency-group advice (§7). |
| `pages/docs/reference/release-commands.md` | `## release push`: flags, both digests and when each is used, the refusals and their exits (§7). |
| `CHANGELOG.md` | Unreleased `### Added`: `release push`. |
| `rfcs/0039-…md`, `rfcs/INDEX.md` | Status to 🚧 with P1 built, once it is; decision rows only by the author, from the proposals below. |

### Flow, and the row that governs each step

1. **Reference** (D-2). `ports.ParseRef`; scheme must be `oci`; a `@sha256:`
   reference is refused; a tag, when given, must equal the manifest's version.
2. **Archive** (D-11). Only a `tar.zst` (`atomicfs.IsTarZst`). Extracted with
   `local.New().Fetch` into a temporary directory (extraction limits, path
   refusals), loaded with `release.LoadAs(dir, archive)` for the content
   digest, then `checkBundleIntegrity` (providers, templates, `SHA256SUMS`).
   With `--signing-key`, `Deps.Verifier.Verify(... Required: true)`; without
   it, a missing `SHA256SUMS.minisig` warns in `release archive`'s words. No
   registry request happens before all of this passes.
3. **Existence** (D-3, D-6), one `Resolve` of `:<version>`:
   - not found: push;
   - found: fetch its manifest (digest-verified, `fetchManifest`), pick the layer
     (`bundleLayer`). Layer digest equals the archive's sha256: `already-published`,
     no layer pulled. Else the `dev.morzer.bundle.digest` annotation names a
     different content digest: refused, nothing pulled. Else (annotation equal
     or absent): pull the layer through `writeLayer`, resolve it through the
     local source, compare content digests: equal is `already-published`,
     different is refused.
4. **Push** (D-5). `HEAD` the layer, upload it when absent; `oras.PackManifest`
   with `PackManifestVersion1_1`, `ArtifactType`, the one layer and the §5.2
   manifest annotations (pushes the empty config); `Tag` with the version.
5. **Channels** (D-7, D-4). Re-resolve `:<version>`; it must be the manifest just
   pushed, or on the `already-published` path the one step 3 confirmed. Then each
   `--channel` in order: read its current manifest's version annotation for the
   backwards warning, `Tag`. The first failure stops the rest.
6. **Output** (D-10). §5.4; `--json` as above.

### Refusals and exits

All through the existing constructors, so no new exit code (D-3).

| Case | Constructor | Exit |
|---|---|---|
| Not `oci://`, a digest reference, a directory, tag ≠ manifest version, a channel that parses as a version | `domain.Usage` | 2 |
| Extraction, integrity, template, provider or signature failure | as `release verify` returns them | 2 |
| Version published with different content (annotation or pulled content) | `domain.ValidationError`, message names both content digests | 2 |
| Version tag resolves elsewhere after the tag write | `domain.ValidationError`, names both manifest digests; no channel moved | 2 |
| Registry 401/403 | `registryError` | 2 |
| Registry unreachable or 5xx, on any step | `registryError` → `RuntimeError` | 7 |
| A channel move fails | the step's `registryError`; `--json` still carries `channels` with what moved | 2 or 7 |

## Decisions the RFC does not settle

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T22:32:39Z
attempt: 1
claim: decision 4 warns when a channel moves to an older version but does not say how push learns the version the channel points at; reading the version annotation of the channel's current manifest costs one manifest request, pulling its layer would download a bundle to print a warning, and an artifact pushed by hand carries no annotation, so the warning is skipped when the annotation is absent or does not parse
evidence: internal/lifecycle/ops/channel.go:336-346 — machines refuse to follow a channel backwards anyway, so a missed warning moves nobody
action: decided
proposal: ASSUMED — the backwards warning reads `org.opencontainers.image.version` from the channel's current manifest and is skipped when it is absent
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T22:32:39Z
attempt: 1
claim: decision 2 guards the version tag against a mismatch, but a --channel named like a version is listed by List as one, so `--channel 1.4.0` on a 1.3.1 bundle makes update --check offer 1.4.0 for a bundle that carries 1.3.1 -- the same hazard
evidence: internal/adapters/source/oci/oci.go:209-229
action: decided
proposal: LOCKED — a --channel that parses as a semantic version is refused (exit 2)
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T22:32:39Z
attempt: 1
claim: §5.1 names the input an archive, but the local source also accepts a directory; pushing a directory would mean archiving it inside push, which is `release archive`'s job and its refusals (checkArchivable)
evidence: internal/cli/release_archive.go:109-157
action: decided
proposal: ASSUMED — push takes only a tar.zst; a directory is refused with a hint to run `release archive`
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T22:32:39Z
attempt: 1
claim: §5.2 lists the title annotation without saying on which descriptor; `oras push` writes the file name as the layer's title, and `oras pull` skips a layer without one, so a customer pulling by hand would get nothing
evidence: pages/docs/authoring/publishing.md:195
action: decided
proposal: ASSUMED — the layer carries `org.opencontainers.image.title` = the archive's file name; the manifest carries title (product), version and `dev.morzer.bundle.digest`
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T22:32:39Z
attempt: 1
claim: decision 7 moves channels only once the version tag resolves to the manifest just pushed, but does not say what happens when it does not -- another pipeline's tag write landed after this one's
evidence: rfcs/0039-publishing-a-bundle-to-an-oci-registry.md:216-219
action: decided
proposal: ASSUMED — no channel moves; refused with exit 2, naming the manifest written and the one the tag resolves to
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T22:32:39Z
attempt: 1
claim: §5.1 lists --dry-run and §6 says it makes no write request, but nothing says what it reports; every read (including the content-compare pull) is allowed, and a refusal still refuses, so a pipeline can dry-run the existence check
evidence: rfcs/0039-publishing-a-bundle-to-an-oci-registry.md:195
action: decided
proposal: ASSUMED — --dry-run runs every check and read, writes nothing, refuses what push would refuse, and reports `dry_run: true` with `published` and each channel's `moved` describing what the run would do; the manifest digest is omitted for a version not yet pushed
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T22:32:39Z
attempt: 1
claim: decision 7 reports a failed channel move per channel, but a failing command's --json is an error envelope; the envelope already carries data beside the error
evidence: internal/cli/root.go:281
action: decided
proposal: ASSUMED — a failed channel move exits with that step's error, and --json carries the result (with each channel's `moved` or `error`) beside it
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T22:32:39Z
attempt: 1
claim: §6 says the in-process registry gains blob upload, manifest PUT and tag listing; that registry serves one fixed artifact for any tag, which ten pull tests depend on, and a writable one needs per-tag state
evidence: test/suite/oci_test.go:41-65
action: decided
proposal: ASSUMED — a separate map-backed writable registry in test/suite/oci_push_test.go (version probe, manifests GET/HEAD/PUT by tag or digest, blobs HEAD/GET, POST upload with a monolithic PUT, tags/list, a write counter); the pull fake is unchanged
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T22:32:39Z
attempt: 1
claim: `oras.PackManifest` stamps `org.opencontainers.image.created` with the current time, as `oras push` does, so two pushes of one archive produce different manifest digests; identity never compares manifest digests (decision 6), so this costs nothing but the dry-run's manifest digest
evidence: internal/adapters/source/oci/oci.go:150-153
action: decided
proposal: ASSUMED — the created annotation is left to oras; the manifest digest is reported only for a manifest that exists
```

## Ruled 2026-10-30

The nine entries above are U1 to U9, in order. The author ruled the three that
change what a user or a script sees as proposed: U2 (a channel named like a
version is refused), U6 (what `--dry-run` reports) and U7 (`--json` on a failed
channel move). U1, U3, U4, U5, U8 and U9 stand as `ASSUMED`.

### Proposed rows for RFC 0039 §11

For the author to append; the table is append-only, and the RFC's prose is not
edited to match what was built. U8 is a test arrangement rather than a design
decision, so it proposes no row.

| # | Grade | Decision |
| --- | --- | --- |
| 12 | `ASSUMED` | **The backwards-channel warning (decision 4) reads the version annotation of the manifest the channel points at, and is skipped when there is none.** One manifest request; pulling the layer would download a bundle to print a warning, and machines refuse to follow a channel backwards anyway. See [logs/release-push.md](../logs/release-push.md) (U1). |
| 13 | `LOCKED` | **A `--channel` that parses as a semantic version is refused, exit 2.** `List` reads such a tag as a version, so it would carry decision 2's mismatch in through the channel. Ruled by the author 2026-10-30. See [logs/release-push.md](../logs/release-push.md) (U2). |
| 14 | `ASSUMED` | **Push takes only a `tar.zst`.** A directory is refused with a hint to run `release archive`, which owns the refusals between a tree and an archive. See [logs/release-push.md](../logs/release-push.md) (U3). |
| 15 | `ASSUMED` | **The layer's title annotation is the archive's file name**, as `oras push` writes it and `oras pull` needs it; the manifest carries title (product), version and the content digest. See [logs/release-push.md](../logs/release-push.md) (U4). |
| 16 | `ASSUMED` | **When the version tag resolves to a manifest other than the one this push wrote, no channel moves**, and push refuses with exit 2, naming both. See [logs/release-push.md](../logs/release-push.md) (U5). |
| 17 | `LOCKED` | **`--dry-run` runs every check and every registry read, writes nothing, and refuses what push would refuse**; it reports `dry_run: true`, with `published` and each channel's `moved` saying what the run would do. Ruled by the author 2026-10-30. See [logs/release-push.md](../logs/release-push.md) (U6). |
| 18 | `LOCKED` | **A failed channel move exits with that step's error, and `--json` carries the result beside the error**, each channel with `moved` or its `error`. Ruled by the author 2026-10-30. See [logs/release-push.md](../logs/release-push.md) (U7). |
| 19 | `ASSUMED` | **The manifest's `created` annotation is left to oras**, so two pushes of one archive differ in manifest digest; identity never compares manifest digests (decision 6). See [logs/release-push.md](../logs/release-push.md) (U9). |

## Built

P1 is built as planned; nothing departed from the plan above or from a graded
row. The three `LOCKED` decisions it touched, closed out:

```divergence
decision: D-1
grade: LOCKED
kind: resolved
at: 2026-10-29T22:48:29Z
attempt: 1
claim: built `release push` as an OCI-only command, the push side through the oras-go already in go.mod
evidence: internal/adapters/source/oci/push.go:89-162
action: decided
```

```divergence
decision: D-2
grade: LOCKED
kind: resolved
at: 2026-10-29T22:48:29Z
attempt: 1
claim: the reference takes a repository with an optional :<version>; omitted, the tag is the archive's version; given and different, it is refused before any request (exit 2)
evidence: internal/adapters/source/oci/push.go:226-256
action: decided
```

```divergence
decision: D-3
grade: LOCKED
kind: resolved
at: 2026-10-29T22:48:29Z
attempt: 1
claim: a version already published with the same content returns published=false and exit 0, with channels still moved; different content is refused through domain.ValidationError, exit 2, with no new exit code
evidence: internal/adapters/source/oci/push.go:168-221
action: decided
```
