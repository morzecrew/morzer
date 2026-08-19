---
title: Manifest
icon: lucide/file-code
summary: The release manifest schema — every field of selfhost/v1alpha1, and the secret schema alongside it
---

# Release manifest

`manifest.yaml` is the release contract: everything the manager needs to know
about a product, declared by the vendor rather than configured by the operator.

```text
release/
├── manifest.yaml       api_version: selfhost/v1alpha1
├── compose/            service topology
├── templates/          configuration + the secret schema
├── hooks/              migrate · smoke-test · backup · restore · check-db
└── VERSION
```

The schema is versioned independently of the manager. `morzer version --json`
reports which `api_version` values a given binary can read; a previously
published version stays readable until it is explicitly deprecated.

!!! info "Unknown fields are an error"

    Not a warning, and not silently ignored. A typo must not fall back to a
    default — the failure would surface as behaviour nobody asked for, days
    later. Vendor-specific keys go under `extensions.<namespace>`.

## Top level

| Field | Type | Required | Meaning |
| --- | --- | :---: | --- |
| `api_version` | string | ✅ | Manifest schema version. `selfhost/v1alpha1` is the only one currently supported. |
| `kind` | string | ✅ | Must be `application-release`. |
| `metadata` | table | ✅ | Identity of the release. |
| `providers` | table | | Which port implementation to use for each capability. |
| `runtime` | table | | **Deprecated.** The Compose project and its files. Superseded by `runtimes`, and still read. |
| `runtimes` | map | ✅ | Which runtimes this release supports, keyed by runtime name, and the files each is declared with. |
| `requirements` | table | | What the machine must provide. |
| `parameters` | map | | Knobs an operator may set, typed and defaulted. See [Parameters](parameters.md). |
| `images` | map | ✅ | Container images, pinned by digest. |
| `configuration` | list | | Templates rendered to absolute paths on the host. |
| `secrets` | table | | Where the encrypted state lives and where it renders. |
| `operations` | map | | Named lifecycle operations: migrate, smoke test, backup, restore. |
| `backup` | table | | What the manager may do about the project's volumes. |
| `bundle` | table | | What the release says about its own packaging, as distinct from what it needs of the host. |
| `health` | table | | How to tell whether the product is working. |
| `compatibility` | table | | What this release can be installed over, and rolled back from. |
| `retention` | table | | How many releases and backups to keep. |
| `extensions` | map | | Namespaced vendor data the manager passes through untouched. |

## metadata

| Field | Type | Required | Meaning |
| --- | --- | :---: | --- |
| `name` | string | ✅ | Product name. Drives `/etc/<name>/`, `/var/lib/<name>/`, `/run/<name>/`, `/opt/<name>/` and the hook environment prefix. |
| `version` | semver | ✅ | Release version. |
| `description` | string | | One line, shown by `release show`. |
| `vendor` | string | | Who publishes this. |
| `release_notes` | path | | Bundle-relative path to what changed in this release, conventionally `RELEASE.md`. |
| `support_url` | https URL | | Where an operator goes when something is wrong. |

`release_notes` is **declared, not discovered**. Every other path a bundle
ships is declared and existence-checked, and a declared-but-missing file fails
`release verify`; there is deliberately no fallback to looking for a
`RELEASE.md` that nothing points at, because a convention layered under a
declaration reintroduces the ambiguity the declaration removes.

`support_url` must be `https`. It is shown by `status` and by `doctor` **when a
check fails** — not appended to every error hint, which would put a vendor URL
in every log line. The operator of a self-hosted product is usually not its
vendor, and "where do I get help" otherwise has no home.

## providers

Each entry selects a port implementation by `name`, with an optional `version`
constraint. New capabilities arrive as new provider names, never as changes to
the core.

| Field | Default | Accepted |
| --- | --- | --- |
| `runtime` | `compose` | `compose` |
| `secrets` | `sops-age` | `sops-age` |
| `backup` | `hooks` | `hooks` |
| `health` | | `http` |

## runtimes

