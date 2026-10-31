# RFC 0043 — Notifications that survive a failed delivery, and a scheduled doctor

- **Status:** 📝 Draft — proposed, and ruled by the author 2026-10-31: §11's
  `OPEN` rows as recommended. P1 is ready to build
- **Scope:** Two gaps in how a machine nobody is watching tells someone.
  First, a notification that does not reach its target is lost: there is no
  retry and nothing is kept (RFC 0015 decision 6). Second, nothing runs
  `doctor` on a schedule, so a run that was killed, a backup that went stale or
  an unreachable backup target is found only when someone types `doctor`. The
  RFC adds a retry inside the existing deadline, an outbox for what still did
  not arrive, an id on each forwarded event so a receiver can drop a
  duplicate, and a `<product>-doctor.timer` that runs `doctor`, notifies what
  it finds and sends what is in the outbox. It refines RFC 0015 decision 6 and
  adds forwarded event kinds under decision 4's allowlist. It does not add
  notification channels other than the webhook, deduplication, escalation or
  silencing, which RFC 0015 leaves to the receiver. It does not make a
  notification a guarantee that anything downstream may rely on.
- **Related:** [`internal/lifecycle/ops/ops.go`](../internal/lifecycle/ops/ops.go)
  (`notify`, `NotifyDeadline`, `forwardedKinds`);
  [`internal/adapters/notify/webhook/webhook.go`](../internal/adapters/notify/webhook/webhook.go);
  [`internal/ports/notify.go`](../internal/ports/notify.go) (`Notifiers`);
  [`internal/lifecycle/ops/doctor.go`](../internal/lifecycle/ops/doctor.go);
  [`internal/adapters/supervisor/systemd/systemd.go`](../internal/adapters/supervisor/systemd/systemd.go)
  (the timers);
  [0015](0015-notifications.md) §5.3 and decisions 4, 6 and 11,
  [0016](0016-update-checking-and-unattended-updates.md),
  [0030](0030-unit-enablement-is-the-operators.md) decision 1,
  [0041](0041-files-an-operator-passes-to-a-task.md) decision 19.

---

## 1. Summary

A notification gets one retry inside the 15-second deadline it already has. If
a target still has not accepted it, it goes into an outbox under the manager's
directory. A new `<product>-doctor.timer` runs `morzer doctor --unattended`
once a day. That run sends what is in the outbox, runs every check, and
notifies the checks that warn or fail. Each forwarded event carries an `id`,
sent as `Idempotency-Key`, so a receiver can drop a redelivered one. Delivery
becomes at-least-once for what the outbox holds and stays best-effort for the
rest. The journal remains the record.

## 2. Motivation

- **A target that was down means nobody was told.** RFC 0015 §5.3 states it:
  "No retries. A webhook that was down means the operator was not told. No
  persistence." That was right for a first adapter. For the unattended update
  and the scheduled backup, though, the notification is how a person learns
  anything happened, and the chat service that blinked for ten seconds at 03:00
  is the common case.
- **A killed run sends nothing.** An operation that is `SIGKILL`ed, or whose
  machine loses power, never reaches `notifyFinished`. Its journal record stays
  unfinished, and `doctor`'s `config.operations` fails on it
  ([`doctor.go:369-405`](../internal/lifecycle/ops/doctor.go)). But `doctor`
  runs only when somebody types it.
- **`doctor` is never scheduled, and never notifies.** It is "deliberately not
  wired to the event bus" ([`doctor.go:64-68`](../internal/lifecycle/ops/doctor.go))
  and has no notify call. A stale backup, an overdue secret rotation or an
  unreachable backup target is found at the next manual run, which on a
  machine nobody logs into is after the incident.
- **RFC 0015 left the door open.** "Adding retries later is compatible with all
  of this. Adding them now would mean deciding what happens when an operation
  finishes and its notification is still retrying." The outbox answers that
  question: nothing is still retrying when the process exits, because what has
  not arrived is on disk.

## 3. Current state

