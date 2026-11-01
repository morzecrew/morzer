# notify-outbox · RFC 0043 P2, the outbox and the scheduled doctor

On branch `notify-outbox` from `4496ab3`, building
[RFC 0043](../rfcs/0043-notifications-that-survive-a-failed-delivery-and-a-scheduled-doctor.md)
P2 as ruled by the author on 2026-10-31: §5.2 and §5.4. That covers:

- the outbox: an entry per target whose error is resendable, bounded at 100 per target, dropping the oldest;
- `morzer notify flush`, and the scheduled `doctor` sending it, one sender at a time under the `notify-outbox` lock;
- `notify target remove` deleting that target's entries, and `doctor`'s `notify.outbox`;
- `doctor --unattended` and its one `doctor.finished` event, filtered by each target's `min_level`;
- `<product>-doctor.timer`, `doctor.scheduled` and `doctor.schedule`;
- `config.operations` leaving out the record of the operation holding the deployment lock (decision 12);
- the tests of §6 that P2 reaches, and the docs of §7.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Seven things the RFC leaves to the build, decided while planning and before any
code. Each follows a precedent already in the tree, which is why none of them
stops the build.

§5.2 names an entry `<event-id>.<target-name>.json`, and a target's name is its
label: the name an operator gave it, or its URL with the password removed.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T08:59:00Z
attempt: 1
claim: a target's label can be a URL, which holds `/` and `:` and cannot be part of a file name
evidence: internal/domain/installation.go:627
action: decided
proposal: ASSUMED — an entry is `<event-id>.<token>.json`, where the token is the first 16 hex digits of the SHA-256 of the target's label, and the entry holds the label; the outbox's place, mode and bound are as decision 2 has them.
```

The file name is §5.2's sketch, not what decision 2 locks: an outbox per
target, under that directory, bounded at 100, sent by the scheduled `doctor`
and `notify flush` and never during an operation. All of that is built as
ruled, and the close-out at the end of this log says where.

§5.2 has `notify.outbox` report how many entries were dropped, and an entry
dropped is gone, so the count has to live somewhere else.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T09:00:00Z
attempt: 1
claim: the number of entries dropped past the bound is not recorded anywhere an entry is not
evidence: rfcs/0043-notifications-that-survive-a-failed-delivery-and-a-scheduled-doctor.md:173
action: decided
proposal: ASSUMED — each target's drop count is kept in `<token>.dropped` beside its entries, and is removed with its last entry, so `notify.outbox` reports the drops since the target last caught up.
```

§5.2 sends oldest first and keeps an entry on a resendable answer, and says
nothing of the entries behind it for the same target.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T09:01:00Z
attempt: 1
claim: a target that is still down would be tried for each of its up to 100 entries, each under the client timeout and its retry
evidence: rfcs/0043-notifications-that-survive-a-failed-delivery-and-a-scheduled-doctor.md:164
action: decided
proposal: ASSUMED — a send that gets a resendable answer from a target sends none of that target's later entries in the same run; the other targets' entries are still sent.
```

An entry can outlive its target by a route other than `notify target remove`:
an imported installation, or a state file edited by hand.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T09:02:00Z
attempt: 1
claim: an entry whose target is no longer configured has nowhere to be sent
evidence: rfcs/0043-notifications-that-survive-a-failed-delivery-and-a-scheduled-doctor.md:176
action: decided
proposal: ASSUMED — a send deletes an entry whose target is not configured, as `notify target remove` would have, and counts it as dropped in what it reports.
```

§5.4 sketches the unit with `--product <product>`, and every unit the manager
writes selects its installation with `--config` instead.

```divergence
decision: 8
grade: ASSUMED
class: spec-gap
at: 2026-11-01T09:03:00Z
attempt: 1
claim: the unit sketch selects the installation differently from the four units beside it
evidence: internal/adapters/supervisor/systemd/systemd.go:522
action: departed
proposal: ASSUMED — `<product>-doctor.service` runs `doctor --unattended --plain --log-format json --config <path>`, as the backup and update units run theirs; `doctor.schedule` defaults to `*-*-* 06:00:00`, after the nightly backup and update ticks, with `RandomizedDelaySec=1800` and `Persistent=true`.
```

The two settings are installation state, and adding a field to it has bumped
the schema every time, so an older manager refuses the state rather than
rewriting it without the field (#116 took 11 to 12 for the update delay).

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T09:04:00Z
attempt: 1
claim: `doctor.scheduled` and `doctor.schedule` add installation fields, and the RFC does not say whether the schema moves
evidence: internal/domain/installation.go:112
action: decided
proposal: ASSUMED — the installation schema goes from 14 to 15, with a migration that converts nothing; `doctor.schedule` is `policy.doctor_schedule`, and `doctor.scheduled=false` is `policy.skip_scheduled_doctor`, negative as `skip_scheduled_backups` is, so a missing field means on.
```

Decision 6 says what `doctor.finished` names, and not where in the event.

```divergence
decision: 6
grade: ASSUMED
class: spec-gap
at: 2026-11-01T09:05:00Z
attempt: 1
claim: the event shape for the checks `doctor.finished` names is not settled
evidence: internal/events/event.go:185
action: departed
proposal: ASSUMED — `doctor.finished` carries `level` as the worst, `message` naming the checks that warn or fail, and `checks`, each a check result as the `check` event carries it, with its remedy; only the checks that warn or fail are in it.
```

**Drift count: 0.**

## Found while building

`TestADownTargetsEventIsKeptAndFlushedOnce` found it: two events minted in one
millisecond get ULIDs whose random halves order them by chance, so the outbox
sent the newer first. The outbox sorts by name to send oldest first.

```divergence
decision: 3
grade: ASSUMED
class: spec-gap
at: 2026-11-01T09:12:00Z
attempt: 1
claim: event ids minted in the same millisecond do not sort in the order they were minted, and the outbox sends in id order
evidence: internal/lifecycle/ops/ops.go:523
action: departed
proposal: ASSUMED — the event id's ULID takes its entropy from a source monotonic within the process, so ids minted in one millisecond sort in the order they were minted.
```

**Drift count: 0.**

The `LOCKED` row this branch touches, honored: an outbox per target under
`<var>/manager/notify/outbox/`, bounded at 100, sent by the scheduled `doctor`
and `notify flush`, and never during an operation, where `ops.notify` only
writes it.

```divergence
decision: 2
grade: LOCKED
kind: resolved
at: 2026-11-01T09:16:00Z
attempt: 1
claim: what a target did not accept is kept per target, bounded at 100, and sent only by the scheduled doctor and notify flush
evidence: internal/lifecycle/ops/notify_outbox.go:219
action: decided
```

**Drift count: 0.**

## Self-audit, 2026-11-01

- **An entry was deleted when its target could not be built.** The send took
  "configured" from the notifiers, and a target whose secret does not decrypt
  in a given run is left out of those, so every entry it had waiting was
  deleted as orphaned. Configured is now the installation's list, and an entry
  whose target is configured but not built this run is kept.
  `TestAnEntryWaitsForATargetThatCannotBeBuilt` failed before the fix. The
  09:02 entry's rule is unchanged; the code had implemented it against the
  wrong list.
