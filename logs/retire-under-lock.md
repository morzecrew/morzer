# Retire under the lock · #206

`update` and `rollback` decided which recorded parameter values to retire from
the installation they read before taking the deployment lock
(`internal/lifecycle/ops/update.go:406`, `internal/lifecycle/ops/rollback.go:374`;
the lock is taken at `update.go:144` and `rollback.go:141`). A `config set`
landing in that window, for a parameter the target release does not declare,
stayed recorded. Fixed on branch `retire-under-lock` from `11fad80`.

The fix moves the decision into `stepRetireParameters`, which now retires
whatever the target release does not declare in the installation as recorded
when it runs, inside the operation and so under the lock. The set computed
before the lock still drops planned names from the converge's in-memory view
and names them in the step's description for a dry run. A late value can only
be for a parameter the target does not declare, and nothing the converge
renders reads one, so that view stays right.

Re-reading the installation under the lock and rebuilding the steps was the
alternative. It means building each operation inside the locked section, for
both `update` and `rollback`, and the plan a dry run shows would be built from
a different read than the run. The step-local change covers both operations
and a resumed run with one function.

RFC 0001 owns update and rollback, and its decision table carries no grades, so
its rows are treated as `LOCKED`, as `logs/rollback-preflight.md` does. Neither
row this touches is contradicted.

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
at: 2026-10-30T09:30:00Z
attempt: 1
claim: touched the governed area; update and rollback still reuse applySteps verbatim, and only the retire step that follows it changed
evidence: internal/lifecycle/ops/update.go:500-576
action: decided
```

```divergence
decision: D-8
grade: LOCKED
kind: resolved
at: 2026-10-30T09:30:00Z
attempt: 1
claim: touched the governed area; no port, event kind, exit code or state-schema field was added; the only new hook is on the test fake's locker
evidence: test/fakes/support.go:429-450
action: decided
```
