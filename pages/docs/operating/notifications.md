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

```yaml title="installation.yaml"
notify:
  targets:
    - url: https://hooks.example/morzer
      credentials: notify-webhook
      min_level: error
```

| Field | Meaning |
| --- | --- |
| `url` | The endpoint. Must be `https`. |
| `url_secret` | Names a secret holding the whole URL, for services where the URL *is* the credential. Exclusive with `url`. |
| `credentials` | Names a secret holding a small YAML document with `header` and `value`, sent with each request. |
| `min_level` | Lowest `doctor` severity this target receives: `warn` or `error`. Default `error`. |
| `name` | Label used in diagnostics. Required with `url_secret`, since nothing else is safe to print. |

Several targets each receive every event. One failing does not stop the others.

### When the URL is the credential

A Slack or Teams incoming-webhook URL is a bearer token spelled as a path.
Putting one in `url` writes it into a file you will paste into support tickets,
into `doctor` output, and into an `installation export` that travels beside a
recovery key.

```yaml
notify:
  targets:
    - name: chat
      url_secret: notify-chat-url
```

```sh
morzer secret set notify-chat-url
```

Diagnostics then name the target `chat` and never say where it points, which is
the trade: you lose "unreachable host x" and keep the token out of your logs.

## What is sent

Two kinds of event, and no others:

- `operation.finished` — the outcome of an `apply`, `update`, `rollback`,
  `config`, `backup` or `restore`, including **failures**.
- `check` — a `doctor` result at or above the target's `min_level`.

The payload is the manager's own event JSON, the same shape `--log-format json`
emits. A service wanting a different body gets a two-line receiver.

### What is not sent, and why

Step output is never forwarded. It carries raw hook stdout, `docker compose`
stderr, and whatever a vendor's migration script prints — the highest-volume,
least structured, least predictable thing the manager handles. The engine
redacts secrets before publishing any event, and that guarantee is worth more
when the blast radius of it being wrong is a local log rather than a third
party's servers.

Progress, plans and step narration are not forwarded either. They are what a
terminal is for.

## What you should not build on it

**Delivery is at-most-once.** One attempt per target, five seconds each, fifteen
seconds for the whole fan-out, no retries and no queue. A target that was down
means you were not told.

This is a deliberate choice rather than a gap: a queue that survives reboots is
a different component with different failure modes, and a one-shot CLI is a poor
host for one. The consequence is worth stating plainly — **the journal is the
record, and the notification is how a human learns to go and read it.** Do not
build a process that assumes every failure produced a message.

A notifier failure never changes an operation's outcome. An `update` does not
fail because a webhook was down, and you should not learn about a chat outage by
way of a rolled-back deployment.
