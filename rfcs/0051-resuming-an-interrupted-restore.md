# RFC 0051 — Resuming an interrupted restore

- **Status:** 📝 Draft — proposed 2026-11-06, and ruled by the author
  2026-11-06: §11's `OPEN` rows as recommended. Ready to build.
- **Scope:** `morzer restore --resume`: continuing a restore that was
  interrupted (Ctrl-C, a killed process, a reboot) or that needed
  intervention and was cleared, from its first incomplete step, without
  restoring again what already went back. It covers the step engine's new
  notion of a step that may be started again but keeps its credit once done,
  splitting the restore of volumes from the release's restore hook, what the
  journal records for a resume, the unfinished-operation gate's remedy, and
  whether a release can declare its restore hook safe to run again. Not
  covered: resuming inside one volume or one hook run, a restore from a
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

A resume continues whatever ran since the interruption. `stop-services` runs
again on every resume, so a product `apply` brought up in between is taken
down before anything is written (decision 6). It is refused, naming
`morzer start` or `morzer stop`, when the installation was stopped or started
since: the step list depends on it (decision 9).

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

### 5.3 The restore's steps

| Step | Today | Proposed | Why |
| --- | --- | --- | --- |
| `verify-backup` | idempotent | restartable | Done once, it need not be done again. A resume after the data went back must not fail because the backup has since been pruned. |
| `check-compatibility` | idempotent | restartable | As above. |
| `stop-services` | idempotent | idempotent | Runs on every resume, so nothing started in between is running under the restore. It also rebuilds the runtime configuration the later steps read. |
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
  the same command, and keeps `morzer apply` as the road that leaves the
  restore unfinished. A restore interrupted inside a hook that is not
  restartable says that a resume would be refused and why.
- **After intervention.** A restore that failed and needed intervention
  resumes once the operator has repaired the cause and cleared it, which
  journals it `failed`, as every resumable operation does. It resumes only
  from a step that may be started again (decision 11).
- **The backup.** Retention keeps the backup an unfinished restore names, so
  a backup taken in the meantime cannot prune it before the resume
  (decision 3).

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
  whatever the code now says.
- Suite, against the fakes:
  - interrupted in `reapply-release`: a resume skips both data steps and
    finishes with the smoke test;
  - interrupted in `restore-volumes`: a resume writes them again;
  - interrupted in the hook: a resume is refused without the declaration,
    and runs the hook again with it;
  - the record's backup, components and cross-installation flag are used;
    `--backup` beside `--resume` is refused;
  - a stop or start in between refuses the resume;
  - the gate's remedy names `restore --resume`;
  - after `--clear-intervention`, a failed restartable step resumes;
  - decision 3's retention, as ruled.
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

- **A resume after `apply` replaces what the product wrote since.** That is
  what finishing a restore means. The hint says the resume takes the product
  down again.
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
| 3 | `ASSUMED` | **Whether retention keeps the backup an unfinished restore names.** Recommended: yes; `Prune` skips the backup named by the restore record `--resume` would continue (the most recent restore, when it is `running`, `interrupted` or `failed`), so at most one backup outlives the policy, until a later restore finishes. Alternative: no; a resume whose backup was pruned is refused at the step that reads it, and the operator restores afresh. Was `OPEN`; ruled by the author 2026-11-06: as recommended. |
| 4 | `ASSUMED` | `morzer restore --resume` continues the most recent restore record that is `running`, `interrupted` or `failed`, under the deployment lock and through the engine's resume, as `stop --resume` does. |
| 5 | `ASSUMED` | `engine.Step` and `domain.StepRecord` gain `Restartable`: a step that did not finish may be run again from its start, and one that finished keeps its credit. `resumePoint` accepts a started step when it is idempotent or restartable in both the code and the journalled record. |
| 6 | `ASSUMED` | The restore's steps as §5.3: `verify-backup` and `check-compatibility` restartable; `stop-services` idempotent and run on every resume; `restore-data` split into `restore-volumes` (restartable) then `restore-data` (the hook; decision 1); both need intervention on failure. |
| 7 | `ASSUMED` | `ports.RestoreOptions` gains the part a call restores, volumes or the rest. Each call verifies and stages only what it restores, and both check the backup's installation. |
| 8 | `ASSUMED` | The record carries what a resume reuses: `backup_id` as today, plus `components` and `allow_cross_installation`. `--backup`, `--component` and `--allow-cross-installation` beside `--resume` are refused. `--identity` is accepted and never journalled. |
| 9 | `ASSUMED` | A resume is refused, naming `morzer start` or `morzer stop`, when the installation was stopped or started since the restore began. |
| 10 | `ASSUMED` | The gate's remedy for a restore names `morzer restore --resume --force --confirm <installation-id>`. `restoreLeftBehind`'s hint names it too, and keeps `morzer apply` as the road that leaves the restore unfinished. |
| 11 | `ASSUMED` | A restore that needed intervention resumes once cleared, since `--clear-intervention` journals it `failed`, and only from a step that may be started again. |
| 12 | `ASSUMED` | `restartable` is a field of `operations.restore` alone; `release verify` refuses it elsewhere; a release declaring it needs `min_manager_version` 0.13.0 (`RestartableRestoreMinManagerVersion`), on the assumption that 0.13.0 is the next release. |
| 13 | `ASSUMED` | A resumed restore attests and notifies as a resumed `apply` does: under the interrupted run's id, so its statement replaces the interrupted run's. |
| 14 | `ASSUMED` | A fresh `restore` still runs over an unfinished one, as today; it starts a new record, and the old one is left for `--clear-intervention`. |

## 12. Phasing

- **P1 — `restore --resume`.** Decisions 2–11, 13 and 14: the engine's
  `Restartable`, the split, the port, the record, the gate and the hint, the
  docs. The hook stays refused. One PR.
- **P2 — a restartable hook.** Decisions 1 and 12: the manifest field,
  `release verify`, the schema and the authoring docs. One PR.
