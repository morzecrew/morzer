# stop-and-start · RFC 0040 P1, `morzer stop` and `morzer start`

On branch `stop-and-start` from `e4915c8`, building
[RFC 0040](../rfcs/0040-keeping-an-installation-down.md) P1 as ruled by the
author on 2026-10-30. That covers `stop` and `start`, the stop record in
installation state (schema 13 → 14), and what boot, the update timer, `apply`,
`update`, `rollback`, `restore`, the scheduled backup and `task run` do while
stopped. It also covers what `status`, `doctor` and `ls` report, the tests of
§6 and the docs of §7. Closes #214.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Three things the RFC does not settle, found while planning and decided before
any code.

Decision 4 skips the scheduled backup while stopped and lets an explicit one
run, but nothing tells the two apart today. The backup unit runs
`backup --yes`, and `--reason` defaults to `manual` either way.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-30T19:01:00Z
attempt: 1
claim: decision 4 needs to tell the timer's backup from an operator's, and the unit's command line is the same as an explicit `backup --yes`, whose `--reason` defaults to manual (internal/cli/commands.go:762)
evidence: internal/adapters/supervisor/systemd/systemd.go:522
action: decided
proposal: ASSUMED — the backup unit runs `backup --unattended`, as the update unit runs `update --unattended`, and only an unattended backup is skipped while stopped. A unit written by an older manager has no flag and backs up as before until the next `apply` or `start` rewrites it.
```

§5.3 lists what runs while stopped but leaves out the commands that restart
services as a side effect. `config set` re-creates the affected services with
`Up`, and `secret set`, `generate`, `rotate` and `edit` restart the dependent
services. Either one would bring a stopped product back up, against goal 1.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-30T19:02:00Z
attempt: 1
claim: config and secret changes start stopped services through Up (internal/lifecycle/ops/config.go:386-396) and Restart, and §5.3 does not list them
evidence: internal/cli/secret.go:493
action: decided
proposal: ASSUMED — while stopped, `config set` and the secret commands store the change, and secrets render as they do today, but nothing is re-created or restarted. The output says the change takes effect at `start`, which runs `apply`.
```

An export carries the whole installation document, so a stopped machine's
export would carry the stop record onto the machine it is imported on.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-30T19:03:00Z
attempt: 1
claim: `installation import` copies the exported installation, which would carry a stop record to a machine where nothing was stopped
evidence: internal/lifecycle/ops/recovery.go:57
action: decided
proposal: ASSUMED — import drops the stop record. It describes the old machine's runtime, and the new machine starts with the `apply` the operator runs after the import.
```

## Found while building

Four more, each decided when the code reached it. They are written down here
after the fact, which the skill asks not to do, so each says what it changed and
where.

When the runtime call failed in a test, it showed that a `stop` killed between
its steps would block everything. The unfinished-operation gate refuses a fresh
operation over the unfinished record and offered `--resume` only for `apply` and
`update`. So the first draft's "run `stop` again" pointed at a road the gate
closes.

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-10-30T19:20:00Z
attempt: 1
claim: an interrupted `stop` or `start` left a record the gate refuses to drive over, and the gate's remedy did not name a way to finish it
evidence: internal/lifecycle/preflight/preflight.go:603
action: decided
proposal: ASSUMED — `stop` and `start` resume with `--resume`, as `apply` does, and the gate names `morzer stop --resume` or `morzer start --resume` for their unfinished records.
```

`init --repair` rebuilds the installation from its flags and carries a named
list of fields. Without the stop on that list, a repair would have quietly
ended a stop.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-30T19:22:00Z
attempt: 1
claim: `init --repair` carries only the fields it names, so a repair would have cleared the stop
evidence: internal/lifecycle/ops/init.go:625
action: decided
proposal: ASSUMED — a repair carries the stop; only `morzer start` clears one.
```

A second `stop` on a stopped product keeps the first record and stops the
services again, so the time `status` shows is when the product actually went
down.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-30T19:24:00Z
attempt: 1
claim: the RFC does not say what `stop` does on a product that is already stopped
evidence: internal/lifecycle/ops/stop.go:147
action: decided
proposal: ASSUMED — `stop` on a stopped product keeps the first stop's record and stops the services again; it says it was already stopped.
```

Decision 4 says the skipped run leaves "a journalled notice". This build reads
that as the unit's systemd journal: the skip is the run's summary line, exit 0,
as the update timer's staged outcome is. No operation record is written for a
run that did nothing.

```divergence
decision: 4
grade: ASSUMED
class: spec-gap
at: 2026-10-30T19:26:00Z
attempt: 1
claim: "journalled notice" can mean the systemd journal or morzer's operation journal, and a record for a run that did nothing would be the only one of its kind
evidence: internal/lifecycle/ops/backup.go:68
action: departed
proposal: ASSUMED — the skipped scheduled backup exits 0 and says so in the unit's journal; it writes no operation record.
```

The `LOCKED` rows this branch touches, honored:

```divergence
decision: 1
grade: LOCKED
kind: resolved
at: 2026-10-30T19:34:00Z
attempt: 1
claim: `stop` calls Runtime.Stop on the whole project and never Down; neither command has a flag that reaches DownOptions
evidence: internal/lifecycle/ops/stop.go:172
action: decided
```

```divergence
decision: 3
grade: LOCKED
kind: resolved
at: 2026-10-30T19:34:00Z
attempt: 1
claim: `apply`, `update` and `rollback` are refused with exit 2 and a hint to `start`; `restore` runs, re-applies nothing, does not bring the product up when it fails, and ends with a hint to `start`
evidence: internal/lifecycle/ops/apply.go:49
action: decided
```

```divergence
decision: 5
grade: LOCKED
kind: resolved
at: 2026-10-30T19:34:00Z
attempt: 1
claim: `task run` is refused with exit 2 and a hint to `start` while stopped
evidence: internal/lifecycle/ops/task.go:117
action: decided
```

```divergence
decision: 6
grade: LOCKED
kind: resolved
at: 2026-10-30T19:34:00Z
attempt: 1
claim: the installation schema is 14, and 13 migrates with no record
evidence: internal/infra/state/state.go:372
action: decided
```

**Drift count: 0.**
