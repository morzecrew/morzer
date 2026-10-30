# RFC 0040 — Keeping an installation down

- **Status:** 📝 Draft — proposed, **awaiting the author's rulings** on the
  `OPEN` decisions in §11; not scheduled
- **Scope:** `morzer stop` and `morzer start`: taking a product down for
  maintenance, a host migration or an incident, and keeping it down until an
  operator brings it back. The stop is recorded in installation state, so every
  path that starts the product today (the boot-time `apply --startup`, an
  unattended update with `auto_apply`, a plain `apply`) reads the record and
  leaves the product down. It covers what each command does, which operations
  run, are refused or are skipped while stopped, the scheduled backup, what
  `status` and `doctor` report, the journal, and the installation schema bump.
  It does not add `down`, never touches volumes, never enables or disables a
  systemd unit, and does not add `restart` (RFC 0021 §8 stays as written for
  that).
- **Related:** [`internal/ports/runtime.go`](../internal/ports/runtime.go)
  (`Stop`, `Start`, `Down`);
  [`internal/adapters/supervisor/systemd/systemd.go`](../internal/adapters/supervisor/systemd/systemd.go)
  (the units);
  [`internal/lifecycle/ops/channel.go`](../internal/lifecycle/ops/channel.go)
  (`RunUnattended`);
  [`internal/adapters/backup/hookbackup/volumes.go`](../internal/adapters/backup/hookbackup/volumes.go)
  (cold capture);
  [0001](0001-update-and-rollback.md), [0016](0016-update-checking-and-unattended-updates.md),
  [0020](0020-several-installations-on-one-machine.md),
  [0021](0021-into-the-running-deployment.md) §8,
  [0030](0030-unit-enablement-is-the-operators.md),
  [0034](0034-removing-an-installation.md),
  [0035](0035-tasks-an-operator-runs-on-demand.md).
- **Discussion:** issue #214.

---

## 1. Summary

`morzer stop [--reason TEXT]` halts the whole project with `Runtime.Stop` and
records the stop (when, why, which release) in installation state.
`morzer start` clears the record and runs `apply`. While the record is there,
boot leaves the product down, the update timer stages but never installs, and
`status` and `doctor` say the product is down on purpose instead of reporting
broken services. The units stay enabled and the volumes are never in reach.

## 2. Motivation

The only way to take a product down today is the runtime directly
(`docker compose -p <project> stop`), and it does not stay down:

- **The project name** is `runtimes.compose.options.project`, which defaults to
  the product name and need not equal it.
- **`down -v`** is one flag from `stop` and deletes the volumes. morzer keeps
  `DownOptions.Volumes` behind an operator's confirmation
  ([`runtime.go:55-57`](../internal/ports/runtime.go)); a hand-typed command
  has no such guard.
- **A reboot brings it back.** `<product>.service` runs
  `apply --startup` ([`systemd.go:498`](../internal/adapters/supervisor/systemd/systemd.go)),
  which converges and starts every service.
- **An unattended update brings it back.** With `update.auto_apply` on, the
  update timer (`update --unattended`, [`systemd.go:562`](../internal/adapters/supervisor/systemd/systemd.go))
  installs the release the channel offers ([`channel.go:485`](../internal/lifecycle/ops/channel.go)),
  which starts the services.

Keeping it down therefore takes `systemctl disable <product>.service` and
stopping `<product>-update.timer`, and morzer still knows nothing:
`doctor`'s `runtime.services` warns about stopped services "because an operator
may have stopped a service on purpose"
([`doctor.go:1189-1194`](../internal/lifecycle/ops/doctor.go)), since it cannot
tell. RFC 0021 named the condition for revisiting a mutating command beside
`exec`: "evidence that operators are running `docker compose restart` anyway"
([0021 §8](0021-into-the-running-deployment.md)). #214 is that evidence for
stopping.

## 3. Current state

- **The runtime half exists.** `Runtime.Stop` halts services without removing
  containers, networks or volumes, and `Runtime.Start` resumes them without
  converging ([`runtime.go:62-79`](../internal/ports/runtime.go)). Only the
  backup's quiesce path calls them
  ([`volumes.go:380-386`](../internal/adapters/backup/hookbackup/volumes.go)).
- **Boot is an `apply`.** `Options.Startup` marks systemd's boot-time run, which
  skips pulls when images are local and migrations when the schema is current
  ([`ops.go:182-185`](../internal/lifecycle/ops/ops.go),
  [`apply.go:652-661`](../internal/lifecycle/ops/apply.go)).
- **The update timer** already has a "nothing while waiting for a person" rule
  for an open intervention ([`channel.go:460-467`](../internal/lifecycle/ops/channel.go))
  and a "staged, not installed" outcome reported as success when `auto_apply`
  is off ([`channel.go:485-494`](../internal/lifecycle/ops/channel.go)).
