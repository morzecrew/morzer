# RFC 0061 — Backup state a fleet can see

- **Status:** 📝 Draft — proposed 2026-11-23 from #418. §11's `OPEN` rows
  wait for the author.
- **Scope:** What an installation says about its backups, to an authority
  reading its fleet row and to an operator reading `doctor`. Covers a `backup`
  block in the fleet row (fleet schema 4): how many off-machine targets are
  configured, when the newest backup was taken, when the newest one that left
  the machine was taken, and whether an offline recovery recipient exists. It
  covers the small record that answers the third question without a network
  call, and a manifest key, `backup.off_machine`, by which a vendor whose data
  cannot be rebuilt makes `doctor` warn about a machine with no target.
  Contract changes: the fleet row's schema, and a manifest key with a manager
  version floor. No installation state schema change, and nothing that adds,
  removes or probes a target.
- **Related:** [`internal/domain/fleet.go`](../internal/domain/fleet.go)
  (`FleetRow`, `NewFleetRow`, `FleetSchemaVersion`);
  [`internal/lifecycle/ops/fleet_row.go`](../internal/lifecycle/ops/fleet_row.go),
  [`internal/lifecycle/ops/fleet_publish.go`](../internal/lifecycle/ops/fleet_publish.go);
  [`internal/lifecycle/ops/doctor.go`](../internal/lifecycle/ops/doctor.go)
  (`checkBackupTargets`, `checkLastBackup`, `checkRecoveryRecipient`);
  [`internal/lifecycle/ops/backup.go`](../internal/lifecycle/ops/backup.go);
  [`internal/domain/manifest.go`](../internal/domain/manifest.go) (`BackupSpec`);
  [0009](0009-backup-targets.md) (targets), [0026](0026-fleet-as-a-read-model.md)
  (the fleet row, the roster, absence and staleness),
  [0030](0030-unit-enablement-is-the-operators.md) (`backup.scheduled`),
  [0047](0047-exporting-status-and-doctor-to-prometheus.md) (the newest backup
  as a metric), [0057](0057-desired-state-an-installation-pulls-and-applies-to-itself.md)
  (an authority reading rows).
