# RFC 0051 — Resuming an interrupted restore

- **Status:** ✅ Complete — proposed 2026-11-06, and ruled by the author
  2026-11-06: §11's `OPEN` rows as recommended. **Built 2026-11-06 and
  2026-11-07**: P0 in #284, P1 in #285 and P2 in #295. The entries in
  [logs/resume-keeps-credit.md](../logs/resume-keeps-credit.md),
  [logs/restore-resume.md](../logs/restore-resume.md) and
  [logs/restore-restartable.md](../logs/restore-restartable.md) are decisions
  16–22, accepted by the author 2026-11-07.
- **Scope:** `morzer restore --resume`: continuing a restore that was
  interrupted (Ctrl-C, a killed process, a reboot) or that needed
  intervention and was cleared, from its first incomplete step, without
  restoring again what already went back. It covers the step engine's new
  notion of a step that may be started again but keeps its credit once done,
  splitting the restore of volumes from the release's restore hook, what the
  journal records for a resume, the unfinished-operation gate's remedy, and
  whether a release can declare its restore hook safe to run again. It also
  fixes the engine's resume, for every operation, losing the credit of
  completed steps when a resume is itself interrupted. Not covered: resuming inside one volume or one hook run, a restore from a
  backup that is no longer on this machine, and what a backup taken during
  an unfinished restore captures. Contract changes: `Step` and `StepRecord`
  gain `Restartable`; `ports.RestoreOptions` gains the part of the backup a
  call restores; and, in P2, the manifest's `operations.restore` gains
  `restartable`.
- **Related:** [0001](0001-update-and-rollback.md) (`--resume` from the
  first incomplete step); [0010](0010-compose-volume-capture.md) (volumes
  restored before the hook); [0040](0040-keeping-an-installation-down.md)
  (decision 3, a restore while stopped; decision 16, resuming `stop` and
  `start`); [0018](0018-the-pre-1-0-manifest-surface.md) (a manager floor for
  a new manifest field); [0025](0025-attesting-an-installation.md) (a
  restore attests);
  [`internal/lifecycle/ops/backup.go`](../internal/lifecycle/ops/backup.go)
  (`Restore`, `restoreSteps`, `restoreLeftBehind`);
  [`internal/lifecycle/engine/engine.go`](../internal/lifecycle/engine/engine.go)
  (`resumePoint`);
  [`internal/adapters/backup/hookbackup/backup.go`](../internal/adapters/backup/hookbackup/backup.go)
  (`Restore`, `Prune`);
  [`internal/lifecycle/preflight/preflight.go`](../internal/lifecycle/preflight/preflight.go)
  (`NoUnfinishedOperation`);
  [`pages/docs/operating/backups.md`](../pages/docs/operating/backups.md)
  ("Restore is deliberately not resumable").

---

## 1. Summary

`morzer restore --resume` continues the most recent restore that did not
finish, from the backup that restore named, through the step engine's
existing resume. A restore interrupted after its
data went back re-applies the release and runs the smoke test, rather than
restoring hours of data again. One interrupted while it was writing volumes
writes them again from the start, since a volume restore replaces what is
there. One interrupted inside the release's restore hook is refused, as
today, unless the release declares the hook safe to run again (decision 1).

The engine gains a step that may be started again when it was interrupted
but keeps its credit once it finished (`Restartable`), which `Idempotent`
cannot say: an idempotent step runs again on every resume. The restore's
data step splits in two, volumes and then the hook, so each part answers the
question for itself. The author ruled §11's three questions as recommended
on 2026-11-06: the hook runs again only when its release declares it
restartable (decision 1), a resume is confirmed again (decision 2), and
retention keeps the backup a resume needs (decision 3).

## 2. Motivation

The Phase 3 plan lists "Resumable restore (M/L)", against the gap report's
"Restore cannot resume".

A restore is the longest operation the manager runs. Its data step has a
three-hour timeout (`backup.go:878`), and it writes every volume the backup
holds before the hook writes the database (`hookbackup/backup.go:957-963`),
so it is also the step most likely to be under way when a session drops or
a machine reboots. Today:

