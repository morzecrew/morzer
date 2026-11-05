# restart-service · RFC 0050 P1, `morzer restart <service>`

On branch `restart-service` from the RFC's branch at `447d1e35`, building
[RFC 0050](../rfcs/0050-restarting-a-service-and-an-interactive-exec.md) P1
as ruled by the author on 2026-11-05: RFC 0021 decision 8 reopened, and
`restart` re-creates and is journalled, not attested.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/domain/operation.go`: `OpTypeRestart` (decision 4).
- `internal/lifecycle/ops/restart.go`: `Restart`, shaped as `Stop` is: the
  services checked against `Runtime.Validate`'s list before anything runs,
  refused while stopped, under the deployment lock, gated on unfinished
  operations, and two steps: re-create through `d.recreate` (decision 1),
  then `stepHealthChecks` (decision 4). Not attested (decision 3).
- `internal/cli/restart.go`: the command, beside `stop` and `start`, with
  service completion as `logs` has.
- Tests in `test/suite`; `looking-inside.md` and the command reference; the
  changelog.

Found while planning, and decided before any code.

The RFC says a failed health check fails the restart "with nothing to
compensate". The health step `apply` uses compensates on failure, and a
compensated record reads as rolled back.

```divergence
decision: 4
grade: ASSUMED
class: spec-gap
at: 2026-11-05T13:00:00Z
attempt: 1
claim: the reused health step's failure policy is Compensate, which would journal a failed restart as compensated when nothing was undone
evidence: internal/lifecycle/ops/apply.go:1119-1125
action: departed
proposal: ASSUMED — as decision 4. Both of the restart's steps abort on failure, so a failed restart is journalled as failed and the summary says which service did not come back healthy.
```

The RFC does not say what happens to a restart that is interrupted. `apply`,
`update`, `stop` and `start` resume; `config` does not, and the gate tells
the operator to clear it.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-05T13:00:00Z
attempt: 1
claim: the RFC is silent on resuming an interrupted restart, and the unfinished-operation gate then refuses every later operation
evidence: internal/lifecycle/preflight/preflight.go:604-618
action: decided
proposal: ASSUMED — a restart has no `--resume`. Its steps are idempotent, so an interrupted one is cleared with `morzer status --clear-intervention`, as `config` is, and run again.
```

The RFC says the re-create is "without dependencies". `d.recreate` names the
services to `up` without `--no-deps`, so Compose does not re-create a
dependency but starts one that is down.

```divergence
decision: 1
grade: ASSUMED
class: discovery
at: 2026-11-05T13:00:00Z
attempt: 1
claim: "without dependencies" as `--no-deps` would leave a stopped dependency down and the restarted service waiting on it
evidence: internal/lifecycle/ops/config.go:434-444
action: departed
proposal: ASSUMED — as decision 1, through `d.recreate` as `config set` uses it: the named services are re-created, forced, and a dependency is started if it is down but never re-created.
```

From #279's review.

The re-create resolves a bundled image through its local alias, a mutable
tag. `apply` refuses a missing one in its preflight; the restart had no
preflight, so Compose would have pulled whatever the vendor's registry holds
under that tag.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-05T13:23:00Z
attempt: 2
claim: a restart with a bundled image missing from the store re-created the service from an image pulled under a mutable tag
evidence: internal/lifecycle/ops/restart.go:124-140
action: decided
proposal: ASSUMED — a restart's first step runs apply's `images.bundled` check and refuses, naming `release ingest`, before anything is re-created.
```

A health check's `OPERATION_TYPE` was `apply` whatever ran it, against the
hook reference. The step now passes the operation running it, which gives
the ABI a value it did not have.

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-11-05T13:23:00Z
attempt: 2
claim: hooks are told the operation running them, and a health check during a restart is run by a restart
evidence: internal/lifecycle/ops/apply.go:1130-1134
action: decided
proposal: ASSUMED — a health check run by `morzer restart` sees `OPERATION_TYPE=restart`, a value the hook reference now lists; under `update`, `rollback` and `start` it sees those.
```

From #278's second review. Attempt 1's proposal for decision 1 said a
dependency is never re-created. `up` converges every dependency it touches,
so one whose container no longer matches the release is re-created, as the
Compose adapter's own note records for `run`.

```divergence
decision: 1
grade: ASSUMED
class: discovery
at: 2026-11-05T13:40:00Z
attempt: 3
claim: a forced `up` of named services still re-creates a dependency whose container has diverged from the release
evidence: internal/adapters/runtime/compose/compose.go:557-560
action: departed
proposal: ASSUMED — supersedes attempt 1's proposal. The named services are re-created, forced; their dependencies are converged as any `up` converges them, not forced: started if down, re-created only when their container no longer matches. After an `apply` every dependency matches, so only the named services are replaced.
```
