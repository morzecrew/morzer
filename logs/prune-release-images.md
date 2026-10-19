# Prune release images · a pruned release's images leave the store with it

On branch `prune-release-images` from `7aea28e`, building
[RFC 0031](../rfcs/0031-images-of-pruned-releases.md) P2: the `ImageRemover`
capability, the candidate and exclusion sets keyed as decision 14 rules, the
fail-safe, the order, the report, `dry_run`, and the deployment lock around
`release prune`.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

A reference that is already absent is not a failure. §4.5 lists "already gone"
among the reasons a reference is reported as kept, but "kept" says the name is
still in the store, and it is not. The case is ordinary: §2.2 measured that
removing an alias removes its repository's digest references on the same image,
so a pruned release set holding both `A` and `R@D` sees the second removal
answer `No such image`. `RemoveImage` treats an absent reference as done, as
`IngestImages` treats a present one, and the prune reports it as removed.

```divergence
decision: D-8
grade: ASSUMED
class: spec-gap
at: 2026-10-19T21:36:20Z
attempt: 1
claim: §4.5 reports an already-absent reference as kept, which claims a name the store no longer holds; removing an alias removes its digest reference first, so the case arises inside one prune
evidence: rfcs/0031-images-of-pruned-releases.md:414
action: departed
proposal: ASSUMED — RemoveImage is idempotent: a reference already absent from the store returns nil, and the prune reports it as removed; kept means the name is still there
```

The port gains its ninth optional capability. RFC 0023 §6 recorded 8 as the
baseline the Quadlet adapter is measured against. `ImageRemover` is one Podman
can implement with `podman image rm`, so it is not the stub §6 watches for, but
the count it states is now wrong.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-19T21:36:20Z
attempt: 1
claim: RFC 0023 §6 counts 13 core methods and 8 optional capabilities as P3's baseline; ImageRemover makes it 9
evidence: rfcs/0023-runtimes-beyond-compose.md:370
action: decided
proposal: ASSUMED — RFC 0023 §6's baseline reads 13 core methods and 9 optional capabilities, the ninth being RFC 0031's ImageRemover
```

## As built

The four `LOCKED` decisions this touches are built as written; each entry below
locates the implementation.

```divergence
decision: D-3
grade: LOCKED
kind: resolved
at: 2026-10-19T21:47:44Z
attempt: 1
claim: the exclusion is every reference of every release directory remaining in this installation and in every installation DiscoverProducts finds, whatever its role; TestPruneNeverRemovesWhatAnotherInstallationNames goes red when the other installations are dropped
evidence: internal/lifecycle/ops/prune.go:302
action: decided
```

```divergence
decision: D-4
grade: LOCKED
kind: resolved
at: 2026-10-19T21:47:44Z
attempt: 1
claim: the daemon's refusal is not relied on for references; removal is never forced, and the exclusion decides before any removal runs
evidence: internal/adapters/runtime/compose/compose.go:688
action: decided
```

```divergence
decision: D-5
grade: LOCKED
kind: resolved
at: 2026-10-19T21:47:44Z
attempt: 1
claim: an Undecidable directory, or any release directory on the machine whose manifest does not load, staging trees included, withholds all image removal while the directories are still pruned, and the report names the directory
evidence: internal/lifecycle/ops/prune.go:295
action: decided
```

```divergence
decision: D-14
grade: LOCKED
kind: resolved
at: 2026-10-19T21:47:44Z
attempt: 1
claim: candidates and remaining references are compared by normalized repository and digest, an alias keyed as its digest reference, and a candidate with no key is kept; TestPruneNeverRemovesWhatAnotherInstallationNames goes red when strings are compared instead
evidence: internal/domain/image.go:395
action: decided
```

Sabotage, each run against the suite and restored:

| Sabotage | Red |
|---|---|
| Only this installation's release store in the exclusion | `TestPruneNeverRemovesWhatAnotherInstallationNames`, `TestAnUnreadableReleaseAnywhereWithholdsImageRemoval` |
| Strings compared instead of `ImageKey` | `TestPruneNeverRemovesWhatAnotherInstallationNames` |
| An unloadable manifest skipped instead of withholding | `TestAnUnreadableReleaseAnywhereWithholdsImageRemoval` |
| Images removed before directories | `TestAPruneThatCannotRemoveADirectoryRemovesNoImage` |
| No lock in `PruneReleasesLocked` | `TestReleasePruneRefusesWhileTheDeploymentIsLocked` |
| The in-use text not matched, against Docker 29.7.2 | `TestRemoveImageAgainstARealDaemon/a_name_a_stopped_container_uses`, `TestRemoveImageTellsARefusalFromAnAbsentName` |
| `docker image rm -f` | the same two |
