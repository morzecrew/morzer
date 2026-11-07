# restore-restartable · RFC 0051 P2, a restartable restore hook

On branch `restore-restartable` from `main` at `a34a2c35`, building
[RFC 0051](../rfcs/0051-resuming-an-interrupted-restore.md) P2: decisions 1
and 12, as ruled by the author on 2026-11-06. P1 landed in #285.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/domain/manifest.go`:
  - `OperationSpec.Restartable` (`yaml:"restartable"`,
    `json:"restartable,omitempty"`) (decision 1).
  - `RestartableRestoreMinManagerVersion = "0.13.0"` (decision 12).
  - `Validate` refuses `restartable` on any operation but `restore`, and
    refuses a release that declares it below that floor (decision 12).
- `internal/domain`: tests for the field on `restore`, refused on the other
  three operations, and refused below the floor.
- `internal/schema/schema.go`: the step's `restartable` is described as
  `restore` only, with its floor, the way `operationsSchema` already
  describes `migrate`'s list. Then `schemas/selfhost-v1alpha1-manifest.json`
  is regenerated (`just schemas`).
- `internal/lifecycle/ops/backup.go`: `stepRunRestore` takes the release and
  sets `Restartable` from `operations.restore` (decision 1). The engine
  journals it, `resumePoint` reads it from both sides (decision 5), and the
  gate and the hint already go by `StartsAgain` (P1), so neither changes.
- `test/suite/restore_resume_test.go`: under a release that declares it, a
  restore interrupted in its hook resumes and runs the hook again; the
  journal calls `restore-data` restartable; the gate names the resume. The
  P1 refusal test stays as the case for a release that does not declare it.
- Docs:
  - `pages/docs/reference/manifest.md`: the `restartable` row of the
    operations table, with the floor.
  - `pages/docs/authoring/hooks.md`: under "The backup and restore hooks",
    when to declare it, and that the demo bundle's hook does not.
  - `pages/docs/operating/backups.md`: the "At the release's restore hook"
    bullet, which says the resume is refused, gains the exception.
  - The changelog.
- The example bundle declares nothing: its hook extracts over what is there
  (§5.4).

Decisions the plan needs that the RFC does not settle: one, not
load-bearing, below.

`restartable` is a boolean, so `restartable: false` on another operation
reads the same as nothing. Telling them apart needs a pointer for a value
that changes nothing.

```divergence
decision: 12
grade: ASSUMED
class: spec-gap
at: 2026-11-06T14:01:00Z
attempt: 1
claim: decision 12 refuses `restartable` on any other operation, but a boolean cannot tell `restartable: false` from the field's absence
evidence: internal/domain/manifest.go:570-575
action: departed
proposal: ASSUMED — refines decision 12. `release verify` refuses `restartable: true` on any operation but `restore`; `false` is the default and is accepted everywhere, as it changes nothing. The floor applies only to `true`.
```

## Build, 2026-11-07

0.13.0 was released before this was built, with #286, #288, #289 and #292
taken first. Decision 12 named 0.13.0 as the floor on the assumption that it
would be the next release, and it no longer is.

```divergence
decision: 12
grade: ASSUMED
class: discovery
at: 2026-11-07T19:02:25Z
attempt: 1
claim: decision 12 sets the floor at 0.13.0 on the assumption that 0.13.0 is the next release, and 0.13.0 shipped without `restartable`, so a release declaring it at 0.13.0 would reach a manager that refuses the field
evidence: `git ls-remote --tags origin v0.13.0` prints `9c2bbce5d27730f8b9db4d1e3159ad6aa87b767b	refs/tags/v0.13.0`
action: departed
proposal: ASSUMED — refines decision 12. `RestartableRestoreMinManagerVersion` is 0.14.0, the next release.
```

The rest of the plan was built as written: the field on `OperationSpec`, its
refusals in `Validate`, the schema text, and `stepRunRestore` reading the
release (`internal/lifecycle/ops/backup.go`). The engine, the gate and the
hint needed no change, as the plan said: they go by what the journal records.

## Self-audit, 2026-11-07

Scope: the branch against `9c2bbce5`, three commits, code, tests, the
regenerated schema, docs and the changelog.

- **Sabotage:** four mutations, each run against the test written for it,
  and each went red:
  - the step ignoring the declaration;
  - the step restartable whatever the release says, which P1's refusal test
    catches;
  - `restartable` accepted on another operation;
  - the floor left out.
- **Residue:**
  - The acceptance stack's bundle does not declare it, and its resume step cuts
    the restore off while it writes volumes, so real Docker does not run a
    resume through a restartable hook. The suite does.
  - Whether a hook really is restartable is the vendor's word, which the
    manager cannot check. The docs say what the promise means.
