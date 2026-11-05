# RFC 0049 — Pruning the attestations a machine keeps

- **Status:** 📝 Draft — proposed 2026-11-05, and ruled by the author
  2026-11-05: §11's `OPEN` rows as recommended. Ready to build.
- **Scope:** A retention setting for the signed statements under
  `<VarDir>/attestations/`, which nothing prunes today. It covers what the
  limit counts, when the prune runs, which statements it never removes, and
  what `attest verify` and `attest log` read after a prune. Not covered:
  where statements are pushed (RFC 0025 decision 5), a target list of their
  own, and fetching a target's copies to verify them. No change to the
  statement format or the predicate type. No installation schema bump.
- **Related:** [0025](0025-attesting-an-installation.md) (the statements;
  §10's volume risk and the status line's "no retention on the attestation
  directory"); [0042](0042-settings-an-operator-changes-after-init.md)
  (`retention.releases` and `retention.backups` as settings);
  [`internal/lifecycle/ops/attest.go`](../internal/lifecycle/ops/attest.go)
  (`emitAttestation`, `fileAttestation`);
  [`internal/lifecycle/ops/attest_push.go`](../internal/lifecycle/ops/attest_push.go)
  (`attestationPushState`, `localStatements`);
  [`internal/domain/attest_verify.go`](../internal/domain/attest_verify.go)
  (`VerifyChain`);
  [`internal/lifecycle/ops/settings.go`](../internal/lifecycle/ops/settings.go)
  (the retention settings);
  [`pages/docs/reference/attestations.md`](../pages/docs/reference/attestations.md)
  ("What this does not do yet").

---

## 1. Summary

An operator sets `retention.attestations`, and after each statement is filed
and pushed, the oldest statements past the limit are removed from this
machine with their signatures. A statement that has not reached every backup
target is never removed, nor is the newest successful one, which
`attest verify --against-live` compares the deployment with. Copies on the
targets are not touched. Unset, nothing is pruned, which is today's
behaviour.

The author ruled §11's three questions as recommended: the limit is a count
(decision 1), there is no default (decision 2), and the targets' copies are
not pruned (decision 3).

## 2. Motivation

Every lifecycle operation files one statement (RFC 0025 §4.1), and nothing
ever removes one. The reference page says so under "What this does not do
yet" (`attestations.md:337-341`): "a machine applying at every boot
accumulates one per boot." RFC 0025 §10 named it as a risk: "Retention
policy mirrors backups', and the local directory needs a prune the way
releases do."

The volume is small but has no bound. Measured 2026-11-05, an unsigned
statement for the test bundle's `apply` is 2,700 bytes; a signature adds a
`.minisig` beside it. A product with more services and images writes a few
times that. A machine that applies at boot and takes a `config set` a day
writes about a thousand statements a year: a few megabytes, and a thousand
files that `attest verify` and `attest log` read and parse on every run
(`attest_verify.go:91-191`).

Releases and backups have had retention from the start, and RFC 0042 made
both counts settings. Attestations are the one store under `<VarDir>` that
only grows.

## 3. Current state

- **Where statements live.** `fileAttestation` writes
  `<VarDir>/attestations/<operation id>.json`, mode 0644, and `sign` writes
  `<file>.minisig` beside it when this machine can sign
  (`attest.go:96-110`, `attest.go:190`). Operation ids are `op_` plus a ULID
  (`ops.go:246-248`), so sorting the names orders the statements by when
  their operations started, to the millisecond. `localStatements` already
  lists them in that order (`attest_push.go:332-351`).
- **When they are written.** `emitAttestation` files the statement, then
  pushes it to every backup target (`attest.go:45-53`). Callers emit after
  the deployment lock is released (for example `apply.go:170`, after the
  `withLock` that starts at `apply.go:103`), so two operations' emissions
  can overlap.
- **What has left the machine.** `attestationPushState` lists each target's
  `attestations/` prefix and counts, per target, the local statements whose
  document or signature is missing there (`attest_push.go:259-326`). `doctor`
  reports the result as `backup.attestations-pushed` (`doctor.go:641-650`),
  and `morzer attest push` sends what is missing.
- **What reads them.** `attest verify` parses every statement in the
  directory, checks each signature, and runs `VerifyChain` over the
  version-moving ones (`attest_verify.go:91-191`). `VerifyChain` orders them
  by start time and carries forward the version the installation was left
  at (`domain/attest_verify.go:135-200`); the first statement it sees starts
  the chain, so removing the oldest statements creates no break, while
  removing one from the middle can. `--against-live` compares the deployment
  with the newest successful statement (`attest_verify.go:348`).
- **How the other stores are pruned.** Release retention resolves the
  installation's `policy.retain_releases`, then the manifest's
  `retention.releases`, then a default of 3 (`installation.go:1017-1027`);
  backups do the same with 7. Both are settings, at least 1
  (`settings.go:337-363`, `retentionCount` at `settings.go:386-393`). Backup
  retention also prunes the copies on the targets, in a step of its own that
  continues on failure (`backup.go:439`).

## 4. Goals / Non-goals

**Goals**

- An operator can bound the attestation directory.
- No statement is removed while it is the only copy of itself.
- `attest verify`, with and without `--against-live`, still works on what
  is left.

**Non-goals**

- Pruning by the vendor. Retention of an audit record is the operator's
  decision, or their auditor's, so the manifest gains no field (decision 6).
- Compaction, archiving or rotating statements into one file. A statement
  is a signed document; anything that rewrites it is not that document.
- Verifying a target's copies. That is RFC 0025's remaining gap and its own
  design.

## 5. Design

### 5.1 The setting

One setting in RFC 0042's registry, recorded as
`policy.retain_attestations`:

```console
$ morzer config set retention.attestations=500
$ morzer config unset retention.attestations
```

Its unit is decision 1's. The recommendation is a count, as releases and
backups have, read by the same `retentionCount` (at least 1). The
alternative is an age in days, `retention.attestations_days`, which is how
audit retention is usually stated: "keep a year". A count is what RFC 0025
§10 assumed and what the other two stores use. An age is what an auditor
asks for, and the right count depends on how often the machine operates.

Unset, decision 2 decides. The recommendation is that nothing is pruned, as
today. A default would start deleting audit records on the first operation
after an upgrade, on machines whose operators never asked for it, to save a
few megabytes a year.

An older manager that rewrites the state drops the field and keeps
everything, which is the safe direction, so the installation schema stays
at 16 (decision 7). `retain_pre_update_backups` was added the same way.

### 5.2 When the prune runs

After `emitAttestation` has filed and pushed a statement: the end of every
operation that attests. It is a step of the emission path, not of the
engine, and it follows RFC 0025 decision 6. Nothing it does fails the
operation; a failure is a warning, and the next operation tries again.

It runs after the lock is released, as the emission does, so two
operations' prunes can overlap. A statement already gone is not an error,
and neither prune can remove the statement the other just wrote, since the
newest statements are the ones kept.

No command of its own. An operator who sets the limit sees it applied at
the next operation; one who wants it now runs any operation that attests.
A `morzer attest prune` is §8's.

### 5.3 What is never removed

The oldest statements past the limit are candidates, in name order. A
candidate is kept when either holds:

- **It has not reached every backup target.** With targets configured, the
  prune reads `attestationPushState` once and keeps every statement any
  target is missing, document or signature. A target that cannot be listed
  keeps everything, since nothing can be known about it. This is the same
  answer `doctor` gives (`backup.attestations-pushed`), so a statement
  `doctor` says has not left the machine is never the one that goes. With no
  targets, the local copy is the only copy by the operator's own
  configuration, and the limit applies.
- **It is the newest successful statement.** `--against-live` compares the
  deployment with it (`attest_verify.go:348`). A run of failed operations
  longer than the limit would otherwise leave nothing to compare against.

Only a prefix is removed: the prune stops at the first candidate it must
keep, so it never removes a statement newer than one it kept. A hole in the
middle would be a chain break (`VerifyChain`), reported to an auditor as a
release installed by something that filed no statement. Stopping at the
first kept statement means one statement stuck on an unreachable target
holds back the prune behind it, which is the price of never reporting a
false break. Decision 4.

Each statement goes with its `.minisig`, the signature first, so an
interrupted prune leaves a statement without a signature rather than a
signature without a statement. A statement with no signature already reads
as unsigned to `verify`; an orphaned `.minisig` reads as nothing at all.

### 5.4 What `verify` and `log` read afterwards

Unchanged. The oldest statement left starts the chain, as the first
statement does on an installation that began attesting after an upgrade.
The targets still hold the whole history, and an auditor who wants it
fetches it and runs `attest verify <path>` (`attestations.md:341-343`).

No marker records that a prune happened. A file saying "statements before
X were removed" would be an unsigned claim on the machine the audit is
about, and anyone able to delete the oldest statements could write it.

### 5.5 Copies on the targets

Decision 3. The recommendation is that the prune never touches them. A
target is where a record survives this machine, and the case for keeping it
there is strongest exactly when the machine is the one being audited.
Object stores have lifecycle rules for an archive's age, and an operator
who wants the targets pruned sets one there.

The alternative is to prune them as backup retention prunes backups
(`backup.go:439`), with the same limit, after the local prune. It bounds
the targets without a lifecycle rule, at the cost of a machine deleting its
own off-site evidence.

### 5.6 Alternatives considered

- **Prune by the journal's operation records instead of the directory.**
  The journal has its own life; a statement whose operation record was
  compacted would be orphaned, or kept forever. The directory is the store
  being bounded.
- **Keep every statement that is part of the version chain, prune only
  `apply` and `config`.** These are the frequent ones, so this would bound
  most of the volume and keep the chain whole. It also makes the limit
  mean something no operator can predict, and an `apply` that failed is
  part of what an audit reads.
- **A hard cap with no exceptions.** Simpler, and it removes the only copy
  of a statement that could not be pushed, which is the loss RFC 0025 §1 is
  about.

## 6. Tests

- A limit below the number of statements removes the oldest, with their
  signatures, and keeps the newest up to the limit.
- A statement a target is missing is kept, and so is every statement newer
  than it; an unreachable target keeps everything.
- With no targets, the limit applies to every statement.
- The newest successful statement survives a run of failures longer than
  the limit, and `--against-live` still runs.
- `attest verify` reports no chain break after a prune.
- A failed prune warns and the operation succeeds.
- Unset, nothing is removed.
- Two overlapping prunes leave the same directory one would.

## 7. Docs

- `pages/docs/reference/attestations.md`: replace "Nothing prunes them"
  under "What this does not do yet" with a section on the setting, what is
  never removed, and that the targets keep everything.
- The settings list in `pages/docs/operating/changing-configuration.md`,
  beside `retention.backups`.
- The changelog.

## 8. Out of scope

- **`morzer attest prune`.** A command that prunes now, with `--dry-run`.
  Worth adding if operators set the limit and then ask why nothing changed
  until their next operation.
- **A vendor default in the manifest.** Decision 6.
- **Fetching a target's copies for `verify`.** RFC 0025's remaining gap.

## 9. Risks

- **An operator sets a limit to clean up and loses evidence an auditor
  wanted.** The targets keep everything (decision 3's recommendation), and
  a statement that has not reached them is never removed. With no targets
  configured, the local directory is the only copy, and the limit means
  what it says.
- **One unpushed statement holds back every prune.** Accepted: `doctor`
  already warns about it, and `attest push` clears it.
- **Settings an older manager drops.** A state rewritten by an older manager
  loses `retain_attestations`, and the newer one keeps everything until the
  operator sets it again. Accepted, as for `retain_pre_update_backups`.

## 10. Unresolved questions

None. Decisions 1–3 were ruled by the author 2026-11-05.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | **What the limit counts.** Recommended: statements, as `retention.attestations=N`, at least 1, as releases and backups count, and as RFC 0025 §10 assumed. Alternative: an age in days, `retention.attestations_days=N`, measured from each statement's operation start, which is how audit retention is usually stated and does not depend on how often the machine operates. Was `OPEN`; ruled by the author 2026-11-05: as recommended. |
| 2 | `ASSUMED` | **Whether there is a default.** Recommended: no; unset prunes nothing, as today, so no machine starts deleting audit records because its manager was upgraded. Alternative: a default (1,000 statements, or 365 days under decision 1's alternative) that every installation gets, so the directory is bounded without anyone asking. Was `OPEN`; ruled by the author 2026-11-05: as recommended. |
| 3 | `ASSUMED` | **Whether the targets' copies are pruned.** Recommended: no; a target is where a record survives this machine, and its own lifecycle rules can bound it. Alternative: prune them as backup retention prunes backups, with the same limit, after the local prune. Was `OPEN`; ruled by the author 2026-11-05: as recommended. |
| 4 | `ASSUMED` | Only the oldest statements are removed, in name order, and the prune stops at the first one it must keep: one that a backup target is missing (document or signature), every statement when a target cannot be listed, and the newest successful statement. A hole in the middle would read as a chain break. |
| 5 | `ASSUMED` | The prune runs after `emitAttestation` has filed and pushed a statement, outside the engine and the lock. A failure warns and never fails the operation (RFC 0025 decision 6), and a statement already gone is not an error. |
| 6 | `ASSUMED` | The setting is the installation's only. The manifest gains no `retention.attestations`: how long an audit record is kept is the operator's decision, not the vendor's. |
| 7 | `ASSUMED` | No installation schema bump. An older manager that rewrites the state drops `policy.retain_attestations` and keeps everything, the safe direction. |
| 8 | `ASSUMED` | A statement's `.minisig` is removed before the statement, so an interrupted prune leaves an unsigned statement and never an orphaned signature. No marker records a prune. |

## 12. Phasing

One phase, one PR: the setting, the prune in the emission path, tests and
docs.