Which runtimes this release supports. The keys are the declaration: a release
declaring one runtime installs only where that runtime is present, and one
declaring several carries a set of files for each.

```yaml
runtimes:
  compose:
    files: [compose.yaml]
    profiles:
      external-db: [compose.external-db.yaml]
```

| Field | Type | Required | Meaning |
| --- | --- | :---: | --- |
| `files` | list | ✅ | The files this runtime is declared with, release-relative. |
| `profiles` | map | | Named deployment topologies; each is a list of *additional* files. |
| `options` | map | | Settings this runtime understands and the manager does not. Keys are lower-case identifiers, values one short line. |

One `files` field for every runtime, rather than a differently named key per
runtime. What the files mean is the runtime's business — a Compose file and a
systemd unit are both paths into the release from here — and a manifest whose
shape changed per runtime could only be validated by asking which runtime it
was, which is the coupling the manager does not have.

An unknown profile is an error rather than a silent fall back to the base files.
Deploying the wrong topology quietly is worse than refusing.

### Options

The manager carries `options` and never reads them. It checks the shape — a key
is a lower-case identifier, a value is one line of at most 200 characters,
because these are handed to a runtime that puts them in a command line — and the
runtime refuses any key it does not understand. A mistyped option is an error,
not a setting that quietly does nothing.

The compose runtime understands one:

| Option | Meaning |
| --- | --- |
| `project` | The Compose project name: the namespace containers, networks and volumes are created in. Defaults to `metadata.name`. |

**Changing it renames every volume.** Compose prefixes a named volume with the
project, so `project: alpha` and `project: beta` are two different sets of
storage. An installation records the options it was created with, and a release
that changes them is refused rather than applied — the deployment would come up
against volumes nothing has ever written to, with the real data still on the
disk and nothing referring to it. If you mean it, the path is a backup, a fresh
`init` and a `restore`.

**What is compared is the value the runtime resolves, not the line you typed.**
Omitting `project` and setting it to your product's name are the same
deployment, so a release that starts spelling out the default is applied rather
than refused, and so is one that stops. Only a value that actually changes the
namespace is a change. The runtime decides this, which is why an option a
runtime does not understand is compared exactly as written — the manager has no
way to know what it would have meant.

Options are per runtime because their meaning is. A project is Compose's idea of
grouping; another runtime answers the same question differently or not at all,
which is why this is a map the manager passes through rather than a field it
defines.

**Which runtime an installation uses is fixed when it is created** and never
transitions. The state directory records volume names and image references that
belong to one runtime, so changing it is a migration of everything the manager
knows rather than a setting. A release that does not declare the runtime an
installation was created against is refused rather than run on a substitute.

## runtime (removed in 0.3.0)

The original single-runtime block. **It is no longer read.** A manifest carrying
it is refused, naming what to write instead — the manager will not install a
release whose runtime declaration it cannot read, because doing so would bring
the deployment up with no Compose files at all.

It was deprecated rather than removed at first, on the understanding that a
release would exist in which both spellings worked. None did: `runtimes` shipped
in 0.3.0 and no earlier manager ever read it, so 0.1.0 through 0.2.0 read only
`runtime:` and 0.3.0 reads only `runtimes:`. There was no version that read both
and so no migration window to preserve, which is why the removal came a release
earlier than first announced.

**Migrating.** Move the files under `runtimes.compose.files`, move `project` to
`runtimes.compose.options.project`, delete the old block, and raise
`compatibility.min_manager_version` to `0.3.0`. The old block's shape was:

| Field | Type | Required | Meaning |
| --- | --- | :---: | --- |
| `project` | string | | Compose project name. Defaulted to `metadata.name`. |
| `files` | list | ✅ | Base Compose files, release-relative. |
| `profiles` | map | | Named deployment topologies; each a list of *additional* Compose files. |

The version floor is not optional bookkeeping: `runtimes` is an unknown field to
anything older, and under strict decoding an unknown field refuses the whole
manifest — so without the floor your customer gets a report about a typo instead
of an upgrade requirement.

