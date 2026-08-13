---
title: The fleet view
icon: lucide/network
summary: Each installation publishes one small row at a stable key, and a stateless command reads them back — what the row carries, what it never carries, and what the reader cannot yet tell you
---

# The fleet view

An operator or vendor with twelve deployments wants one screen: what version
each is on, whether it is up, and which one stopped reporting three weeks ago.

The obvious way to build that is a control plane — a privileged agent on every
machine, inbound connectivity to hosts whose selling point is that they are on
someone else's network, and a second source of truth about installation state
that will disagree with the first exactly when it matters.

morzer does not do that, because the valuable half does not need it. "What is
running where" is a **read**, and reads need a place to put facts and a way to
read facts back. Each installation writes one small document to a target it
already uses; `morzer fleet ls` lists them.

Nothing polls a managed machine. Nothing listens on one. And there is no command
here that acts on one — see [what this will never do](#what-this-will-never-do).

## Publishing

```sh
morzer fleet publish
```

The row goes to the same targets this installation keeps its backups on, at a
key derived from the product and the installation id:

```text
fleet/<product>/<installation-id>/status.json
fleet/<product>/<installation-id>/status.json.minisig
```

The signature is detached and covers the bytes as published, so checking one is
`minisign -Vm status.json -P <key>` — the same gesture as an
[attestation](attestations.md), not a second thing to learn.

Nothing is scheduled. Run it from cron or a systemd timer; it is safe to repeat.

| Flag | What it does |
| --- | --- |
| `--target` | publish to one target instead of all of the installation's |
| `--credentials-file` | a YAML credential document, for a target whose keys are not in the secret store |
| `--dry-run` | print the row that would be published and write nothing |
| `--force` | replace whatever is at the key, skipping the ordering check below |

`--dry-run` prints the document itself rather than a description of it, which
is the honest way to answer "what would leave this machine".

### A failed publish fails nothing

A row that did not leave is a gap in a *view* whose subject — the deployment —
is fine, and this machine still knows everything the row would have said. So an
unreachable target is reported per target and the command carries on. It exits
non-zero when one did not answer, so a cron job finds out.

This is the same asymmetry [attestations](attestations.md) have, and the
opposite of what a [backup](../operating/backups.md) does. A backup that did not
leave the machine is a data-loss risk that has already materialised; a row that
did not leave is bookkeeping.

### Replacing in place, without going backwards

The key is stable and every publish replaces what is there. So a slow publisher
finishing after a fast one would otherwise install stale state as current — a
real hazard once a timer runs beside an operator typing `morzer fleet publish`.

Before writing, the publisher reads what is at the key and declines to replace a
row that is **newer**, or one written by a **newer manager**. `--force`
overrides both.

That check is deliberately best effort. The credential a managed machine holds
for a shared bucket should be write-only and prefix-scoped, and a write-only
credential cannot perform this read. Refusing to publish would make the safer
credential the one that breaks the feature, so the row is published and the
report says the check did not happen:

```sh
morzer fleet publish --json | jq -r '.data.targets[] | select(.unchecked) | .url'
```

## Reading

```sh
morzer fleet ls s3://bucket/prefix
```

```text
2 row(s) on file:///tmp/fleet-target
  PRODUCT  VERSION  HEALTH  PUBLISHED  DRIFT  SIGNATURE
  demo     1.3.0    2/2 up  0s ago     none   signed
  web      1.0.0    3/3 up  0s ago     none   signed

  a row is called stale once it is older than 24h0m0s

  no roster was given, so no row below is authenticated and no absent installation can be shown;
  both need the roster that binds an installation id to a public key
```

Two installations, on two machines, read from one of them — which is the whole
feature. Neither machine knows anything about the other; both wrote a document
to the same place.

Stateless: no daemon, no database, no cache, and nothing on this machine is read
or written. It runs on a laptop. With no target URL, the installation on this
machine names its targets.

| Flag | What it does |
| --- | --- |
| `--stale-after` | call a row stale once it is this old (default 24h; a negative value judges nothing) |
| `--credentials-file` | a YAML credential document for the target |

`--json` emits the same data, and that is where anyone wanting a dashboard
starts. A static site generated from these objects is a fine thing to build; it
is not in this repository.

### Nothing is hidden

A row that will not parse, one written by a newer manager, one whose object
cannot be fetched, or one sitting at a key that names a *different* installation
is printed carrying that problem. `fleet ls` exits non-zero when there is one.

A view that quietly dropped what it could not read would report health it never
observed, which is worse than no view at all.

### What `not checked` means

Two columns can say `not checked`, and it is a different statement from a zero.

- **health** — the container runtime did not answer when the row was published.
  A deployment that is genuinely down publishes `0/3 up`. The distinction is
  carried in the payload as an absent count rather than a zero, so a machine
  whose daemon is wedged never looks like a machine whose services stopped.
- **drift** — no release is installed, or its configuration could not be
  rendered.

The `not checked` case is the one this design exists to make visible: reading
installation state needs no Docker call, so a machine whose runtime is the
problem can still publish a row saying exactly that.

## What the row carries

| Field | What it is |
| --- | --- |
| `schema` | the payload version, stated rather than inferred |
| `bound` | what a signature over this row does and does not prove |
| `product`, `installation_id` | which installation this is |
| `mode` | `dev` on a sandbox, absent on a production machine |
| `version` | the release currently installed |
| `manager_version` | which manager published it |
| `health` | services and running counts, or why they were not taken |
| `drift` | how many configuration targets differ — a count, never a diff |
| `last_operation` | id, kind, outcome and when |
| `signing_key` | the public half, for comparing a fingerprint by eye |
| `published_at` | when this row was written |

## What the row never carries

Parameter values, hostnames, container logs, configuration content, and anything
the [support bundle](support-bundle.md) refuses. The payload is deliberately
smaller than a support bundle and smaller than an attestation: it is a *row*,
not a record.

Drift is the clearest case. It is published as a count of targets that differ
and never as the diff, because the number is the signal an operator acts on and
the files are on the machine for whoever is allowed to look at them. A shared
bucket holding twelve machines' configuration would be exactly the artifact this
design exists not to produce.

## What this reader cannot yet tell you

Two things, and they have one cause. `morzer fleet ls` prints them on every run
rather than leaving them to this page.

**It cannot authenticate a row.** The `signature` column says a signature is
*there* — never that it checks out. This is not laziness about verification: the
key a row names is part of the row's own claim, and rows from several machines
share one bucket. A machine overwriting its neighbour's row rewrites the
payload, the embedded key and the signature together, and the result verifies
perfectly against itself. Checking a row against its own key therefore
establishes nothing against the only attacker this design has, which is why it
is not done at all rather than done and captioned.

**It cannot show an installation that stopped publishing.** An object that was
never written cannot announce itself, and listing a prefix shows you exactly the
population that is fine — which is the failure mode of every fleet view ever
built.

Both need the same thing: a roster naming the installations you expect and the
public key each one signs with. That is the next phase, and until it lands a
complete-looking table is not a complete one.

## What this will never do

There is no `fleet update`, no `fleet exec` and no fan-out. Updating ten
machines means ten `morzer update`s over ssh, and the fact that this is tedious
is the point: it keeps the destructive path per-machine, per-decision, and
locally journalled.

A console that can act needs to know who may act, which is an authorisation
model, which is a control plane. Every user of the fleet view will ask for this
within a week. The refusal is the product.

## A sandbox must not publish into a production prefix

An installation imported from a production export keeps its id — deliberately,
because restore checks against it — and a recovery export carries backup targets
and their credentials. So a sandbox rebuilt from a production export would hold
the customer's bucket, the customer's credentials, and a matching id.

`morzer installation import` already drops backup targets for exactly this
reason. Until fleet targets join that same drop list, **do not point a
`--mode dev` installation at a production target by hand.**
