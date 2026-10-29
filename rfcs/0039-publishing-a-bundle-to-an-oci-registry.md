# RFC 0039 — Publishing a bundle to an OCI registry

- **Status:** 📝 Draft — proposed, **awaiting the author's rulings** on the
  `OPEN` decisions in §11; not scheduled
- **Scope:** A `morzer release push` command that publishes a bundle archive to
  an OCI registry: it checks the archive, refuses to overwrite a published
  version with different content, pushes with morzer's own artifact and layer
  media types, moves channel tags only after the version is in place, and
  reports both the manifest digest and the content digest. It adds one command,
  an `ArtifactType` constant beside the existing `MediaType`, and manifest
  annotations a later push reads back. It does not change the bundle format,
  the OCI source's pull path, or what an installation trusts. It does not cover
  HTTPS uploads, tagging the images a release pins, or signing. It reverses
  [0014 §8](0014-building-a-release-bundle.md)'s "Publishing" item for OCI only.
- **Related:** [`pages/docs/authoring/publishing.md`](../pages/docs/authoring/publishing.md)
  "Publish it" and "Moving a channel";
  [0004](0004-distribution-and-verification.md) (the OCI source);
  [0014](0014-building-a-release-bundle.md) §8 and decision 12;
  [0016](0016-update-checking-and-unattended-updates.md) (channels);
  [`internal/adapters/source/oci/oci.go`](../internal/adapters/source/oci/oci.go);
  [`internal/cli/release.go`](../internal/cli/release.go) (`release fetch`).
- **Discussion:** issue #181.

---

## 1. Summary

`morzer release push demo-1.3.0.tar.zst oci://registry.example/demo/bundle
--channel stable` publishes an archive the way the docs tell vendors to with
`oras`. It adds the four steps every vendor pipeline writes around that call:
- it refuses a version already published with different content;
- it uses the media types morzer defines;
- it moves a channel only after the version is pushed;
- it prints the digests customers need.

A version already published with the same content is not an error, so a
pipeline that failed after the push can be re-run.

## 2. Motivation

The authoring guide publishes with `oras push` and `oras tag`
([`publishing.md:189-219`](../pages/docs/authoring/publishing.md)). Issue #181
lists what each vendor's CI then writes around those calls:

1. **An existence check.** The guide's rule is "never republish a version with
   different content" ([`publishing.md:254`](../pages/docs/authoring/publishing.md)),
   and `oras push` overwrites a tag without a word.
2. **The media types, spelled by hand.** A typo in the layer type is harmless
   for a single-layer artifact but not for one with several layers, which the
   OCI source then refuses (`bundleLayer`, `oci.go:360`).
3. **Ordering.** A channel tag moved before the version is fully pushed points
   machines at a half-published release.
4. **The digest.** The guide asks vendors to give customers the content digest
   for `update --digest`. A registry reports a different digest, of the OCI
   manifest, and pipelines confuse the two.

morzer already speaks OCI for pulling, already depends on `oras-go`, and
owns both media types.

## 3. Current state

- **The OCI source** pulls a single-layer artifact; the layer media type is the
  constant `MediaType` (`oci.go:58`). The artifact type
  `application/vnd.morzer.release.bundle.v1` exists only in the docs, and no
  code reads it.
- **Two digests.** `Peek` returns the *manifest* digest as a change token, and
  its comment says it "must never be compared" with the *content* digest
  (`oci.go:150-153`). `update --digest` takes the content digest
  (`commands.go:388`). A reference pinned as `oci://repo@sha256:…` is a manifest
  digest.
- **Tags are read as versions.** `List` parses every tag as a version and skips
  the rest (`oci.go:208`). A tag `1.3.0` pointing at a bundle whose manifest
  says `1.3.1` would be offered by `update --check` as `1.3.0`.
- **Credentials** for pulls come from the ambient Docker configuration
  (`DOCKER_CONFIG`, then `~/.docker/config.json`, `oci.go:532`). Loopback
  registries are reached over HTTP. `OpenRepository` (`oci.go:486`) is exported
  for `release pack` and returns a repository that can also push.
- **Archive checks.** The local source opens an archive with extraction limits
  and computes the content digest (`release.LoadAs`); `release fetch <ref>`
  (`release.go:439`) is the pull side of what this RFC adds.
