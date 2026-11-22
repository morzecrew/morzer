# channel-index · RFC 0037 P2, the signed channel index

On branch `channel-index-publish` from `a1ab0bd0`, building
[RFC 0037](../rfcs/0037-freshness-of-a-signed-release.md) P2 as ruled by the
author on 2026-11-22 (decisions 23 to 33), in two parts. The vendor's side
comes first: `morzer release index` signs and publishes the index (decisions
24, 27 and 33) and the publishing guide shows how to keep it fresh. The
machine's side, which reads and checks it, follows on its own branch and
appends here.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Two things the vendor's side needs that the RFC does not settle, found while
planning and decided before any code.

Decision 24 refuses a sequence that is not higher than the index already
published under the tag, and does not say which published index counts. A tag
is writable by anyone who can push to the repository, so one carrying a
planted index at a high sequence would stop the vendor re-signing until their
clock passed it. `state publish` met the same question for its serials and
counts only what its key signed.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-22T22:52:42Z
attempt: 1
claim: decision 24 compares the new sequence with the published index's without saying whether an index another key signed, or one that cannot be read, counts
evidence: internal/lifecycle/ops/state.go:161-168
action: decided
proposal: ASSUMED — `release index` compares its sequence only with a published index whose signature verifies against the public half of `--key`. Any other index under the tag, one that cannot be read or one another key signed, is replaced, and the command warns that it was.
```

§5.2 names the two files the artifact carries and grades the mechanics
`ASSUMED`. The media types are what a machine selects the layers by, and what
the guide's hand-signing route has to pass to `oras push`.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-22T22:52:42Z
attempt: 1
claim: §5.2 names index.json and index.json.minisig but not the artifact's media types, which the reader selects layers by and a hand-made artifact must match
evidence: rfcs/0037-freshness-of-a-signed-release.md:223-225
action: decided
proposal: ASSUMED — the index is an artifact of type `application/vnd.morzer.channel.index.v1` with two layers, `index.json` as `application/vnd.morzer.channel.index.v1+json` and its signature as `application/vnd.morzer.channel.index.v1.minisig`, each titled by its file name as `oras push` titles it. Each layer is read up to 64 KiB, and a layer over that is refused.
```
