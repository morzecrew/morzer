---
title: Several installations
icon: lucide/layers
summary: One machine can hold more than one installation — what they share, how to say which one you mean, and what to expect when they collide
---

# Several installations on one machine

A machine can run more than one installation, and always could. Every path is
keyed by the product name — `/etc/demo`, `/var/lib/demo`, `/run/demo`,
`/opt/demo` — the deployment lock lives under the product's own manager
directory, and the five systemd units are named per product. Two installations
never contend for anything the manager owns.

What they do share is the host: one Docker daemon, one port space, one
filesystem, one set of CPUs. morzer does not pretend otherwise. This page is
about making that sharing legible rather than preventing it.

## What is on this machine

```sh
morzer ls
```

```text
PRODUCT  RELEASE     MODE  UNITS  PATH
demo     1.4.0                 5  /etc/demo
sandbox  1.5.0-rc1   dev       0  /etc/sandbox
```

Read from the state files alone: no Docker call, no lock, no network. That is
deliberate — the moment somebody most needs to know what a machine is running is
the moment the daemon is wedged, and a listing that needed the daemon would be
unavailable exactly then.

`morzer installation list` is the same command under its long name.

| Column | What it is |
| --- | --- |
| `PRODUCT` | The name every path, unit and Compose project derives from |
| `RELEASE` | The version the state records as current, or `none` |
| `MODE` | `dev` for a sandbox; blank for a production installation |
| `UNITS` | How many systemd units this installation manages; `0` after `init --install-units=false` |
| `PATH` | Where its configuration lives |

`morzer ls --json` adds `schema_version` per row. It is absent from the table on
purpose: it matters on one day in a hundred, and a terminal's width is scarcer.

### An installation it cannot read

A row whose state will not load says `unreadable` and the reason is printed
underneath:

```text
PRODUCT  RELEASE     MODE  UNITS  PATH
demo     1.4.0                 5  /etc/demo
legacy   unreadable            5  /etc/legacy

✗ legacy
  installation state at /var/lib/legacy/manager/installation.json is invalid:
  installation was written by a newer manager (schema 9, this manager reads 5)
```

It is listed rather than skipped. The moment an installation's state breaks is
the moment it must not look absent — and the row carries nothing this manager
could not read, because a `mode` or a `release` reported out of a state file it
does not understand would be a guess presented as a fact.

### What is running

```sh
morzer ls --status
```

Adds a `SERVICES` column of running-out-of-total, at the cost of one runtime
query per installation. Each query is bounded at five seconds on its own, so one
unresponsive daemon costs that row and no other:

```text
PRODUCT  RELEASE  MODE  UNITS  SERVICES  PATH
demo     1.4.0            5    3/3       /etc/demo
sandbox  1.5.0-rc1  dev   0    unknown   /etc/sandbox

! sandbox
  services: timed out after 5s
```

The counts are read **without** taking the deployment lock. A listing that
blocked behind a running update would be useless exactly when somebody is
watching one; the price is that a row can be a moment out of date, and the
output says so.

## Saying which installation you mean

With one installation, nothing has to be said: the manager finds it. With two or
more, a command that acts on an installation refuses rather than guessing.

```text
$ morzer status
error: this machine has 2 installations, so --product is required
hint:  demo, sandbox — pass `--product demo`, or `--config /etc/demo/installation.yaml`; `morzer ls` lists them
```

Guessing is the one thing it must not do. An operator who typed `morzer update`
on the wrong deployment finds out during the downtime.

Three ways to say it, in precedence order:

| Source | Rank | Notes |
| --- | --- | --- |
| `--config <path>/installation.yaml` | 1 | Selects the layout the file sits in. This is what the generated systemd units pass. |
| `--product <name>` | 1 | The ordinary spelling. |
| `MORZER_PRODUCT` | 2 | For a shell session pinned to one installation. |
| Discovery | 3 | Used only when exactly one installation exists. |

`--config` and `--product` **share** rank 1, and passing both is refused rather
than resolved:

