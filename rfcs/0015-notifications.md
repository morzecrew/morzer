# RFC 0015 — Notifications

- **Status:** 📝 Draft — design proposed
- **Scope:** Implements `ports.Notifier`, a port that has existed since the
  lifecycle layer was written and has **no adapter, no wiring and no
  configuration** — so every `d.notify(...)` call in the codebase returns on a
  nil check. Adds a webhook adapter, an installation-side target list mirroring
  [0009](0009-backup-targets.md)'s backup targets, an **allowlist** of which
  event kinds may leave the machine, and the missing call sites — `backup` and
  `restore` currently publish nothing, and `backup` is the one operation that
  already runs unattended. Covers delivery semantics, credential handling and the
  redaction question that sending events off-machine raises. Does **not** add
  Slack, email or PagerDuty adapters, does not add an event bus consumer beyond
  the notifier, and does not make delivery reliable — at-most-once is a decision,
  not an omission.
- **Related:** [`internal/ports/notify.go`](../internal/ports/notify.go) (the
  port) · [`internal/events/event.go`](../internal/events/event.go) (the payload)
  · [`internal/lifecycle/ops/ops.go`](../internal/lifecycle/ops/ops.go)
  (`notify`) · [0009](0009-backup-targets.md) (the target-list shape this
  mirrors, and the failed-push case that currently tells nobody) ·
  [0008](0008-test-coverage-program.md) (which found the redaction handler
  leaking non-string values) ·
  [`internal/adapters/supervisor/systemd/systemd.go`](../internal/adapters/supervisor/systemd/systemd.go)
  (the backup timer that already runs unattended) ·
  [0016](0016-update-checking-and-unattended-updates.md) (the consumer that makes
  this a prerequisite rather than a nicety)

---

## 1. Summary

`ports.Notifier` is a fully specified port with zero implementations. Four
operations call it and it does nothing, every time, on every machine. Meanwhile
the one operation that genuinely runs with nobody watching — the nightly backup,
scheduled by a systemd timer this repository generates — does not call it at
all.

This RFC ships a webhook adapter, configures it the way backup targets are
configured, and decides the two things that make sending events off-machine
different from printing them: **which** kinds may leave, and what happens when
delivery fails.

## 2. Motivation

**A port with no adapter is a promise the codebase makes to itself.**
[`notify.go:16`](../internal/ports/notify.go) defines `Notifier`;
[`ops.go`](../internal/lifecycle/ops/ops.go) holds a `Deps.Notifier` field; four
operations publish a finish event to it —
[`apply.go:87`](../internal/lifecycle/ops/apply.go),
[`update.go:119`](../internal/lifecycle/ops/update.go),
[`rollback.go:143`](../internal/lifecycle/ops/rollback.go),
[`config.go:166`](../internal/lifecycle/ops/config.go). Nothing anywhere
constructs a `Notifier`, and nothing sets the field, so
[`ops.go:224`](../internal/lifecycle/ops/ops.go) returns on `if d.Notifier == nil`
on every call. The port's careful failure semantics — "an update must not fail
because a webhook endpoint was down" — describe behaviour that has never run.

**The operation that most needs it is the one that does not call it.**
`backup` has no `d.notify` call at all, and neither does `restore`. The systemd
adapter already generates a backup timer
([`systemd.go:312`](../internal/adapters/supervisor/systemd/systemd.go)) running
`OnCalendar` at 02:30 with `Persistent=true`, so on any machine that ran `init`
with systemd available, a backup runs nightly with no human present. If it
fails, the evidence is a journal entry nobody reads. And
[0009](0009-backup-targets.md) made a failed push to a remote target **fail the
whole backup**, precisely so that "success" cannot mean "still only on the
doomed machine" — which produces a correct, loud failure that is delivered to an
empty room.

**Everything the manager knows, it can only tell whoever is looking.** `doctor`
computes overdue secret rotations, a render directory that is not tmpfs, and
disk pressure. All of it is reported to the terminal that invoked it.