Do not drop `project` on the way. It is the namespace every volume, network and
container of a running deployment lives in, so a release that loses it points
existing installations at storage that does not exist. A `project:` left behind
left behind beside `runtimes:` is refused with the rest of the old block, for
the same reason, and a manager that has recorded what an installation was
created with refuses the change as well.

## requirements

Checked in preflight and reported by `doctor`.

| Field | Type | Meaning |
| --- | --- | --- |
| `architectures` | list | Accepted machine architectures, e.g. `[amd64, arm64]`. |
| `os` | list | Accepted distributions. Each entry has an `id` and a `version` constraint. |
| `tools` | map | Version constraints for external tools, e.g. `docker: ">=24"`. |
| `memory` | size | Minimum RAM, e.g. `2GiB`. |
| `disk` | size | Minimum free disk, e.g. `5GiB`. |
| `cpus` | int | Logical CPUs the release wants. A cgroup quota is honoured where one is in force, so a containerised manager sees what it may actually use rather than what the host has. |
| `ports` | list | TCP ports the release binds, checked for conflicts. A literal number, or `"{{ .Parameters.<name> }}"` so the check follows the port the deployment actually publishes. |

## bundle

What the release says about its own packaging, as distinct from what it needs
of the host.

| Field | Type | Meaning |
| --- | --- | --- |
| `uncompressed_size` | size | What the archive expands to, e.g. `12GiB`. Raises the extraction ceiling for a bundle carrying container images. |

A separate block from `requirements` on purpose: everything there describes the
*host*, and this describes the *artefact*.

!!! warning "It can only ever lower the limit"

    This value is read out of the tar stream **before the signature is
    checked**, so it is attacker-controlled input in the strictest sense. The
    effective limit is `min(declared, hard cap)` — a declaration is a request
    for a smaller budget than the manager allows, never permission to exceed
    it. A bundle needing more than the cap is refused, and raising the cap is a
    change to morzer rather than something a bundle can ask for.

    Omitting it means the **default** ceiling, not "unbounded". A missing field
    must never be the permissive reading of anything that gates untrusted
    bytes.

## parameters

What an operator may change, and nothing else. Each entry declares a `type`, an
optional `default`, and — for an enum — the `values` it accepts.

```yaml
parameters:
  http_port:
    type: port
    default: 18080
    description: Port the application is published on
    services: [app]
  log_level:
    type: enum
    values: [debug, info, warn, error]
    default: info
    services: [app]
```

| Field | Type | Required | Meaning |
| --- | --- | :---: | --- |
| `type` | string | ✅ | One of `port`, `int`, `bool`, `string`, `enum`, `duration`, `bytes`. |
| `default` | string | | Used when the operator sets nothing. Validated against `type` at `release verify`. |
| `required` | bool | | The operator must supply a value. Declaring it alongside a `default` is a validation error. |
| `description` | string | | Shown when a value is refused, so the operator learns what is accepted. |
| `values` | list | | The permitted set. Required for `enum`, an error for anything else. |
| `services` | list | | Services to restart when the value changes. Empty means the change needs a full `apply`. |

A declared parameter reaches three places, all under the same name:

| Consumer | Form |
| --- | --- |
| Compose files | `<PRODUCT>_PARAM_<NAME>` |
| Configuration templates | `.Parameters.<name>` |
| Hooks | `<PRODUCT>_PARAM_<NAME>` |

Every declared parameter is always exported, holding its default when the
operator has set nothing — so a Compose file's `:-` fallback is belt-and-braces
rather than the actual source of the value.

!!! danger "Parameters are not secrets"

    A parameter's value is visible in `docker inspect`, in `status --json`, in
    the journal, and in `installation.yaml` in the clear. Anything that must
    not be is a [secret](../authoring/index.md), which has its own declared,
    audited path and is rendered to tmpfs as a file.

### required

Without it, a parameter with no default resolves silently to the empty string —
so a vendor could declare a knob that exists to be an operator choice and had
no way to say the choice was not optional, while a *secret* could be required
all along.

