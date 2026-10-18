# Retention and update delay · pre-update backups are pruned, the timer's delay is a setting

Items M3 and M10 of the ERP bundle's requests after 0.4.0, on branch
`retention-and-update-delay` from `f1dc136`. Both add an installation field, so
they share one schema bump, 11 to 12.

- **M3.** Retention exempts every pre-update backup
  (`internal/adapters/backup/hookbackup/backup.go:1099`, and the same exemption
  for targets at `internal/lifecycle/ops/targets.go:167`). A dev VM with
  `update.schedule='*:0/10'` takes a full backup before every update and keeps
  them all.
- **M10.** A production update timer always renders `RandomizedDelaySec=1800`
  (`internal/adapters/supervisor/systemd/systemd.go:548`), whatever the
  schedule says.

**Ruled 2026-10-17 by the author:**

- M3: keep the last N pre-update backups, default 3, setting
  `retention.pre_update_backups`, and the newest is never pruned. This refines
  RFC 0001 decision 3, which exempts them forever.
- M10: a setting `update.randomized_delay`, a duration with `0` allowed,
  default 1800 seconds, shown with the schedule. This refines RFC 0016
  decision 26.

The request's other half of M3, a setting that skips the pre-update backup on
a dev machine, was not ruled and is not built.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

```divergence
decision: RFC 0001 D-3
grade: LOCKED
class: spec-gap
at: 2026-10-18T10:00:00Z
attempt: 1
claim: D-3 exempts every pre-update backup from retention forever, so an installation that updates often keeps a full backup per update and fills its disk; a dev VM following a channel every ten minutes is the case that found it
evidence: internal/adapters/backup/hookbackup/backup.go:1099
action: halted
proposal: LOCKED — refines D-3: the newest `retention.pre_update_backups` pre-update backups (default 3, at least 1) are exempt from pruning; an older one is pruned like any other backup. The newest pre-update backup is never pruned, and none is deleted on compensation.
```

```divergence
decision: RFC 0016 D-26
grade: LOCKED
class: spec-gap
at: 2026-10-18T10:00:00Z
attempt: 1
claim: D-26 fixes the update timer's spread at 1800 seconds in production and none on a sandbox, so a production machine on a short schedule, or one whose operator spreads load another way, cannot change it
evidence: internal/adapters/supervisor/systemd/systemd.go:548
action: halted
proposal: LOCKED — refines D-26: `update.randomized_delay` sets the spread as a duration, `0` for none. Unset, production keeps 1800 seconds and a sandbox keeps none, as D-26 says. The port still carries `SkipUpdateJitter`, named for the unsafe direction, beside the duration.
```

The ruling says "default 1800" and that it refines decision 26, not that it
replaces it. Read together, an unset delay keeps both of decision 26's
defaults, 1800 seconds in production and none on a sandbox, and a set one wins
in either mode. A sandbox defaulting to 1800 would bring back the five-to-
thirty-five-minute tick decision 26 was added to remove.

## As built

- `ports.RetentionPolicy.Prunes` decides what a newest-first listing loses, and
  the local engine, the targets and the fake all prune by it. The exemption by
  reason (`KeepReasons`) is gone: `pre-update` was the only reason ever passed.