**And it is a hard prerequisite for [0016](0016-update-checking-and-unattended-updates.md).**
An unattended update whose failure mode is "a human must decide whether to
restore" is only defensible if a human is told. That RFC should be gated on this
one rather than inventing a channel of its own.

## 3. Current state

Verified against `86da400`.

| Fact | Where |
| --- | --- |
| `Notifier` is defined, with `Name()` and `Notify(ctx, Event)` | [`notify.go:16`](../internal/ports/notify.go) |
| `Notifiers` fan-out type exists, so several targets need no lifecycle change | [`notify.go:26`](../internal/ports/notify.go) |
| **No implementation exists.** Outside the port file, the only mention in the tree is the `Deps.Notifier` field | `grep ports.Notifier` — one hit |
| Exactly four operations notify — `apply`, `update`, `rollback`, `config` | [`apply.go:87`](../internal/lifecycle/ops/apply.go), [`update.go:119`](../internal/lifecycle/ops/update.go), [`rollback.go:143`](../internal/lifecycle/ops/rollback.go), [`config.go:166`](../internal/lifecycle/ops/config.go) |
| `backup` and `restore` — both in `backup.go` — have no call site, and neither do `init`, `doctor`, `recovery` or `targets` | [`backup.go`](../internal/lifecycle/ops/backup.go) (`Backup`, `Restore` at :583) |
| A backup timer is generated and enabled where systemd is available | [`systemd.go:312`](../internal/adapters/supervisor/systemd/systemd.go), `DefaultBackupSchedule = "*-*-* 02:30:00"` |
| `Event` is a flat struct; `Kind` values are "stable: JSONL event streams are a monitoring surface" | [`event.go:18`](../internal/events/event.go) |
| `KindStepOutput` carries a line of **raw subprocess output** | [`event.go:31`](../internal/events/event.go) |
| "Events carry no secrets. The engine redacts before publishing" | [`event.go:58`](../internal/events/event.go) |
| That redaction has been wrong once: the handler claimed to scrub anything that stringifies while handling only `string` and `error`, so a `Stringer` printed its secret in full | [0008 §12](0008-test-coverage-program.md) |
| Backup targets are configured as a list of `{url, credentials}` where `credentials` names a **secret**, not a value, so the operator-facing file and `doctor` output carry no keys | [`installation.go`](../internal/domain/installation.go) (`BackupTargetConfig`) |

The last two rows are the design constraints. The first says the "no secrets"
claim is enforced by code that has already been defective. The second is the
shape to copy, byte for byte, the way [0009](0009-backup-targets.md) copied the
release-source registry rather than inventing a second one.

## 4. Goals / Non-goals

**Goals**

- One working `Notifier` adapter, so the port stops being aspirational.
- Notify on the operations that run unattended, which today means `backup`.
- Make it impossible for a high-risk event kind to be forwarded by accident.
- Keep credentials out of the operator-facing installation file, as
  [0009](0009-backup-targets.md) does.

**Non-goals**

- **Chat-service adapters.** Slack, Teams and Discord all accept an incoming
  webhook; the difference is payload shape, not transport. If someone needs
  Slack's exact JSON, that is a template, not an adapter.
- **Reliable delivery.** §5.3 decides at-most-once explicitly. A queue that
  survives reboots is a different component with different failure modes.
- **A general event export.** `--log-format json` already emits the full JSONL
  stream for a machine that wants everything. This is for the events a human
  should be interrupted by.
- **Alerting policy.** Deduplication, escalation and silencing belong to
  whatever receives the webhook.

## 5. Design

### 5.1 Configuration, mirroring backup targets

```yaml
# installation.yaml
notify:
  targets:
    - url: https://hooks.example/morzer
      credentials: notify-webhook      # a secret name, not a value
```

Same two fields, same reasoning, same `Notifiers` fan-out already on the port.
The credential is a secret **name** because this file is a report an operator
reads and `doctor` prints, and a bearer token in it is a bearer token in every
support ticket — [0009](0009-backup-targets.md)'s wording, and it applies here
unchanged.

