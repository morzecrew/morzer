# Migrate steps · `operations.migrate` may list several steps

Item 2 of the ERP bundle's requests, on branch `migrate-runs-an-ordered-list`,
stacked on `a-failed-update-restores-the-previous-runtime` because both change
`stepMigrate`. The request offered three shapes: a list under `migrate`, a
`post_migrate` operation, or wiring `post-update` and adding `post-install`.
The author ruled for the list on 2026-10-15, on the argument that `operations`
is a map an older manager decodes without complaint, so a new key would be
accepted and silently never run.

Measured before building, because the requester's migration is a
`runtime-service` and that path had never run end to end: in a scratch Compose
project with `db` and a `migrate` service that `depends_on` it,
`docker compose run --rm --no-deps migrate` left `db` unstarted and
unresolvable (DB-UNRESOLVED, exit 3); without `--no-deps`, `db` was started,
waited for, and resolved; and with `db` running and its configuration changed,
`run` without `--no-deps` recreated it before the one-shot ran.

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
at: 2026-10-15T06:15:56Z
attempt: 1
claim: nothing let a release run a second step after its migrations and before services start; migrate may now list steps, run in order in the one migrate step, each a migration as far as failure goes
evidence: internal/domain/manifest.go:530
action: decided
proposal: ASSUMED — RFC 0018 decision 14, as ruled: a list under migrate and nowhere else.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T06:15:56Z
attempt: 1
claim: the list is only refused legibly by an older manager when the manifest declares the floor, so a list with a lower min_manager_version is refused at load rather than advised against
evidence: internal/domain/manifest.go:1158
action: decided
proposal: ASSUMED — part of decision 14: a list requires min_manager_version 0.4.0 or later.
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-15T06:15:56Z
attempt: 1
claim: RunOneShot passed --no-deps, so a migration service on a first install ran before anything started the database it depends on; measured above, and pinned against the daemon by TestComposeRunOneShotStartsWhatItDependsOn; dropping it recreates a changed dependency before the migration on an update
evidence: internal/adapters/runtime/compose/compose.go:434
action: decided
proposal: ASSUMED — a one-shot starts and waits for what it depends on; on an update, migrations run against the new release's dependency services.
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-15T06:15:56Z
attempt: 1
claim: a runtime-service migration exiting 2 was a failure needing intervention, though the adapter returns the exit as data precisely because 2 means "nothing to do" under the hook ABI
evidence: internal/lifecycle/ops/apply.go:687
action: decided
proposal: ASSUMED — exit 2 from a migration service is "already current", as from a hook.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T06:15:56Z
attempt: 1
claim: a runtime-service step is handed none of the hook ABI's variables; the adapter supports an environment and its docker test says the ABI's variables "must reach the one-shot container", but which of them mean anything inside a container -- host paths do not -- is a design question, left unbuilt
evidence: internal/lifecycle/ops/apply.go:676
action: decided
proposal: OPEN — which hook ABI variables a migration service receives.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T06:15:56Z
attempt: 1
claim: every migrate step is told OPERATION_TYPE=apply with no PREVIOUS_VERSION, on a first install, an update and a reboot alike, so a step cannot tell them apart; a bootstrap written to be safe to repeat does not need to, and nothing here changes it
evidence: internal/lifecycle/ops/apply.go:701
action: decided
proposal: OPEN — whether migrate steps learn which operation is running them.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T06:15:56Z
attempt: 1
claim: operation names are never checked, so an invented or misspelled one -- post_migrate, smoke-test -- loads and never runs; the list sidesteps this for the request but does not close it
evidence: internal/domain/manifest.go:1138
action: decided
proposal: OPEN — refuse operation names the manager does not look up.
```

Sabotage, each mutant compiled and restored from a copy, with the change
committed first: eleven on running only the first step, exit 2, the reported
schema, both list refusals, the version floor, the single-step JSON shape, the
indexed field names, the loader's executable check, `--no-deps` against the
daemon, and the schema's two spellings. Eleven killed.

**Drift count: 0.**
