# Release push --tag-images · RFC 0039 P2

On branch `release-tag-images` from `b30a2007`, executing
[RFC 0039](../rfcs/0039-publishing-a-bundle-to-an-oci-registry.md) P2:
`morzer release push … --tag-images <prefix>`, decisions 20–28. Decisions 17
and 18, which P2 extends to images, are `LOCKED`; 20–28 are `ASSUMED`.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

### Files

| File | Change |
|---|---|
| `internal/domain/image.go` | `ImageTag`, the tag name (D-21, D-27), and `(*Manifest).ImagesToTag`, the selection (D-22), with the refusals U1, U2 and U4 below. |
| `internal/adapters/source/oci/push.go` | `PushRequest.Images` and `PushResult.Images`, and the tagging step between `published()` and `publish()` (D-23, D-28): the pinned digest resolved and tagged as it is (D-26), a tag already at that digest counted as tagged and one at another digest refused (D-24), nothing written under `--dry-run` (D-17, D-25). |
| `internal/cli/release_push.go` | The repeatable `--tag-images` flag; the report is also printed when tagging failed before any channel was tried (D-18, D-25). |
| `internal/ui/views/release.go` | One `image` line per image; `--json` gains `images` (D-25). |
| `test/fakes/registry.go` | Tags held per repository, and a way to seed an image manifest. |
| `test/clitest/release_push_test.go`, `internal/domain/image_test.go` | The cases in RFC 0039 §6's spirit, below. |
| `pages/docs/reference/release-commands.md`, `pages/docs/authoring/publishing.md`, `CHANGELOG.md` | The flag, the tag, the output, and why to use it. |

### Flow

1. **Selection** (D-22, U1, U2, U4), before anything is extracted to the
   registry: each prefix checked for shape, each image in key order taken or
   skipped with its reason, every prefix required to select an image, every
   selected image required to be off Docker Hub, and every tag name required to
   fit in 128 characters (D-21, D-27).
2. **Existence** of the version, unchanged (§5.3).
3. **Tagging** (D-23, D-24, D-26), per selected image in key order: resolve the
   pinned digest in the image's own repository, resolve the tag, then write the
   tag with the bytes the digest names when it is absent. The first failure
   stops the rest (U3).
4. **Push** of the bundle and **channels**, unchanged, only once every selected
   image is tagged (D-28).

## Decisions the RFC does not settle

