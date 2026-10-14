# Update schedule · a setting for the update timer's `OnCalendar`

Planned against `main` at `4c5e31d`. The update timer renders
`systemd.DefaultUpdateSchedule` (`*-*-* 03:30:00`) on every installation, and
`ports.UnitParams.UpdateSchedule` -- which the adapter already honours -- is
set by nothing. A dev VM that should follow `main` within minutes has no way to
say so except a systemd drop-in.

**Drift count: 0.** Nothing is built yet; the entries below are the plan's.

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

```divergence
decision: RFC 0016 D-12
grade: LOCKED
class: spec-gap
at: 2026-10-14T18:57:45Z
attempt: 1
claim: D-12 says OnCalendar is the maintenance window and that systemd expresses it "better than a config field would", but nothing on the machine lets an operator write that expression -- unitParams never sets UpdateSchedule, so every installation renders 03:30 -- and the one route left, the drop-in the docs show, appends a trigger rather than replacing one
evidence: internal/lifecycle/ops/settings.go:382
action: halted
proposal: LOCKED — refines D-12: OnCalendar is still the maintenance window, and the operator writes it in `update.schedule`, an installation setting stored verbatim and rendered into the timer, as `backup.schedule` already is. What D-12 rejected is a second mechanism that translates into OnCalendar; a field that holds the expression itself is not one. A drop-in is not an alternative: it adds a trigger, so the documented `OnCalendar=Sun *-*-* 04:00:00` runs Sunday *and* nightly.
```

Further evidence for the entry above, as of `4c5e31d`: the decision at
`rfcs/0016-update-checking-and-unattended-updates.md:705`, the drop-in recipe at
`pages/docs/operating/unattended-updates.md:73`, and `man systemd.timer` on
`OnCalendar=` -- "May be specified more than once, in which case the timer unit
will trigger whenever any of the specified expressions elapse".

