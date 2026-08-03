# RFC 0001 — Update and rollback

- **Status:** ✅ Complete — shipped 2026-08-03. P1: `CheckUpgrade` and `AssessRollback` are now covered (100% of both) and the `bundle-1.3.0` fixture exists. Testing them found and fixed two defects, recorded in §10 as decisions 9 and 10. P2: `ops.Update`, the `update` command, and fault injection at six points of the pipeline. P3: `ops.Rollback`, the `rollback` command and its refusal paths. P4: `--to` on both, which was **not** gated on RFC 0004 as §11 claimed — see decision 15.
- **Scope:** Adds two operations to `internal/lifecycle/ops`: `update`, which
  fetches a release, verifies it, gates it on the manifest's `compatibility`
  block, takes a pre-update backup and swaps the release pointer; and
  `rollback`, which returns to the previous release after reporting separately
  whether containers, schema and data each permit it. Adds the `update` and
  `rollback` cobra commands. No new ports, no changes to the manifest schema,
  the state schema, the event schema or the exit-code table — every mechanism
  this needs is already in the step engine. Explicitly **not** in scope:
  automatic destructive database rollback, and the release sources beyond a
  local directory (RFC [0004](0004-distribution-and-verification.md)).
- **Related:** [`internal/domain/version.go`](../internal/domain/version.go),
  [`internal/lifecycle/engine/engine.go`](../internal/lifecycle/engine/engine.go),
  [`internal/lifecycle/ops/apply.go`](../internal/lifecycle/ops/apply.go),
  [`internal/lifecycle/ops/backup.go`](../internal/lifecycle/ops/backup.go),
  [`internal/ports/source.go`](../internal/ports/source.go),
  RFC [0004](0004-distribution-and-verification.md) for where bundles come from

---

## 1. Summary

`morzer update <ref>` installs a new release: resolve and fetch the bundle,
verify its digest, validate the manifest, check compatibility against what is
installed, take a backup, stage the release, run the existing `apply` pipeline
against it, and record the pointer swap. On failure the engine compensates and
the release pointer returns to where it was.

`morzer rollback` returns to the previous release. It answers three questions
separately before doing anything, and refuses when the answers do not permit a
safe return.

## 2. Motivation

The manager can install a release and converge to it, but it cannot move
between releases. That makes it a deployment tool for exactly one deployment:
an operator with a running product and a newer bundle has no supported path
forward except editing `/opt/<product>/current` by hand and re-running `apply`,
which skips every compatibility check and leaves no journal record of a version
transition.

The gap is visible in the code. `domain.CheckUpgrade` and
`domain.AssessRollback` exist in
[`internal/domain/version.go:164`](../internal/domain/version.go) and
[`:211`](../internal/domain/version.go) with full compatibility semantics —
`upgrade_from` constraints, schema ranges, `min_manager_version`, and a
three-part rollback assessment. A grep for callers across `internal/` returns
nothing. They were written alongside the domain model and never wired, because
the operations that would call them do not exist.

**Verified after P1–P3 shipped:** a second rollback returns to where the first
started. `SetCurrentRelease` promotes the displaced release to previous, so
1.3.0 → 1.2.0 leaves previous at 1.3.0 and the next rollback goes forward again.
Reaching a release two steps back is impossible without naming it, which is what
P4's `--to` is for — not a convenience.

## 3. Current state

Verified against the tree at the time of writing.

**What exists and is used.**

- The step engine ([`engine.go`](../internal/lifecycle/engine/engine.go))
  supports everything `update` needs and nothing here extends it: journaling
  before and after each step, compensation newest-first including the failing
  step, `--dry-run` planning, `--resume` from the first incomplete step, and
  `RequiresInterventionOnFailure` for steps that cannot be undone.
- `ops.Backup` ([`backup.go`](../internal/lifecycle/ops/backup.go)) already
  takes a `Reason` that lands in the backup manifest, and its retention policy
  already exempts the reason `"pre-update"` from pruning — written for this RFC's
  benefit and currently unreachable, since nothing passes that reason.
- `applySteps` ([`apply.go`](../internal/lifecycle/ops/apply.go)) is a free
  function taking `(d, inst, rel, opts)`. `update` reuses it verbatim against
  the new release rather than duplicating the eleven-step pipeline.
- `StateStore.SetCurrentRelease` promotes the displaced release to previous, and
  deliberately does not shift the pointer when the same version is re-applied —
  asserted by the StateStore contract suite.
- `ops.Options` already carries `SkipBackup`, `Resume` and `Force`. `SkipBackup`
  has no reader today.

**What does not exist.**

