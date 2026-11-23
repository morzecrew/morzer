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

Found on review, refining the first entry above. An index this key signed for
another channel is one no machine following this channel would accept, so it
should not count either. And a read the registry refuses is not an unreadable
index: it says nothing about what is there, so replacing it would skip the
check the entry exists for. The channel comparison is in
`internal/cli/release_index.go:246`.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-22T23:40:54Z
attempt: 2
claim: the first entry counted a same-key index that names another channel, and treated a read the registry refused as an unreadable index that is replaced
evidence: internal/adapters/source/oci/index.go:60-70
action: decided
proposal: ASSUMED — `release index` compares its sequence only with a published index whose signature verifies against the public half of `--key` and which names the same channel. Any other index under the tag, one that cannot be read, one another key signed, or one for another channel, is replaced, and the command warns that it was. A read the registry refuses, or cannot answer, stops the command before anything is pushed.
```

The machine's side, on branch `channel-index-follow` from `d52aa67f`: the
follower reads and checks the index (decisions 3, 9, 10, 16, 17 and 25 to 32).
Planning it found four questions the RFC does not settle, so the plan stopped
before code; the author ruled all four as recommended on 2026-11-23. Three
smaller ones were decided with them.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T07:29:03Z
attempt: 3
claim: §5.2 says a replayed index, or a different one under an accepted sequence, is refused, without saying what the poll does then, nor with an index whose signature or content does not hold
evidence: rfcs/0037-freshness-of-a-signed-release.md:226-232
action: decided
proposal: ASSUMED — an index the follower cannot accept, because its signature fails, it does not parse, it names another channel, its sequence is lower than the accepted one, or it reuses the accepted sequence with other bytes, counts as no index: decision 25 then judges the tag against the accepted index if there is one, and a channel with none is followed as today. The reason is recorded, and `status` and `doctor` report it. Refusing to stage over it adds nothing, since whoever can plant a bad index can delete it. Ruled by the author 2026-11-23.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T07:29:03Z
attempt: 3
claim: decision 10 compares the index with the bundle after a fetch, and an unmoved tag is never fetched, so a registry that serves the fresh index but holds the tag still is never caught: that index never expires
evidence: rfcs/0037-freshness-of-a-signed-release.md:357
action: decided
proposal: ASSUMED — every poll compares the accepted index's release digest with the content digest already known for what the tag points at: the staged candidate's, or the installed release's when the channel delivered it. When they disagree, the tick does not install the staged candidate, and `status` and `doctor` report that the index names one release while the tag still serves another. Ruled by the author 2026-11-23.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T07:29:03Z
attempt: 3
claim: decision 3 refuses only under unattended auto_apply without saying whether that refuses the stage or the install
evidence: rfcs/0037-freshness-of-a-signed-release.md:350
action: decided
proposal: ASSUMED — refines decision 3. Under `auto_apply`, a release behind an expired index is staged and not installed, with the expiry as the tick's reason, as the drift and stopped gates hold a release. Ruled by the author 2026-11-23.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T07:29:03Z
attempt: 3
claim: decision 9 fetches the index in the same poll as the peek without saying what a failed read of it does
evidence: rfcs/0037-freshness-of-a-signed-release.md:356
action: decided
proposal: ASSUMED — with a key pinned, a registry that cannot be reached for the index fails the poll, as a failed peek does. A read the registry answers with a refusal other than 404 counts as no index, and is recorded and reported. Without a pinned key the index is not consulted, and a failed read only leaves it unrecorded. Ruled by the author 2026-11-23.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T07:29:03Z
attempt: 3
claim: the index carries a `channel` field and §5.2 lists no check of it
evidence: rfcs/0037-freshness-of-a-signed-release.md:215
action: decided
proposal: ASSUMED — the follower accepts an index only when its `channel` is the followed reference's tag. Another channel's index, copied under this channel's index tag, would otherwise let whoever can push tags move a channel to a release the vendor published for another one.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T07:29:03Z
attempt: 3
claim: decision 17 records the index's expiry in state without saying where
evidence: internal/lifecycle/ops/channel_marks.go:24
action: decided
proposal: ASSUMED — the accepted indexes are a file under the manager directory beside the channel marks, keyed the same way and written atomically by the lifecycle layer, as decision 18 placed the marks. The same record holds what the last poll found that `status` and `doctor` report: an index this machine cannot check, one it did not accept and why, and a tag that disagrees with it.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T07:29:03Z
attempt: 3
claim: decision 25 names `update --stage --forget-index` without saying what it does alone or with nothing to forget
evidence: rfcs/0037-freshness-of-a-signed-release.md:372
action: decided
proposal: ASSUMED — refines decision 25. `--forget-index` is a usage error without `--stage`. It forgets the followed channel's accepted index under the deployment lock, journals that as a `release` operation, then polls; with no accepted index it forgets nothing, journals nothing and says so. Only expiry notifies (decision 28): a refused index is reported by `status` and `doctor`.
```

Found on the machine side's review (#412), and decided there. Each refines
an entry above; they are written here after the fixes landed, so that the rows
reconciled from this log carry them.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T13:41:00Z
attempt: 4
claim: the third entry of 07:29:03Z holds a release behind an expired or disagreeing index at the tick, which runs before the update takes the deployment lock, so a poll that lands while the update waits, or a change of `update.channel`, is not seen
evidence: internal/lifecycle/ops/channel_index.go:479-500
action: decided
proposal: ASSUMED — refines the third entry of 07:29:03Z. An unattended update asks again under its deployment lock whether the channel it was staged from holds the release, judged against that channel's accepted index and the keys in force then, and is refused before anything runs when it does. It is refused too when `update.channel` names another channel by then, since that channel's index says nothing about this release; the next tick follows the new one. An operator's update still does not read the index (decision 30).
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T13:41:00Z
attempt: 4
claim: decision 9 puts the index under `<channel>.index`, and a channel tag within five characters of the 128-character tag limit cannot carry that suffix
evidence: internal/adapters/source/oci/index.go:47
action: decided
proposal: ASSUMED — a channel whose tag cannot carry `.index` within the tag limit has no index, as a reference with no tag or a digest reference has none, so its poll neither fails nor records a problem.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T13:41:00Z
attempt: 4
claim: the last entry of 07:29:03Z journals `--forget-index` without saying in which order it journals and forgets, nor what a plan does
evidence: internal/lifecycle/ops/channel_index.go:429-470
action: decided
proposal: ASSUMED — refines the last entry of 07:29:03Z. `--forget-index` writes its journal record first and forgets the index only once the record is written, so a journal that cannot be written leaves the index accepted. A plan forgets nothing: it reports the sequence it would forget as `would_forget_index`, and polls as if that index were gone.
```
