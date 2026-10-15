# Previous runtime · a failed update puts the old release's services back

Item 4 of the ERP bundle's requests, on branch
`a-failed-update-restores-the-previous-runtime`, stacked on
`a-migrated-database-is-not-rolled-back` (decision 18), which decides when
compensation is allowed at all. The request: a failed update leaves the new
release's containers running because `start-services` has no compensator.
Verified: RFC 0001 decision 2 and §9 chose that deliberately and accepted the
risk on two mitigations, a hint naming `morzer apply` and a `doctor`
mismatch check, neither of which exists. One correction to the request: an
unattended machine is not left so for ever, because every boot runs
`apply --startup`.

**Drift count: 1.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

RFC 0001 carries no grades, so its rows are treated as `LOCKED`.

```divergence
decision: RFC 0001 D-2
grade: LOCKED
class: drift
at: 2026-10-15T08:30:00Z
attempt: 1
claim: §9 accepted leaving the new containers running on two mitigations, a failure message naming morzer apply and a doctor service/release mismatch check, and neither was built; the compensated error carries no hint and doctor checks only that services run
evidence: internal/lifecycle/ops/doctor.go:1148-1188
action: halted
proposal: LOCKED — reverses §9's accepted risk: when compensation is allowed, a failed update that reached start-services brings the previous release back up and waits for its health checks; a failure there is exit 12 with both failures. The two mitigations are superseded rather than built.
```

The drift above is the original implementation's, not this task's: the risk was
accepted on mitigations nobody built. It is counted in this log because this is
the first place it is written down. **Ruled 2026-10-15 by the author: "fix
finding 1 first, then do amendment".** Entries from here on record the build.

```divergence
decision: RFC 0001 D-2
grade: LOCKED
kind: resolved
at: 2026-10-15T06:05:01Z
attempt: 1
claim: built as ruled -- the staging step's compensator brings the previous release up after the configuration and secrets compensators have run, and waits for its health checks; recorded as decision 19
evidence: internal/lifecycle/ops/update.go:892
action: decided
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-15T06:05:01Z
attempt: 1
claim: start-services's own compensator would run first, newest-first, while the new release's configuration and secrets are still in place; the restore has to run last, so it lives in the staging step's compensator and is guarded by a flag start-services sets before Up, because a failed Up may still have replaced containers
evidence: internal/lifecycle/ops/apply.go:740
action: decided
proposal: ASSUMED — part of decision 19: restore from the earliest compensable step, guarded by whether services were touched.
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-15T06:05:01Z
attempt: 1
claim: the configuration compensator wrote every restored file at 0640 whatever it had been, which mattered little while nothing restarted and matters now that the previous release's containers read those files again
evidence: internal/lifecycle/ops/apply.go:487
action: decided
proposal: ASSUMED — part of decision 19: a restored file keeps its own mode.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T06:05:01Z
attempt: 1
claim: the request asks that a failed restore report both failures, and the engine's "rollback did not complete" error carried only the first; the compensators that failed are now named in it, for every operation, not only update
evidence: internal/lifecycle/engine/engine.go:406
action: decided
proposal: ASSUMED — an incomplete compensation names each compensator that failed, in the error itself.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T06:05:01Z
attempt: 1
claim: the secrets compensator removes the rendered directory, which the restore renders again for the previous release; on `apply`, which has no previous release to restore, a failure after start-services still removes the secrets from under the running containers, and that is left as found
evidence: internal/lifecycle/ops/update.go:908
action: decided
proposal: OPEN — whether apply's secrets compensator should re-render rather than remove; outside this task.
```

Sabotage, each mutant compiled and restored from a copy, with the change
committed first: ten on the guard in both directions, the flag, the secrets
render, the health wait, the Up error, the call itself, the mode in both
halves, and the engine's message. Ten killed. One more survives and is kept: a
render whose error is ignored, which the fakes cannot fail on the second render
alone; a restore without its secrets would fail its own health wait anyway.

**Drift count: 1.**