- No `update.go` or `rollback.go` in `internal/lifecycle/ops/`.
- No `newUpdateCommand` / `newRollbackCommand` in `internal/cli/`.
- `CheckUpgrade` and `AssessRollback` have **no callers and no tests**. They are
  the one substantial piece of untested domain logic in the repository.
- `domain.OpTypeUpdate` and `OpTypeRollback` are declared but never constructed.

**The surprising fact worth not re-deriving:** the engine's compensation model
already gives `update` its safety property. Because `SetCurrentRelease` is
called in a step that has a `Compensate`, reverting the pointer is a
compensation function, not a bespoke rollback path. `update` does not need a
recovery mechanism of its own.

## 4. Goals / Non-goals

**Goals**

- Move between releases with the compatibility gates the manifest declares.
- A failed update leaves the previously running release current and serving.
- Report rollback feasibility as three independent answers, not one boolean.
- Wire and test `CheckUpgrade` / `AssessRollback`.
- Record version transitions in the journal with `from` and `to` populated.

**Non-goals**

- **Automatic destructive database rollback.** Not built, not planned. When the
  schema has moved past what the previous release reads, the answer is a
  restore, which is an operator decision with a typed confirmation — not
  something an update failure triggers on its own.
- **Multi-step upgrade planning.** Choosing an intermediate version to hop
  through is the operator's call; the manager refuses an unsatisfiable
  `upgrade_from` and says what the constraint was.
- **Fetching from anywhere but a directory.** RFC 0004's job.
- **Changing `apply`.** If `update` needs a step `apply` does not have, it wraps
  the pipeline rather than editing it.

## 5. Design

### 5.1 `update` step sequence

```go
func updateSteps(d *Deps, inst domain.Installation, from domain.ReleaseRecord,
    target domain.Release, opts UpdateOptions) []engine.Step {

    steps := []engine.Step{
        stepResolveAndVerifyBundle(d, opts),  // fetch + digest + manifest validate
        stepCheckCompatibility(d, inst, from, target),
        stepPreUpdateBackup(d, inst, opts),   // skipped when opts.SkipBackup
        stepStageRelease(d, target),          // copy into /opt/<p>/releases/<v>
    }
    // The convergence pipeline is apply's, reused verbatim against the new
    // release. Duplicating it would mean two step lists to keep in agreement.
    steps = append(steps, applySteps(d, inst, target, opts.Options)...)
    return steps
}
```

`stepStageRelease` carries the compensation that makes the whole operation
safe:

```go
Compensate: func(ctx context.Context, st *engine.State) error {
    // Restore the pointer and the symlink to the release that was current
    // when the operation began. The new release directory is left in place:
    // it is immutable, addressed by digest, and deleting it would destroy
    // evidence an operator may want. `release prune` reclaims it later.
    if err := d.State.SetCurrentRelease(ctx, previous); err != nil {
        return err
    }
    return atomicfs.ReplaceSymlink(previous.Root, d.Paths.CurrentLink())
},
```

Compensation reverts the *pointer*, not the containers. The `apply` steps that
follow have their own compensators, and `start-services` deliberately has none:
on a failed update the previous release's containers are still what Compose
converges back to when the pointer moves and `apply` is re-run.

### 5.2 Compatibility gating

`stepCheckCompatibility` is a pure call into the domain, with `OnFailure: Abort`
— nothing has mutated yet, so there is nothing to compensate:

```go
report := domain.CheckUpgrade(
    from.Version,                  // zero on a first install
    target.Version(),
    target.Manifest.Compatibility,
    d.ManagerVersion,
    from.SchemaAtInstall,          // 0 when unknown; skips the schema range check
)
if err := report.Err(); err != nil {
    return err                     // exit 9, listing every problem at once
}
for _, w := range report.Warnings {
    st.Warn("%s", w)               // downgrade, same-version, schema below min
}
```

`report.Err()` already joins every problem into one message. A `--force` does
**not** bypass this: a release stating it cannot be installed over what is
running is stating a fact about its migrations, not expressing a preference.

### 5.3 Pre-update backup

```go
Check: func(ctx, st) (bool, error) {
    // Skipping requires --force as well as --skip-backup, and the choice is
    // journaled: an incident review must be able to see it was made.
    return opts.SkipBackup, nil
},
Execute: func(ctx, st) error {
    ref, err := d.Backup.Create(ctx, ports.Scope{
        Components: ports.AllComponents,
        Reason:     "pre-update",     // exempt from retention pruning
    }, map[string]string{"from": from.Version.String(), "to": target.Version().String()})
    st.Set(engine.KeyBackupRef, ref)
    return err
}
```

The backup is **not** deleted on compensation. A backup taken immediately before
a failed update is the most valuable artifact in the system at that moment.

