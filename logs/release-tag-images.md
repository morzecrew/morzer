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
