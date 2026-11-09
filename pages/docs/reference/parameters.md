---
title: Parameters
icon: lucide/sliders-horizontal
summary: The knobs a release exposes, how to set them, where the values go, and why they are not secrets
---

# Parameters

A parameter is a value the **release declares** and the **operator chooses** —
a port, a log level, an upload limit. The release says which exist and what
each accepts; the manager refuses anything else.

Declared, rather than free-form, because a free-form setting has no failure
mode for a typo: `htpp_port: 9000` would be accepted, ignored, and discovered
much later as "the port did not change".

## Setting one

After install, with [`config set`](#changing-one-after-install). At install
time:

```sh
morzer init --release ./bundle --set http_port=9000 --set log_level=debug
```

`--set` needs a `--release` to validate against — there is nothing to check a
value against without the manifest that declares it — and a value the release
does not accept is refused before anything is created:

```text
[4/6] stage release bundle
        failed: parameter "log_level"
        hint: log_level accepts debug, info, warn, error; default info -- Application log verbosity
```

`--set-file name=path` reads a value from a file instead, and `-` reads it from
standard input. It is how a multi-line [text value](#text-values) is given at
install time:

```sh
morzer init --release ./bundle --set-file rules=./rules.txt
```

The chosen values are recorded in the installation's state, and shown in
`installation.yaml`:

```yaml
parameters:
  http_port: "9000"
  log_level: debug
```

Stored as written, and re-validated against the release on every operation. A
release that later narrows an enum, or drops a parameter, surfaces as an error
naming the value rather than silently reinterpreting it.

## Where a value goes

Every declared parameter reaches three places, always under the same name, and
always present — an unset one carries the release's default, so nothing has to
tell "unset" from "set to the default":

| Consumer | Form | Example |
| --- | --- | --- |
| Compose files | `<PRODUCT>_PARAM_<NAME>` | `${DEMO_PARAM_HTTP_PORT}` |
| Configuration templates | `.Parameters.<name>` | `{{ .Parameters.http_port }}` |
| Hooks | `<PRODUCT>_PARAM_<NAME>` | `$DEMO_PARAM_HTTP_PORT` |

The `PARAM_` in the middle is not decoration. Without it a parameter named
`data_dir` would export `DEMO_DATA_DIR` — [a variable the manager already
owns](hooks.md) — and take the deployment's storage with it.

## The manifest follows the value

Two manifest fields interpolate parameters, and they are the two that would
otherwise contradict a changed port:

```yaml
requirements:
  ports: ["{{ .Parameters.http_port }}"]

health:
  checks:
    - name: api
      type: http
      url: "http://127.0.0.1:{{ .Parameters.http_port }}/health/ready"
```

Without this, changing a port gives you a deployment that works and an `apply`
that fails: Compose publishes 9000, preflight checks that 18080 is free, and the
health probe asks 18080 and times out. The three have to move together.

Only these two fields, and the only thing they can see is `.Parameters` —
not paths, not secrets, not the installation. A health URL able to interpolate
a secret path is a health URL able to put one in a log line.

## Only declared values reach the runtime

Setting `DEMO_PARAM_HTTP_PORT` in your shell does nothing. The manager builds
the runtime's environment from an allow-list plus the declared parameters, so
the recorded configuration is what runs.

This is deliberate, and it closed a real hole: the environment used to be
inherited wholesale, which meant any `<PRODUCT>_*` variable in a shell
interpolated into a Compose file — undocumented, unvalidated, unrecorded, and
invisible to the manifest that was supposed to describe the release.

What still passes through is what a tool needs to run at all: `PATH`, `HOME`,
`TMPDIR`, the `XDG_*` directories, Docker's own client configuration
(`DOCKER_HOST`, `DOCKER_CONTEXT`, `DOCKER_CONFIG`, the TLS variables),
`SSH_AUTH_SOCK`, and the proxy variables.

## Types

| Type | Accepts | Normalised to |
| --- | --- | --- |
| `port` | 1–65535 | the number |
| `int` | a whole number | the number |
| `bool` | `true`, `false`, `1`, `0`, `TRUE`… | `true` or `false` |
| `string` | anything | trimmed |
| `enum` | one of the declared `values` | as written |
| `duration` | `30s`, `5m`, `2h` | canonical form |
| `bytes` | `512KiB`, `25MiB`, `2GiB` | canonical form |
| `text` | several lines of UTF-8, up to 64 KiB | LF line endings, trailing newlines removed |

Values are normalised, so ` 9000 ` and `9000` cannot become two different ports
downstream. Port `0` is refused: Compose reads it as "pick any", and the health
check would then probe a port nothing is listening on.

!!! danger "Parameters are not secrets"

    A parameter's value is visible in `docker inspect`, in `status --json`, in
    the journal, and in `installation.yaml` in the clear.

    Anything that must not be visible is a **secret**. Secrets are declared in
    their own schema, encrypted at rest, rendered to a tmpfs directory as files
    with mode `0400`, never passed as environment, and audited by `doctor`. See
    [Secret commands](secret-commands.md) and
    [How secrets work](../explanation/secrets.md).

## Text values

A `text` parameter holds several lines: routing rules, an allow list, a
fragment of the product's own configuration. Line endings become LF and
trailing newlines are removed, and nothing else is trimmed, so indentation
survives. A value is at most 64 KiB once normalised, and a NUL byte or invalid
UTF-8 is refused.

Set one from a file, from standard input, or in your editor:

```sh
morzer config set --set-file rules=./rules.txt
generate-rules | morzer config set --set-file rules=-
morzer config edit rules
```

`--set-file` works for every type, and on `init` too. Whatever the type, it
refuses a file holding a NUL byte or invalid UTF-8: no process environment can
carry a NUL, and the recorded state is JSON, which would replace invalid bytes.

`config edit <name>` opens one text parameter's value alone, as plain text, in
`$VISUAL` or `$EDITOR`, and needs a terminal. Saving it runs `config set`.
Saving it unchanged changes nothing, and so does emptying it. To clear a value,
`config set rules=` records an empty one where the release declares a default,
and `config unset rules` returns to that default. A parameter with no default
cannot be left empty.

What each view shows:

| Where | A text value |
| --- | --- |
| `config list` | the first line and a count, such as `DOMAIN-SUFFIX,example.ru (42 lines)` |
| `config get`, `config list --json` | the whole value |
| `installation describe`, `installation.yaml` | the whole value, as a block scalar |
| the `config set` summary | the line counts before and after, such as `set rules (42 → 44 lines)` |

`config get rules > rules.txt` writes back what `--set-file rules=rules.txt`
reads.

A text value is not secret, like every parameter. Anything that must not be
visible in `docker inspect` or `installation.yaml` is a secret.

## Changing one after install

See [Changing configuration](../operating/changing-configuration.md) for the
operator's walkthrough. In short:

`morzer config set` takes the deployment lock, validates the value, records it,
re-renders the configuration and re-creates the services the release says depend
on it.

```sh
morzer config list                    # every parameter, its value, and where it came from
morzer config get http_port           # the value alone, for a script
morzer config set http_port=9000      # validate, record, re-create the dependants
morzer config set --set-file rules=rules.txt   # a value from a file; - is stdin
morzer config edit rules              # a text value in your editor
morzer config unset http_port         # back to the release's default
morzer config set http_port=9000 --dry-run   # the step list, and the config diff
```

`morzer config get` prints the value alone, with no decoration, so it
substitutes directly into a script. `config list` names the source of every
value, so *what have I changed on this machine* is answerable without diffing
against the bundle:

```text
NAME                 TYPE       VALUE            SOURCE
http_port            port       9000             installation
log_level            enum       info             release
```

### Installation settings are not parameters

A parameter is declared by the vendor and reaches the deployment. An
**installation setting** is your arrangement with the manager — whether it may
contact a registry, which reference it follows — and changes nothing that is
running. The two exceptions are `installation.domains` and
`installation.profile`, below.

They share `morzer config` because that is where you look to change a value, and
they cannot collide: a parameter name is lowercase letters, digits and
underscores, so a dotted name is always a setting.

```sh
morzer config settings                       # every setting, its value and what it means
morzer config set update.check=true          # a setting: a flag, no services touched
morzer config unset update.channel           # back to absent, which is always the safe state
```

| Setting | Meaning |
| --- | --- |
| `update.check` | Contact the vendor's registry unprompted, for `doctor` and `status`. Absent means off. |
| `manager.check` | Ask GitHub unprompted whether a newer manager is published, for `doctor`. Absent means off. See [upgrading the manager](../operating/upgrading-the-manager.md). |
| `update.channel` | A mutable reference to follow. See [following a channel](../operating/updating.md#following-a-channel). |
| `update.auto_apply` | Install what the channel offers, when the release declares a failure cannot end needing a database restore. |
| `update.schedule` | When the update timer follows the channel — and installs, if `update.auto_apply` allows — as a systemd `OnCalendar` expression. This is the maintenance window. Absent takes daily at 03:30. See [unattended updates](../operating/unattended-updates.md#the-maintenance-window-is-an-oncalendar-expression). |
| `update.randomized_delay` | How long each update tick may wait at random, as a duration in whole seconds such as `30m`; `0` for none. Absent takes 30 minutes, or none on a [dev-mode](../operating/unattended-updates.md#dev-mode-a-machine-that-is-a-sandbox-from-birth) machine. `morzer status` shows it with the schedule. |
| `backup.schedule` | When scheduled backups run, as a systemd `OnCalendar` expression. Absent takes the nightly default. |
| `backup.scheduled` | Whether this manager runs backups on a timer at all. Absent means it does. |
| `doctor.schedule` | When the [scheduled `doctor`](../operating/notifications.md#the-scheduled-doctor) runs, as a systemd `OnCalendar` expression. Absent takes the morning default, `*-*-* 06:00:00`. |
| `doctor.scheduled` | Whether `doctor` runs on a timer. Absent means it does, while a notify target or a metrics export is configured; with neither there is no timer. |
| `metrics.textfile` | The node exporter's textfile directory, which a timer writes this installation's metrics into every minute. An absolute path to an existing directory, with no whitespace, quotes, backslashes or `%`. Absent means no export. See [monitoring with Prometheus](../operating/monitoring-with-prometheus.md). |
| `backup.before_update` | Whether an update takes a backup before it changes anything. Absent means it does. Only a [dev-mode](../operating/unattended-updates.md#dev-mode-a-machine-that-is-a-sandbox-from-birth) machine may set `false`; on a production machine, `update --skip-backup --force` skips it for one update. |
| `retention.pre_update_backups` | How many of the newest pre-update backups retention keeps, on top of the ordinary count; at least `1`. Absent takes `3`. See [retention](../operating/backups.md#retention). |
| `retention.releases` | How many releases the store keeps beside the current, previous and staged ones, which are never pruned; at least `1`. Absent takes the release's `retention.releases`. |
| `retention.backups` | How many of the newest backups retention keeps; at least `1`. Absent takes the release's `retention.backups`. |
| `retention.attestations` | How many of the newest [attestations](attestations.md#keeping-fewer-of-them) this machine keeps; at least `1`. Absent keeps them all, and no release sets it. A statement a backup target does not have yet is never removed. |
| `release.require_signature` | Refuse any release not signed by a key [`release trust`](release-commands.md#release-trust) lists. `true` needs a trusted key; `false` is refused while `update.auto_apply` is on, and is journalled. See [unattended updates](../operating/unattended-updates.md#turning-it-on). |
| `installation.domains` | The public names the product is served under, comma-separated; the first is canonical. Each is a host name or an `https://` URL. A change renders the configuration and re-creates every running service. |
| `installation.profile` | The [deployment profiles](manifest.md#runtimes) the release is rendered and started with, comma-separated; a selection the installed release can deploy. Absent, or empty, is the base files. A change on a running product applies the release under it. |

`backup.schedule` is the same value `morzer init --backup-schedule` takes, and
until it was a setting it could not be changed afterwards at all. Changing it
rewrites the timer unit; unsetting it returns the default rather than turning
backups off, because an empty `OnCalendar` is a unit systemd refuses to load.

Both schedules are checked with systemd's own parser before they are stored —
by `config set`, and by `init --backup-schedule` before anything is created — so
an expression systemd cannot read is refused rather than left as a timer that
never loads. On a host without `systemd-analyze` there is nothing to ask, and
the value is stored as given.

A retention count takes effect where retention already runs: releases after an
update and at `morzer release prune`, backups after a backup. Setting one prunes
nothing by itself.

`release.require_signature` applies from the next `release fetch` or `update`.
The installed release keeps running and nothing already in the store is checked
again, but an `update --to` a stored release whose signature does not verify is
then refused.

### Changing the domains

```sh
morzer config set installation.domains=demo.example,www.demo.example
morzer config unset installation.domains     # no domain, as an install without --domain
```

A domain reaches the deployment three ways: the templates' `.Domain` and
`.Domains`, the services' `DOMAIN` variable, and the public URL `status` shows.
No release says which services read it, so the change renders the configuration
and re-creates **every running service**, which is a short outage. It runs as
one `config` operation in `morzer history`: a render that fails puts the old
domains back, and `--dry-run` shows the steps without running them.

While the product is [stopped](../operating/taking-a-product-down.md), nothing is
re-created: `morzer start` brings every service up with the new value.

morzer issues no certificate for a new domain. Whatever terminates TLS, the
release's proxy or yours, needs one before the name is served.

### Changing the profile

```sh
morzer config set installation.profile=embedded
morzer config set installation.profile=embedded,xdns   # several, where the release allows
morzer config unset installation.profile     # the base files, as an install without --profile
```

The profile chooses which of the release's files are deployed, so a change can
add services and remove others. On a running product it applies the release
under the new profile, as `morzer apply` does, and re-creates **every
container**, including any whose definition is unchanged, since a service can
read `.Profile` from a rendered file. Services the new profile does not declare
are removed. It runs as one `config` operation in `morzer history`: a step that
fails brings back the old profile, its files and its services, and `--dry-run`
shows the steps without running them.

While the product is stopped, or nothing is running, the change is recorded and
rendered, and the next `morzer start` or `morzer apply` deploys it. A runtime
that cannot say what is running refuses the change.

Several profiles are a comma list, where the release lists them as
[`combinable`](manifest.md#combinable-profiles): any of those, plus at most one
other. The order you name them in does not count. The release decides the
order their files are layered in, and that is the order `config get` shows.
Naming the same profiles in another order changes nothing and runs nothing.

A name the installed release does not declare is refused, and so are two
profiles it does not let run together. `apply --profile` and `update
--profile` still choose a selection for one run and leave the recorded one as
it is; each says so when the two differ. An update or rollback to a release
that does not declare a recorded profile, or does not let the recorded ones run
together, is refused before it starts, even with `--profile`: change the
selection first, to one both releases accept, or unset it.

### `config diff`

```sh
morzer config diff            # each file that differs, then a summary on stderr
morzer config diff --json     # release, targets, redacted, drift, unreadable, services, services_compared
```

Renders every configuration file from the installed release and the recorded
parameters, and shows where the file on disk differs, scrubbed of secret
values, and which services were created from another definition than the
release declares. It changes nothing and exits 0 whether or not a file differs. What the
output means, and what replaces a hand edit, is in
[A hand edit is replaced](../operating/changing-configuration.md#a-hand-edit-is-replaced).

## Turning scheduled backups off

Two ways, and they are not the same thing.

```sh
morzer config set backup.scheduled=false   # this machine's backups are not morzer's job
systemctl disable --now demo-backup.timer  # this timer, on this machine, stops
```

The setting decides whether the timer **exists**. Declaring it removes the
backup service and timer, and `morzer doctor` stops asking how old the last
backup is — you have told it the job belongs to something else, which is a
declaration it can read rather than a fact it would be guessing at. It travels
with the installation, so `morzer init --repair` reproduces it and
[`installation describe`](installation-commands.md#installation-describe) records it.

`systemctl disable` decides whether an existing timer **runs**, and it now
sticks: reconciliation no longer re-enables a unit that already exists. The
units are reconciled when a setting or a target changes, and after each
`apply`, `start` and `update` that succeeds, so a machine an older manager set
up gets the units this one writes without being switched back on. Only
`morzer init --repair --install-units` switches it back on, which is the one
command whose job is to put a machine right. `doctor` reports a unit you
disabled as `not enabled`, and names both ways out of that.

`systemctl mask` does not work on a generated unit, and that is settled rather
than pending. A mask is a symlink to `/dev/null` at
`/etc/systemd/system/<unit>`, and that is exactly where morzer writes the unit,
so the path is already occupied. The units live there deliberately: systemd
loads `/etc/systemd/system` in preference to `/usr/lib/systemd/system`, so the
file morzer writes is the one that takes effect, and moving them elsewhere would
leave an older copy silently winning on every machine that already has one. Use
either of the two above — between them they say everything a mask would.

Taking a backup by hand is unaffected by both: `morzer backup` still works, and
a backup that exists is still expected to reach a target.

Settings and parameters are set in separate commands. They run on different
machinery — one converges a deployment, the other writes a flag — so a mixed
command is refused rather than half-applied.

### It re-creates, it does not restart

A published port is fixed when a container is created. `docker compose restart`
restarts the *existing* containers, so a restart after a port change would
report success and leave the old mapping in place. `config set` re-creates the
affected services instead, forced: a service that reads the value only from a
rendered file has an unchanged Compose definition, and a plain `up` would leave
it running with the file's old contents.

This is the opposite of [`secret rotate`](secret-commands.md), which restarts:
a secret reaches a container as a mounted file, and restarting re-reads it.

A parameter that declares no dependent services is recorded and takes effect on
the next `apply`. The summary says which of the two happened, and says it in the
tense it happened in — a `--dry-run` never claims a service was re-created.

### A value an older release left behind

A release that drops a parameter leaves its recorded value bound to nothing.
That is the vendor's decision, so it is reported rather than refused:
`config list` marks it stale, and `config unset <name>` clears it.