- **0014 §8** scoped publishing out ([`0014:454`](0014-building-a-release-bundle.md)),
  noting that the `oras-go` push side was already imported.

## 4. Goals / Non-goals

**Goals**

- One command, safe to re-run, that a CI step can call after `release archive`.
- The same artifact shape `oras push --artifact-type` produces today, so
  bundles published either way are indistinguishable to the OCI source.
- Both digests reported, each labelled for what it is for.

**Non-goals**

- **An HTTPS upload.** There is no standard upload protocol to target; each
  host has its own.
- **Signing.** The manager does not hold signing keys (0014 §4).
- **A registry login.** Credentials stay where `docker login` puts them.

## 5. Design

### 5.1 The command

```sh
morzer release push <archive> oci://<registry>/<repository>[:<version>] \
    [--channel <tag>]... [--dry-run] [--json]
```

1. Open the archive through the local source: extraction limits, the sums
   completeness check and the content digest, as `release verify` checks a
   directory. A missing `SHA256SUMS.minisig` warns, as `release archive` does
   (0014 decision 12).
2. Take the version from the archive's manifest. A tag in the reference must
   equal it; omitted, it is derived (decision 2).
3. Look up `:<version>` in the repository (decision 3).
4. Push the layer, the empty config and the manifest, then tag the manifest
   with the version.
5. Resolve the version tag back. Only if it returns the pushed digest, move
   each `--channel` tag to that digest, one at a time.
6. Print the result (§5.4).

### 5.2 What is pushed

The shape `oras push --artifact-type` produces: an OCI image manifest with
`artifactType` set, the empty config descriptor, and one layer.

| Field | Value |
| --- | --- |
| `artifactType` | `application/vnd.morzer.release.bundle.v1` (new constant `ArtifactType`) |
| layer `mediaType` | `application/vnd.morzer.release.bundle.v1.tar+zstd` (`MediaType`) |
| annotation `org.opencontainers.image.title` | the product name |
| annotation `org.opencontainers.image.version` | the version |
| annotation `dev.morzer.bundle.digest` | the content digest |

The content-digest annotation is what decision 3's comparison reads, so a
later push answers "same content?" from one manifest request rather than by
downloading the layer.

### 5.3 A version that already exists

| Registry has `:<version>` | Result |
| --- | --- |
| No | Push. |
| Yes, annotation equals this archive's content digest | Nothing pushed; reported as `already-published`; exit 0; channels still move. |
| Yes, annotation differs | Refused; nothing pushed, no channel moved. |
| Yes, no annotation (pushed by `oras`) | The layer is pulled and its content digest computed, then as above. |

Exit 0 on identical content is what makes a re-run converge: a pipeline whose
channel move failed after the push can run the same step again.

### 5.4 Output

```
pushed demo 1.3.0 to oci://registry.example/demo/bundle:1.3.0
  manifest   sha256:5f1c…   pin with oci://registry.example/demo/bundle@sha256:5f1c…
  content    sha256:bcca…   give customers: morzer update … --digest sha256:bcca…
  channel    stable -> 1.3.0
```

`--json` carries `version`, `reference`, `manifest_digest`, `content_digest`,
`published` (false when `already-published`) and `channels` (each with
`moved` or the error that stopped it).

### 5.5 Alternatives considered

- **Keep documenting `oras`.** No new code, but every vendor keeps writing the
  existence check, and the docs cannot make `oras push` refuse an overwrite.