### 5.4 `rollback` and the three questions

Rollback is not "update in reverse". It reports before it acts:

```go
assessment := domain.AssessRollback(
    current.Manifest.Compatibility,
    previous.Manifest.Compatibility,
    currentSchema,
)
```

`RollbackAssessment` carries `ContainersReversible`, `SchemaCompatible`,
`RestoreRequired` and a `Reason` — three answers because they fail
independently and an operator needs to know which one blocked them.

Behaviour by outcome:

| Assessment | Behaviour |
| --- | --- |
| All clear | Swap the pointer, run `apply` against the previous release. |
| `rollback_safe: false` on the installed release | **Refuse.** Exit 9. The message names the restore path and the backup to use. |
| Schema past what the previous release reads | **Refuse.** Exit 9, naming the schema numbers on both sides. |
| Refused, and `--force` given | Still refuse. `--force` authorises destructive *actions*, not incorrect ones. |

Refusing rather than proceeding-with-a-warning is the whole point: a rollback
that leaves an old binary reading a new schema corrupts data quietly, and the
operator's real option — restore from the pre-update backup — is one command
away and named in the error's hint.

### 5.5 CLI surface

```text
morzer update <ref>            [--skip-backup --force] [--dry-run] [--resume]
morzer rollback                [--to <version>] [--dry-run]
```

`update` honours the existing global flags without addition. `--dry-run` shows
the compatibility verdict and the full step plan without fetching into the
release store. `rollback --to` selects an older installed release rather than
the immediate previous one; it runs the same assessment against that target.

## 6. Tests

- **Domain unit tests for `CheckUpgrade` / `AssessRollback`** — the first tests
  these functions have had. Table-driven across: `upgrade_from` satisfied and
  violated, schema above `database_schema_max`, schema below
  `database_schema_min` (warning, not failure), `min_manager_version` newer than
  the running binary, downgrade and same-version warnings, and every branch of
  the rollback assessment.
- **Fake-adapter integration**, extending `test/suite`: a successful update
  1.2.0 → 1.3.0; an update whose health checks fail, asserting the pointer is
  back at 1.2.0 and status is `compensated`; an update refused on compatibility,
  asserting exit 9 and that `Runtime.Calls` is empty; a rollback refused for
  `rollback_safe: false`; a rollback refused for schema drift.
- **Fault injection**, extending the existing loop in
  [`engine_test.go`](../internal/lifecycle/engine/engine_test.go) to the update
  pipeline: fail at each step in turn, assert the pointer always ends where it
  started.
- **Second bundle fixture** — `testdata/bundle-1.3.0/`, differing from the
  existing one in version, `upgrade_from` and schema range, so compatibility is
  exercised against a real manifest rather than a hand-built struct.

## 7. Docs

- README: `update` and `rollback` rows in the command table; the rollback
  section states plainly that a database is never rolled back automatically.
- CHANGELOG: `Added` entries for both commands, and one for compatibility gating
  that names the refusal behaviour rather than implying best-effort.
- The `rollback` command's long help carries the three-question model, since
  that is where an operator meets it.

## 8. Out of scope

- **`update --to` for a version already in the release store.** The store is
  populated by `release fetch`, which exists; wiring a store-local reference into
  `update` is a small follow-up once RFC 0004 settles reference parsing.
- **Post-update hooks.** The manifest ABI reserves `post-update` and the hook
  runner supports the phase; no operation invokes it. Wiring it is one step and
  is deliberately left until a bundle needs it, rather than shipping an
  unexercised code path.
- **Automatic retry.** An update that failed and compensated is re-runnable by
  the operator. Retrying automatically would obscure the failure that a human
  should look at.

## 9. Risks

- **Compensation reverts the pointer but not the containers.** Between the
  failed step and the operator re-running `apply`, Compose may still be running
  the new release's containers against the old pointer. Mitigated by the failure
  message naming `morzer apply` as the next step, and by `doctor` reporting the
  service/release mismatch. Accepted: forcing a container revert inside
  compensation risks a second failure while the first is still being handled.
- **`AssessRollback` needs the current schema, which the manager does not own.**
  It comes from `ReleaseRecord.SchemaAtInstall`, populated by the migrate hook's
  structured result. A release whose hook does not report one yields `0`, which
  skips the schema check. This is a real gap and is stated in the operator-facing
  message ("schema version unknown; skipping the schema compatibility check")
  rather than silently treated as compatible.
- **The refusal could read as obstruction.** An operator who wants their old
  version back and is told "no" needs the alternative in the same breath. Every
  refusal message names the restore path and the specific backup.

## 10. Decisions