`required: true` **and** a `default` is refused. It is a vendor saying two
contradictory things, and picking a winner silently would leave them believing
they had made the choice mandatory.

An unset required parameter fails preflight, so it is refused by `init`, by
`config unset`, and by `apply`. That last one is deliberate: a release that
introduces a required parameter fails to deploy rather than deploying with an
empty value the product then misreads, and the currently running release keeps
serving. Set the value first:

```sh
morzer config set admin_email=ops@example
morzer update ./demo-1.4.0.tar.zst
```

See [Parameters](parameters.md) for the operator's side.

## images

A map of logical name to image reference. **Every reference must be pinned by
digest**:

```yaml
images:
  app: registry.example/demo/app@sha256:0000…0001
```

A bare tag is rejected. An unpinned image makes a release mutable, and a mutable
release makes rollback meaningless — the same version could produce a different
system on a different day.

**The key** is lowercase letters, digits and hyphens, starting and ending with a
letter or digit — the same rule parameters carry, and for the same reason: it
becomes the tail of `<PRODUCT>_IMAGE_<NAME>`, which your Compose file
interpolates. Dots and underscores are refused because the variable name folds
both to `_`, so `web-ui` and `web.ui` would name the same variable and one
pinned reference would silently overwrite the other.

### Where the bytes come from

An entry may instead be a mapping, which adds one field:

```yaml
images:
  db: postgres@sha256:0000…0002              # pulled, as above
  app:
    ref: registry.example/demo/app@sha256:0000…0001
    from: bundle                             # travels inside the bundle
```

| Field | Type | Meaning |
| --- | --- | --- |
| `ref` | string | The image reference, pinned by digest. Same rule as the scalar spelling. |
| `from` | string | `registry` (default) or `bundle`. |

Both spellings exist because most images are never bundled, and making every one
of them carry a `from:` to say so is noise in the file you read most.

`from: bundle` means the image travels in the bundle as an OCI layout under
`images/`, covered by the same `SHA256SUMS` and signature as every other file —
so a customer installs a release containing your private images without ever
holding credentials for the registry they came from. **Per-image**, so a release
bundles what is private and keeps pulling `postgres` from Docker Hub.

`ref` stays a real image reference in both spellings. It is interpolated into
Compose as `<PRODUCT>_IMAGE_<NAME>`, so it must remain something the daemon can
resolve — which is why the source is a separate field rather than a scheme on
the reference the way `update` references are spelled.

An unrecognised `from` is refused rather than defaulted. The two plausible
typos — `bundled`, and `from` under the wrong image — both fail towards a
release you believe ships its own bytes and does not, which surfaces as a
credential failure on your customer's machine.

See [bundled images](../authoring/bundled-images.md) for the layout, what
`release verify` checks, and how to produce one.

## configuration

A list of templates rendered onto the host.

| Field | Type | Required | Meaning |
| --- | --- | :---: | --- |
| `template` | path | ✅ | Release-relative template file. |
| `target` | path | ✅ | Absolute destination on the host. |
| `mode` | quoted octal | | File mode. Default `"0640"`. |

Rendered files contain *paths* to secrets, never secret values.

The quotes on `mode` are load-bearing. YAML reads an unquoted `0640` as the
decimal number 416, which is the permission `0416` — owner read-only, group
execute-only, other read/write. Unquoted modes are refused rather than applied,
so a manifest that gets this wrong fails to load instead of installing a file
with permissions nobody chose.

## secrets