The author ruled U1 and U2 on 2026-11-25 as the plan leaned, and left U3 to U6
to the build, also as it leaned.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-25T11:59:18Z
attempt: 1
claim: decision 22 lists every image not tagged with its reason, but does not say what a prefix that selects no image does; accepted, a typo in the prefix means nothing is protected and the push still succeeds, which is the failure P2 exists to prevent. A prefix that covers only `from: bundle` images selects none too
evidence: rfcs/0039-publishing-a-bundle-to-an-oci-registry.md:261
action: decided
proposal: ASSUMED — a `--tag-images` prefix that selects no `from: registry` image is refused with exit 2 before any registry request, naming the prefix
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-25T11:59:18Z
attempt: 1
claim: decision 22 matches a prefix against an image's repository, but `postgres@…` and `vendor/api@…` name no host, and the repository opener hands such a name to oras as it is, which reads `vendor` as a host and cannot reach Docker Hub at all; matching the reference as written would select `vendor/api` under the prefix `vendor` and then contact a host called `vendor`
evidence: internal/adapters/source/oci/oci.go:497-507
action: decided
proposal: ASSUMED — a prefix is matched against the repository normalized by Docker's rule (`internal/domain/image.go:409`), so `vendor/api` is `docker.io/vendor/api`; a Docker Hub image a prefix selects is refused with exit 2 before any registry request, rather than reached through `registry-1.docker.io`
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-25T11:59:18Z
attempt: 1
claim: decisions 23 and 25 stop at a tagging failure and report each image, but do not say in what order images are tagged or what the rest report once one fails; channels already answer both (decision 7), and a version whose images were not all tagged was not pushed by this run, which the report has to say rather than "already published"
evidence: internal/adapters/source/oci/push.go:149-169
action: decided
proposal: ASSUMED — images are tagged in key order, and the first failure stops the rest, which are listed as not tried, as are the channels; a version this run would have written reports `published: false` and reads "did not push"
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-25T11:59:18Z
attempt: 1
claim: decision 22 does not say what a prefix may look like; a trailing `/` changes nothing a reader means, while a scheme, a tag or a digest cannot be a repository prefix and would otherwise select nothing in silence or something by accident
evidence: rfcs/0039-publishing-a-bundle-to-an-oci-registry.md:261
action: decided
proposal: ASSUMED — a prefix's trailing `/` is trimmed; one that is empty, carries `oci://` or another scheme, or names a tag or a digest is refused with exit 2
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-25T11:59:18Z
attempt: 1
claim: decision 25 names what `--json` carries for images and leaves the names `ASSUMED`; P1's output must not change for a push without the flag, and a dry run reads as P1's channels do, the outcome the run would have with `dry_run: true`
evidence: rfcs/0039-publishing-a-bundle-to-an-oci-registry.md:264
action: decided
proposal: ASSUMED — `images` is omitted without `--tag-images`; each entry carries `name`, `reference`, `tag`, and `status` (`tagged`, `already-tagged`, `skipped`, `failed` or `not-tried`), with `reason` for a skipped image and `error` for a failed one
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-25T11:59:18Z
attempt: 1
claim: decision 26 tags the bytes the pinned digest names, but does not say what happens when the registry no longer has them: that is the deletion P2 exists to catch, so the push stopping there tells the vendor before a customer finds out
evidence: rfcs/0039-publishing-a-bundle-to-an-oci-registry.md:259
action: decided
proposal: ASSUMED — a pinned digest the image's repository no longer has stops the push before the bundle is written, with an error naming the image and its reference
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-25T11:59:18Z
attempt: 1
claim: the push tests' fake registry holds one repository, so a tag written to an image's repository would be indistinguishable from one written to the bundle's; the tests need tags held per repository, and a way to seed an image manifest a release can pin
evidence: test/fakes/registry.go:19-32
action: decided
proposal: ASSUMED — the fake keeps tags per repository and gains a way to seed a manifest; `Tagged` reads the bundle repository every existing test pushes to. A test arrangement, so it proposes no row
```

## Built

P2 is built as planned; nothing departed from the plan above or from a graded
row. The two `LOCKED` decisions it extends to images, closed out:

```divergence
decision: D-17
grade: LOCKED
kind: resolved
at: 2026-11-25T12:09:58Z
attempt: 1
claim: under --dry-run every image's pinned digest and tag are resolved and none is written; a tag at another digest or a deleted pinned image is refused as the push would refuse it, and each image reports what the run would do
evidence: internal/adapters/source/oci/push.go:271-275
action: decided
```

```divergence
decision: D-18
grade: LOCKED
kind: resolved
at: 2026-11-25T12:09:58Z
attempt: 1
claim: a tagging failure exits with that image's error, and --json carries the result beside it: each image's status, the error that stopped it, the images after it and every channel as not tried
evidence: internal/cli/release_push.go:132-138
action: decided
```

### Proposed rows for RFC 0039 §11

For the author to append, from U1 to U6 above, in order; U7 is a test
arrangement and proposes no row.

| # | Grade | Decision |
| --- | --- | --- |
| 29 | `ASSUMED` | **A `--tag-images` prefix that selects no `from: registry` image is refused**, exit 2, before any registry request. A typo would otherwise protect nothing while the push succeeds. Refines decision 22. See [logs/release-tag-images.md](../logs/release-tag-images.md) (U1). |
| 30 | `ASSUMED` | **A prefix is matched against the repository as Docker spells it**, host included, so `vendor/api` is `docker.io/vendor/api`; a Docker Hub image a prefix selects is refused, exit 2, before any registry request. Rejected: reaching Docker Hub through `registry-1.docker.io`, which no test here can exercise. Refines decision 22. See [logs/release-tag-images.md](../logs/release-tag-images.md) (U2). |
| 31 | `ASSUMED` | **Images are tagged in key order, and the first failure stops the rest**, which are listed as not tried, with every channel. A version this run would have written reports `published: false` and reads "did not push". Refines decisions 23 and 25. See [logs/release-tag-images.md](../logs/release-tag-images.md) (U3). |
| 32 | `ASSUMED` | **A prefix's trailing `/` is trimmed; an empty prefix, a scheme, a tag or a digest is refused**, exit 2. Refines decision 22. See [logs/release-tag-images.md](../logs/release-tag-images.md) (U4). |
| 33 | `ASSUMED` | **`images` is omitted without `--tag-images`.** Each entry carries `name`, `reference`, `tag` and `status`: `tagged`, `already-tagged`, `skipped` with `reason`, `failed` with `error`, or `not-tried`. Refines decision 25. See [logs/release-tag-images.md](../logs/release-tag-images.md) (U5). |
| 34 | `ASSUMED` | **A pinned digest the image's repository no longer has stops the push** before the bundle is written, with an error naming the image. Refines decision 26. See [logs/release-tag-images.md](../logs/release-tag-images.md) (U6). |

## Review of #429

Two findings from review are decisions the plan did not list. The first is a
gap: nothing refused a channel spelled as an image's tag. The second corrects
U5, which said every entry carries `tag`.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-25T13:31:46Z
attempt: 1
claim: decisions 21 and 27 name the tag each image gets, and P1 refuses a channel named like a version, but nothing refuses a channel spelled as an image's tag when the bundle is pushed to that image's repository; the channel move then writes the bundle's manifest under the image's tag, and the image is reported tagged while nothing keeps it
evidence: internal/adapters/source/oci/push.go:357-368
action: decided
proposal: ASSUMED — a `--channel` that is the tag `--tag-images` writes for an image, in the repository the bundle is pushed to, is refused with exit 2 before any registry request
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-25T13:31:46Z
attempt: 2
claim: U5 above said each entry carries `tag`, and row 33 says the same; a skipped image has no tag, because none is written for it, and the build leaves the field out. Decision 25 names the tag without saying a skipped image has one
evidence: internal/ui/views/release.go:249
action: decided
proposal: ASSUMED — `images` is omitted without `--tag-images`; each entry carries `name`, `reference` and `status`, with `tag` for an image the flag selects, `reason` for a skipped one and `error` for a failed one
```

