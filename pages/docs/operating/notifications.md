---
title: Notifications
icon: lucide/bell
summary: Reporting operation outcomes to a webhook, and what deliberately is not sent
---

# Notifications

The manager can post the outcome of an operation to an HTTPS endpoint. It is
off until you configure a target.

The case it exists for is the nightly backup. On any machine where `init` found
systemd, a backup runs on a timer with nobody watching — and
[a failed push fails the whole backup](backups.md), deliberately, so that
"success" cannot mean "still only on the doomed machine". Without a
notification, that correct and loud failure is a journal entry nobody reads.

## Configuring a target

```sh
morzer notify target add https://hooks.example/morzer
morzer notify target list
```

The target is checked when you add it, and nothing is sent to it: the URL must
be `https`, a password in it is refused, and any secret it names must be set
and well formed. Several targets each receive every event, and one failing does
not stop the others. `morzer notify target remove <url|name>` stops reporting
to one, and `morzer status` names the targets a machine reports to.

| Flag | Meaning |
| --- | --- |
| `[url]` | The endpoint. Must be `https`. |
| `--url-secret` | Names a secret holding the whole URL, for services where the URL *is* the credential. In place of `[url]`. |
| `--credentials` | Names a secret holding a small YAML document with `header` and `value`, sent with each request. |
| `--min-level` | Lowest `doctor` severity this target receives: `warn` or `error`. Default `error`. |
| `--name` | Label used in diagnostics. Required with `--url-secret`, since nothing else is safe to print. |

A header carrying a bearer token goes in a secret:

```sh
printf 'header: Authorization\nvalue: Bearer %s\n' "$TOKEN" | morzer secret set notify-webhook
morzer notify target add https://hooks.example/morzer --credentials notify-webhook
```

The targets are recorded in the installation, and `installation.yaml` lists
them; that file is a report the manager rewrites, so change them with these
commands rather than by editing it. If a secret a target reads is removed
later, the target is dropped when a command starts, and `morzer doctor` warns
about it by name until you fix the secret or remove the target.

### When the URL is the credential

A Slack or Teams incoming-webhook URL is a bearer token spelled as a path.
Putting one in `[url]` writes it into a file you will paste into support
tickets, into `doctor` output, and into an `installation export` that travels
beside a recovery key.

```sh
morzer secret set notify-chat-url
morzer notify target add --name chat --url-secret notify-chat-url
```

Diagnostics then name the target `chat` and never say where it points, which is
the trade: you lose "unreachable host x" and keep the token out of your logs.

## What is sent

Four kinds of event, and no others:

- `operation.finished` — the outcome of an `apply`, `update`, `rollback`,
  `config`, `backup` or `restore`, including **failures**.
- `doctor.finished` — what the [scheduled `doctor`](#the-scheduled-doctor)
  found, when a check warns or fails, at the target's `min_level` or above.
- `check` — a `doctor` result at or above the target's `min_level`.
- `update.staged` — a followed reference moved, and the release it points at
  was fetched and verified: the [update channel](unattended-updates.md), on a
  timer tick or `morzer update --stage`, or a reference passed to
  `update --stage`. It is sent when the release is staged, before anything decides whether it installs itself, and it is the one
  event here that is not an outcome: it may be a decision waiting for a person
  who was not at the terminal when the timer ran.

The payload is the manager's own event JSON, the same shape `--log-format json`
emits. A service wanting a different body gets a two-line receiver.

Each event sent to a target carries an `id`, such as `evt_01J…`, in the body and
as the `Idempotency-Key` header. A retried event carries the same `id`, so a
receiver that keeps the ones it has seen can drop the duplicate. A
`--credentials` document cannot name that header.

### What is not sent, and why

Step output is never forwarded. It carries raw hook stdout, `docker compose`
stderr, and whatever a vendor's migration script prints — the highest-volume,
least structured, least predictable thing the manager handles. The engine
redacts secrets before publishing any event, and that guarantee is worth more
when the blast radius of it being wrong is a local log rather than a third
party's servers.

Progress, plans and step narration are not forwarded either. They are what a
terminal is for.

## The scheduled doctor

Once a target is configured, a `<product>-doctor.timer` runs `doctor
--unattended` every morning, after the nightly backup and update ticks. It sends
what the [outbox](#delivery) holds, runs every check, and sends one
`doctor.finished` naming the checks that warn or fail, with their remedies, at
the level of the worst. A run where every check passes sends nothing. A target
left at the default `min_level` of `error` hears only about failures.

A backup running at the same moment is not reported as unfinished: `doctor`
leaves out the operation holding the deployment lock while it runs.

```sh
morzer config set doctor.schedule='Mon..Fri *-*-* 07:00:00'   # when it runs
morzer config set doctor.scheduled=false                      # no timer at all
```

The timer exists while a target does, and goes when the last one is removed.

## Delivery

Each target gets five seconds per attempt, and fifteen seconds cover the whole
fan-out. A target that did not answer, or answered 408, 429 or a 5xx, is tried
once more after a second, inside those fifteen.

An event a target did not accept, and could accept later, is kept in the
**outbox**: no answer, a 408, 429 or 5xx, a redirect, or a 401, 403 or 404,
which a fixed credential or URL gets past. Any other 4xx refuses the event
itself, and it is not kept. The scheduled `doctor` sends the outbox, and so
does `morzer notify flush`, oldest first; an entry goes once its target
answers. Nothing sends it during an operation, so an operation never waits on a
target that is down.

```sh
morzer notify flush      # sent 3; kept 2 for a target that is still down
```

A target keeps at most 100 entries, and past that its oldest are dropped.
`doctor` warns about what is waiting as `notify.outbox`, naming the target, the
count and how many were dropped. `notify target remove` deletes the target's
entries.

So delivery is at least once for what the outbox keeps, and best-effort for the
rest. A target can receive an event twice: an attempt may have arrived and its
answer been lost, or a send stopped between the answer and deleting the entry.
The `id` is how to tell. **The journal is still the record**, and a
notification is how a human learns to go and read it.

## What you should not build on it

**A run the operating system kills does not notify.** Delivery happens inside
the operation, so an ordinary failure — a hook exiting non-zero, a push that
could not reach its target, a health check that never passed — is reported
normally. What is not reported is a run terminated from outside: Ctrl-C, or
systemd's `TimeoutStartSec` firing on the backup timer.

That is a deliberate trade rather than an oversight. Sending the message anyway
would mean the manager keeping a connection open after it had been told to
stop, which leaves an operator's Ctrl-C looking wedged for as long as the
deadline allows. The journal records the interruption either way.

A notifier failure never changes an operation's outcome. An `update` does not
fail because a webhook was down, and you should not learn about a chat outage by
way of a rolled-back deployment. The failure is logged instead: a target that cannot
be reached or answers an error is a `notifier failed` warning naming it.