The documented recipe is the sharper half. An operator who followed
`unattended-updates.md` believes auto-apply is confined to Sunday morning, and it
still runs every night at 03:30. A drop-in needs `OnCalendar=` on a line of its
own first to reset the list, and the page does not say so.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-14T18:57:45Z
attempt: 1
claim: RandomizedDelaySec=1800 is unconditional, so a schedule of every five minutes fires every five to thirty-five; RFC 0016 §5.4 says dev mode has no poll-cadence floor and D-11 licenses dev mode to relax scheduling, but neither says the jitter is part of the cadence
evidence: internal/adapters/supervisor/systemd/systemd.go:493
action: decided
proposal: ASSUMED — a dev-mode installation's update timer carries no RandomizedDelaySec. The jitter protects a vendor's registry from its customers arriving at once, and a sandbox polls the vendor's own registry. Production keeps 1800 whatever the schedule. Carried to the port as `SkipUpdateJitter`, named for the unsafe direction so the zero value keeps the jitter.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-14T18:57:45Z
attempt: 1
claim: where the value lives is unsettled; backup.schedule went to `policy.backup_schedule`, and the update knobs it sits beside in `config` live under `update.*`
evidence: internal/domain/installation.go:384
action: decided
proposal: ASSUMED — `update.schedule` in UpdateConfig, so the setting's name is its path in installation.yaml, as `update.check` and `update.channel` already are. Schema 10 → 11 for the write path, as schema 7 did for backup_schedule: an older manager's `config set` would drop the field and the next reconciliation would put 03:30 back.
```

**Ruled 2026-10-14 by the author: all three proposals accepted as written.**
D-12 is refined rather than reversed, the dev-mode jitter and the `update.*`
location are taken as proposed, and building starts on branch
`update-schedule`. Entries from here on record the build.

```divergence
decision: RFC 0016 D-12
grade: LOCKED
kind: resolved
at: 2026-10-14T19:04:30Z
attempt: 1
claim: built as ruled -- OnCalendar is still the maintenance window, now written in `update.schedule` and rendered verbatim; recorded as decision 25
evidence: internal/lifecycle/ops/settings.go:94
action: decided
```

```divergence
decision: unlisted
grade: UNLISTED
kind: resolved
at: 2026-10-14T19:04:30Z
attempt: 1
claim: built as ruled -- a dev-mode update timer renders without RandomizedDelaySec; recorded as decision 26
evidence: internal/lifecycle/ops/settings.go:432
action: decided
proposal: ASSUMED — recorded in RFC 0016 as decision 26.
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-14T19:04:30Z
attempt: 1
claim: the plan listed no sandbox-import verdict for the new field, and `TestEveryInstallationFieldIsClassifiedForASandbox` refused the build until one existed; kept, because a timer's cadence reaches nothing a production export could point at
evidence: internal/domain/sandbox_test.go:98
action: decided
proposal: ASSUMED — `update.schedule` survives `import --mode dev`, like every other `update.*` field.
```

The location row (`update.*`, schema 11) needed no RFC row: it follows schema
7's precedent and RFC 0016 has no section on where its settings live. It is
recorded in the CHANGELOG and in the schema 11 comment beside the constant.

Sabotage sweep, each mutant compiled and restored from a copy rather than
`git checkout`: dropping the schedule from `unitParams`, hard-coding
`SkipUpdateJitter: false`, removing the load-time `ValidateUpdateSchedule`,
forcing the jitter line in the template, dropping the field from `Units`'
copy, removing `case 10`, and not storing the value in `Apply` -- seven of
seven killed.

**Drift count: 0.**

**A note on the entries above, appended rather than hidden.** Before this log
was first committed, the `evidence` field of five entries carried several
citations separated by semicolons, which `log_check.py` refuses (E2), and the
dev-mode close-out lacked the `proposal` an unlisted decision owes (S9). Each
field was cut to its first citation and the rest moved into prose beside the
entry; no claim, grade, action or proposal changed meaning. The second
entry's other citation was RFC 0016 §5.4's "Poll cadence — no floor"
(`rfcs/0016-update-checking-and-unattended-updates.md:443`), the third's was
`policy.backup_schedule` (`internal/domain/installation.go:636`).

## Self-audit, 2026-10-14

Scope: the whole branch before its first commit -- 13 tracked files and two new
ones, source, tests, docs, RFC, schema and CHANGELOG. Ten passes walked.

- **Fixed — the setting's description understated what a tick does.** It said
  the timer "checks the channel"; a tick follows the channel, stages, and
  installs when `update.auto_apply` allows. An operator choosing a window
  from `config settings` would read it as a poll and set it during working
  hours. Reworded in `settings.go`, `parameters.md` and the CHANGELOG.
- **No action — test helpers repeat the shape of `installedMachine` and
  `onCalendar`.** Those return a runner and read the backup timer; widening
  them to serve this file would change four tests to save twelve lines.

Evidence. Every path that renders units goes through `unitParams`
(`init.go:962`, `settings.go:476`, `doctor.go:1290`), so a dev-mode `init`
gets the unjittered timer without a later `config set`. `doctor` compares unit
presence and enablement, not contents, so the change raises no new warning on
an upgraded machine. Patch coverage over `./internal/...` and `./test/suite/`
with `-coverpkg=./internal/...`: 40 of 40 added statement lines executed.
Sabotage: seven of seven killed, recorded above. `just check`, lint and
`just log-check` pass.

Residue. A well-formed but wrong expression (`every 5m`) is stored and
rendered, and systemd refuses the timer -- the same as `backup.schedule`, and
by `ValidateBackupSchedule`'s stated design, but untested for either.

**Drift count: 0.**

## Review, round 1, 2026-10-14

**Correction to the self-audit's scope line:** the branch touched 12 tracked
files and two new ones, not 13. The thirteenth was `.claude/CLAUDE.md`,
modified in the working tree and never part of the branch.

Found by review and missed by the self-audit, each fixed on the branch:

- **The schedule bound measured a trimmed copy** of a value rendered as it
  stands, so a hand-edited schedule padded past 200 characters loaded. It
  predates this branch -- `ValidateBackupSchedule` had it -- and the refactor
  carried it into the update schedule unexamined. Reproduced red in
  `TestTheScheduleBoundRefusesWhatItSaysItRefuses` before the fix.
- **The jitter check in `TestUnitsCarriesEveryFieldIntoTheRendering` passed
  when the timer was missing**, shown by a sabotage that dropped the timer
  whenever `SkipUpdateJitter` was set. Seven of seven at the self-audit
  measured the mutants imagined; this was not one of them.
- **The schema 11 comment said an older manager reads and ignores the
  field.** It refuses the file; the comment described the no-bump case, the
  exact misreading the schema history above it warns against.

Rule worth keeping: a refactor that shares a guard between two callers
inherits the guard's defects for both, so audit the shared body against what
each caller renders, not against what it used to do.