| # | Decision |
| --- | --- |
| 1 | `update` reuses `applySteps` verbatim rather than duplicating the convergence pipeline. Two step lists would drift, and the second one would be the one nobody tests. |
| 2 | Compensation reverts the release pointer and symlink only. The new release directory stays: it is immutable and digest-addressed, and deleting it destroys evidence. `release prune` reclaims it. |
| 3 | The pre-update backup is never deleted on compensation, and its `pre-update` reason exempts it from retention pruning. It is the most valuable artifact in the system at the moment an update fails. |
| 4 | `--force` does not bypass a compatibility refusal. It authorises destructive actions, not incorrect ones. A release declaring `rollback_safe: false` is stating a fact about its migrations. |
| 5 | Rollback refuses rather than warns when the schema has moved past the previous release. A warning that can be scrolled past is not a safety mechanism when the failure mode is quiet data corruption. |
| 6 | Rollback reports three independent answers — containers, schema, restore-required. Collapsing them into one boolean would hide which of the three actually blocked the operator. |
| 7 | An unknown current schema (`0`) skips the schema check and says so in the message, rather than assuming compatibility. The manager does not own the database and must not pretend to know its state. |
| 8 | No new ports, event kinds, exit codes or state-schema fields. Everything here composes mechanisms the engine already has; needing a new one would mean the design is wrong. |
| 9 | *(P1, 2026-08-03)* Each schema bound is guarded by its own presence. The minimum warning had been nested inside the maximum's guard, so a release declaring only `database_schema_min` never warned about a schema below it. |
| 10 | *(P1, 2026-08-03)* `AssessRollback` evaluates both blockers rather than returning early on the first. The early return left `SchemaCompatible` reporting `true` having never been checked — the opposite of the three-independent-answers property the struct exists for. This amends, but does not reverse, decision 6. |
| 11 | *(P2, 2026-08-03)* A version already staged with a different digest is refused in `resolveUpdateTarget`, before the engine runs, rather than by the staging step's `Check` as §5.1 sketched. It is a precondition on the reference, so it reports as a validation failure (exit 2) instead of an operation that rolled back (exit 11), and journals nothing as compensated. |
| 12 | *(P2, 2026-08-03)* `--dry-run` plans the convergence steps against the bundle at its source rather than its release-store destination. Nothing is staged during a plan, so planning against the destination reported every template and hook as missing and told the operator their bundle was broken when it was merely not installed yet. |
| 13 | *(P3, 2026-08-03)* The rollback assessment runs before the engine rather than as a step. A refused rollback is one that never started, so journaling it as a failed operation would file a record of work that did not happen beside records of work that did — and checking before the engine is also what makes the assessment available to `--dry-run`, which executes no steps. |
| 14 | *(P3, 2026-08-03)* `ReleaseRecord.SchemaAtInstall` is carried forward across a rollback, not reset. The schema describes the database, and rolling containers back migrates nothing; the migrate hook overwrites it at the end of the pipeline with what the database actually reports. |
| 15 | *(P4, 2026-08-03)* P4 was **not** gated on RFC 0004, contrary to §11 as written. `--to` resolves a version against the release store, which needs neither the source registry nor a new transport. The gating claim was made before update and rollback existed. |
| 16 | *(P4, 2026-08-03)* `rollback --to` refuses a target newer than, or equal to, what is installed. Forward is `update --to`, which gates on `upgrade_from` and takes a backup; sideways is `apply`. Accepting either here would silently skip those. |
| 17 | *(P4, 2026-08-03)* `--to` selects where to go, never whether it is safe. A named target gets the same three-question assessment as the default one, so `--to` cannot be used to route around a refusal. |

## 11. Phasing

- **P1** — ✅ *Shipped 2026-08-03.* Domain tests for `CheckUpgrade` /
  `AssessRollback`, and the `bundle-1.3.0` fixture. Billed as pure additions;
  it was not — the tests found two defects in the previously-uncalled functions
  and fixing them changed behaviour. See decisions 9 and 10.
- **P2** — ✅ *Shipped 2026-08-03.* `ops.Update`, the `update` command, and the
  fault-injection extension. Two divergences from §5, both recorded as
  decisions 11 and 12.
- **P3** — ✅ *Shipped 2026-08-03.* `ops.Rollback`, the `rollback` command and
  its refusal paths. One divergence from §5.4, recorded as decision 13.
- **P4** — ✅ *Shipped 2026-08-03.* `--to` on both. The claim that this was
  gated on RFC 0004 was wrong: resolving a version to a release-store path needs
  no source registry and no new transports. See decision 15.

P1 is worth landing on its own regardless of when P2 follows: it puts the
repository's only untested domain logic under test.
