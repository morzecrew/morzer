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

The chosen values are recorded in `installation.yaml`:

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
running.

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
| `update.channel` | A mutable reference to follow. See [following a channel](../operating/updating.md#following-a-channel). |

Settings and parameters are set in separate commands. They run on different
machinery — one converges a deployment, the other writes a flag — so a mixed
command is refused rather than half-applied.

### It re-creates, it does not restart

A published port is fixed when a container is created. `docker compose restart`
restarts the *existing* containers, so a restart after a port change would
report success and leave the old mapping in place. `config set` re-creates the
affected services instead.

This is the opposite of [`secret rotate`](secret-commands.md), which restarts:
a secret reaches a container as a mounted file, and restarting re-reads it.

A parameter that declares no dependent services is recorded and takes effect on
the next `apply`. The summary says which of the two happened, and says it in the
tense it happened in — a `--dry-run` never claims a service was re-created.

### A value an older release left behind

A release that drops a parameter leaves its recorded value bound to nothing.
That is the vendor's decision, so it is reported rather than refused:
`config list` marks it stale, and `config unset <name>` clears it.
