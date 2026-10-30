# Rollback preflight · #199 and #200

Two rollback defects filed on 2026-10-30, fixed on branch `rollback-preflight`
from `f039960`: a rollback refused by preflight left the release pointer moved
(#199), and a value for a parameter only the newer release declares refused the
rollback (#200).

RFC 0001 owns update and rollback, and its decision table carries no grades, so
its rows are treated as `LOCKED`. Neither fix contradicts one. Decision 1 is
about `update` reusing `applySteps`, which it still does verbatim; rollback
still reuses it too, with apply's first step hoisted ahead of the pointer swap.
Decision 8 rules out new ports, event kinds, exit codes and state-schema
fields, and the fixes add none: the retire step is update's own, and the
refusal exits 3 through the existing preflight mapping. The two close-outs
below record that.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

```divergence
decision: D-1
grade: LOCKED
kind: resolved
at: 2026-10-30T12:00:00Z
attempt: 1
claim: touched the governed area; update still reuses applySteps verbatim, and rollback reuses it with preflight run before the pointer swap rather than after it
evidence: internal/lifecycle/ops/rollback.go:363-390
action: decided
```

```divergence
decision: D-8
grade: LOCKED
kind: resolved
at: 2026-10-30T12:00:00Z
attempt: 1
claim: touched the governed area; no port, event kind, exit code or state-schema field was added, and rollback's retire step is update's stepRetireParameters
evidence: internal/lifecycle/ops/update.go:487-549
action: decided
```

RFC 0001's rollback table says "All clear: swap the pointer, run `apply`
against the previous release". The order now runs apply's preflight, a
read-only step, before the swap and the rest of apply after it. That is the
table's behaviour with its check moved ahead of the first mutation, which is
what `Abort` on preflight already assumes (`internal/lifecycle/engine/step.go:25-27`).

`update` does not have #199's shape. Its steps before preflight are
`verify-bundle`, `check-compatibility`, `pre-update-backup`, `restore-point`,
`stage-release`, `ingest-images` and `generate-secrets`
(`internal/lifecycle/ops/update.go:410-442`). None of them moves the pointer:
`stage-release` only copies the bundle into the store, and `record-state`,
apply's last step, is what moves it.
