# Migrate step context · a step is told what is running it

ERP request M6, issues #99 and #100, on branch `migrate-step-context`. Both are
proposals left `OPEN` in `logs/migrate-steps.md`:

- Every migrate step was told `OPERATION_TYPE=apply` with no
  `PREVIOUS_VERSION`, on a first install, an update and a reboot alike. The
  smoke test was told the same.
- A `runtime-service` step was handed no hook ABI variable at all, though the
  adapter passes an environment and its Docker test checks one arrives.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

No RFC row covers either. The author delegated both rulings on 2026-10-19
("decide whatever is best for a mature package").

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-18T22:18:15Z
attempt: 1
claim: a migrate step and the smoke test were told OPERATION_TYPE=apply with no PREVIOUS_VERSION whatever operation ran them, which the hook ABI reference does not say
evidence: internal/lifecycle/ops/apply.go:695
action: decided
proposal: LOCKED — RFC 0018 decision 17: the step is told the operation running it, and PREVIOUS_VERSION on an update or rollback; a first install is apply with none
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-18T22:18:15Z
attempt: 1
claim: a runtime-service migrate step received no hook ABI variable; which of them mean anything inside a container was undecided
evidence: internal/ports/hooks.go:149
action: decided
proposal: LOCKED — RFC 0018 decision 18: a migration service receives the variables that mean the same inside a container; host paths, RESULT_FD, parameters and the runtime's variables are left out
```

## Why these answers

- **#100, tell the truth rather than nothing.** The ABI reference already
  promises `OPERATION_TYPE` and `PREVIOUS_VERSION`. Telling nothing on
  purpose would have meant changing the reference to withdraw them, for the
  sake of an idempotence rule that holds either way: every step runs on every
  `apply`, and the reference still says to write steps that way.
- **A first install stays `apply`.** `init` runs no hook, and the first
  migration runs under the `apply` that follows it. Calling that run `init`
  would name an operation the journal does not record. The reference now says
  `init` is reserved. A step that must tell a first install from a re-apply
  checks the database it migrates.
- **#99, the subset.** What describes the installation and the operation means
  the same in a container. Host paths do not, unless the vendor's Compose file
  mounts them, and it can then set the path itself. `DRY_RUN` is kept because
  the reference promises it is always present; a service never runs during a
  plan, so it is always `0`.
- **Not built: `schema_version` from a service.** The request floated a result
  file on a tmpfs the manager mounts. `docker compose run --rm` removes the
  container with its tmpfs, so the file would need a host directory mounted
  into a vendor's container. `database_schema_produces` already answers the
  question for an unattended update, so this waits for a bundle that needs it.

## As built

- `Options.PreviousVersion` carries the replaced version, set by `update`
  (`from.Version`) and `rollback` (`current.Version`), the same way
  `SourceRef` travels.
- `stepMigrate` builds the environment once, from `st.OpType`, and hands it
  to each step. A hook gets all of it, and a service gets
  `ports.ServiceEnvVars` of it.
- `stepSmokeTest` uses `st.OpType` and the previous version.

`TestAStepIsToldWhatIsRunningIt` and
`TestAMigrationServiceIsToldWhatIsRunningIt` were red before the change.
Rollback's previous version has no test: it needs a hook in the earlier
release, which the fixtures do not carry.