`credentials`, when set, names a secret holding a credential document. The
webhook adapter reads one field from it, a header value, so both
`Authorization: Bearer …` and a vendor-specific signing header are expressible
without a new schema per service.

Only `https` is accepted. A plaintext `http` webhook would carry operational
detail about a deployment across an unencrypted hop, and the release-source
parser already refuses plaintext for the same reason
([`source.go`](../internal/ports/source.go), `ParseRef`).

**This bumps `InstallationSchemaVersion` to 4**, and the reasoning is already
written down: version 3 exists because `backup.targets` arrived, and an older
manager reading a newer state file "sees no targets, takes a backup, reports
success, and leaves it on the machine the operator configured a target to
survive" ([`installation.go:17-22`](../internal/domain/installation.go)). A
`notify` block has the identical shape — an older binary sees no targets, runs
an operation, reports success, and the operator it was supposed to interrupt is
never told. Same class, same remedy: a refusal naming the manager version.

### 5.2 What may leave the machine — an allowlist

Not every kind. The forwarded set is:

| Kind | Why |
| --- | --- |
| `operation.finished` | The outcome. This is the event a human wants. |
| `check` at `warn` or `error` | `doctor`'s findings — the things that are wrong and nobody has looked at. |

Everything else — `step.started`, `step.progress`, `step.finished`, `plan`,
`message`, and above all `step.output` — is **not** forwarded, and the mechanism
is an allowlist rather than a denylist so that a `Kind` added later is not
forwarded until someone decides it should be.

`step.output` is the one worth naming. It carries raw subprocess output
([`event.go:31`](../internal/events/event.go)): hook stdout, `docker compose`
stderr, whatever a vendor's migration script prints. The engine's claim is that
events carry no secrets, and that claim is enforced by a redaction handler that
[0008](0008-test-coverage-program.md) found silently leaking any value that was
not a `string` or an `error`. Forwarding the highest-volume, least-structured,
vendor-controlled kind to a third-party endpoint is where that class of defect
stops being a local log problem.

This is a deliberate asymmetry: the terminal and the JSONL log get everything,
the network gets two kinds.

### 5.3 Delivery: at-most-once, bounded, and honest about it

The port already fixes the important half — a notifier failure never changes the
operation outcome, and is logged and dropped
([`notify.go:12-14`](../internal/ports/notify.go)). This RFC fixes the rest:

- **Synchronous, with a short timeout** (5s, per target). The allowlist means
  roughly one event per operation, so the latency is bounded and the code has no
  queue, no goroutine lifetime and no shutdown ordering.
- **No retries.** A webhook that was down means the operator was not told.
- **No persistence.** A notification not delivered is not delivered.

The last two are uncomfortable and are stated as decisions rather than left
implicit, because **a downstream design must not treat "notified" as a
guarantee.** [0016](0016-update-checking-and-unattended-updates.md) in
particular: an unattended update may not rely on the operator having received a
message. The durable record is the journal, which is already written and already
survives; the notification is how a human learns to go and read it.

Adding retries later is compatible with all of this. Adding them now would mean
deciding what happens when an operation finishes and its notification is still
retrying, which is a shutdown-ordering question this design does not need to
answer.

#### Alternatives considered

**Asynchronous with a bounded queue.** Rejected for now: it buys the ability to
retry without extending the operation, at the cost of a component that outlives
the operation and must be drained before exit. The manager is a one-shot CLI;
`apply` runs as `Type=oneshot` under systemd
([`systemd.go:275`](../internal/adapters/supervisor/systemd/systemd.go)) and
exits. A background sender in a process that is about to exit is a queue that
loses messages with extra steps.

**Reuse the event bus and subscribe a notifier to it.** Rejected: the bus feeds
presenters, which [0002](0002-rich-terminal-renderer.md) deliberately gave no
back-channel and a contained-panic policy. A subscriber that makes network calls
has different failure semantics from one that draws a spinner, and the `Notifier`
port already exists to keep them apart.