| Field | Type | Meaning |
| --- | --- | --- |
| `source` | path | Absolute path to the encrypted state, e.g. `/etc/demo/secrets.sops.yaml`. |
| `render_to` | path | Absolute tmpfs directory the decrypted values are written to. |
| `schema` | path | Release-relative [secret schema](#the-secret-schema). |

## operations

A map of well-known name to how it runs. The manager looks up `migrate`,
`smoke_test`, `backup`, `restore` and `preflight` by convention.

| Field | Type | Meaning |
| --- | --- | --- |
| `kind` | enum | `hook` — an executable in the release, run under the [hook ABI](hooks.md); or `runtime-service` — a one-shot container from the Compose project. |
| `command` | list | For `hook`: argv, release-relative. Not allowed for `runtime-service`. |
| `service` | string | For `runtime-service`: the Compose service to run. Not allowed for `hook`. |
| `timeout` | duration | Time budget. Default `10m`. |

The two kinds are not collapsed into one field because their failure semantics
differ: a hook runs on the host and sees the host's filesystem, a runtime
service runs on the application network and sees the container's.

## backup

What the manager may do about the project's named volumes, which it captures
itself rather than through the backup hook.

| Field | Type | Meaning |
| --- | --- | --- |
| `volumes` | map | Keyed by the volume's name in the Compose `volumes:` block. |

Each entry takes one field, and it is **required** — an entry that names a
volume without saying anything about it is refused at load. Leaving the volume
out of the map entirely is how you ask for the default:

| Field | Type | Required | Meaning |
| --- | --- | :---: | --- |
| `consistency` | enum | ✅ | `cold`, `hot` or `exclude`. |

| Value | Meaning |
| --- | --- |
| `cold` | The services mounting the volume are stopped for the copy. The default, and correct for every volume. |
| `hot` | The vendor claims a copy taken while the product runs is usable. True for write-once files, false for anything with a write-ahead log. |
| `exclude` | The manager never reads or writes this volume. Expected for a database's storage, which the backup hook owns. |

```yaml
backup:
  volumes:
    uploads:    { consistency: hot }
    caddy_data: { consistency: hot }
    pgdata:     { consistency: exclude }
```

The map is partial: declare only the volumes that need something other than the
default of `cold`. A volume named here that the project does not declare is
ignored by the backup, and reported by `morzer doctor` — a typo in this map
otherwise does nothing at all, which for an intended `exclude` means the
manager captures a database volume the hook already owns.

`hot` is a claim about the product, not a performance hint — see
[declaring volume consistency](../authoring/backing-up-volumes.md) for what it
commits a vendor to. Bind mounts are never captured and have no declaration.

## health

`checks` is a list; each entry has a unique `name`, a `type`, a `timeout`
(default `120s`) and an optional `start_period`.

| `type` | Additional field | Meaning |
| --- | --- | --- |
| `http` | `url` | Considered healthy on a 2xx response. |
| `tcp` | `address` | Considered healthy when the connection is accepted. |
| `command` | `command` | Considered healthy on exit 0. Release-relative argv. |

`timeout` bounds a **single attempt**. `start_period` is different: it is how
long the check may keep failing before the failure means anything.

```yaml
health:
  checks:
    - name: api
      type: http
      url: http://127.0.0.1:18080/health
      timeout: 5s
      start_period: 90s
```

Without it, a product with a ninety-second first boot and a product that is
dead are the same observation, and the only lever is a timeout long enough to
delay noticing the second. With it, `apply` stops waiting once every
still-failing check has outlived its own declared period, and says so in
different words from a plain timeout — "the vendor said how long this takes and
it took longer" is acted on differently from "we ran out of time".

Omitted means the waiter keeps trying for as long as the operation allows,
which is what it has always done. A check that declares none also holds the
wait open for the checks beside it, so adding `start_period` to one check never
shortens the wait for another.

## compatibility

The declarations that make `update` and `rollback` decidable without running
anything.

| Field | Type | Meaning |
| --- | --- | --- |
| `database_schema_min` | int | Oldest schema this release can read. |
| `database_schema_max` | int | Newest schema this release can read. |
| `database_schema_produces` | int | What this release's migrations leave the database at. Optional; see below. |
| `rollback_safe` | bool | Whether this release's migrations can be undone by returning to the previous one. |
| `min_manager_version` | semver | Oldest manager that may install this release. |
| `upgrade_from` | constraint | Which currently-installed versions this may be installed over, e.g. `">=1.0.0 <2.0.0"`. |

`rollback_safe: false` is how a vendor says "going back needs a restore". The
manager takes it literally and refuses the rollback, naming the backup instead.

### `database_schema_produces` is what opts a release into unattended updates

```yaml
compatibility:
  rollback_safe: true
  database_schema_min: 12
  database_schema_max: 14
  database_schema_produces: 14   # what my migrations leave the database at
```

The other three fields describe what a release can *read*. This one states what
it *writes*, and it exists so the manager can answer a question before an update
rather than after one: if this release is installed and then has to be rolled
back, would the previous release still understand the database?

**Omitting it is a valid and conservative choice.** A release that does not
declare it is never installed unattended — the manager will fetch it, verify it,
stage it and notify the operator, and stop there. If you do not want customers
running your releases without a human present, declare nothing and nothing
changes for you.

Declaring it is not sufficient on its own. See
[unattended updates](../operating/unattended-updates.md) for the whole gate,
and for what the promise is and is not.

## retention

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `releases` | int | 3 | Non-active releases kept in the store. Must be at least 1. |
| `backups` | int | 7 | Backups kept. Must be at least 1. |

## extensions

Namespaced free-form data, passed through untouched:

```yaml
extensions:
  example.com/telemetry:
    endpoint: https://telemetry.example/ingest
```

The namespace must contain a `.` or a `/`, so a typo'd core field cannot hide
inside a vendor block.

---

## The secret schema

The document `secrets.schema` names — `secrets.schema.yaml` at the bundle root
by convention — declares what secrets exist, so `init` can provision them and
`doctor` can audit them without the manager knowing anything about the product.

| Field | Type | Meaning |
| --- | --- | --- |
| `api_version` | string | Same version vocabulary as the manifest. |
| `secrets` | list | The declarations below. |

Each declaration:

| Field | Type | Meaning |
| --- | --- | --- |
| `name` | string | Secret name, as `morzer secret` refers to it. |
| `description` | string | Shown by `secret list`. |
| `required` | bool | Whether `doctor` reports its absence as a failure. |
| `generator` | table | How the manager may produce it: `kind`, `length`, `alphabet`. |
| `file` | string | Filename under the render directory. Defaults to `name`. |
| `services` | list | Compose services that consume it. A rotation restarts exactly these. |
| `rotation_period` | duration | Advisory; `doctor` warns when a secret is older. |

Generator `kind` is one of `password`, `hex`, `base64`, `uuid`, `age-key`, or
absent — meaning the operator must supply the value.

The default password alphabet excludes characters that are ambiguous read aloud
or that need shell quoting. Secrets get copied by humans more often than anyone
plans for.

---

## Validating a manifest

A JSON Schema for `selfhost/v1alpha1` ships with every release, generated from
the Go types that enforce the contract rather than written alongside them:

```text
schemas/selfhost-v1alpha1-manifest.json
schemas/selfhost-v1alpha1-secrets.json
```

Point an editor at it for completion and typo-catching, or validate in CI
without installing the manager. It is regenerated by `just schemas`, and a test
fails the build when the checked-in copy no longer matches the types — two
descriptions of one contract disagree eventually, and the one a vendor validates
against is usually the more permissive.

The schema describes **shape**, not rules. Images must be pinned by digest,
paths may not escape the release root, an unknown field is an error — none of
that is expressible here, and all of it is enforced by the loader. The schema
catches a typo in an editor; `morzer release verify` catches everything:

```sh
morzer release verify ./bundle
```

## A complete example

This is `testdata/bundle/manifest.yaml`, included from the repository rather
than transcribed: the manifest loader, the step engine and the contract suites
all run against it, so it cannot drift from what the manager actually accepts.

```yaml title="testdata/bundle/manifest.yaml"
--8<-- "testdata/bundle/manifest.yaml"
```

And its secret schema:

```yaml title="testdata/bundle/secrets.schema.yaml"
--8<-- "testdata/bundle/secrets.schema.yaml"
```
