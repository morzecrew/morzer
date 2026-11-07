# restore-resume · RFC 0051 P1, `morzer restore --resume`

On branch `restore-resume` from `main` at `0687e459`, building
[RFC 0051](../rfcs/0051-resuming-an-interrupted-restore.md) P1: decisions
2–11, 13 and 14, as ruled by the author on 2026-11-06. P0 (decision 15)
landed in #284. The hook stays refused; P2 is decisions 1 and 12.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/lifecycle/engine/step.go`, `internal/domain/operation.go`:
  `Step.Restartable` and `StepRecord.Restartable` (`restartable,omitempty`)
  (decision 5).
- `internal/lifecycle/engine/engine.go`: the journal records each step's
  `Restartable`; `resumePoint` accepts a started step when the code and the
  journal each call it idempotent or restartable (decision 5). The run loop
  is unchanged: a finished restartable step is not idempotent, so it keeps
  its credit (decision 15, #284).
- `internal/ports/backup.go`: `RestoreOptions.Part`, a `RestorePart` whose
  zero value restores everything as today, with `RestoreVolumes` and
  `RestoreHook` for the two steps (decision 7).
- `internal/adapters/backup/hookbackup/backup.go`: `Restore` by part. Each
  part verifies and stages only its own components and checks the backup's
  installation; the volumes part runs no hook, the hook part writes no
  volume (decision 7).
- `internal/lifecycle/ops/backup.go`:
  - `restoreSteps`: `verify-backup` and `check-compatibility` restartable,
    `stop-services` idempotent, `restore-volumes` restartable, then
    `restore-data` (the hook), both needing intervention on failure
    (decision 6).
  - `Restore`: the record's flags gain `components` and
    `allow_cross_installation` (decision 8). With `--resume`, `--backup`,
    `--component` and `--allow-cross-installation` are refused before
    anything else; `--force` and `--confirm` are required as for a fresh
    restore (decision 2); the record is found, re-read under the lock and
    handed to the engine as `stop` does (decision 4); the backup, components
    and cross-installation flag come from it (decision 8).
  - Under the lock, a resume is refused when an operation other than a
    `backup` was journalled after the restore, or the installed release is
    not the record's `To` (decision 9).
  - `restoreLeftBehind`: an interrupted restore names
    `morzer restore --resume --force --confirm <id>`, and `morzer apply` (or
    `start`, when stopped) as the road that abandons it; a restore stopped at
    a step the journal does not call safe to start again names a fresh
    restore instead, and says why (decision 10).
  - `stepPruneBackups`: keeps the backup the most recent restore names when
    that restore is `running`, `interrupted`, `failed` or waiting on
    intervention (decision 3).
  - Decision 13 needs no code: the engine gives a resumed record the
    interrupted run's id, which `emitAttestation` reads, and
    `notifyFinished` already takes the invocation's id. A test pins both.
  - Decision 14 needs no code: a fresh restore consults no gate.
- `internal/lifecycle/preflight/preflight.go`: the gate's remedy for a
  restore (decision 10, and the first entry below).
- `internal/cli/commands.go`: the restore help names `--resume`.
- `test/fakes/support.go`: the fake backup records each restore's part and
  honours the kept backup when it prunes.
- Tests: the engine cases of §6; a suite file for the suite cases of §6; the
  hookbackup parts; `TestAnInterruptedRestoreSaysWhatItLeftBehind` follows
  the new hint.
- `.github/scripts/acceptance.sh`: a restore killed while `restore-volumes`
  runs, then `--resume`, and the marker read back.
- Docs: `pages/docs/operating/backups.md` (the "deliberately not resumable"
  note becomes how to resume), `pages/docs/reference/commands.md`; the
  changelog.

Decision 5 reads "idempotent or restartable in both the code and the
journalled record" as each side saying one or the other, so a step the
journal calls idempotent and the code calls restartable may be started again.

Decisions the plan needs that the RFC does not settle: three, none
load-bearing, each below.

Decision 10 names `restore --resume` in the gate's remedy for every
unfinished restore. A crashed restore whose hook had started is refused by
the resume it names. The hint already says so for an interrupted one.

```divergence
decision: 10
grade: ASSUMED
class: spec-gap
at: 2026-11-06T09:26:00Z
attempt: 1
claim: the gate's remedy would name a resume that resumePoint refuses, for a restore that stopped inside a step not safe to start again
evidence: internal/lifecycle/engine/engine.go:630-636
action: departed
proposal: ASSUMED — refines decision 10. The gate names `morzer restore --resume --force --confirm <installation-id>` only when the journal calls the step the restore stopped at safe to start again, or it never started; otherwise it keeps today's remedy.
```

Decision 3 keeps a backup from `Prune`, a port method, which RFC 0051 does
not list among its contract changes. `RetentionPolicy` also governs the
targets' pruning, and a restore reads only local backups.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-06T09:27:00Z
attempt: 1
claim: decision 3 changes the backup port, and the RFC's contract changes name only Step, StepRecord and RestoreOptions
evidence: internal/ports/backup.go:35
action: decided
proposal: ASSUMED — `ports.RetentionPolicy` gains `KeepID`, a backup the local engine never prunes. The ops layer sets it from the unfinished restore; a target's pruning leaves it unset, since a restore reads only this machine's backups.
```

A Ctrl-C that lands between two steps marks the step about to start
`interrupted`, so a resume treats it as started. Between the volumes and the
hook, that refuses a resume whose hook never ran.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-06T09:28:00Z
attempt: 1
claim: an interruption between restore-volumes and restore-data journals restore-data interrupted though it never started, and resumePoint then refuses it as started
evidence: internal/lifecycle/engine/engine.go:177-182
action: decided
proposal: ASSUMED — kept as the engine does it for every operation: refusing is the safe side. The hint says the restore stopped at the hook rather than inside it. Telling the two apart is the engine's, for every operation, and not this RFC's.
```

## Build

Decision 11 resumes a restore that needed intervention once it is cleared,
and §6 tests "a failed restartable step resumes". A data step that fails
compensates the steps before it first: `stop-services` brings the product
back up and is journalled `compensated`, and `FirstIncompleteStep` refuses
any record holding a compensated step. A restore that crashed is not
compensated, so clearing it does make it resumable.

```divergence
decision: 11
grade: ASSUMED
class: spec-gap
at: 2026-11-06T10:45:00Z
attempt: 1
claim: a restore whose data step failed is journalled with stop-services compensated, and no cleared record holding a compensated step can be resumed
evidence: internal/domain/operation.go:213-216
action: departed
proposal: ASSUMED — refines decision 11. A restore that crashed resumes once cleared. One whose data step failed has brought the product back up and is restored again from the start; whether a failed data step should leave the product down so that its resume can continue is a question for the author, not settled here.
```

## Self-audit, 2026-11-06

The branch's diff against `main`, every file it touches.

- **Fixed:** decision 14 had no test. `TestAFreshRestoreRunsOverAnUnfinishedOne`
  pins it.
- **Not done as planned:** the plan says a test pins both halves of decision
  13. Only the attestation is pinned. The notification carries the
  invocation's id, which decision 13 accepts and names as a defect shared
  with `apply`; a test pinning it would fail the fix.
- **Open, out of scope:** a manager killed on its own (`kill -9`, the OOM
  killer) leaves the docker CLI it started running, and that CLI holds the
  staged volume's file open, so the helper container goes on extracting. A
  resume started inside that window writes the same volume beside it. A
  reboot ends both, and the acceptance step kills both as a reboot does.
- **Checked by sabotage:** undoing each change to `ops`, the gate and the
  backup adapter, one at a time, turns a test red.

## Review, 2026-11-06

The residue the self-audit left open is closed. A manager killed on its own
leaves its helper writing the volume, and a resume wrote the same volume beside
it. The volume step's restartability rests on nothing else writing the volume,
and the RFC does not say who makes that true.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-06T12:35:00Z
attempt: 2
claim: a resume of restore-volumes after a manager killed on its own writes the volume beside the helper the killed run left extracting into it
evidence: internal/adapters/runtime/compose/volumes.go:309-362
action: decided
proposal: ASSUMED — the compose runtime names the container that writes a volume after that volume, and removes one an earlier run left behind before writing; a removal that fails leaves the name taken, so the restore is refused rather than written beside it.
```

## Resolution, 2026-11-07

The author resolved the question attempt 1 left open on decision 11, as
recommended: a failed data step leaves the product down, and the restore
resumes once cleared, which is decision 11 as written. Built with the
reconciliation in #296.

```divergence
decision: 11
grade: ASSUMED
kind: resolved
at: 2026-11-07T20:16:28Z
attempt: 2
claim: attempt 1's departure is withdrawn: from the first step that writes the data on, a step passes the point of no return as it begins, so a failure leaves `stop-services` as it was instead of starting the product over a half-restored installation, and the cleared record resumes from the failed step, as decision 11 says
evidence: internal/lifecycle/ops/backup.go:993-1000
action: decided
```

The engine had marked the point only for a step that passed it and then
finished. A step that passes it and fails now counts the same
(`internal/lifecycle/engine/engine.go:356-361`), which restore's data steps
need and nothing else did: `stepMigrate` passes it only as it returns success.
