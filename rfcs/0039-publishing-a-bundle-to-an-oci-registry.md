# RFC 0039 — Publishing a bundle to an OCI registry

- **Status:** 🚧 In progress — ruled by the author 2026-10-29: §11's `OPEN`
  rows as recommended. **P1, the command, built 2026-10-30**; the decisions the
  build had to make are rows 12–19, accepted by the author 2026-10-30 from
  [logs/release-push.md](../logs/release-push.md). P2 is gated on a vendor asking
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
  and loads it with `release.LoadAs`, which computes the content digest but does
  not check `SHA256SUMS`. `release verify` adds that with `checkBundleIntegrity`
  (`release.go:254`), and checks the signature when given `--signing-key`.
  `release fetch <ref>` (`release.go:439`) is the pull side of what this RFC adds.
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
    [--channel <tag>]... [--signing-key <key>] [--dry-run] [--json]
```

1. Extract the archive through the local source (extraction limits, content
   digest), then run on the extracted directory what `release verify` runs: the
   sums check and template parsing, and the signature check when
   `--signing-key` is given. Without it, a missing `SHA256SUMS.minisig` warns,
   as `release archive` does (0014 decision 12).
2. Take the version from the archive's manifest. A tag in the reference must
   equal it; omitted, it is derived (decision 2).
3. Look up `:<version>` in the repository (decision 3).
4. Push the layer, the empty config and the manifest, then tag the manifest
   with the version.
5. Resolve the version tag. Move each `--channel` tag, one at a time, to the
   digest it resolves to: the manifest just pushed, or on the
   `already-published` path the existing one whose content §5.3 confirmed. If
   the tag resolves to anything else, no channel moves.
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

The content-digest annotation is a hint, never proof: anyone who can push to
the repository can write it. A later push trusts it only to refuse (§5.3).

### 5.3 A version that already exists

| Registry has `:<version>` | Result |
| --- | --- |
| No | Push. |
| Yes, and its layer digest equals this archive's | `already-published`: nothing pushed, exit 0, channels still move. The registry checked the blob against that digest. |
| Yes, and its annotation names another content digest | Refused; nothing pushed, no channel moved. |
| Yes, otherwise (annotation equal or absent, layer bytes differ) | The layer is pulled and its content digest computed: equal is `already-published`, different is refused. |

The annotation decides only in the refusing direction. A manifest that lies
about it can make push refuse, never make it accept different content.

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

- An archive whose sums do not match is refused before any registry request.
- A table test against the in-process registry in `test/suite/oci_test.go`,
  which today answers only the three requests a pull makes; it gains blob upload,
  manifest `PUT` and tag listing. The test covers a manifest whose annotation
  matches while its layer differs (refused), and also: a fresh push; identical content (exit
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
  would need write access to all of them. Decision 8 defers it until a
  vendor's registry retention deletes a pinned image.
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

None. The author ruled §11's `OPEN` rows on 2026-10-29.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `LOCKED` | **Build `release push`.** The four steps in §2 are repeated in every vendor pipeline, the push side of `oras-go` is already a dependency, and the command is OCI-only. It reverses 0014 §8's "Publishing" item for OCI. Ruled by the author 2026-10-29. |
| 2 | `LOCKED` | **The reference names a repository, and `:<version>` is optional.** Omitted, the tag is the archive's version. Given, it must equal that version, because `List` reads tags as versions and a mismatch makes `update --check` offer a version the bundle does not carry. Ruled by the author 2026-10-29. |
| 3 | `LOCKED` | **A version already published with the same content is `already-published`, exit 0; with different content, push refuses.** Always refusing, as #181 proposed, would block the re-run that converges after a failed channel move. The refusal uses exit 2 (`usage`) with a distinct error message, so no new exit code is needed. Ruled by the author 2026-10-29. |
| 4 | `ASSUMED` | **Moving a channel to a version older than the one it points at warns, and moves it.** Machines already refuse to follow a channel backwards (`internal/lifecycle/ops/channel.go`), and a new installation taking the older version may be what the vendor wants. Ruled by the author 2026-10-29. |
| 5 | `ASSUMED` | The artifact is the shape `oras push --artifact-type` makes (§5.2), with `ArtifactType` as a constant beside `MediaType`, so the OCI source reads bundles published either way. |
| 6 | `ASSUMED` | The manifest carries the content digest as the annotation `dev.morzer.bundle.digest`, trusted only to refuse. "Same content" needs an identical layer digest, or a pulled layer whose content digest matches. |
| 7 | `ASSUMED` | Channel tags move only after the version tag resolves to the manifest just pushed, or to the existing one decision 6 confirmed, one at a time. A failure stops the rest and is reported per channel, and a re-run converges under decision 3. |
| 8 | `ASSUMED` | **`--tag-images`, from #181, is not in P1.** It is built only when a vendor's registry retention deletes a pinned image: push would need write access to every image repository. Ruled by the author 2026-10-29. |
| 9 | `ASSUMED` | Credentials are the ambient Docker configuration through `OpenRepository`, with the pull side's loopback-HTTP rule. Push adds no credential flags. |
| 10 | `ASSUMED` | Output names both digests, each with its use (§5.4); `--json` carries both under distinct keys. |
| 11 | `ASSUMED` | Before anything is sent, the archive is extracted through the local source and checked as `release verify` checks a directory: sums, templates, and the signature under `--signing-key`. Without that flag a missing signature warns, as `release archive` does. |
| 12 | `ASSUMED` | **The backwards-channel warning (decision 4) reads the version annotation of the manifest the channel points at, and is skipped when there is none.** One manifest request; pulling the layer would download a bundle to print a warning, and machines refuse to follow a channel backwards anyway. Accepted by the author 2026-10-30. See [logs/release-push.md](../logs/release-push.md) (U1). |
| 13 | `LOCKED` | **A `--channel` that parses as a semantic version is refused, exit 2.** `List` reads such a tag as a version, so it would carry decision 2's mismatch in through the channel. Ruled by the author 2026-10-30. See [logs/release-push.md](../logs/release-push.md) (U2). |
| 14 | `ASSUMED` | **Push takes only a `tar.zst`.** A directory is refused with a hint to run `release archive`, which owns the refusals between a tree and an archive. Accepted by the author 2026-10-30. See [logs/release-push.md](../logs/release-push.md) (U3). |
| 15 | `ASSUMED` | **The layer's title annotation is the archive's file name**, as `oras push` writes it and `oras pull` needs it; the manifest carries title (product), version and the content digest. Accepted by the author 2026-10-30. See [logs/release-push.md](../logs/release-push.md) (U4). |
| 16 | `ASSUMED` | **When the version tag resolves to a manifest other than the one this push wrote, no channel moves**, and push refuses with exit 2, naming both. Accepted by the author 2026-10-30. See [logs/release-push.md](../logs/release-push.md) (U5). |
| 17 | `LOCKED` | **`--dry-run` runs every check and every registry read, writes nothing, and refuses what push would refuse**; it reports `dry_run: true`, with `published` and each channel's `moved` saying what the run would do. Ruled by the author 2026-10-30. See [logs/release-push.md](../logs/release-push.md) (U6). |
| 18 | `LOCKED` | **A failed channel move exits with that step's error, and `--json` carries the result beside the error**, each channel with `moved` or its `error`. Ruled by the author 2026-10-30. See [logs/release-push.md](../logs/release-push.md) (U7). |
| 19 | `ASSUMED` | **The manifest's `created` annotation is left to oras**, so two pushes of one archive differ in manifest digest; identity never compares manifest digests (decision 6). Accepted by the author 2026-10-30. See [logs/release-push.md](../logs/release-push.md) (U9). |

## 12. Phasing

- **P1** — the command, decisions 1–7 and 9–11, the docs in §7. One PR.
- **P2** — `--tag-images`, only when a vendor's registry retention deletes a
  pinned image (decision 8).
