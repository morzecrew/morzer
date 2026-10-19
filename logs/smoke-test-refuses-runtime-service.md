# Smoke test refuses runtime-service · only migrate runs a one-shot container

Item M7 of the ERP bundle's requests after 0.4.0, on branch
`smoke-test-refuses-runtime-service` from `f927b4c` (v0.4.0). The request
offered two fixes: run a `runtime-service` smoke test as a service, as migrate
does, or refuse it at load. The author ruled for refusing it at load.

Measured before deciding which operations it covers. `kind` is validated per
step (`checkOperation`, `internal/domain/manifest.go`), which requires a
`service` and refuses a `command` for `runtime-service`, but nothing ties a
kind to an operation. Only migrate dispatches on the kind
(`runMigrateStep`, `internal/lifecycle/ops/apply.go:727-742`). The
other three hand the step's `command` to the hook runner whatever its kind:
the smoke test at `internal/lifecycle/ops/apply.go:977` and restore at
`internal/adapters/backup/hookbackup/backup.go:975`. A `runtime-service` step
has no command, and the runner refuses an empty one as an internal error
(`internal/adapters/hooks/hooks.go:94-96`). So a `runtime-service` smoke test
failed every `apply` that reached it, and a `runtime-service` restore failed
every restore. Backup alone refused one, and only when a backup ran
(`backup.go:268-271`).

No RFC row settles which operations accept which kind. RFC 0018 carries no
grades, so its rows are treated as `LOCKED`, and none is contradicted.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-17T20:54:19Z
attempt: 1
claim: a runtime-service step was accepted under every operation and only migrate can run one; smoke_test, backup and restore are now refused at load unless they are hooks, with a message naming hook as what they support
evidence: internal/domain/manifest.go:1150
action: decided
proposal: LOCKED — RFC 0018 decision (next free number): runtime-service is accepted only for migrate; smoke_test, backup and restore must be hooks. Not added to the RFC on this branch, because the branch for issue #101 adds decision 15 to the same table.
```

Backup's own refusal at run time (`backup.go:268-271`) is left in place. A
manifest that passed validation can no longer reach it, but it costs nothing
and it covers a manifest built in code rather than loaded.

## Recorded 2026-10-19

The proposal above is RFC 0018 decision 16, added once #110's decision 15 had
merged.
