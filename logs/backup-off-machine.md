# Backup off machine · RFC 0061 P2

On branch `backup-off-machine` from `721d074d`, executing
[RFC 0061](../rfcs/0061-backup-state-a-fleet-can-see.md) P2: the manifest key
`backup.off_machine`, its manager floor, `doctor`'s warning, the row's
`expected`, and the manifest, doctor and authoring docs. Decisions 8–10, all
`ASSUMED`. The author ruled the plan's four open points on 2026-11-24 as the
plan leaned; the entries below record them.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

| File | Change |
|---|---|
| `internal/domain/manifest.go` | `BackupSpec.OffMachine`, taking `expected` only (D-8); `BackupOffMachineMinManagerVersion`, refused below it as `ports` is (D-10). |
| `internal/schema/schema.go`, `schemas/` | The key's enum and description; the schema regenerated. |
| `internal/lifecycle/ops/doctor.go` | `backup.target-reachable` warns for a product expecting off-machine backups with no target (D-8). |
| `internal/lifecycle/ops/fleet_row.go`, `internal/domain/fleet.go` | The release resolved once per row, and the block's `expected` set from it (D-9). |
| `pages/docs/reference/manifest.md`, `operating/backups.md`, `authoring/backing-up-volumes.md`, `reference/fleet.md` | The key, the warning, when to declare it, the row's field. |
| `CHANGELOG.md` | Unreleased `### Added`. |

The example bundle does not declare the key: no released manager reads it yet,
as with `preflight`.

## Entries

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-24T16:37:15Z
attempt: 1
claim: decision 10 names the floor as the release that reads the key without naming the release; the last tag is v0.19.1, and the author releases in minor steps, so the floor assumes the next release is 0.20.0, as `PreflightMinManagerVersion` assumed 0.12.0
evidence: internal/domain/manifest.go:1247-1251
action: decided
proposal: ASSUMED — `BackupOffMachineMinManagerVersion` is 0.20.0, the release that ships P2; it moves if that release is numbered otherwise
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-24T16:37:15Z
attempt: 1
claim: §5.3 does not say what the row's `expected` is when the installed release cannot be read; leaving it out alone would read as a product that expects nothing, the confident zero decision 7 forbids, so the block's `problem` says the release could not be read; with no release installed nothing is expected and nothing is said
evidence: rfcs/0061-backup-state-a-fleet-can-see.md:306
action: decided
proposal: ASSUMED — an installed release that cannot be read leaves `expected` out and names the release in the block's `problem`; no release installed leaves it out silently
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-24T16:37:15Z
attempt: 1
claim: §5.3 does not say what `backup.target-reachable` does with no target when the installed release cannot be read; the release check already fails in that case, and a second finding about the same missing manifest adds nothing an operator can act on
evidence: rfcs/0061-backup-state-a-fleet-can-see.md:221
action: decided
proposal: ASSUMED — `doctor` reads the key as absent when the installed release cannot be read, and the check keeps today's OK for no target
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-24T16:37:15Z
attempt: 1
claim: §5.4 lists what the `backup` column shows and says nothing of `expected`; the case it exists for, a product expecting off-machine backups on a machine with no target, already shows `none left`
evidence: rfcs/0061-backup-state-a-fleet-can-see.md:233-235
action: decided
proposal: ASSUMED — `fleet ls` does not render `expected`; `--json` carries it
```

## Review, 2026-11-24

Found in review and fixed in 9f16ea36: `doctor` registered
`backup.target-reachable` only when the manager had a target registry, so a
build without one gave a product declaring `backup.off_machine: expected` no
warning. The manager builds and wires the registry on every command
(`internal/cli/root.go:1086-1109`), so the product warned as decision 8 has it,
and the finding is no entry. The check is now registered without a registry
when no target is configured, since it then probes nothing.
