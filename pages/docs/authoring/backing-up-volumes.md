---
title: Declaring volume consistency
icon: lucide/hard-drive
summary: Which of your project's volumes may be copied while it runs, and what claiming that commits you to
---

# Declaring volume consistency

Your backup hook dumps your database. The manager reads your project's named
volumes. Between them they cover the deployment — but only one of you knows
whether a volume can be read while your product is writing to it, and it is not
the manager.

That is what `backup.volumes` is for. It is a small declaration with a
disproportionate consequence, so this page is mostly about what you are
claiming.

## The default needs no declaration

Every named volume in your Compose project is captured. A volume you say nothing
about is captured **cold**: the manager stops the services that mount it, copies
it, and starts them again.

That is correct for every volume, and slow for some. If your product's
deployment is fine with a short stop during a nightly backup, you can ship
nothing at all and it will work.

## What `hot` means

```yaml
backup:
  volumes:
    uploads:    { consistency: hot }
    caddy_data: { consistency: hot }
    pgdata:     { consistency: exclude }
```

`consistency: hot` says: **a copy of this volume taken while my product is
running is a usable copy.**

Copying a live volume gives a *crash-consistent* copy — byte-for-byte what a
power cut would have left. So the question you are answering is: would my
product come up correctly from this volume if the machine had lost power at an
arbitrary instant?

| Usually `hot` | Never `hot` |
| --- | --- |
| Uploaded files written once and never modified | Anything with a write-ahead log — Postgres, MySQL, MongoDB, etcd |
| Generated thumbnails and other derived artifacts | A search index that is written incrementally |
| A certificate store written by an ACME client | A queue whose spool assumes ordered writes |
| Static assets extracted at build time | Anything you would not `kill -9` and restart |

The test that catches most of it: *if this volume is half-written, does my
product notice and repair it, or does it silently serve corrupt data?* The first
is `hot`. The second is not, however convenient it would be.

!!! warning "`hot` is a claim you make about your own product"

    It is recorded in every backup manifest taken under it. If an operator ever
    has to explain why a restore did not work, the manifest says which volumes
    were copied live and on whose word.

    When you are not sure, say nothing. The default is correct; it is only slow.

## What `exclude` means

`consistency: exclude` keeps the manager out of a volume entirely — nothing is
copied and nothing is restored.

This is the expected declaration for your database's storage. Your backup hook
already dumps it properly, and a second copy taken by other means is a copy
somebody could restore instead of the good one.

```yaml
backup:
  volumes:
    pgdata: { consistency: exclude }
```

Excluded volumes are reported to the operator — in the backup manifest and in
`morzer doctor` — so nobody is left believing a volume is covered when it is
not.

## What it costs the operator

Before you leave a volume undeclared, know what the default does on their
machine:

- Only the services that mount that volume are stopped, not the whole project.
- Every cold volume in one backup shares a single stop-and-start, so a project
  with four undeclared volumes has one downtime window, not four.
- The services come back up even if the copy fails.
- An operator can run `morzer backup --no-downtime`, which **skips** undeclared
  volumes rather than copying them live. Their nightly backup then quietly
  covers less than it looks like it does — which is the outcome your
  declarations exist to prevent.

That last point is the argument for declaring. A vendor who classifies their
volumes gives operators a fast backup that covers everything; a vendor who
declares nothing forces them to choose between downtime and coverage.

## Bind mounts

The manager never captures a bind mount, and there is no declaration that
changes it. A bind mount points at an arbitrary host path — it can be enormous,
it can be shared, it can be outside anything the manager manages.

If your product's data lives on a bind mount, it is not in any backup. Use a
named volume.

## Checking it

`morzer release verify` rejects a `consistency` that is not one of the three
values, so a typo fails at your terminal rather than turning into a volume
captured differently than you meant.

`morzer doctor` on a deployment running your release reports what is and is not
covered:

```
backup   ! 2 of 3 named volume(s) captured — pgdata excluded by the release
```

See also [backups](../operating/backups.md) for the operator's side of this, and
[hooks](hooks.md) for the half of the backup that stays yours.
