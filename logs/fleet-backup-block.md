# Fleet backup block · RFC 0061 P1

On branch `fleet-backup-block` from `e77ff3bb`, executing
[RFC 0061](../rfcs/0061-backup-state-a-fleet-can-see.md) P1: the push record,
the row's `backup` block, schema 4, the `fleet ls` column and the fleet
reference. Decisions 2–7, 9's field, 11, 13 and 14. Decision 5 is `LOCKED`; the
rest are `ASSUMED`. Decisions 8 and 10, the manifest key and its floor, are P2.

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
| `internal/domain/fleet.go` | Schema 4; the `FleetBackup` block, built by `NewFleetRow` and bounded on the way in and on read (D-2, D-4–D-7, D-9). |
| `internal/domain/backup_pushes.go` (new) | The push record: keys from a canonical URL's digest, an entry set only for a newer backup, `pushed_at` as the oldest entry among the configured targets (D-13, D-14). |
| `internal/domain/paths.go` | `BackupPushesFile()`, beside the channel marks. Derived, so the export does not carry it. |
| `internal/lifecycle/ops/backup_pushes.go` (new) | Read and write the record. |
| `internal/lifecycle/ops/backup.go` | `Push` and `stepPushBackup` note every target that took a whole copy, also when a later target fails (D-14). |
| `internal/lifecycle/ops/fleet_row.go` | The block from local state (D-3, D-6, D-7). |
| `internal/lifecycle/ops/fleet_list.go`, `internal/ui/views/fleet.go` | The `backup` column (D-11). |
| `pages/docs/reference/fleet.md` | Schema 4, the block, the column. |
| `CHANGELOG.md` | Unreleased `### Added`. |

Decision 9's field is read as: `expected` is defined in schema 4 and never set
until P2 adds the manifest key, so P2 needs no second schema bump.