- **Backups and a stopped stack.** Cold volume capture leaves services that
  were already down alone ([`volumes.go:784-800`](../internal/adapters/backup/hookbackup/volumes.go),
  the "Already down" case). A backup hook that runs inside a service needs that
  service running. Restore stops writers, restores, and re-applies, which starts
  them ([`backup.go:592`](../internal/lifecycle/ops/backup.go),
  [`backup.go:704-716`](../internal/lifecycle/ops/backup.go)).
- **A task starts its dependencies.** `RunOneShot` starts the services a
  one-shot depends on and leaves them running
  ([`compose.go:464-473`](../internal/adapters/runtime/compose/compose.go),
  RFC 0035 decision 6).
- **`exec` refuses a service that is not running**
  ([`inspect.go:424`](../internal/cli/inspect.go)).
- **Operations notify on finishing** through `notifyFinished`
  ([`ops.go:472`](../internal/lifecycle/ops/ops.go)); operation types are a
  closed list ([`operation.go:8-38`](../internal/domain/operation.go)).
- **The installation schema is 13** ([`installation.go:107`](../internal/domain/installation.go)).
  A newer state file is refused by an older manager
  ([`installation.go:917-921`](../internal/domain/installation.go)), and each
  bump has a forward migration ([`state.go:356-364`](../internal/infra/state/state.go)).
- **A declared machine-level flag has precedent:** `policy.skip_scheduled_backups`
  ([`installation.go:760`](../internal/domain/installation.go), RFC 0030
  decision 4).

## 4. Goals / Non-goals

**Goals**

- One command takes the product down, and it stays down across a reboot and an
  unattended update until an operator starts it.
- No path can reach the volumes.
- Every reader (`status`, `doctor`, the timers) tells "stopped on purpose" from
  "broken".

**Non-goals**

- **Stopping one service.** The record is per installation. One service down on
  purpose is a Compose profile or a release change.
- **`down`.** Removing containers buys nothing a stop does not, and puts
  `Down` one flag from the volumes.
- **Managing systemd enablement.** RFC 0030 decision 1 (`LOCKED`) leaves
  enablement to the operator. The units stay enabled and read the record.
- **A maintenance page or traffic draining.** That is the product's or the
  proxy's.

## 5. Design

### 5.1 `morzer stop [--reason TEXT]`

An operation, `stop`, under the deployment lock and the unfinished-operation
gate, through the step engine:

1. **`record-stop`** writes the stop record into installation state: the time,
   the reason (single line, length-bounded, scrubbed of known secret values as
   task arguments are), the operation id and the current release version.
   Compensated by clearing it.
2. **`stop-services`** calls `Runtime.Stop` with no service list (the whole
   project) and the backup's stop timeout. Never `Down`.

The record is written first, so a manager killed between the two steps leaves a
product marked stopped and still running. The next boot leaves it alone, and
`status` shows the record beside running services. The other order would leave
a product down with no record, which the next boot would start: the failure
this RFC exists to remove. A `stop` with no release installed, or before the
first `apply`, is refused: there is nothing to stop.

### 5.2 `morzer start`

An operation, `start`: clears the record, then runs `apply`'s steps. Not
`Runtime.Start`: a stop can outlast a `config set`, a secret rotation or a
staged release, and `apply` is what makes the running stack match them and
waits for health. `start` on an installation that is not stopped runs `apply`
and says nothing was stopped, so a script can call it twice.

### 5.3 While stopped

| Path | Behaviour |
| --- | --- |
| `apply --startup` (boot) | Leaves the product down, exits 0, logs `stopped by the operator since <time>: <reason>`. No runtime call. |
| Update timer (`update --unattended`) | Fetches, verifies and stages as it does with `auto_apply` off; never installs. The assessment says `stopped by the operator`. |
| `apply`, `update`, `rollback` | Decision 3. |
| `restore` | Decision 3. |
| Scheduled backup | Decision 4. An explicit `morzer backup` runs, with the operator watching. |
| `task run` | Decision 5. |
| `exec` | Refused, as for any stopped service. |
| `status` | `stopped by the operator since <time>: <reason>` above the services; `--json` gains a `stopped` object. |
| `doctor` | `runtime.services` reports the stop as information, not a warning, and the health check is skipped. A runtime that cannot be asked still fails (the daemon is needed to start again). |

### 5.4 Journal and notifications

`stop` and `start` are new operation types, journalled and notified through
`notifyFinished` like `apply`. The journal holds the reason as recorded (already
scrubbed) and nothing else new.

### 5.5 Schema

The record is a new field of installation state (decision 6). An older manager
that read it would drop the field and start the product at the next boot, which
is why the version moves.

### 5.6 Alternatives considered

- **A file in `/run`.** `/run` is a tmpfs; a reboot, the one event this has to
  survive, clears it.
- **Disabling the units.** Works for boot, not for the update timer's install,
  breaks RFC 0030 decision 1, and leaves `status` and `doctor` blind.
- **`stop` as `Runtime.Stop` and `start` as `Runtime.Start`.** `Start` resumes
  exactly what was stopped without converging, which is right for a backup's
  minutes and wrong for a maintenance window's days.

