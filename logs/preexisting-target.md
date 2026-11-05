# preexisting-target · RFC 0048, a configuration target that lands on an existing file

On branch `preexisting-target` from `6bd20291` and the RFC's branch, building
[RFC 0048](../rfcs/0048-a-configuration-target-that-lands-on-an-existing-file.md)
as ruled by the author on 2026-11-04: keep a copy, hold an unattended update,
and have `rollback` name the copy.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/lifecycle/ops/config_diff.go`: the one function of decision 2,
  and the tick's reason (decision 4).
- `internal/lifecycle/ops/apply.go`: the render step names each counted file
  in its plan (decision 7), copies it before replacing it (decisions 3, 5),
  records the copy in the journal, and removes the operation's copies when it
  compensates (§6).
- `update.go` and `rollback.go`: the summaries name the copies (decisions 3,
  6). `channel.go`: the gate beside the drift gate (decision 4).
- The operating page on updates, the manifest reference, the changelog.

Found while planning, and decided before any code.

The RFC says "the installed release" without saying when it is read. The
render step runs inside `update` before the new release is recorded as
current, and on a resume after that.

```divergence
decision: 1
grade: ASSUMED
class: spec-gap
at: 2026-11-04T21:52:00Z
attempt: 1
claim: the installed release is whatever is recorded as current when the step runs, and a current record whose release cannot be loaded leaves nothing to compare with
evidence: internal/lifecycle/ops/update.go:501
action: departed
proposal: ASSUMED — as decision 1, with the installed release read when the step runs. A resume after the new release became current finds the files already rendered, so nothing counts. When the current release cannot be loaded, as an update with a fallback release can find, no target counts as declared, so every differing file is copied rather than none.
```

Decision 6 has `rollback` name a copy without saying how it finds one, and
`rollback` does not know which update it undoes.

```divergence
decision: 6
grade: ASSUMED
class: spec-gap
at: 2026-11-04T21:52:00Z
attempt: 1
claim: the copies are found under `<VarDir>/replaced/`, where the copied path is the target's, without reading the journal for the update being undone
evidence: internal/lifecycle/ops/rollback.go:392
action: departed
proposal: ASSUMED — as decision 6, and `rollback` names every kept copy whose target the release it returns to does not render, found by walking `<VarDir>/replaced/`. A copy from an earlier update of the same target is named too, which is what an operator looking for the file wants.
```

Found while building.

RFC 0044 decision 14 holds a tick that cannot render to compare. Here a
candidate whose files cannot be rendered fails at its own render step, before
it replaces anything, so holding it would only replace that failure's message
with a vaguer one.

```divergence
decision: 4
grade: ASSUMED
class: discovery
at: 2026-11-04T21:58:00Z
attempt: 1
claim: a candidate that cannot be rendered replaces no file, because its update fails at the render step before the first write
evidence: internal/lifecycle/ops/config_diff.go:406-419
action: departed
proposal: ASSUMED — as decision 4, and a candidate the tick cannot render is not held by this gate. Its update fails at the render and says why, as any failed candidate does.
```

From #273's review.

The render step called the list before its write loop, and kept what the list
named. A file written after that scan and before the write was replaced with
no copy, and the scan rendered every template a second time.

```divergence
decision: 2
grade: ASSUMED
class: discovery
at: 2026-11-04T22:33:00Z
attempt: 2
claim: a list computed before the writes can miss a file that appears before them, since the deployment lock does not stop an operator writing one
evidence: internal/lifecycle/ops/apply.go:614-621
action: departed
proposal: ASSUMED — as decision 2 for the plan and the tick. The render step decides per file inside its write loop, from the installed release's targets and the bytes it is about to replace, so the copy is taken of what is actually there.
```

The tick's gate runs before the update, and the backup and staging run
between the two. A file written in that window met the attended render, which
kept a copy and replaced it with nobody watching.

```divergence
decision: 4
grade: ASSUMED
class: discovery
at: 2026-11-04T23:04:00Z
attempt: 2
claim: a gate before the update cannot see a file that appears while the update runs, and the render then treats the run as attended
evidence: internal/lifecycle/ops/apply.go:618-624
action: departed
proposal: ASSUMED — as decision 4, and the tick marks its update unattended. Its render refuses such a file as a failed preflight, before replacing it, rather than keep a copy. The update fails and is compensated, and the candidate is not retried until `morzer update --to` or the channel moves, as any failed candidate.
```

Review found that the manifest accepts two spellings of one target, such as
`/etc/nginx/./demo.conf` and `/etc/nginx/demo.conf`. The render wrote the
file twice. The second pass kept the first template's output as the copy, and
a failed update put the two backups back in map order and then removed the
copy. The operator's file was lost either way.

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-11-05T08:02:00Z
attempt: 3
claim: a release may declare one file under two target spellings, and the second render's copy and backup then hold the first render, not the file it replaced
evidence: internal/lifecycle/ops/apply.go:575-586
action: decided
proposal: ASSUMED — the render refuses a release whose configuration targets clean to the same path, as a validation error naming both, before it writes anything. The manifest's own validation is unchanged, so an installed release that declares one still loads.
```

Review found that a rollback's plan and its render disagreed. Rollback moves
the release pointer before it renders, so the render read the release it
returns to as the installed one and owned every target, while the plan, which
runs before the move, named a file the release being left does not render as
one it would keep. The render replaced that file and kept nothing.

```divergence
decision: 2
grade: ASSUMED
class: drift
at: 2026-11-05T08:24:00Z
attempt: 3
claim: the render read the installed release when it ran, which for a rollback is after the pointer has moved, so it computed the list from different inputs than the plan
evidence: internal/lifecycle/ops/rollback.go:396-398
action: departed
proposal: ASSUMED — as decision 2, and the installed release is the one the operation started from. Rollback passes the release it leaves to the render, which counts files against it; a resume, which rollback does not have, keeps reading the current release. A rollback keeps a copy of a counted file as an attended update does (decision 3), and names it in its summary.
```

**Drift count: 1.**
