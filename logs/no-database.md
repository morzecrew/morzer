# no-database · a release that keeps no database (#289)

On branch `no-database` from `main` at `a45f78e7`, building
[#289](https://github.com/morzecrew/morzer/issues/289) against
[RFC 0016](../rfcs/0016-update-checking-and-unattended-updates.md). Decision 16
there makes `database_schema_produces` the unattended gate's schema half, with
absent meaning not auto-applicable. A product with no database can only pass
it by declaring a schema it does not have. The author ruled the field's shape
on 2026-11-07: `compatibility.database: none`.

RFC 0016's decision table is ungraded, so decision 16 is treated as `LOCKED`.
This does not contradict it: absent still means not auto-applicable, and the
new field is a declaration the vendor makes. It is recorded as RFC 0016
decision 33, ruled by the author, as RFC 0042 decisions 15–19 were in #287.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/domain/version.go`:
  - `Compatibility.Database` (`yaml:"database"`, `json:"database,omitempty"`),
    with `DatabaseNone = "none"` its only value;
  - `AssessUnattended` counts the schema half as passed for a target that
    declares `database: none`. The signature, `rollback_safe` and the
    pre-update backup are asked as before.
- `internal/domain/manifest.go`, `Validate`:
  - `database` is absent or `none`;
  - `none` is refused beside any `database_schema_*` field or an
    `operations.migrate` step;
  - `NoDatabaseMinManagerVersion = "0.13.0"`, the floor a release declaring
    it needs, as every manifest field has one.
- `internal/schema/schema.go`: the field's description, then
  `schemas/selfhost-v1alpha1-manifest.json` regenerated.
- `internal/lifecycle/ops/release_diff.go`: `release diff` shows the field
  among the compatibility changes.
- Tests: validation (accepted, refused beside each schema field and a migrate
  step, refused below the floor, an unknown value); the assessment (passes the
  schema half, still refuses `rollback_safe: false`); a suite test in which an
  unattended tick installs a release declaring it.
- Docs:
  - `pages/docs/reference/manifest.md`: the compatibility table's row, and a
    paragraph for a product with no database beside "is what opts a release
    into unattended updates";
  - `pages/docs/operating/unattended-updates.md`: the gate table's schema row
    and the sentence at "Declaring `database_schema_produces`".
- `rfcs/0016`: decision 33, ruled by the author in #289.
- The changelog.
- The example bundle keeps its database and declares nothing new.

Decisions the plan needs that #289 and the ruling do not settle: one, not
load-bearing, below.

`database: none` passes only the gate's schema half. A release that also
declares `rollback_safe: false` is still refused, by the half that asks it.
Nothing new is needed for that, and the issue says as much. What the issue
does not settle is the update preview, which reports whether the schema was
checked.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-07T09:31:18Z
attempt: 1
claim: the update preview reports `schema_checked`, false for a release with no schema numbers, which for `database: none` would read as a check that was skipped
evidence: internal/lifecycle/ops/update_preview.go:65-67
action: decided
proposal: ASSUMED — the preview and the unattended assessment say a release declaring `database: none` keeps no database, rather than that its schema was not checked.
```

## Build, 2026-11-07

The author's ruling is RFC 0016 decision 33, appended in this branch. The
entry above was built as proposed: `UpdateRollback.NoDatabase`
(`internal/lifecycle/ops/update_preview.go:72`) and its line in the plan
(`internal/cli/commands.go:599`).

One thing the plan did not foresee. `AssessUnattended` asks `rollback_safe`
only inside the schema half, through `AssessRollback`. A release declaring
`database: none` still goes through `AssessRollback` with no schema, so
`rollback_safe: false` is refused. The test table has the case. This follows
the plan and is not a departure, so no entry.

## Self-audit, 2026-11-07

Scope: the branch against `a45f78e7`, three commits, 16 files, 313 lines added
and 3 removed: code, tests, the regenerated schema, docs, the RFC row and the
changelog.

- **Sabotage:** seven mutations, each run against the test written for it,
  and each went red:
  - the gate ignoring `none`;
  - `none` skipping `rollback_safe`;
  - each of the three `release verify` refusals: the migrate step, the
    floor, an unknown value;
  - the preview's flag;
  - `release diff` leaving the field out.
- **Residue:**
  - A release that adds a database after declaring `none` moves to
    `database_schema_*` like any first declaration. Its first update is then
    judged against a previous release with no `database_schema_max`, so the
    schema comparison is skipped, as it is today for any release that
    declares one for the first time.
  - The example bundle keeps its database, so the demos do not install a
    release declaring `none`. The suite and the CLI tests do.
