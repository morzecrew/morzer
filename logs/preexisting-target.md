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