- **An interruption after the data went back costs the whole restore.** The
  docs offer two roads (`backups.md:412-418`): `morzer apply`, which starts
  the release with no smoke test and leaves the restore's record
  interrupted, or the restore again from the start, volumes and all.
- **A killed or crashed restore blocks every converge.** Its record is still
  `running`, which the unfinished-operation gate refuses (`state.go:621-633`),
  and the remedy is to investigate and `--clear-intervention`
  (`preflight.go:604-615`), then restore again from the start.
- **`--resume` is accepted and does nothing useful.** It is a global flag
  (`root.go:565`). `Restore` passes the engine no prior record
  (`backup.go:665`), so `morzer restore --resume` takes the deployment lock
  and is told there is nothing to resume (`engine.go:565-568`).

The reason restore was made not resumable is in its comment
(`backup.go:703-706`): "its middle step overwrites a database, no automatic
action can tell how far a hook got, and guessing a repair is the failure
mode the design forbids." That holds for the hook, and this RFC keeps it
there. It does not hold for the steps around the hook, nor for the volumes,
which the manager restores itself by wiping and extracting
(`compose/volumes.go:320-330`).

## 3. Current state

- **Steps.** `restoreSteps` (`backup.go:756-770`): `verify-backup` and
  `check-compatibility` (idempotent, abort), `stop-services` (idempotent,
  compensated by bringing the product back up), `restore-data` (not
  idempotent, needs intervention on failure, three hours), then
  `reapply-release` and `smoke-test`, which a restore of a stopped
  installation leaves out (RFC 0040 decision 3).