## 6. Tests

- Suite: `stop` records and stops; a reboot (`apply --startup`) leaves it down
  and exits 0; `start` clears and converges; `start` twice succeeds.
- Suite: the update timer with `auto_apply` stages and does not install while
  stopped, and installs after `start`.
- Suite: each row of §5.3 as ruled, including the refusals' exit code and hint.
- Suite: `stop` killed between its steps leaves a record and running services,
  and the next `apply --startup` leaves them running and untouched.
- Contract: `stop` never calls `Down` (the fake runtime fails the test on it).
- State: schema 13 migrates to 14 with no record; a 0.10 manager refuses a
  schema-14 file.
- Real Docker (acceptance): `stop`, a simulated reboot, `start`.

## 7. Docs

- A how-to, "Taking a product down for a while", in `pages/docs/operating/`.
- `commands.md` entries for `stop` and `start`; `status` and `doctor` reference
  for the new report; the unit reference says boot reads the record.

## 8. Out of scope

- **`restart`.** RFC 0021 §8 stands: converging one service is a different
  command, and nothing here needs it.
- **The fleet row.** Whether RFC 0026's fleet document carries the stop follows
  from what it reads; not designed here.
- **Stopping on a schedule.** A maintenance window is the operator's cron.

## 9. Risks

- **Forgotten stops.** A product left down for weeks. Mitigated by `status` and
  `doctor` naming the stop and its age on every run; a warning after a long
  stop is decision 7.
- **A manager downgrade.** Covered by the schema bump: the older manager
  refuses the installation rather than starting it.
- **Restore semantics.** If restore runs while stopped (decision 3), its
  re-apply must not start the product; that couples restore to this record.

## 10. Unresolved questions

The `OPEN` rows in §11 are the author's to rule. Nothing else blocks the design.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `LOCKED` | `stop` calls `Runtime.Stop` on the whole project and never `Down`; no flag of `stop` or `start` reaches `DownOptions.Volumes`. The point of the command over a hand-typed `docker compose` is that the volumes are out of reach. |
| 2 | `ASSUMED` | The stop is a record in installation state, written before the runtime call; boot (`apply --startup`) and the update timer read it. Units stay enabled (RFC 0030 decision 1), and `/run` is not used because a reboot clears it. `start` clears the record and runs `apply`, not `Runtime.Start`. |
| 3 | `OPEN` | **Plain `apply`, `update`, `rollback` and `restore` while stopped.** Recommended: `apply`, `update` and `rollback` are refused with exit 2 and a hint to `start` (each converges and waits for health, which a stopped product cannot pass); `restore` runs and leaves the product stopped, ending with a hint to `start`, because restoring during a maintenance window is the natural use. Alternative: all four refused, which keeps restore simple and makes the operator `start` a product whose data is about to be replaced. |
| 4 | `OPEN` | **The scheduled backup while stopped.** Recommended: the timer's run is skipped with a journalled notice naming the stop, and an explicit `morzer backup` runs whatever the hooks can do. A hook that runs inside a stopped service would fail every night of a long window, and a scheduled run that reported success with half its components would be worse. Alternative: run it, and let cold volume capture (the most consistent copy) succeed while hook-based components fail and notify. |
| 5 | `OPEN` | **`task run` while stopped.** `RunOneShot` starts the task's dependencies and leaves them running (`compose.go:464-473`), so a task would bring part of the stack up. Recommended: refused with a hint to `start`, in P1. Alternatives: run it and stop the dependencies it started afterwards; or run it and leave them up. |
| 6 | `OPEN` | **The installation schema.** Recommended: 13 → 14, with a forward migration that adds no record, so a 0.10 manager refuses a stopped machine's state instead of dropping the record and starting the product at boot. Alternative: no bump, accepting that a downgrade starts a stopped product. |
| 7 | `OPEN` | **A long stop.** Recommended: `doctor` reports the stop as information whatever its age; no threshold. Alternative: warn after a stop older than a configurable age, as a stale backup warns. |
| 8 | `ASSUMED` | `stop` and `start` are top-level commands and new operation types, journalled and notified like `apply`. `--reason` is optional, single-line and scrubbed of known secret values. A `stop` with no release installed or before the first `apply` is refused. |
| 9 | `ASSUMED` | While stopped, the update timer stages and never installs, whatever `auto_apply` says, and reports it as the `auto_apply`-off outcome does: success, with the reason. |
| 10 | `ASSUMED` | `doctor`'s `runtime.services` reports a recorded stop as information and skips the health probe; a runtime that cannot be asked still fails it, as since #186. `status` shows the stop above the services and in `--json`. |
| 11 | `ASSUMED` | This refines RFC 0021 decision 8 for `stop` and `start` only: they are the mutating commands #214 supplied the evidence for. `restart` stays out. |

## 12. Phasing

- **P1** — §5 in full, decisions 1–11 as ruled, the docs in §7. One PR.
- Nothing is demand-gated.