```text
$ morzer --config /etc/demo/installation.yaml --product sandbox status
error: --product sandbox and --config /etc/demo/installation.yaml name different installations
hint:  pass one or the other
```

The exception is agreement: if both name the same installation, the command
proceeds. Somebody who spelled one deployment two ways has said one thing twice.

`MORZER_PRODUCT` is *not* in that exclusion — a flag overrides it, which is what
makes it usable in the case it exists for:

```sh
export MORZER_PRODUCT=sandbox
morzer status                       # sandbox
morzer --product demo status        # demo, just this once
```

!!! note "It does not reach your timers"

    The generated systemd units pass `--config`, which outranks the variable. An
    operator who exports `MORZER_PRODUCT` globally has not redirected their
    backup or update timers, and should not expect to have.

There is deliberately no `morzer use <product>` that writes a selection to disk.
That is kubectl's context, and its failure mode is why people wrap kubectl in
scripts that print the context in the prompt: a mutable global that decides which
deployment a destructive command hits. A variable dies with the shell that set
it.

### The commands that answer anyway

Not everything needs an installation. These run on a machine with three of them
and no selection, because they are about the host, a file, or an installation
they name themselves:

`morzer ls` · `morzer version` · `morzer init` · `morzer doctor` ·
`morzer installation import` · `morzer release new` · `morzer release verify` ·
`morzer release pack` · `morzer release build` · `morzer release archive`

`doctor` is the interesting one: it runs its machine-scope checks, reports them,
and refuses the rest by name. Taking the diagnostic away at the moment the
diagnosis is "you have two installations" would be exactly the wrong time.

## What `doctor` says about the neighbours

Two checks, both read-only, both warnings and never failures — a machine with
several installations is a supported arrangement, and a `doctor` that failed on a
supported arrangement would teach everyone to ignore `doctor`.

**`machine.installations`** counts them, and warns about the one arrangement
nothing else reports: an installation whose units are installed and whose state
will not load. systemd starts it on every boot and the manager cannot tell it
anything.

**`machine.ports`** warns when two installations declare the same host port:

```text
! no two installations publish the same port
  two installations want the same host port: 18080 (demo, sandbox)
  they cannot both run; change one release's port parameter with
  `morzer --product <name> config set`, or keep only one started
```

They cannot both run. Without this check the second one's `apply` fails inside
Compose with a message about a port already in use — true, and silent about the
neighbour holding it, which sends an operator looking for a stray process with
`ss -tlnp`.

It reads what each release *declares* it needs, resolved against that
installation's own parameters, not what is bound right now. The collision matters
before either is started, which is exactly when nothing is listening and a probe
would report all clear.

The usual fix is a parameter:

```sh
morzer --product sandbox config set http_port=18081
morzer --product sandbox apply
```

## What is deliberately not here

**No `--all`.** There is no `morzer update --all`, and there will not be. Three
installations have three releases, three compatibility gates and three windows of
downtime; a command that updated all of them would be a loop with one exit code
and no way to say which one failed. That loop belongs in a shell script, where
its failure handling is yours.

**No isolation between installations.** They share a daemon, a port space and a
disk. Making them not share those is a container-per-installation design, which
is a different product. What ships instead is the `machine.ports` check above,
and the fact that everything the manager owns is already keyed by product.

**No renaming.** The product name is the paths, the Compose project, the unit
names and the comment on every secret recipient. Changing it is a migration, not
a flag: export, `init` under the new name, import, restore.

**No machine-level registry file.** `/etc/*/installation.yaml` is the truth, and
`morzer ls` reads exactly that. A second index would be one more thing to keep in
sync — wrong precisely when a machine had been rebuilt by hand, which is when
somebody is reading it.

## Related

- [`morzer ls`](../reference/installation-commands.md#morzer-ls) — the command surface
- [Recovering a lost machine](recovering-a-lost-machine.md) — rebuilding one installation
- [Changing configuration](changing-configuration.md) — `config set`, which is how a port moves