- **Discussion:** #418.
- **Origin:** a machine set up by hand, before it followed an authority, was
  lost on 2026-11-21 with no off-machine target, and its keys and device ids
  went with it (#418).

---

## 1. Summary

Each fleet row gains a `backup` block of counts, times and booleans, never a
target's URL:

```json
"backup": {"targets": 1, "last_at": "2026-11-23T03:00:12Z",
           "pushed_at": "2026-11-23T03:00:12Z", "recovery_recipient": true}
```

An authority can then tell a machine whose backups stopped, one whose backups
stay on the machine while its row still publishes, and one with no recovery
recipient. A vendor whose data cannot be rebuilt declares
`backup: {off_machine: expected}`, and `doctor` warns on a machine of that
product with no target, where today it reports OK on purpose.

## 2. Motivation

A machine whose data cannot be rebuilt can run for months with no backup off
the machine, and nothing outside it can tell (#418).

- **`doctor` reports it as OK, on purpose.** `backup.target-reachable` says
  "no off-machine target is configured" and passes, because many deployments
  keep backups on one machine by choice (`doctor.go:1955-1961`). That is right
  for them and wrong for a product whose data cannot be rebuilt, which is the
  machine #418 lost.
- **The fleet row says nothing about backups.** It carries health, drift, sync
  and the last operation (`fleet.go:73-160`). An authority watching a fleet
  cannot flag a machine whose backups stopped a week ago, or whose backups
  have been failing to leave it, or which has no recovery recipient.

The authority that now watches the affected fleet adds a target and a recovery
recipient when it joins a machine. It still cannot see whether either is
working, or catch a machine where someone removed them.

## 3. Current state

Verified against `97c6ceac`.

- **A machine with no target publishes no row.** `FleetPublish` writes to the
  installation's own backup targets (`fleet_publish.go:138`, `targets.go:498`).
  With none configured and no `--target`, it refuses: "this installation
  configures no backup targets" (`targets.go:539-542`). An authority with a
  roster already sees such a machine as absent, and one whose targets were all
  removed as stale (RFC 0026 P3). So a row a reader sees reports
  `targets` ≥ 1, unless an operator published it with `--target`. What the
  roster cannot see is everything else in this RFC.
- **The row** is schema 3 (`fleet.go:38`). 3 added `staged` and
  `sync.pending` (RFC 0058). A reader refuses a schema newer than its own
  (RFC 0026 decision 4, `fleet.go:468`), so the authority is upgraded first.
  The row carries counts, never contents: no parameter values, hostnames,
  logs or configuration (`fleet.go:20-24`). It is readable by every machine
  sharing the bucket.
- **The newest backup** is what `Backup.List` returns first. `backup.freshness`
  reads it (`doctor.go:2093-2140`), and so does
  `morzer_last_backup_timestamp_seconds` (`metrics.go:136`). `StampBackup`
  (`paths.go:190`), which #418 names, is a different thing: the catch-up stamp
  for a scheduled run reaching the engine, written whether or not the backup
  succeeded.
- **Whether a backup left the machine is not recorded.** A push that fails
  fails the backup operation, removes the partial copies, keeps the local
  backup and says to run `morzer backup push <id>` (`backup.go:395-407`). That
  later push is journaled as `OpTypeBackup` too (`backup.go:252`), so the
  journal cannot tell a backup whose push failed from one whose push was
  retried and succeeded. Only a listing of each target answers it today, which
  is what `doctor`'s `backup.target-freshness` does: the newest backup must be
  on every configured target, each keyed on its canonical URL
  (`doctor.go:2001-2091`, the key at `doctor.go:2057`).
- **A recovery recipient** is what `secrets.recovery-recipient` counts:
  recipients of kind `recovery` in the secret store (`doctor.go:784-825`). It
  is a local read.
- **`backup.scheduled` off** (RFC 0030) declares that backups happen at the
  storage layer. `backup.freshness` then reports OK and does not warn
  (`doctor.go:2101-2116`).
- **The manifest's `backup:`** holds only `volumes` (`manifest.go:1298`). An
  older manager refuses a key it does not know, so a new key comes with a
  floor: a constant naming the release that reads it, and a refusal unless the
  manifest declares `compatibility.min_manager_version` at least that
  (`ports.go:17-21`, `manifest.go:1861`).

## 4. Goals / Non-goals

**Goals**

- An authority reading a row can tell a machine whose backups stopped, whose
  backups are not leaving it, or which has no recovery recipient.
- An operator on a machine whose product's data cannot be rebuilt is told by
  `doctor` that it has no off-machine target.
- Nothing in the row that a machine sharing the bucket should not read.

**Non-goals**

- **A target's URL, name or listing in the row.** A URL can carry credentials,
  and the row is readable by every machine sharing the bucket.
- **Probing targets to build the row.** `doctor` already probes, and a row
  built from a slow target would be late for the case it exists to report.
- **Changing `doctor` for a product that declares nothing.** Keeping backups on
  one machine stays a choice `doctor` does not second-guess.

## 5. Design

### 5.1 The row's `backup` block

Schema 4 adds one block, built from local state at publish time:

```json
"backup": {
  "targets": 1,
  "last_at": "2026-11-23T03:00:12Z",
  "pushed_at": "2026-11-23T03:00:12Z",
  "recovery_recipient": true
}
```

- `targets`: how many backup targets the installation configures.
- `last_at`: when the newest backup was taken, from `Backup.List`. Absent when
  there is none.
- `pushed_at`: when the newest backup that reached every configured target was
  taken (§5.2). Absent when none has. A reader compares it with `last_at`: the
  same time means the newest backup left the machine, and an older one says
  for how long backups have stayed on it. #418 proposed a `pushed` boolean,
  which cannot say how long (decision 2).
- `recovery_recipient`: whether the secret store has a recipient of kind
  `recovery`, as `secrets.recovery-recipient` counts.
- `scheduled: false`, only when `backup.scheduled` is off, so a machine
  backed up at the storage layer reads as declared rather than as a machine
  whose backups stopped (decision 6).
- `problem`, when the backups or the recipients cannot be read, as
  `health.problem` does. A count that could not be taken is never published
  as zero (decision 7).

Counts, times and booleans only (decision 5).

### 5.2 Where `pushed_at` comes from

The newest backup that reached every target is recorded when its push
completes, by the backup operation's push step and by `backup push`, in a
derived file beside the channel marks:

```json
{"id": "20261123T030012Z", "taken_at": "2026-11-23T03:00:12Z",
 "targets": ["sha256:9f2c…", "sha256:41ab…"]}
```

`targets` names each target the copy reached, by a digest of its canonical URL,
the form `backup.target-freshness` keys targets on. A digest, so the file holds
no second copy of a URL that can carry credentials. `pushed_at` is the record's
`taken_at` only while at least one target is configured and the record names
every one of them. A count would not do: a target replaced by another leaves the
count as it was, and the new target has no copy. So a target added, or one
replaced, leaves the row without `pushed_at` until a push reaches it. A target
removed leaves the record standing for the ones that remain (decision 13).

The file is written only when every configured target took the copy. It replaces
the record only when the record does not name every configured target, or when
this backup was taken after the recorded one. A retry of an older backup after a
newer one reached every target leaves the newer record, so `pushed_at` never
moves back (decision 14).

It is derived: deleting it costs a row with no `pushed_at` until the next push,
which an authority reads as "nothing has left this machine". That is the loud
side to fail on. Recommended over a probe of each target at publish time, which
costs a listing per target on every publish, and over the journal, which cannot
tell a failed push from a retried one (§3) (decision 3).

### 5.3 `backup.off_machine: expected`

```yaml
backup:
  off_machine: expected
```

For such a product, `backup.target-reachable` warns when no target is
configured: the product's data cannot be rebuilt, and a lost machine loses it.
The hint names `morzer backup target add`. Every other product keeps today's
OK. An unreachable target still fails the check, as it does today. The key
takes `expected` only. An absent key is today's behaviour, and needs no second
value (decision 8).

A manifest that uses the key declares `compatibility.min_manager_version` of at
least the release that reads it, as `ports` does (decision 10).

The row may carry the expectation, as `"expected": true` in the block, so an
authority with several products can flag a machine without reading each
product's manifest (decision 9).

### 5.4 `fleet ls`

`fleet ls` renders the block in one column: the age of `pushed_at`, `behind`
when it is older than `last_at`, `none left` when there is no `pushed_at`,
`elsewhere` when `scheduled` is false, and `no recovery key` beside any of them
when `recovery_recipient` is false (decision 11).

## 6. Tests

- `NewFleetRow` carries each field from its input, omits `last_at` and
  `pushed_at` when absent, and never zero for a count it could not take.
- The push record is written only when every target took the copy, by both the
  backup operation and `backup push`, and not by a plan.
- A target replaced by another leaves the row without `pushed_at` until a push
  reaches the new one. A target removed does not.
- Pushing an older backup after a newer one reached every target leaves
  `pushed_at` at the newer one.
- A reader at schema 3 refuses a schema 4 row as it refuses any newer schema.
- `doctor` warns for a product declaring `off_machine: expected` with no
  target, and reports OK for one that does not.
- The manifest floor refuses the key without `compatibility.min_manager_version`.

## 7. Docs

- `reference/fleet.md`: schema 4 and the block, with what each field is for
  and how `pushed_at` and `last_at` are read together.
- `reference/manifest.md`: `backup.off_machine`.
- The doctor reference: the warning `backup.target-reachable` gives for such a
  product.
- `authoring/` on backups: when a vendor should declare it.

## 8. Out of scope

- **`init` refusing a product that expects off-machine backups without a
  target.** A first install often has its target added minutes later. Named,
  not built: it would make the expectation a requirement.
- **An authority adding or repairing targets.** A desired-state document cannot
  manage backup targets (RFC 0057 §5, "Settings are limited to a fixed set").
  Whatever the authority does on joining a machine stays its own.
- **Backup content or size in the row.** Counts and times answer the question
  #418 asks.

## 9. Risks

- **Schema 4 needs the authority upgraded first.** As with schema 3, a reader
  that is not upgraded reports each schema 4 row as unreadable. Accepted: it is
  the rule RFC 0026 decision 4 chose, and the docs say to upgrade the reader
  first.
- **The times tell a reader when backups run.** Accepted: every machine sharing
  the bucket can already see when backup objects appear.
- **A lost push record reads as nothing pushed.** Accepted: it fails loud, and
  the next successful push writes it again.
- **The record says what was delivered, not what a target still holds.** A
  copy deleted at the target, by hand or by a retention rule, still reads as
  pushed. Accepted: `backup.target-freshness` lists each target and fails on
  that machine, and a row built from a listing is what decision 3 rejected.

## 10. Unresolved questions

The `OPEN` rows in §11, for the author: whether to build now, the shape of
`pushed`, where it comes from, the manifest key, whether the row carries the
expectation, `fleet ls`, and the phasing.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `OPEN` | **Build now, or wait?** Recommended: build now. #418 records a machine lost with its keys, and both halves use state morzer already has. |
| 2 | `OPEN` | **`pushed` as a boolean (#418) or `pushed_at` as a time?** Recommended `pushed_at`: compared with `last_at` it says the same thing, and it also says since when, which a week of failing pushes needs. |
| 3 | `OPEN` | **Where `pushed_at` comes from: a record written when a push reaches every target, a probe of each target at publish, or the journal?** Recommended the record (§5.2): local, cheap, and loud when lost. A probe costs a listing per target on every publish, and the journal cannot tell a failed push from a retried one. |
| 4 | `ASSUMED` | The block raises the fleet schema from 3 to 4, and a schema 3 reader refuses the row (RFC 0026 decision 4), so the authority is upgraded first. |
| 5 | `LOCKED` | The block carries only counts, times and booleans: never a target's URL, name or listing. A URL can carry credentials, and the row is readable by every machine sharing the bucket. Reversing it leaks a credential to every neighbour, which nothing can take back. |
| 6 | `ASSUMED` | With `backup.scheduled` off (RFC 0030), the block says `scheduled: false`, so a machine backed up at the storage layer is not read as one whose backups stopped. |
| 7 | `ASSUMED` | A read that fails is `problem` in the block, and a count that could not be taken is never published as zero, for the reason `FleetHealth` gives (`fleet.go:230-249`). |
| 8 | `OPEN` | **The manifest key: `backup.off_machine: expected`, a boolean such as `backup.require_off_machine: true`, or none?** Recommended `off_machine: expected`, with `backup.target-reachable` warning rather than failing when no target is configured. A failure would fire on every first install before its target is added. |
| 9 | `OPEN` | **Does the row carry the product's expectation as `expected: true`?** Recommended yes: an authority watching several products can flag a machine without reading each manifest, and it is one boolean. |
| 10 | `ASSUMED` | A manifest using `backup.off_machine` declares `compatibility.min_manager_version` of at least the release that reads it, as RFC 0059 did for `ports` (`ports.go:17-21`). |
| 11 | `OPEN` | **`fleet ls` shows the block as one column (§5.4), or only `--json` carries it?** Recommended the column: `fleet ls` is where a human reads a fleet. |
| 12 | `OPEN` | **Phasing: the row (P1) and the manifest key (P2) as two PRs, or one?** Recommended two: each helps alone (#418), and P1 needs no vendor to release anything. |
| 13 | `ASSUMED` | The push record names each target the copy reached, by a digest of its canonical URL, and `pushed_at` is published only while the record names every target configured now. A count cannot tell a replaced target from the one it replaced, and the replacement has no copy. With no target configured, the row has no `pushed_at`. A digest rather than the URL, so the derived file holds no second copy of a URL that can carry credentials. |
| 14 | `ASSUMED` | A push replaces the record only when the record does not name every configured target, or when its backup was taken after the recorded one. A retry of an older backup after a newer one reached every target would otherwise move `pushed_at` back, and the row would read as behind. |

## 12. Phasing

- **P1 — the row.** The push record, the block, schema 4, `fleet ls`, and the
  fleet reference. Decisions 2–7, 9's field if ruled yes, 11, 13 and 14.
- **P2 — the manifest key.** `backup.off_machine`, the floor, `doctor`'s
  warning, and the manifest and authoring docs. Decisions 8–10.