### 5.4 The missing call sites

`backup` and `restore` gain `d.notify(ctx, events.OperationFinished(...))`,
matching the four that have it. `backup` is the point of this RFC: it is the
only operation with a generated timer, so it is the only one that already runs
with nobody watching.

A failed backup and a failed *push* of a good backup must be distinguishable in
the payload — [0009](0009-backup-targets.md) makes the second fail the whole
operation, and "the backup itself was fine but it is still only on this machine"
is a different message to a human than "no backup exists". The existing
`Status` and `Message` fields carry it; the test asserts they differ.

### 5.5 Redaction, and not double-checking it

The tempting move is to redact again in the adapter. That is the wrong instinct:
a second scrubber hides defects in the first, and the first is the one every
other consumer relies on.

Instead, the allowlist does the structural work (§5.2), and the test does the
rest: for every allowlisted kind, run an operation with a registered secret in
scope and assert the delivered payload contains none of its value. That is a
test of the engine's claim, delivered at the boundary where the claim's cost is
highest — which is the shape [0008](0008-test-coverage-program.md) argues for
throughout.

## 6. Tests

- **A down endpoint does not fail the operation** — the port's central promise,
  currently untestable because there is nothing to fail. Verified-red by
  construction: it cannot pass before an adapter exists.
- **The allowlist**, driven by every `Kind` the events package defines rather
  than a hand-written list, so a new kind fails the test until it is classified.
  A hand-maintained list here would silently stop covering the thing it exists
  to cover.
- **No secrets in a delivered payload**, per §5.5, against a real registered
  secret rather than a fixture string.
- **The credential comes from the secret state**, and the installation file and
  `doctor` output contain no token — the same assertion
  [0009](0009-backup-targets.md) makes for backup targets.
- **`http://` is refused** at configuration time, not at delivery time.
- **A failed push is distinguishable from a failed backup** in the payload.
- **Several targets each receive the event, and one failing does not stop the
  others** — `Notifiers` fans out today with no implementation to fan out to.

## 7. Docs

- A new `operating/notifications.md`: configuring a target, what is sent and
  what is deliberately not, and the at-most-once property stated plainly rather
  than implied. An operator who believes notifications are reliable will build a
  process on them.
- `reference/installation-commands.md` and the installation-file reference gain
  the `notify` block — the docs-drift gate reads these out of the source.
- `explanation/secrets.md`: the webhook credential joins the list of things that
  live in the secret state rather than the installation file, and therefore
  travels in an `installation export`.

## 8. Out of scope

- **Notifying on `doctor` runs that nobody scheduled.** This RFC forwards
  `check` events when `doctor` runs; it does not add a `doctor` timer. That is a
  natural sibling and belongs with whatever decides scheduling policy —
  [0016](0016-update-checking-and-unattended-updates.md) has to generate a timer
  anyway.
- **Templating the payload.** Sending morzer's `Event` JSON keeps the adapter
  trivial and the receiver's job explicit. A service that wants a different
  shape gets a two-line receiver. Reopens if two vendors independently write the
  same shim.
- **Delivery receipts or a `doctor` check for "this target has never
  succeeded".** Genuinely useful and it needs state — a last-success timestamp
  per target — which is a schema bump for a diagnostic. Deferred until someone
  has been bitten.
- **Rate limiting or deduplication.** One event per operation; the receiver owns
  the rest.

## 9. Risks

- **The allowlist will be widened by whoever wants more detail.** The pressure
  is real — the first time a webhook says "update failed" and the operator has
  to SSH in to find out why, someone will propose forwarding `step.output`. The
  mitigation is that this RFC records *why* not, so the widening is a decision
  rather than a diff.
- **A notification is the wrong place to learn a secret leaked.** If redaction
  regresses, the leak now goes to a third party. §5.5's test is the guard; it is
  a test, and tests can be deleted.
- **At-most-once will be read as reliable.** Documentation is the only mitigation
  and it is a weak one. The stronger mitigation is
  [0016](0016-update-checking-and-unattended-updates.md) not depending on
  delivery, which this RFC states as a constraint on that one.
