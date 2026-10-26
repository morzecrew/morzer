# RFC 0033 · the schema a migration service leaves

On branch `rfc-0033-schema-from-declaration` from `bcf91b6`, building
[RFC 0033](../rfcs/0033-the-schema-a-migration-service-leaves.md) as ruled on
2026-10-26: a `runtime-service` migrate step that succeeds, when no step
reported a schema, records the release's `database_schema_produces`
(decisions 1–3, §5.1–5.2), and `release verify` warns when a release that
migrates with a service declares none (decision 4). The tests of §6 and the
docs of §7. Closes #143.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Both entries below were raised in the plan, before any code.

`passIfFallbackCannotRead` keeps its own fallback to the declaration. §5.1 says
it "then needs no fallback of its own", which is true of a release that
migrates with a service and false of one whose hooks report nothing: §4 keeps
the declaration out of a hook's record, and removing the fallback would change
that release's point of no return, which nothing here asked for.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-26T19:52:32Z
attempt: 1
claim: §5.1 says passIfFallbackCannotRead needs no fallback once the declaration is recorded, but a hook-only release that reports nothing still reaches it with 0
evidence: internal/lifecycle/ops/apply.go:881-894
action: decided
proposal: ASSUMED — `passIfFallbackCannotRead` keeps its fallback to `database_schema_produces`. It judges the point of no return for every migration, and only a service's success puts the declaration in the record; a hook that reports nothing is judged against the declaration in flight and records 0, as §4's non-goal requires.
```

Only `release verify` warns. Deprecations warn in `verify`, `init` and
`update`, because an operator is told what will stop being read; this one is
for the vendor, and decision 4 names the one command a vendor's CI runs.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-26T19:52:32Z
attempt: 1
claim: decision 4 names release verify, and the deprecation warnings it sits beside are also printed by init and update; the RFC does not say whether this one follows them
evidence: rfcs/0033-the-schema-a-migration-service-leaves.md:182
action: decided
proposal: ASSUMED — the warning is printed by `release verify` alone. It asks the vendor to change the bundle, and an operator running `init` or `update` cannot.
```

**Followed, and worth naming:** #153's
`TestAnUpdateWhoseMigrationsReportNothingRecordsNoSchema` updated to a source
that declares `database_schema_produces`, and under §5.2 the declaration now
comes before the empty record it asserted. The test now removes the
declaration, so it still pins decision 5, which is what it was written for.
Nothing in the RFC moved.

The second entry's evidence, `rfcs/0033-the-schema-a-migration-service-leaves.md:182`,
was decision 4's row when it was written. Marking the RFC complete lengthened
its status line, and the row is now at line 185.
