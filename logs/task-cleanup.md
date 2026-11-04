# task-cleanup · completed dependencies a one-shot leaves behind

On branch `task-cleanup` from `afc8eaf`. An operator ran the `reset` task of a
bundle whose task service depends on four one-shot services behind
`profiles: [reset]`. `compose run --rm reset` started the chain, removed only
the `reset` container, and left the four exited with code 0. `status` then
listed them with ✗, beside a deployment that was healthy.

This removes, after every one-shot run with `Remove`, the containers of
services the configuration does not start that exited with code 0, and shows a
stopped container's exit code in `status`.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

RFC 0035 decision 6 has a task's one-shot start its dependencies, as a
migration does. Nothing says what becomes of them once they finish.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-04T16:01:00Z
attempt: 1
claim: `compose run --rm` removes only the one-shot's own container, so dependencies it started that finish stay as exited containers, and `status` draws them as failures
evidence: internal/adapters/runtime/compose/compose.go:562-565
action: decided
proposal: ASSUMED — after a one-shot run with `Remove`, the containers of services the configuration does not start (behind an inactive profile) that exited with code 0 are removed by name. A dependency that failed stays, for `docker compose logs`, and so does any container of a service the configuration starts, which `up` would leave the same way. A removal that fails is logged and does not change the run's result.
```