- `policy.retain_pre_update_backups` stores the count; zero is the default, 3.
- `update.randomized_delay` is a `*Duration`, because `0` (none) and absent
  (the mode's default) have to stay apart. `config get` reads the value in
  force, not the stored one.
- The installation schema is 12; the 11 to 12 step converts nothing.

## Open: retention runs only when a backup is taken

`stepPruneBackups` and `stepPruneRemoteBackups` run in the `backup` operation
and nowhere else (`internal/lifecycle/ops/backup.go:311`). An update takes its
pre-update backup and prunes nothing, so a dev VM updating every ten minutes
still collects up to a day of pre-update backups between scheduled backups, and
with `backup.scheduled=false` it is never pruned at all. Pruning after a
successful update would close that. It is a change to what `update` does on
every machine, and the ruling does not cover it, so it is not built here. It
needs the author's ruling.

```divergence
decision: RFC 0001 D-21
grade: LOCKED
class: spec-gap
at: 2026-10-18T14:30:15Z
attempt: 1
claim: D-21 prunes older pre-update backups, but retention runs only in `backup`, so a machine that updates and never takes a scheduled backup still keeps every pre-update backup
evidence: internal/lifecycle/ops/backup.go:311
action: halted
proposal: LOCKED — refines D-21: a successful update applies retention as its last step, locally once it has taken its pre-update backup and on each target that backup reached. Retention runs only once a newer backup exists, so an update that took no backup, or failed, prunes nothing.
```

## Ruled 2026-10-18 by the author

- M10: the reading above is confirmed. An unset delay keeps 1800 seconds in
  production and none on a sandbox; a set one wins in either mode.
- M3: the only condition for pruning is that a new backup exists. The
  pre-update backup is one, so a successful update prunes, as proposed above.
  RFC 0001 row 22.

## As built, the update's retention

- Two steps close the update, `prune-backups` and `prune-remote-backups`, the
  same ones `backup` runs. Last and `Continue`, so a failed update removes
  nothing and a failed prune never fails an update that converged.
- `prune-backups` is skipped when the state holds no backup ref: a first
  install, `--skip-backup`, or `policy.skip_backup_before_update`.
- The pre-update push records the copies that landed under
  `pushed-backups`, and `prune-remote-backups` prunes only those targets. A
  copy the update makes is best effort, unlike `backup`'s, so a target it
  missed holds nothing newer.

## Open: `backup --no-push` still prunes the targets

`backupSteps` adds `prune-remote-backups` whenever `--no-prune-remote` is
absent, pushed or not (`internal/lifecycle/ops/backup.go:312`). A local-only
backup therefore prunes targets that received nothing new, which the 2026-10-18
ruling says it should not. It predates this branch and is not changed here.

## Review on #116

Raised by CodeAnt:

- **An update took a pre-update backup and pruned nothing.** The open item
  above, built as ruled in `2445834`.
- **Prune treated a manifest it could not reread as an ordinary backup.** List
  reads every manifest, and Prune reads them again for their reasons. A reread
  failing between the two left the reason empty, so an older pre-update backup
  could be pruned against the policy. It now fails the prune, which removes
  nothing; the step is `Continue`, so the backup or update still succeeds. A
  manifest that changes between two reads a moment apart cannot be arranged
  from a test, so this one has no red test.
- **A sub-second delay rendered as none.** `500ms` was accepted, and the unit
  rendered `RandomizedDelaySec=0` while `status` showed 500ms. `Validate` now
  refuses a delay that is not whole seconds, which covers `config set` and a
  hand-written installation file.
- **Text `status` says nothing about a zero delay.** Not changed. Zero is "no
  delay", which the schedule note already implies, and `status --json` carries
  `randomized_delay` either way.

Raised by CodeRabbit, against `2445834`:

- **Prune with an unreadable manifest, and a sub-second delay.** The same two
  findings as above, already fixed in `4a5e343`. CodeRabbit suggested pruning
  around an unreadable entry. Pruning nothing that run is simpler, and the next
  backup retries the prune.
- **The status note promised a bound the timer does not keep.** "up to 5m0s
  late" left out systemd's `AccuracySec`, one minute by default, which comes on
  top of the random delay. The note now describes the setting: "random delay up
  to 5m0s". Setting `AccuracySec` would rewrite every installation's timer
  unit, which is out of scope here.

Raised by CodeRabbit, against `4a5e343`:

- **A plan accepted a sub-second delay that the run refuses.** The whole-second
  check ran only in `Validate`, at save, so `config set --dry-run
  update.randomized_delay=500ms` planned a change the same command then
  refused. `domain.ValidateUpdateRandomizedDelay` now holds the check, and both
  `Validate` and the setting call it.
- **A value equal to the default was not recorded, and could not be unset.**
  Change detection compared what `config get` shows, which is the value in
  force. Setting `30m` in production, or `3` pre-update backups, reported "no
  change" and stored nothing. Unsetting a stored `30m` also reported "no change"
  and left it stored. RFC 0016 decision 31 makes an operator's value distinct
  from unset.
  Change detection now compares the stored value; `config get` still shows the
  value in force.
