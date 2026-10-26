# RFC 0033 — The schema a migration service leaves

- **Status:** 📝 Draft — proposed 2026-10-26. Ruled 2026-10-26: the schema
  is the release's declaration (decision 1), and `release verify` warns when a
  release that migrates with a service declares none (decision 4). The
  carry-forward half of #143 is not designed here; it shipped as a fix beside
  this draft (decision 5 records it).
- **Scope:** What `ReleaseRecord.SchemaAtInstall` holds after a converge whose
  migrations ran as a `runtime-service` step, which today is always 0. Covers
  where the value comes from, which step's value wins when several run, and
  what is recorded when nothing can say. Deliberately not a new channel out of
  the container unless decision 1 rules for one, not a change to the hook ABI's
  `schema_version`, and not a change to how `CheckUpgrade` or `AssessRollback`
  read the value. The recommended answer needs no manifest, schema, hook ABI or
  port change; the two alternatives each add a contract.
- **Related:** [`internal/lifecycle/ops/apply.go`](../internal/lifecycle/ops/apply.go)
  (`stepMigrate`, `runMigrateStep`, `passIfFallbackCannotRead`,
  `stepRecordState`), [`internal/domain/version.go`](../internal/domain/version.go)
  (`Compatibility.DatabaseSchemaProduces`, `CheckUpgrade`, `AssessRollback`),
  [RFC 0001](0001-update-and-rollback.md) decision 14,
  [RFC 0018](0018-the-pre-1-0-manifest-surface.md) decision 18,
  [hooks reference](../pages/docs/reference/hooks.md).
