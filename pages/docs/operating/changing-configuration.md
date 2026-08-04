---
title: Changing configuration
icon: lucide/sliders-horizontal
summary: How to change what a deployment is configured with — parameters, domains, policy — and what each change actually moves
---

# Changing configuration

Almost everything an operator changes after install is a **parameter**: a value
the release declares and you choose. Ports, log levels, limits.

```sh
morzer config list                # what exists, what it is set to, and where that came from
morzer config set http_port=9000  # change one
morzer config unset http_port     # back to the release's default
```

## Find out what you can change

```console
$ morzer config list
NAME                 TYPE       VALUE            SOURCE
http_port            port       9000             installation
log_level            enum       info             release
```

`SOURCE` is the column to read. **installation** means you set it; **release**
means it is the vendor's default and you have not touched it. That answers
*what have I changed on this machine* without diffing against the bundle.

The release decides what exists. There is no way to set something it does not
declare, and a typo is refused by name:

```console
$ morzer config set htpp_port=9000
error: the release declares no parameter "htpp_port"
hint:  it declares: http_port, log_level
```

## Change one

`config set` is an operation, not a file edit. It takes the deployment lock,
plans under `--dry-run`, journals before and after each step, and unwinds what
it changed if a step fails:

```console
$ morzer config set http_port=9000
[1/3] record parameters
[2/3] render configuration
[3/3] re-create app

set http_port; re-created app
```

The last line is the one to read. It tells you which of two things happened:

| Summary ends with | What it means |
| --- | --- |
| `re-created <services>` | The change is live. Those services are running with the new value. |
| `takes effect on the next morzer apply` | Recorded, not yet live. Run `apply`. |

Which one you get is the release's decision: each parameter declares the
services that depend on it, and one that declares none is a value read at
start-up rather than from the environment.

!!! note "It re-creates rather than restarts"

    A published port is fixed when a container is created, so restarting would
    report success and leave the old port in place. `config set` replaces the
    affected containers.

    This is the opposite of [`secret rotate`](secrets.md), which restarts —
    a secret reaches a container as a mounted file, and restarting re-reads it.

## Plan it first

```sh
morzer config set http_port=9000 --dry-run
```

Shows the step list and a diff of any configuration file that would change.
A plan never claims a service was re-created; it says what *would* happen.

## What is not a parameter

| You want to change | How |
| --- | --- |
| A port, a log level, a limit | `config set`, if the release declares it |
| A credential | [`morzer secret`](secrets.md) — never a parameter |
| The deployment profile | `apply --profile` for one run; otherwise re-install |
| Domains, signing policy, retention | Not yet changeable after `init` |
| The service topology | The vendor's, in the release's Compose files |

!!! danger "Parameters are not secrets"

    A parameter's value is visible in `docker inspect`, in `status --json`, in
    the journal and in `installation.yaml` in the clear. Anything that must not
    be visible belongs in the [secret state](secrets.md), which encrypts it at
    rest and renders it to tmpfs as a file.

## Editing installation.yaml does nothing

`/etc/<product>/installation.yaml` is a **report**. The manager reads its own
state file, so an edit there changes nothing at all — and `doctor` says so,
naming the fields that disagree:

```console
$ morzer doctor
config
  [warn] installation.yaml matches the recorded state: /etc/demo/installation.yaml
         disagrees with the recorded state (parameters.log_level); the recorded
         state is what runs
```

The file is rewritten from the recorded state by the next `config set`, or by
`morzer init --repair`.

## After a release changes what it declares

An update can add a parameter — it simply takes its default — or drop one. A
dropped parameter leaves your recorded value bound to nothing, which
`config list` reports as **stale**:

```console
$ morzer config list
…
stale (recorded, but 2.0.0 declares no such parameter): legacy_flag
clear with: morzer config unset legacy_flag
```

Nothing is blocked over it. Dropping a parameter is the vendor's decision, and
refusing every command until you tidy up would help nobody.