### Findings

- **A bundle write that failed after tagging reported `published: true`.**
  `Push` set `Published` before writing the bundle. P1 never showed the result
  of a failed write, so the flag was invisible. With images tagged, the report
  is printed beside the error (D-18), and it read `pushed`. `Published` is now
  set once the write succeeds, or under `--dry-run` as before.
- **The reference said every image carries `tag`.** It now says the tag is
  given for each image the flag selects, as the second entry above records.

### Proposed rows, from review of #429

Row 35 is from the first entry above. Row 33 is restated from the second, and
replaces the row 33 proposed earlier in this log.

| # | Grade | Decision |
| --- | --- | --- |
| 33 | `ASSUMED` | **`images` is omitted without `--tag-images`.** Each entry carries `name`, `reference` and `status`: `tagged`, `already-tagged`, `skipped` with `reason`, `failed` with `error`, or `not-tried`. An image the flag selects also carries its `tag`. Refines decision 25. See [logs/release-tag-images.md](../logs/release-tag-images.md) (U5, attempt 2). |
| 35 | `ASSUMED` | **A `--channel` spelled as an image's tag, in the repository the bundle is pushed to, is refused**, exit 2, before any registry request. The channel move would write the bundle under the image's tag and leave the image unprotected while the push reports it tagged. Refines decisions 21 and 27. See [logs/release-tag-images.md](../logs/release-tag-images.md) (review of #429). |

## Second review of #429

### Findings

- **The channel check compared repositories as written.** A bundle pushed to
  `REGISTRY.example/demo/app`, with a channel spelled as the tag of an image in
  `registry.example/demo/app`, was not refused. A registry reads both names as
  one repository, so the move still wrote the bundle under the image's tag. The
  host is now compared in any case and the path exactly, as a registry compares
  them (`internal/adapters/source/oci/push.go:382-388`). Row 35 above holds,
  with that comparison added.
