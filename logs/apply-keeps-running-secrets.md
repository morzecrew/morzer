# Apply keeps running secrets · a failed apply leaves the secrets its services read

Issue #102, on branch `apply-keeps-running-secrets` from `14bf569`. The issue
was filed from reading the code and is reproduced here before any change: with
the health checks failing, `TestApplyCompensatesWhenHealthChecksFail` finds no
`db_password` in the render directory after the apply, while the containers
`start-services` brought up are still running. The open question it answers is
the last entry of [previous-runtime.md](previous-runtime.md): whether apply's
secrets compensator should re-render rather than remove.

**Ruled 2026-10-16 by the author:** the secrets stay once services have
started.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

RFC 0001 carries no grades, so its rows are treated as `LOCKED`.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-16T07:57:49Z
attempt: 1
claim: the secrets compensator removed the render directory whatever had been started from it, and a test pinned that as intended; on apply, which has no earlier release to put back, a failure after start-services left running containers without their secrets, including on the apply every boot runs. The compensator now returns early once services were touched, and an update's restore renders the previous release's secrets over the directory, pruning what that release does not declare
evidence: internal/lifecycle/ops/apply.go:377-383
action: decided
proposal: LOCKED — RFC 0001 decision 20: compensation removes the rendered secrets only while nothing was started from them; once start-services has run they stay, and an update's restore renders the previous release's secrets over them.
```

The test that pinned the removal said "leaving plaintext credentials behind
after a failed apply is not a state to walk away from". That holds while
nothing reads them, and `TestAFailedApplyThatStartedNothingRemovesItsSecrets`
keeps it for that case. Once containers run from the directory, removing it does
not take the plaintext out of anything that has already read it; it only stops a
container that restarts.

**Drift count: 0.**
