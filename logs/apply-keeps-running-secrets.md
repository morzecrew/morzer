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

## Review, 2026-10-16

Review of #104 found that the implementation was narrower than decision 20. The
row removes the secrets "only while nothing was started from them", but the
compensator asked only whether *this* operation had run `start-services`. An
`apply` or `update` that began over running containers and failed before
starting anything, at an image pull for example, still removed the secrets
those containers read. `TestAFailedApplyOverRunningServicesKeepsTheirSecrets`
and `TestAFailedUpdateKeepsTheSecretsOfTheRunningRelease` reproduced it.
Preflight now records that the project was running, and a runtime that cannot
answer counts as running. The row's wording now names both cases. What it
decides is unchanged.

```divergence
decision: 20
grade: LOCKED
kind: resolved
at: 2026-10-16T14:46:47Z
attempt: 2
claim: the compensator now keeps the secrets when preflight found the project running or could not tell, as well as after start-services, so it removes them only while nothing was started from them, as decision 20 says
evidence: internal/lifecycle/ops/apply.go:246-249
action: decided
```

Not covered: an `update` that stops before `start-services` leaves the previous
release running against the new release's rendering. Rendering prunes any
secret only the previous release declares, so a container of that release that
restarts cannot read it. Before #104 such an update removed every secret.
Rendering the previous release's secrets back would close the gap, but that
work belongs to the restore point and is not done here.

The second point in the same review is the other half of the first. The
compensator kept the secrets once `start-services` had begun, but that step
records it before `Up` returns. An `Up` that failed having started nothing
therefore left plaintext that no container read. The
`service start` case of `TestAFailedApplyThatStartedNothingRemovesItsSecrets`
reproduced it. Neither marker can answer the question decision 20 asks, so the
compensator now asks the runtime whether anything is running when it runs, and
the preflight record from the previous entry is gone. A runtime that cannot
answer still counts as running.

```divergence
decision: 20
grade: LOCKED
kind: resolved
at: 2026-10-16T14:52:20Z
attempt: 3
claim: the compensator removes the secrets only when the runtime reports nothing running, which covers containers an earlier operation started, a start-services that started some or none, and a runtime that cannot answer; this supersedes the preflight record of attempt 2
evidence: internal/lifecycle/ops/apply.go:383-387
action: decided
```

Review of #104 raised the gap noted above as a finding. Closing it means the
restore point re-renders the previous release's secrets when the update stopped
before `start-services`, and decision 19 scopes the restore to updates that got
that far.

```divergence
decision: 19
grade: LOCKED
class: spec-gap
at: 2026-10-16T16:37:20Z
attempt: 3
claim: an update that fails before start-services leaves the previous release running against the new release's rendered secrets, pruned of any only the previous release declares; the restore point that would render them back returns early because nothing was touched, and widening it is outside decision 19
evidence: internal/lifecycle/ops/update.go:925-927
action: halted
proposal: LOCKED — when an update stops before start-services and the secrets were kept, the restore point renders the previous release's secrets over them and brings nothing up; a failure to render ends at requires-manual-intervention
```

**Ruled 2026-10-17 by the author:** #104 merges without widening the restore
point, and the gap is tracked in
[#106](https://github.com/morzecrew/morzer/issues/106) with the proposal above.