- **A composite GitHub Action around `oras`.** Fits beside `actions/setup`
  (#180), but serves one CI system and still cannot read the content digest
  out of the archive without morzer.
- **Derive identity from the layer digest instead of an annotation.** The
  archive's bytes depend on its timestamp, so the same bundle archived twice
  can differ; the content digest does not.

## 6. Tests

- A table test against the in-process registry in `test/suite/oci_test.go`,
  which today answers only the three requests a pull makes; it gains blob upload,
  manifest `PUT` and tag listing. The test covers: a fresh push; identical content (exit
  0, nothing pushed); different content (refused, no channel moved); an
  annotation-less artifact pushed by hand; a channel move after a push.
- Round trip: `release push` then `release fetch` gives the same content digest,
  and `update --check` lists the version.
- A tag in the reference that differs from the manifest's version is refused.
- `--dry-run` makes no write request to the registry.

## 7. Docs

- `publishing.md` "Publish it" and "Moving a channel" lead with
  `release push`. The `oras` recipe stays as the manual route, with the
  artifact type it must use.
- `release-commands.md` gets the command's reference entry, including both
  digests and when each is used.
- The docs must state the race in §9 plainly rather than imply the existence
  check is atomic.

## 8. Out of scope

- **Tagging the images a release pins** (`--tag-images`, raised in #181).
  Images live in other repositories, often under other credentials, and push
  would need write access to all of them. Decision 8 asks whether it should
  come later.
- **Verifying the signature against a vendor key before pushing.** Push warns
  when the signature is absent; a wrong key is still caught on the first
  install. Escape hatch: a `--key` flag, if a vendor ships a wrongly signed
  bundle.
- **HTTPS uploads.** See Non-goals.

## 9. Risks

- **The existence check is not atomic.** OCI distribution has no conditional tag
  write, so two pipelines pushing the same version at once can both pass the
  check, and the second tag write wins. Accepted: the docs recommend a CI
  concurrency group per repository.
- **Registries that reject an OCI 1.1 manifest.** Some older registries refuse
  `artifactType`. Push then fails with the registry's own error, and the manual
  `oras` route stays documented. P1 adds no fallback; whether one is needed is
  measured against the registries vendors report.
- **Two digests confuse readers.** Mitigated by labelling each with its use
  (§5.4), never printing either bare.

## 10. Unresolved questions

The `OPEN` rows in §11 are the author's to rule. Nothing else blocks the design.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `OPEN` | **Build `release push` now?** Recommended yes. The four steps in §2 are repeated in every vendor pipeline, the push side of `oras-go` is already a dependency, and the command is OCI-only. It reverses 0014 §8's "Publishing" item for OCI. |
| 2 | `OPEN` | **The reference: repository only, or `:<version>` required?** Recommended: both accepted. Omitted, the tag is the archive's version. Given, it must equal that version, because `List` reads tags as versions and a mismatch makes `update --check` offer a version the bundle does not carry. |
| 3 | `OPEN` | **A version already published: always refuse (as #181 proposes), or exit 0 when the content digest matches?** Recommended: exit 0 as `already-published` when it matches, and refuse when it differs. Exit 0 lets a re-run converge after a failed channel move. The refusal uses exit 2 (`usage`) with a distinct error message, so no new exit code is needed. |
| 4 | `OPEN` | **Moving a channel to a version older than the one it points at: allow, warn or refuse?** Recommended warn. Machines already refuse to follow a channel backwards, and a new installation taking the older version may be what the vendor wants. |
| 5 | `ASSUMED` | The artifact is the shape `oras push --artifact-type` makes (§5.2), with `ArtifactType` as a constant beside `MediaType`, so the OCI source reads bundles published either way. |
| 6 | `ASSUMED` | The manifest carries the content digest as the annotation `dev.morzer.bundle.digest`. Push compares that; it pulls the layer only for an artifact without it. |
| 7 | `ASSUMED` | Channel tags move only after the version tag resolves to the pushed digest, one at a time. A failure stops the rest and is reported per channel, and a re-run converges under decision 3's recommendation. |
| 8 | `OPEN` | **`--tag-images`, from #181: later, or never?** Recommended: not in P1, and built only when a vendor's registry retention deletes a pinned image. Push would need write access to every image repository. |
| 9 | `ASSUMED` | Credentials are the ambient Docker configuration through `OpenRepository`, with the pull side's loopback-HTTP rule. Push adds no credential flags. |
| 10 | `ASSUMED` | Output names both digests, each with its use (§5.4); `--json` carries both under distinct keys. |
| 11 | `ASSUMED` | The archive is checked through the local source before anything is sent: extraction limits, sums and content digest. A missing signature warns, as `release archive` does. |

## 12. Phasing

- **P1** — the command, decisions 1–7 and 9–11, the docs in §7. One PR.
- **P2** — `--tag-images`, only if decision 8 rules for it and a vendor asks.