- **Delivery** is synchronous under `NotifyDeadline`, 15 seconds
  ([`ops.go:437`](../internal/lifecycle/ops/ops.go)), and not detached from
  the operation's context, so Ctrl-C does not wait on a dead endpoint
  ([`ops.go:514-541`](../internal/lifecycle/ops/ops.go)). `ops.notify` logs
  an error the notifier returns.
- **A failed target is logged nowhere.** The fan-out is concurrent and drops
  each target's error, on the grounds that "the adapter logs its own failures"
  ([`notify.go:42-59`](../internal/ports/notify.go)). The webhook adapter logs
  nothing and returns its error
  ([`webhook.go:146-157`](../internal/adapters/notify/webhook/webhook.go)), and
  every installation is wired through the fan-out, even with one target
  ([`notify.go:28`](../internal/cli/notify.go)). So `ops.notify` always sees
  nil. RFC 0015 §5.3 describes a sequential fan-out; the code has changed
  since.
- **The webhook** posts the event as JSON with a 5-second client timeout and
  refuses redirects. Anything outside 2xx is an error that carries the status
  only in its message
  ([`webhook.go:128-158`](../internal/adapters/notify/webhook/webhook.go)).
- **`min_level` filters `check` events only.** Any other forwarded kind is
  delivered to every target
  ([`webhook.go:160-170`](../internal/adapters/notify/webhook/webhook.go)).
- **An event has no id.** It carries the kind, the time, the operation id and
  type, and the outcome ([`event.go:87-120`](../internal/events/event.go)).
  Two deliveries of one event cannot be told apart from two events of one
  operation.
- **The allowlist** forwards `operation.finished`, `check` and `update.staged`
  ([`ops.go:454-468`](../internal/lifecycle/ops/ops.go)). `check` events come
  from preflight inside an operation, not from `doctor`.
- **Each target has a `min_level`, default `error`** (RFC 0015 decision 11).
  Deduplication is left to the receiver (RFC 0015 §4).
- **Timers.** The backup, update and fleet timers are generated per product
  ([`systemd.go:471-475`](../internal/adapters/supervisor/systemd/systemd.go)),
  each switched by settings such as `backup.scheduled` and `update.schedule`
  ([`settings.go`](../internal/lifecycle/ops/settings.go)). A unit newly
  written is enabled, and enablement of an existing one is never re-asserted
  (RFC 0030 decision 1).

## 4. Goals / Non-goals

**Goals**

- A short outage of a target loses nothing that was sent during it.
- A run that was killed reaches a person within a day, without anyone typing a
  command.
- What `doctor` finds on a machine nobody logs into reaches whoever the
  notify targets name.
- No operation waits longer than it does today on an endpoint that is down.

**Non-goals**

- **Guaranteed delivery.** The outbox survives a reboot and is bounded. A
  downstream design still must not treat "notified" as done (RFC 0015 §5.3).
- **Deduplication, escalation and silencing.** These stay the receiver's (RFC
  0015 §4). The event id is what lets a receiver deduplicate.
- **New channels.** The webhook remains the only adapter (RFC 0015 decision 1).
- **A daemon.** morzer stays a one-shot CLI. The timer is what runs it again.

## 5. Design

### 5.1 Each target's outcome, and one retry

- **The adapter retries, since only it sees the status.** A connection error,
  a timeout, a 408, a 429 or a 5xx is tried once more after one second, while
  the 15-second aggregate deadline allows it. Nothing else is retried at once:
  a 401, 403 or 404 will not change in a second. The deadline is unchanged, so
  an operation never waits longer than today.
- **The adapter's error says whether a resend could succeed.** It is
  resendable for the retried cases and for 401, 403 and 404, which a fixed
  credential or URL can still deliver. It is final for every other 4xx, which
  refuses the content itself.
- **`Notifiers` returns the targets' errors joined**, each naming its target,
  instead of dropping them. `ops.notify` already logs what it is given, so a
  failed target is logged for the first time. The outbox (§5.2) is written
  from the same per-target errors.

### 5.2 The outbox

```text
<var>/manager/notify/outbox/<event-id>.<target-name>.json   0600
```

- An event whose target's error is resendable is written there, once per
  target, as the exact JSON the target would have received. A final error is
  logged and not kept. One
  target failing does not resend the event to the others.