- **The data step.** `hookbackup.Engine.Restore` (`hookbackup/backup.go:888`)
  verifies the backup again, refuses another installation's backup without
  `--allow-cross-installation`, decrypts into a staging directory, restores
  each volume (wipe, then extract; `compose/volumes.go:309-341`), and then
  runs the release's restore hook, whose contract promises nothing about
  being run twice (`pages/docs/authoring/hooks.md`, "The backup and restore
  hooks").
- **Resume.** The engine continues from the first incomplete step
  (`engine.go:564`). A completed step that is not idempotent keeps its
  credit and is skipped; a completed idempotent step runs again, which is
  how in-memory state is rebuilt (`engine.go:152-162`). The step being
  resumed must not have started unless it is idempotent both in the code and
  in the journal that recorded it. `findResumable` (`apply.go:1454`) takes
  the most recent record of the type if it is `running`, `interrupted` or
  `failed`.
- **The gate.** `UnfinishedOperations` returns records still `running` and
  those needing intervention; an `interrupted` record is not among them, so
  a Ctrl-C'd restore blocks nothing. The gate's remedy names `--resume` for
  `apply`, `update`, `stop` and `start` alone (`preflight.go:601-615`).
  `Restore` does not consult the gate.
- **Clearing.** `--clear-intervention` journals the record again as `failed`
  (`status.go:375`), which `findResumable` and `resumePoint` both accept.
- **The record.** A restore journals `backup_id` and `force`
  (`backup.go:647`). Not the components it was limited to, nor
  `--allow-cross-installation`, nor `--identity`.
- **Retention.** `Prune` removes by count and reason (`hookbackup/backup.go:1086`);
  nothing keeps the backup an unfinished restore was reading, and `backup`,
  which prunes, does not consult the gate.

## 4. Goals / Non-goals

**Goals**

- An interrupted or cleared restore can be finished without restoring again
  what already went back.
- A resume never runs a step whose half-done effect nobody has declared safe
  to repeat: the hook stays refused unless its release says otherwise.
- The gate's remedy for an unfinished restore names the command that
  finishes it.

**Non-goals**

- Resuming inside a step: a volume half-extracted is extracted again from
  the start, and so is a hook declared restartable.
- Restoring from a backup that is no longer on this machine. A resume whose
  backup is gone is refused at the step that needs it, as a fresh restore
  would be.
- Making the hook contract stricter for every release. A release that does
  not declare its hook restartable keeps today's behaviour exactly.

## 5. Design

### 5.1 `morzer restore --resume`

`morzer restore --resume` finds the most recent restore record that is
`running`, `interrupted` or `failed` (`findResumable`), re-reads it under the deployment lock
(`refreshResumable`), and runs the engine with it as the prior record, as
`stop` does (`stop.go:62-116`). The engine's checks stand: the step list must
be the one journalled, and the step resumed from must be safe to start again.

The backup, the components and `--allow-cross-installation` come from the
record (decision 8). `--backup`, `--component` and
`--allow-cross-installation` beside `--resume` are refused, since they
would name a different restore. `--identity` is accepted and never
journalled: a key's path is for the operator to supply each time, and a
resume that needs the backup and lacks the key fails at the step that reads
it, as a fresh restore does.

A resume takes `--force` and `--confirm <installation-id>`, as a fresh
restore does (decision 2): it may run a destructive step again, and the
typed identifier costs a resume nothing it did not cost the restore.

A resume is refused, naming a fresh restore, when any operation but a
`backup` has been journalled since the restore began, or when the installed
release is not the one the restore's record names (decision 9). An `apply`
in between may have written to volumes the restore had already put back, and
a resume that then ran the hook again would leave the volumes and the
database from different moments. An `update` or a `rollback` would run
another release's hook over data checked for this one, and a `stop` or a
`start` changes the step list. So `morzer apply` abandons the restore, as the
docs already say. An interrupted restore does not block those operations
today, and this does not make it.

### 5.2 A restartable step

`engine.Step` and `domain.StepRecord` gain `Restartable`. A restartable step
that did not finish may be run again from its start; one that finished keeps
its credit and is skipped on a resume, as any step that is not idempotent
is. `Idempotent` stays what it is: safe to run again even when it finished,
and run again on every resume.

`resumePoint`'s rule for the step resumed from becomes: if it started, it must
be idempotent or restartable, both in the code and in the journalled record
(decision 5). A record written before this change carries neither for the
data steps. Its step list also differs (§5.3), so the engine refuses to
resume it, as it refuses any list that changed.

A resume keeps the credit of every step that finished, wherever it sits.
Today `resumePoint` carries the prior run's outcomes forward only for the
steps before the resume point (`engine.go:633`), and the run skips a
completed step only there (`engine.go:160`). A resume that is itself
interrupted while running an earlier idempotent step again loses the credit
of every step after it, so the next resume runs a completed step that is not
idempotent: an engine test with the journal
`[a idempotent interrupted, b succeeded, c pending]` runs `b` again. That is
true of every resumable operation now, not only of a restore: an `update`
resumed twice can run a completed migration again. The fix is the engine's:
carry every completed step's record forward, and skip a completed step that
is not idempotent wherever it sits (decision 15). It lands first, on its own
(§12).

### 5.3 The restore's steps

| Step | Today | Proposed | Why |
| --- | --- | --- | --- |
| `verify-backup` | idempotent | restartable | Done once, it need not be done again. A resume after the data went back must not fail because the backup has since been pruned. |
| `check-compatibility` | idempotent | restartable | As above. |
| `stop-services` | idempotent | idempotent | Runs on every resume, so nothing a half-done re-apply started is running under the restore. It also rebuilds the runtime configuration the later steps read. |
| `restore-volumes` | (inside `restore-data`) | restartable | The manager wipes each volume and extracts it again, so starting again replaces whatever was half-written. |
| `restore-data` | not idempotent | restartable only per decision 1 | The release's hook. Nothing tells how far it got. |
| `reapply-release` | idempotent | idempotent | Unchanged. |
| `smoke-test` | abort on failure | unchanged | Unchanged. |

Both data steps need intervention when they fail, as `restore-data` does
today: a volume half-written on a failure is in the same state as a database
half-written.

The port changes to match (decision 7). `ports.RestoreOptions` gains the
part of the backup a call restores, volumes or everything the hook reads,
and `hookbackup.Engine.Restore` restores that part alone. Each call verifies
and stages only the components it restores, and both check the backup's
installation. The first step has already verified the whole backup.

### 5.4 The hook (decision 1, P2)

As ruled, a release declares its restore hook safe to run over a restore it
did not finish:

```yaml
operations:
  restore:
    command: ["hooks/restore"]
    restartable: true
```

`restore-data` is then restartable for that release, and a resume
interrupted inside the hook runs it again from the start. A hook that drops
and re-creates its database, or restores with `pg_restore --clean`, can
say so; the demo bundle's, which extracts over what is there, says nothing,
and keeps today's refusal. `release verify` refuses `restartable` on any
other operation, and a release that declares it needs a manager floor of
0.13.0, as every manifest field does (decision 12).

### 5.5 The gate, the hint, the backup

- **The gate.** `NoUnfinishedOperation` names `morzer restore --resume --force
  --confirm <installation-id>`, as it names `apply --resume` (decision 10).
- **The hint.** `restoreLeftBehind`'s hint on an interrupted restore names
  the same command, and keeps `morzer apply` as the road that abandons the
  restore, saying that a resume is refused after it. A restore interrupted inside a hook that is not
  restartable says that a resume would be refused and why.
- **After intervention.** A restore that failed and needed intervention
  resumes once the operator has repaired the cause and cleared it, which
  journals it `failed`, as every resumable operation does. It resumes only
  from a step that may be started again (decision 11).
- **The backup.** Retention keeps the backup an unfinished restore names,
  including one waiting on intervention, so a backup taken in the meantime
  cannot prune it before the resume (decision 3).

### 5.6 Alternatives considered

- **No engine change; mark the volume step idempotent.** It would let an
  interruption inside the volumes resume. But the engine re-runs a completed
  idempotent step on every resume, so a resume interrupted in the hook would
  restore every volume again. Saving that is most of the point.
- **One step per volume.** Finer: a resume would start again at the volume
  it was writing. The step list would then depend on what the backup holds,
  and the gain is bounded by the largest volume. Later, if anyone asks.
- **Resuming without the split.** Then whether the volumes can be started
  again would depend on the vendor's declaration for the hook, which says
  nothing about them.
- **A fresh restore that skips what is already restored.** It would need to
  know what is "already restored", which is the guess the design forbids.

## 6. Tests

- Engine: a restartable step interrupted runs again; one completed is
  skipped; a journalled record without `Restartable` refuses the resume
  whatever the code now says; a resume interrupted while running an earlier
  idempotent step again keeps the credit of the completed steps after it.
- Suite, against the fakes:
  - interrupted in `reapply-release`: a resume skips both data steps and
    finishes with the smoke test;
  - interrupted in `restore-volumes`: a resume writes them again;
  - interrupted in the hook: a resume is refused without the declaration,
    and runs the hook again with it;
  - the record's backup, components and cross-installation flag are used;
    `--backup` beside `--resume` is refused;
  - an `apply`, an `update`, a `stop` or a `start` in between refuses the
    resume, and a `backup` does not;
  - the gate's remedy names `restore --resume`;
  - after `--clear-intervention`, a failed restartable step resumes;
  - decision 3's retention, as ruled, including a restore waiting on
    intervention.
- Acceptance, against real Docker: a restore killed while it writes a
  volume, then `--resume`, and the file read back.

## 7. Docs

- `pages/docs/operating/backups.md`: the "deliberately not resumable" note
  becomes how to resume, and what is refused.
- `pages/docs/reference/commands.md`: `restore --resume`.
- P2: `pages/docs/authoring/hooks.md` and the manifest reference, for
  `restartable`; the schema.
- The changelog.

## 8. Out of scope

- **Telling the hook it is a re-run.** A restartable hook copes with a half
  restore by definition; an environment variable saying so can follow if a
  vendor asks.
- **A backup taken while a restore is unfinished.** It captures a
  half-restored deployment. That is true today; decision 3 only keeps such a
  backup from pruning the one being restored.
- **Fetching a pruned backup back from a target for a resume.** `backup
  fetch` exists; a resume does not call it.

## 9. Risks

- **An `apply` abandons a restore it meant only to look past.** The operator
  restores again from the start. The interrupted restore's hint says so before
  they run it.
- **A vendor declares a hook restartable that is not.** The data it
  half-restored is restored over. That is the vendor's declaration, as the
  hook itself is.
- **The engine change reaches every operation.** Only restore marks a step
  restartable, and a record without the field behaves exactly as today.

## 10. Unresolved questions

None. The author ruled decisions 1–3 on 2026-11-06.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | **Whether a resume runs the release's restore hook again.** Recommended: only when the release declares `restartable: true` on `operations.restore`; otherwise a resume interrupted inside the hook is refused, as today. Alternative A: never; a resume only continues around the hook, and P2 does not exist. Alternative B: always; a restore hook replaces a database by nature, so running it again over a half restore is within its job. Was `OPEN`; ruled by the author 2026-11-06: as recommended. |
| 2 | `ASSUMED` | **Whether a resume is confirmed again.** Recommended: yes; `--resume` takes `--force` and `--confirm <installation-id>`, as a fresh restore does, since it may run a destructive step again. Alternative: the restore's own confirmation stands, journalled as `force: true`, and `--resume` alone continues it. Was `OPEN`; ruled by the author 2026-11-06: as recommended. |
| 3 | `ASSUMED` | **Whether retention keeps the backup an unfinished restore names.** Recommended: yes; `Prune` skips the backup named by the restore record `--resume` would continue (the most recent restore, when it is `running`, `interrupted` or `failed`), so at most one backup outlives the policy, until a later restore finishes. Alternative: no; a resume whose backup was pruned is refused at the step that reads it, and the operator restores afresh. Was `OPEN`; ruled by the author 2026-11-06: as recommended. *(2026-11-06, from the review: a restore waiting on intervention is kept too, since clearing it makes it resumable.)* |
| 4 | `ASSUMED` | `morzer restore --resume` continues the most recent restore record that is `running`, `interrupted` or `failed`, under the deployment lock and through the engine's resume, as `stop --resume` does. |
| 5 | `ASSUMED` | `engine.Step` and `domain.StepRecord` gain `Restartable`: a step that did not finish may be run again from its start, and one that finished keeps its credit. `resumePoint` accepts a started step when it is idempotent or restartable in both the code and the journalled record. |
| 6 | `ASSUMED` | The restore's steps as §5.3: `verify-backup` and `check-compatibility` restartable; `stop-services` idempotent and run on every resume; `restore-data` split into `restore-volumes` (restartable) then `restore-data` (the hook; decision 1); both need intervention on failure. |
| 7 | `ASSUMED` | `ports.RestoreOptions` gains the part a call restores, volumes or the rest. Each call verifies and stages only what it restores, and both check the backup's installation. |
| 8 | `ASSUMED` | The record carries what a resume reuses: `backup_id` as today, plus `components` and `allow_cross_installation`. `--backup`, `--component` and `--allow-cross-installation` beside `--resume` are refused. `--identity` is accepted and never journalled. |
| 9 | `ASSUMED` | A resume is refused, naming a fresh restore, when any operation but a `backup` has been journalled since the restore began, or when the installed release is not the one its record names, checked under the lock. `morzer apply` therefore abandons the restore. |
| 10 | `ASSUMED` | The gate's remedy for a restore names `morzer restore --resume --force --confirm <installation-id>`. `restoreLeftBehind`'s hint names it too, and keeps `morzer apply` as the road that abandons the restore (decision 9). |
| 11 | `ASSUMED` | A restore that needed intervention resumes once cleared, since `--clear-intervention` journals it `failed`, and only from a step that may be started again. |
| 12 | `ASSUMED` | `restartable` is a field of `operations.restore` alone; `release verify` refuses it elsewhere; a release declaring it needs `min_manager_version` 0.13.0 (`RestartableRestoreMinManagerVersion`), on the assumption that 0.13.0 is the next release. |
| 13 | `ASSUMED` | A resumed restore attests as a resumed `apply` does, under the interrupted run's id, so its statement replaces the interrupted run's. It notifies as a resumed `apply` does too, under the id of the invocation (`ops.go:534`), which is not the journal's. |
| 14 | `ASSUMED` | A fresh `restore` still runs over an unfinished one, as today; it starts a new record, and the old one is left for `--clear-intervention`. |
| 15 | `ASSUMED` | *(2026-11-06, from the review)* A resume keeps the credit of every completed step: `resumePoint` carries forward every completed step's record, not only those before the resume point, and the run skips a completed step that is not idempotent wherever it sits. A fix to every resumable operation, landed before P1. |
| 16 | `ASSUMED` | *(2026-11-06, from the build; accepted 2026-11-07)* **Refines decision 15.** A resume skips a completed step unless it is idempotent both in the code and in the journalled record, as `resumePoint` already asks of the step it resumes. A step the manager that started the operation recorded as unsafe to repeat is kept as done, whatever a newer manager says of it. See [logs/resume-keeps-credit.md](../logs/resume-keeps-credit.md) (decision 15, attempt 2, 07:58). |
| 17 | `ASSUMED` | *(2026-11-06, from the build; accepted 2026-11-07)* **Refines decision 10.** The gate names `morzer restore --resume --force --confirm <installation-id>` only when the journal calls the step the restore stopped at safe to start again, or that step never started. Otherwise it keeps the remedy it gave before. See [logs/restore-resume.md](../logs/restore-resume.md) (decision 10, attempt 1, 09:26). |
| 18 | `ASSUMED` | *(2026-11-06, from the build; accepted 2026-11-07)* `ports.RetentionPolicy` gains `KeepID`, a backup the local engine never prunes, so retention keeps the backup an unfinished restore reads. The ops layer sets it from the unfinished restore; a target's pruning leaves it unset, since a restore reads only this machine's backups. See [logs/restore-resume.md](../logs/restore-resume.md) (unlisted, attempt 1, 09:27, `KeepID`). |
| 19 | `ASSUMED` | *(2026-11-06, from the build; accepted 2026-11-07)* A restore that a ctrl-C stops between the volumes and the hook is journalled as interrupted at the hook, as the engine journals any operation stopped between steps, and, unless the release declares its hook restartable (decision 22), its resume is refused: refusing is the safe side. The hint says the restore stopped at the hook rather than inside it. Telling the two apart belongs to the engine, for every operation, and not to this RFC. See [logs/restore-resume.md](../logs/restore-resume.md) (unlisted, attempt 1, 09:28, ctrl-C between steps). |
| 20 | `ASSUMED` | *(2026-11-06, from the build; accepted 2026-11-07)* **Refines decision 11.** A restore that crashed resumes once cleared. One whose data step failed has already compensated and brought the product back up, so it is restored again from the start. Whether a failed data step should instead leave the product down, so that its resume can continue, is left open for the author. See [logs/restore-resume.md](../logs/restore-resume.md) (decision 11, attempt 1, 10:45). |
| 21 | `ASSUMED` | *(2026-11-06, from the review; accepted 2026-11-07)* The Compose runtime names the container that writes a volume after that volume, `morzer-restore-<volume>`, and removes one an earlier run left behind before writing. A removal that fails leaves the name taken, so the restore is refused rather than written beside a writer a killed manager left running. See [logs/restore-resume.md](../logs/restore-resume.md) (unlisted, attempt 2, 12:35, the helper's name). |
| 22 | `ASSUMED` | *(2026-11-07, from the build and the review; accepted 2026-11-07)* **Refines decision 12.** The floor is 0.14.0, the next release, since 0.13.0 shipped first. The field is told from its absence: it is refused on any operation but `restore` whatever it holds, and declared on `restore`, `false` included, it needs the floor, because an older manager's strict decoder refuses the key. The build's first reading, refusing only `true`, was withdrawn in review. See [logs/restore-restartable.md](../logs/restore-restartable.md) (decision 12, attempt 1, 19:02; attempt 2, 19:30). |

## 12. Phasing

- **P0 — credit across repeated resumes.** Decision 15: the engine fix and
  its test, for every resumable operation. One PR.
- **P1 — `restore --resume`.** Decisions 2–11, 13 and 14: the engine's
  `Restartable`, the split, the port, the record, the gate and the hint, the
  docs. The hook stays refused. One PR.
- **P2 — a restartable hook.** Decisions 1 and 12: the manifest field,
  `release verify`, the schema and the authoring docs. One PR.