- **Webhook URLs are themselves credentials for many services.** A Slack
  incoming-webhook URL is a bearer token spelled as a path. Storing it in the
  `url` field puts it in the operator-facing file — the exact thing the
  `credentials` field exists to avoid. This is unresolved question 1.
- **A misconfigured target adds 5s to every operation.** Bounded, but a human at
  a terminal will notice a `config set` that pauses. Acceptable; worth
  documenting.

## 10. Unresolved questions

1. **Should the whole `url` be allowed to live in a secret?** Some services put
   the credential in the path, defeating the value/name split. Options: accept a
   secret name in place of a URL; or keep the URL in the file and document that a
   path-embedded token is the operator's risk. Leaning toward the first — it
   costs one field and closes the hole properly — but it makes `doctor`'s output
   less useful, since it can then name only the target and not where it points.
2. **Does a `check` event at `warn` deserve a notification, or only `error`?**
   Warnings include "a secret rotation is overdue", which is exactly what a
   forgotten machine needs to be told; they also fire on every `doctor` run
   until fixed, which is how alerting fatigue starts. Deduplication is out of
   scope, so this may need to be `error`-only until it is not.
3. Should the notifier be constructed at all when no targets are configured, or
   should `Deps.Notifier` stay nil? Nil keeps the existing no-op path exercised;
   an empty `Notifiers{}` keeps the code path uniform. Implementation may settle
   it.

## 11. Decisions

| # | Decision | Rationale and consequence |
| --- | --- | --- |
| 1 | One **webhook** adapter, no service-specific ones | Every chat service accepts an incoming webhook; the differences are payload shape, which is a receiver's problem. |
| 2 | Configuration mirrors [0009](0009-backup-targets.md)'s backup targets exactly — a list of `{url, credentials}`, credential by **name** | A second shape for the same idea is how a codebase grows two of everything. Keeps tokens out of the operator-facing file and out of `doctor` output. |
| 3 | Only `https` targets | Matches `ParseRef`'s refusal of plaintext release sources; the payload describes a deployment. |
| 4 | An **allowlist** of forwarded kinds: `operation.finished`, and `check` at warn/error | Fail-closed against kinds added later. Consequence: a new `Kind` is silently not forwarded until classified, and the test in §6 is what makes that visible rather than silent. |
| 5 | `step.output` is **never** forwarded | It is raw vendor-controlled subprocess output, and the "events carry no secrets" claim rests on a handler [0008](0008-test-coverage-program.md) already found defective once. Consequence: a webhook alone will not tell an operator *why* something failed, and that is the trade. |
| 6 | Delivery is **at-most-once**: synchronous, 5s timeout, no retry, no persistence | Keeps the adapter free of queue lifetime and shutdown ordering in a one-shot CLI. Consequence, and it binds another RFC: [0016](0016-update-checking-and-unattended-updates.md) may not treat "notified" as a precondition — the journal is the record. |
| 7 | `backup` and `restore` gain notify call sites | `backup` is the only operation with a generated timer, so it is the only one that already runs unattended — and it is the one with no call site today. |
| 8 | The adapter does **not** re-redact | A second scrubber hides defects in the first. The guard is a test asserting no secret appears in a delivered payload, at the boundary where a leak costs most. |
| 9 | `InstallationSchemaVersion` bumps to **4** | Identical in shape to the bump to 3: an older manager reads a newer state file, sees no `notify` targets, reports success, and the operator who configured a way to be told is never told. Consequence: an older binary refuses the state file naming its own version, rather than silently running without notifications. |

## 12. Phasing

Single phase; it is one adapter, one config block, two call sites and a test
family. Splitting it would produce a port with an adapter nothing configures,
which is the situation this RFC exists to end.

Gated on nothing. [0016](0016-update-checking-and-unattended-updates.md) is
gated on **this** — its P3 should not ship before this does, because an
unattended update that can require a human decision needs a way to ask for one.
