# Manifest refuses unknown operations · an operation the manager never runs is refused

Issue #101, item M5 of the ERP bundle's requests after 0.4.0, on branch
`manifest-refuses-unknown-operations` from `f927b4c` (v0.4.0). It closes the
entry in [migrate-steps.md](migrate-steps.md) whose proposal was "OPEN — refuse
operation names the manager does not look up". That log is not edited; this one
carries the decision.

The names the manager runs were measured, not copied from the constants. The
lookups are `Manifest.Operation(domain.OpBackup)` and `(domain.OpRestore)` in
`internal/adapters/backup/hookbackup/backup.go:267` and `:935`,
`(domain.OpSmokeTest)` in `internal/lifecycle/ops/apply.go:962`, and
`OperationSteps(domain.OpMigrate)` in `apply.go:672`. `domain.OpPreflight` is
declared at `internal/domain/manifest.go:580` and nothing reads it:
`grep -rn OpPreflight --include='*.go' .` returns only that line.

RFC 0018 carries no grades, so its rows are treated as `LOCKED`. None of them
settles operation names. Decision 1, the two-pass decode, is what keeps this
change forward compatible: `checkManagerVersion` runs before the strict decode
and `Validate` (`internal/release/load.go:193`), so a manifest naming an
operation a newer manager added, and declaring that manager as its floor, is
refused as needing a newer manager rather than as a malformed operation.

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
at: 2026-10-17T20:49:58Z
attempt: 1
claim: Validate checks every step of every operation but never the operation's name, so an invented or misspelled name loads cleanly and never runs; it now refuses a name outside the four the manager runs, and the refusal lists them
evidence: internal/domain/manifest.go:1138
action: decided
proposal: LOCKED — RFC 0018 decision 15: operations accepts only the names the manager runs, migrate, smoke_test, backup and restore; any other name is refused with the list, and a name a later manager adds reaches an older one through min_manager_version.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-17T20:49:58Z
attempt: 1
claim: preflight was listed in the docs and issue #101 as an operation the manager looks up, and nothing ever ran it; it is refused like any other unknown name and its constant is removed, because accepting it would keep the promise the docs made and the code never kept
evidence: internal/domain/manifest.go:580
action: decided
proposal: LOCKED — part of RFC 0018 decision 15: preflight is not an operation.
```

The `preflight` *hook phase* (`ports.PhasePreflight`) is a separate name and is
left alone here. Like `pre-update` and `post-update` it is declared and never
run, which is the docs item of the same requests (M9), not this one.

## Review: a refusal echoes the bundle's keys raw

CodeRabbit found that the new refusal puts the operation's name, a map key the
bundle controls, into the error as written, so a key such as `"bad\e[2J"`
reached stderr as an escape sequence. The same is true of every map-keyed field
(`runtimes.<name>`, `images.<name>`, `backup.volumes.<name>`,
`extensions.<ns>`), and of every validator that shares `validationErrors`:
manifest, installation, secrets, parameters and export. The fix is in
`validationErrors.add`, once, rather than at the one call site the review named:
a problem holding a character `strconv.IsPrint` rejects is written escaped.
No RFC row settles error text, so this is an implementation detail, not a
divergence.