## Entries

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-24T09:56:02Z
attempt: 1
claim: §5.2 says when a target's entry is set but not what happens when writing the record fails after the copy landed; failing the push would report a backup that is on every target as not copied, and the derived record's loss already fails loud, as a row without `pushed_at`
evidence: rfcs/0061-backup-state-a-fleet-can-see.md:204-207
action: decided
proposal: ASSUMED — a failed write of the push record is logged as a warning and the push succeeds
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-24T09:56:02Z
attempt: 1
claim: §5.2 does not say what a push or a publish does with a record that cannot be read; a push starts a fresh record from its own entries, which can only leave other targets without an entry, the side that drops `pushed_at`; a publish reports `problem` and no `pushed_at`, as decision 7 asks of any read that fails
evidence: rfcs/0061-backup-state-a-fleet-can-see.md:306
action: decided
proposal: ASSUMED — an unreadable push record is replaced by the next push and reported as the block's `problem` at publish
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-24T09:56:02Z
attempt: 1
claim: decision 7 rules for counts; `recovery_recipient` is a boolean, and `false` for a store that could not be read would say no recovery key exists when nobody looked
evidence: internal/lifecycle/ops/doctor.go:791-794
action: decided
proposal: ASSUMED — `recovery_recipient` is absent, with `problem`, when the secret store cannot be read
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-24T09:56:02Z
attempt: 1
claim: §5.4 names the column's words for a block that was read, not for a row without one (published by a schema 3 manager) or a block carrying `problem`
evidence: rfcs/0061-backup-state-a-fleet-can-see.md:231-236
action: decided
proposal: ASSUMED — the column shows `—` for a row without the block, as the other columns do for a row that was not read, and `unknown` when the block carries `problem`
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-24T09:56:02Z
attempt: 1
claim: decision 14 says an entry never moves to an older backup but not what a backup taken at the same instant does; keeping the entry makes pushing the same backup again change nothing
evidence: rfcs/0061-backup-state-a-fleet-can-see.md:313
action: decided
proposal: ASSUMED — an entry moves only to a backup taken strictly after the one it names
```

```divergence
decision: RFC 0026 D-8
grade: LOCKED
class: spec-gap
at: 2026-11-24T09:59:52Z
attempt: 1
claim: RFC 0026 decision 8 (LOCKED) is that the row holds no new state and is derived from what status and ls compute; RFC 0061's push record is new state written by every push, and 0061 does not name the conflict, where RFC 0057 named it before adding the sync block and the facts
evidence: rfcs/0026-fleet-as-a-read-model.md:206
action: halted
proposal: ASSUMED — RFC 0061's push record departs from RFC 0026 decision 8 as RFC 0057's sync block and facts did (rfcs/0057-desired-state-an-installation-pulls-and-applies-to-itself.md:226-228), and 0061 gains a row naming it
```

```divergence
decision: RFC 0026 D-8
grade: LOCKED
kind: resolved
at: 2026-11-24T10:07:21Z
attempt: 2
claim: the author ruled 2026-11-24 to depart from RFC 0026 decision 8 as RFC 0057 did, so the push record is built as RFC 0061 decision 3 has it; the proposal above stands for a row in 0061
evidence: internal/domain/backup_pushes.go:9-16
action: departed
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-11-24T10:27:51Z
attempt: 2
claim: correcting the column entry of attempt 1: `unknown` only when the block has no `pushed_at` and carries `problem`; a block whose recipients could not be read but whose push record could still shows the age, and every `problem` is printed below the table as `backup: …`, beside `health:` and `drift:`
evidence: internal/ui/views/fleet.go:309
action: decided
proposal: ASSUMED — the column shows `—` for a row without the block, `unknown` when `pushed_at` is absent and the block carries `problem`, and the problem's text below the table
```

```divergence
decision: D-5
grade: LOCKED
kind: resolved
at: 2026-11-24T10:27:51Z
attempt: 2
claim: touched the governed area; the block carries counts, times and booleans only, the push record names targets by a digest of the canonical URL, and nothing of a target's URL reaches the row
evidence: internal/domain/fleet.go:263-295
action: decided
```

## Review, 2026-11-24

```divergence
decision: 7
grade: ASSUMED
class: drift
at: 2026-11-24T14:42:13Z
attempt: 3
claim: §5.1 has the row carry `last_at`, and decision 7 a read that fails as `problem`. `fleet publish` and `sync now` never attached a backup engine, so every row a machine published carried neither, and the suite, which builds the row with an engine in hand, did not see it. Found in review; fixed in b3ca5b43.
evidence: internal/lifecycle/ops/fleet_row.go:92
action: departed
proposal: ASSUMED — unchanged; both commands attach the backup engine as `status` does, and a row built without one carries `problem`
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-24T14:42:13Z
attempt: 3
claim: §5.2 sets an entry when a target takes a whole copy and does not say what a failed push does to one. A failed push removes what it wrote by backup id, which can be the copy an earlier push of that backup left, so the failing target's entry goes when it names that backup. Found in review; fixed in 22cdc8a1.
evidence: internal/domain/backup_pushes.go:53
action: decided
proposal: UNLISTED — a push that fails at a target removes that target's entry when it names the backup being pushed; entries naming other backups, and other targets' entries, stay
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-24T14:42:13Z
attempt: 3
claim: §5.2 makes the record derived and leaves it out of the export, and does not say what an import does with the one already on the machine. It names the replaced installation's backups, so an import removes it, as it replaces the channel marks. Found in review; fixed in 612d0c6e.
evidence: internal/lifecycle/ops/recovery.go:726
action: decided
proposal: UNLISTED — `installation import` removes the push record, and the imported row has no `pushed_at` until its first push
```

Also found in review and fixed: the acceptance scenario's row-schema check
moved to 4 (528ddd25), and it now asserts `last_at` (b3ca5b43); the
column's rows said a target holds what the record says it took (7d4df947).

**Drift count: 1.**
