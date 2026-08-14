---
title: Unattended updates
icon: lucide/timer
summary: What it costs, what the gate promises, and why a machine can be a sandbox but never become one
---

# Unattended updates

**Start with what this costs.** Turning it on hands your vendor unattended root
on this machine: hooks run as root, and an update runs the incoming release's
migration hook. A compromise of the vendor's registry or signing key becomes
root here with no human in the path. Today, the operator pasting a command is a
control — a weak one, but a real one — and this removes it.

That is the trade. What you get back is that the fetch, the verification and the
downtime happen without anyone being awake for them, on a machine where the
release has declared that a failure cannot leave the database somewhere the
previous release cannot read.

## What the gate promises

Not "no human will be required". An update can still stop and page someone: a
migration hook that exits non-zero, a health check that never passes, a converge
the engine cannot compensate. None of that is inspected here, and all of it
already ends in [requires-manual-intervention](../reference/exit-codes.md).

What it promises is narrower and is the one that matters at 03:00: **the failure
will not be the unrecoverable kind.** A failed unattended update that
compensates back to the previous release is an incident report. One that needs a
restore decision is the thing morzer refuses to make automatically, awake or
not.

## The gate

An update installs itself only if **all** of these hold:

| Condition | Where it comes from |
| --- | --- |
| The compatibility check passes | The same gate your own `update` runs |
| The release declares `rollback_safe: true` | The incoming manifest |
| The release declares `database_schema_produces` | The incoming manifest; **absent means never** |
| The installed release can read that schema | Its `database_schema_max` |
| `policy.require_signature` is set, with a key | This installation |
| The pre-update backup is not disabled | This installation |

The middle three are one idea: *could this update end needing a database
restore?* They are the [rollback assessment](rolling-back.md) run **before** the
update instead of after one, over what the manifest declares rather than over
anything measured.

Anything that fails the gate is still fetched, verified, staged and notified.
Nothing is silently skipped, and the operator's remaining decision is only the
one that costs downtime.

## Turning it on

```sh
morzer config set update.check=true
morzer config set update.channel=oci://registry.example/demo/bundle:stable
morzer config set update.auto_apply=true
```

`update.auto_apply` is **refused** on an installation that does not require
signatures — at the moment you set it, not at the tick that would have acted on
it. A machine that accepts the setting and then refuses to act every night is
worse than one that refuses the setting: you would believe it was armed.

Setting a channel installs a `<product>-update.timer`; clearing it removes the
timer again.

### The maintenance window is an `OnCalendar` expression

The timer is an ordinary systemd timer, so a window is one line:

```ini
[Timer]
OnCalendar=Sun *-*-* 04:00:00
```

There is no separate maintenance-window setting, deliberately: systemd already
expresses this better than a configuration field would, including "the first
Sunday of the month" and "weekdays only".

`RandomizedDelaySec` spreads installations across a window so a vendor's
registry does not see every customer at the same second, and `Persistent=true`
catches up a tick missed while the machine was off.

### A tick that cannot take the lock exits 0

Your interactive `backup` or `apply` is in the way; the next tick is soon.
Queueing would make the start time unpredictable, and a failing unit every time
somebody deploys by hand is a unit whose alerts get muted.

A release left staged is also a **successful** tick. It is the configured
behaviour when the gate refuses, and a unit that failed every night because a
release is waiting would train you to ignore it.

## Dev mode: a machine that is a sandbox from birth

```sh
morzer init --mode dev --product demo --release ./bundle
```

A sandbox is a machine whose data is disposable and whose purpose is rehearsing
what will happen to a real one. It relaxes:

- **The recovery gate.** Auto-apply installs whatever the channel offers: there
  is nothing here to be unable to recover.
- **Retention.** It prunes old releases after every update, which a fast rebuild
  loop needs — one release directory per build, each with its own images.
- **`--skip-backup`**, without `--force`.
- **Prereleases.** They are admissible update candidates. On a production
  machine they are not: every development build carries a prerelease version,
  and a check answering "1.4.1-dev.7 is available" is a check nobody reads.

It relaxes **nothing about verification**. Signature checking, digest pinning
and `SHA256SUMS` completeness are unchanged, because the sandbox's entire value
is fidelity: every relaxation there reduces what a successful rehearsal proves,
and what you are rehearsing is a customer's install. Sign dev builds with a dev
key the sandbox pins — one `minisign -Sm` in CI, and it rehearses key handling
too.

### Mode is fixed when the installation is created

Not one-way. **No** way, through any of the manager's surfaces: not
`config set`, not `import`, not any command that writes installation state.

| Transition | What breaks | When you find out |
| --- | --- | --- |
| production → dev | Real data is immediately under relaxed rules | At once |
| dev → production | Untrusted history presented as trustworthy: `previous` was pruned and no pre-update backup was ever taken | During an incident |

The second is the quieter one and it lands when it costs most, which is why
neither direction is allowed.

**Promotion is backup → fresh `init` → restore.** That already works, and it is
the right amount of ceremony for a machine about to hold real data.

`installation import` is the other moment a mode is chosen, because import
reproduces an export wholesale:

```sh
morzer installation import ./demo.export.yaml --identity ~/recovery.key --mode dev
```

That is how you test a customer's backup on a sandbox. **It drops everything
that would let the sandbox act on production's infrastructure**, and this is not
optional: an import keeps the original installation id so a lost machine's
backups stay restorable, and an export carries credentials so a rebuilt machine
can reach what it needs to.

- **Backup targets.** A sandbox that kept them would push throwaway backups —
  and [fleet rows](../reference/fleet.md), which go to the same targets — into
  the customer's bucket under a matching id.
- **Notify targets.** A webhook URL is itself the credential, so a sandbox that
  kept them would page the customer's on-call about a machine that exists in
  order to be broken.

The drop is a [single list](../reference/fleet.md#a-sandbox-must-not-publish-into-a-production-prefix)
rather than a special case per hazard, and it is reported rather than silent.

Importing a dev export **as production is refused**. With no `--mode` at all, an
import reproduces whatever the export was — a lost sandbox comes back a sandbox.

### What is not claimed

`mode` is a field in a JSON file, and root can edit it. Nothing here is
tamper-evident, and the immutability above is a statement about the manager's
own surfaces. Defending one boolean against an operator who can equally edit the
recipient list, the backup targets or the installation id would be defending the
wrong thing; root on the machine is outside every threat model in this
documentation.

`status` and `doctor` mark a sandbox permanently and prominently — not as a
first-run notice, because the failure mode is a machine nobody remembers the
provenance of.

## For vendors

Declaring `database_schema_produces` is what opts your releases into this. See
[the manifest reference](../reference/manifest.md#database_schema_produces-is-what-opts-a-release-into-unattended-updates)
and [publishing](../authoring/publishing.md).

Omitting it is a valid choice, and it costs your customers nothing: their
machines still fetch, verify and stage your releases, and still tell them one is
waiting.