- Nothing sends from it during an operation. It is sent by the scheduled
  `doctor` run (§5.4) and by `morzer notify flush`, oldest first. An entry is
  deleted once its target answers 2xx or a final error, and kept on a
  resendable one.
- It is bounded at 100 entries per target, dropping the oldest. `doctor`
  reports what it holds as `notify.outbox`: a warning naming the target and
  the count, and how many were dropped.
- `notify target remove` deletes that target's entries.
- An event holds what was already allowed to leave the machine (RFC 0015
  decision 4 and 5) and nothing else: no step output and no secret.

### 5.3 An id on every forwarded event

Every event that passes the allowlist gets an `id`, a ULID minted when it is
first sent. The webhook sends it in the body and as the `Idempotency-Key`
header. A redelivered event carries the same id, so a receiver can tell a
retry from a second event. The field is an addition to the payload, and a
receiver that ignores it is unaffected.

### 5.4 `<product>-doctor.timer`

```ini
# <product>-doctor.service
ExecStart=<manager> doctor --unattended --plain --log-format json --product <product>
```

- A timer and a oneshot service, generated like the backup timer. The
  `doctor.schedule` setting gives its `OnCalendar`, daily by default with a
  randomized delay. `doctor.scheduled=false` removes it.
- `doctor --unattended` sends the outbox, runs every check, and notifies one
  `doctor.finished` event. The event names the checks that warn or fail, with
  their remedies, and carries the level of the worst. The webhook applies each
  target's `min_level` to it as it does to `check`, so a target left at the
  default `error` does not hear about warnings. A run where every check passes
  notifies nothing.
- The operation holding the deployment lock is left out of `config.operations`,
  as RFC 0041 decision 19 does for staged inputs, so a backup running at the
  same time is not reported as unfinished.
- It exits as `doctor` does: 3 on a failed check. systemd records the failure.

### 5.5 Alternatives considered

- **Retry harder, inline.** More attempts inside the deadline cover a longer
  blip, and nothing about a real outage. Each attempt also costs Ctrl-C
  latency (`ops.go:519-533`).
- **Send the outbox at the start of every operation.** That is quicker
  redelivery, but every operation then waits on a target that is down,
  which is exactly what the deadline exists to prevent.
- **Notify every `check` event from the scheduled run.** That is one message
  per failing check per day, where one event per run carries the same
  information.
- **A `doctor` systemd watchdog, or `OnFailure=`.** That is systemd-only, and
  says that something failed without saying what. The event says what, through
  the targets the operator already configured.

## 6. Tests

- A target answering 503 then 200 receives the event once, with one retry, and
  the outbox stays empty.
- A target that is down leaves one outbox entry. `notify flush` delivers it
  once the target answers, with the same `id` and `Idempotency-Key`, and
  deletes it.
- A 400 is not retried and not kept. A 401 is not retried at once, is kept,
  and is delivered by `notify flush` once the target accepts.
- A failing target is logged by name, where today nothing is.
- A `doctor.finished` at `warn` does not reach a target whose `min_level` is
  `error`.
- With two targets and one down, only that target's entry is written, and the
  other receives the event once.
- The outbox drops the oldest past 100 entries, and `notify.outbox` reports the
  count and the drops.
- `doctor --unattended` with a failing check sends one `doctor.finished` at
  `error`, and sends nothing when every check passes.
- An operation holding the lock is not reported as unfinished by the scheduled
  run. A dead one is.
- The generated units: the doctor timer is present by default, absent with
  `doctor.scheduled=false`, and its `OnCalendar` follows `doctor.schedule`.
- Every new event kind is classified in `forwardedKinds` (RFC 0015 decision 4's
  test).

## 7. Docs

- `operating/notifications.md`: delivery as it now is, at-least-once for what
  the outbox holds and best-effort otherwise; the `id` and `Idempotency-Key`;
  `notify flush`; the scheduled `doctor`. The sentence "a notification not
  delivered is not delivered" is replaced, not appended to.
- `reference/commands.md#doctor`: `--unattended`, and the `notify.outbox`
  check.
