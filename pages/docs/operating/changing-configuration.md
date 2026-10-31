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

Shows the step list and a diff of any configuration file that would change,
the same diff [`config diff`](#a-hand-edit-is-replaced) shows. A plan never
claims a service was re-created; it says what *would* happen.

## What is not a parameter

| You want to change | How |
| --- | --- |
| A port, a log level, a limit | `config set`, if the release declares it |
| A credential | [`morzer secret`](secrets.md) — never a parameter |
| The deployment profile | `apply --profile` for one run; otherwise re-install |
| The keys a release must be signed with | [`release trust add` / `remove`](../reference/release-commands.md#release-trust) |
| How many pre-update backups to keep | `config set retention.pre_update_backups=<n>` |
| No pre-update backup on a dev-mode machine | `config set backup.before_update=false` |
| Domains, whether a signature is required | Not yet changeable after `init` |
| How many releases and ordinary backups to keep | The manifest's [`retention`](../reference/manifest.md#retention); no command overrides it yet |
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

## A hand edit is replaced

A configuration file the release renders, such as `/etc/demo/application.yaml`,
is rewritten from the release and the recorded parameters by every `apply`,
`update`, `rollback` and `config set`. An edit made to it by hand works until
the next one of those, and is then gone. An unattended update leaves the release
staged rather than replace an edited file, but nothing keeps the edit.

`morzer config diff` shows what would be replaced, before it is:

```console
$ sudo morzer config diff
--- /etc/demo/application.yaml
@@ -3,1 +3,1 @@
- log_level: debug
+ log_level: info

1 of 4 configuration files differ from what 1.2.0 renders.
`morzer apply` replaces them; a change meant to last belongs in a parameter.
```

The diffs go to stdout and the summary to stderr, and the command exits 0
whether or not anything differs; `doctor`'s `config.drift` check is the one that
warns. `--json` carries `release`, `targets`, `redacted`, `drift` (each
`{target, diff}`) and `unreadable` (each `{target, error}`).

Every diff is scrubbed of this installation's secret values before it is shown,
because a value pasted into a file would otherwise print. When the secret state
cannot be decrypted, only the file names are shown and `redacted` is `false`.
A secret this installation does not hold, such as a third party's key pasted in
by hand, is not one it can recognise.

A file that cannot be read is listed apart and not compared. Run without
`sudo`, that is every root-only file. A change meant to last belongs in a
[parameter](../reference/parameters.md), if the release declares one, or in the
release.

## After a release changes what it declares

An update can add a parameter — it takes its default, and one with no default
waits for `config set` — or drop one. The update retires a value you recorded
for a dropped parameter, and says so. An operation that stops before it gets
there can leave the value bound to nothing, which `config list` reports as
**stale**:

```console
$ morzer config list
…
stale (recorded, but 2.0.0 declares no such parameter): legacy_flag
clear with: morzer config unset legacy_flag
```

Nothing is blocked over it. Dropping a parameter is the vendor's decision, and
refusing every command until you tidy up would help nobody. `config unset`
clears it even while another parameter is still waiting for a value.