- **Discussion:** [#143](https://github.com/morzecrew/morzer/issues/143).

---

## 1. Summary

When a migration service succeeds and no step reported a schema, record the
release's own `database_schema_produces` as the schema it left. That makes RFC
0018 decision 18's claim, that `database_schema_produces` covers a container
that cannot report, true for the record and not only for the in-flight check.
No new field and no new channel: the declaration exists, and the migrate step
already uses it as the stand-in for an unreported schema.

## 2. Motivation

A release that migrates with a one-shot container, `flyway` in a service being
the usual case, never has a known schema. `runMigrateStep` returns 0 for every
`runtime-service` step, `stepMigrate` sets `KeySchemaVersion` only for a value
above 0, and `stepRecordState` writes what it finds. So on such an installation:

- `rollback` prints `schema compatible: yes` with a warning that it was not
  checked, because `AssessRollback` skips the schema when `currentSchema` is 0.
- `update` warns that the schema range was not checked
  (`update.go`, "database schema version is unknown").
- `update --check` and the unattended gate skip the same check, without saying
  so (`check.go`, `channel.go`, both passing `current.SchemaAtInstall`).

The vendor in #143 had already declared `database_schema_produces` and
`rollback_safe`, which RFC 0018 decision 18 says is how a container reports.
The declaration is read and then dropped.

## 3. Current state

- **The hook path reports.** A hook writes `schema_version` on its result
  descriptor; `runMigrateStep` returns it and the last non-zero value of the
  steps wins (`stepMigrate`'s `reported`).
- **The service path cannot.** RFC 0018 decision 18 leaves `RESULT_FD` out of
  a container's variables, since no descriptor is open there, and notes that a
  mounted result file "is not built; `database_schema_produces` covers it".
- **The declaration is already the stand-in, in flight.**
  `passIfFallbackCannotRead` judges the point of no return against
  `rel.Manifest.Compatibility.DatabaseSchemaProduces` when `reported` is 0. The
  record step does not do the same, so the substitution lasts exactly one step.
- **A re-converge now keeps what it had.** Since the fix that accompanies this
  draft, `stepRecordState` carries the current release's `SchemaAtInstall`
  forward when the converge reports none and the release is the same one. An
  update records what it reported, 0 when nothing did (decision 5).
- `database_schema_produces` is optional, and absent means "not
  auto-applicable" (`version.go`). Nothing requires it of a release that
  migrates with a service.

## 4. Goals / Non-goals

**Goals**

- A release that migrates with a service and declares what it produces has a
  known schema after its first successful converge.
- A value a step actually reported is never overridden by a declaration.

**Non-goals**

- Reading the schema out of the database. The manager does not own it, and
  every product spells its schema table differently.
- Inferring a schema from `database_schema_min` or `_max`. The comment on
  `DatabaseSchemaProduces` already rejects a proxy for this.
- Hook migrations that report nothing. A hook has a channel and did not use
  it; substituting the declaration there hides a hook that broke its own
  contract.

## 5. Design

### 5.1 Record the declaration when a service migrated

In `stepMigrate`, after the loop:

```go
if reported == 0 && ranService && rel.Manifest.Compatibility.DatabaseSchemaProduces > 0 {
	// A container has no descriptor to report on, and RFC 0018
	// decision 18 names the declaration as how it reports.
	reported = rel.Manifest.Compatibility.DatabaseSchemaProduces
}
```

`ranService` is true when any `runtime-service` step exited 0 or 2. Exit 2
("nothing to do") counts: the schema is already what this release produces.
`passIfFallbackCannotRead` then needs no fallback of its own, and the record
step records the value it already reads.

### 5.2 What wins

1. A value any step reported, the last one as today.
2. Otherwise the declaration, if a service step ran and the release declares it.
3. Otherwise, on a re-converge of the current release, the recorded value.
4. Otherwise 0, unknown.

### Alternatives considered

- **A result file the manager mounts.** `compose run --volume
  <tmpdir>:/morzer-result`, with the hook ABI's JSON written there. Reports what
  the database holds, not what the vendor promised, and it is the honest
  analogue of `RESULT_FD`. Costs a port change (`RunOptions` gains a mount), a
  hook ABI addition, a bind mount from the host into a vendor container, and an
  image change for every vendor: `flyway` writes no JSON, so it needs a wrapper
  script anyway. Worth it only if a declaration proves wrong in practice.
- **A declared last line of output**, for example
  `morzer-result: {"schema_version": 15}`. No mount, and #141's captured output
  already carries it. Costs a parsing contract on free-form output, which a
  migration tool's own log line can collide with, and still needs a wrapper.
- **Leave it unknown.** What happens today. Every schema check on these
  installations is skipped, and three of the four say so only as a warning.

## 6. Tests

- A service migration on a release declaring `database_schema_produces: 14`
  records 14 after `apply`, and after an `update` to it.
- Exit 2 records the same.
- A hook step that reported 12 beside a service step records 12.
- No declaration records 0, and `rollback` still says the schema was not
  checked.
- A failed service step records nothing; the operation fails as it does now.

## 7. Docs

`reference/manifest.md` gains a sentence under `database_schema_produces`: for
a release that migrates with a service, it is also the schema recorded after
the migration, so it is how the rollback and unattended checks learn it.
`reference/hooks.md`, under "A migration service", says the same where a
vendor looks for how a container reports.

## 8. Out of scope

- **Making `database_schema_produces` required for service migrations.** A
  `release verify` warning is decision 4's question. Requiring it would refuse
  bundles that install today.
- **Correcting a wrong declaration.** The manager cannot see the database. If a
  vendor declares 14 and migrates to 15, the record says 14, and
  `AssessRollback` may call a rollback safe that is not. That is the
  alternative in §5's first bullet, and the reason to build it.

## 9. Risks

- **A declaration is a promise, not a measurement.** Today the checks skip and
  say so. Under this design they run against the promise. That is more
  checking than today, but against a weaker input than a hook report. Accepted
  if decision 1 rules for it; it is the trade decision 18 already made.
- **Read as "the manager knows the schema".** The docs sentence in §7 says
  where the number came from.

## 10. Unresolved questions

None. Decisions 1 and 4 were ruled on 2026-10-26.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | **The mechanism.** Record `database_schema_produces` when a `runtime-service` step succeeded and no step reported a schema (§5.1). No contract change, and it makes RFC 0018 decision 18's claim true for the record. A wrong declaration lands where today already is: the schema checks treat an unknown schema as compatible. The alternatives in §5 measure instead of trusting, at the cost of a port, an ABI addition and a vendor wrapper, and stay rejected unless a declaration proves wrong in practice. Was `OPEN`; ruled 2026-10-26. |
| 2 | `ASSUMED` | A schema a step reported always wins over the declaration, and among reports the last one wins, as today. The declaration fills a gap and never corrects a measurement. |
| 3 | `ASSUMED` | Exit 2 from a service counts as success for this purpose. "Nothing to do" means the database is already at what this release produces. |
| 4 | `ASSUMED` | `release verify` warns, and never refuses, when a manifest migrates with a `runtime-service` step and declares no `database_schema_produces`. Without the declaration decision 1 records nothing, and the vendor's CI is where the bundle can still change. A refusal would fail bundles that install today (§8). Was `OPEN`; ruled 2026-10-26. |
| 5 | `ASSUMED` | *(2026-10-26, shipped with #143's fix)* A converge that reports no schema keeps the recorded one only when it re-records the current release, which covers `apply`, boot and the converge inside `rollback` (RFC 0001 decision 14). An update records what it reported, 0 when nothing did: a new release's migrations may have moved the schema, and the old value would claim one `AssessRollback` reads as safe to return to. |

## 12. Phasing

One PR once decision 1 is ruled: §5.1, the tests in §6 and the docs in §7. If
the ruling is a mounted file or an output line, this RFC is rewritten around
it before any code, because both add a contract.