- `reference/parameters.md`: `doctor.scheduled` and `doctor.schedule`.

## 8. Out of scope

- **A notification for a killed run at the moment it dies.** Nothing runs then.
  The scheduled `doctor` is how it is found, within a day by default.
- **Deduplication across scheduled runs.** A check that fails every day
  notifies every day. Decision 5 asks whether to notify only on change.
- **Retrying `update.staged` on the update timer's own tick.** It goes through
  the same outbox as everything else.

## 9. Risks

- **At-least-once will be read as exactly-once.** The `id` is the answer, and
  the docs say it outright. A receiver that ignores it posts a duplicate after
  a redelivery.
- **A daily message nobody acts on teaches people to mute the channel.** The
  default `min_level` of `error` keeps warnings out, as RFC 0015 decision 11
  intended. Decision 5 is the remaining lever.
- **The outbox holds deployment events on disk.** They are what the journal
  already holds, mode 0600 under the manager's directory, and they are bounded.

## 10. Unresolved questions

None. The author ruled §11's `OPEN` rows 2026-10-31.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | **Refines RFC 0015 decision 6.** The webhook adapter retries once after one second on a connection error, a timeout, a 408, a 429 or a 5xx, inside the unchanged 15-second aggregate deadline. Its error says whether a resend could succeed: yes for those and for 401, 403 and 404, no for any other 4xx.
| 2 | `LOCKED` | **Whether to keep what did not arrive.** Recommended: an outbox per target under `<var>/manager/notify/outbox/`, bounded at 100 entries, sent by the scheduled `doctor` and `notify flush`, never during an operation. Alternative: the retry alone, keeping RFC 0015's "no persistence". That covers a blip and loses everything sent during an outage. Was `OPEN`; ruled by the author 2026-10-31: as recommended. |
| 3 | `ASSUMED` | Every forwarded event carries an `id`, a ULID, in the body and as `Idempotency-Key`, kept across redeliveries. It is an addition to the payload. |
| 4 | `ASSUMED` | **Whether the doctor timer is on by default.** Recommended: on whenever at least one notify target is configured, and absent otherwise, since a scheduled `doctor` with nobody to tell writes only to the systemd journal. `doctor.scheduled=false` removes it. Alternative: off until an operator sets `doctor.scheduled=true`. That is quieter, and a killed run then reaches nobody by default. Was `OPEN`; ruled by the author 2026-10-31: as recommended. |
| 5 | `ASSUMED` | **How often an unchanged failure notifies.** Recommended: every scheduled run while a check warns or fails, leaving silencing to the receiver as RFC 0015 §4 does. Alternative: only when the set of non-ok checks changes from the last scheduled run, which needs a small state file and can hide a failure nobody saw the first time. Was `OPEN`; ruled by the author 2026-10-31: as recommended. |
| 6 | `ASSUMED` | `doctor --unattended` sends one `doctor.finished` event per run, naming the checks that warn or fail at the level of the worst. It is a new kind, forwarded under RFC 0015 decision 4's allowlist. A run where every check passes sends nothing. It carries the worst level, and the webhook applies `min_level` to it as to `check`. |
| 7 | `ASSUMED` | The scheduled run leaves the operation holding the deployment lock out of `config.operations`, as RFC 0041 decision 19 does for staged inputs. |
| 8 | `ASSUMED` | `doctor.schedule` is an `OnCalendar` expression, daily by default with a randomized delay, validated as `backup.schedule` is. The unit follows RFC 0030 decision 1: enabled when first written, and never re-enabled once it exists. |
| 9 | `ASSUMED` | `Notifiers` returns the targets' errors joined, each naming its target, so `ops.notify` logs a failed target. Today the fan-out drops them and the webhook logs nothing, so a failed delivery is logged nowhere (§3). Part of P1. |

## 12. Phasing

- **P1**: §5.1 and §5.3: each target's outcome logged, the retry, and the id.
  Small, and useful alone.
- **P2**: §5.2 and §5.4, the outbox and the doctor timer, as ruled. They land
  together, because the timer is what sends the outbox.
- Nothing is demand-gated.
