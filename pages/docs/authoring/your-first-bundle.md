---
title: Your first bundle
icon: lucide/file-code
summary: Building a working bundle from the one the test suite runs against
---

# Your first bundle

Every fragment on this page is extracted from `testdata/bundle/` at build time.
That bundle is installed, updated, backed up, restored and rolled back against
real Docker on every CI run, so what you are reading is what passes.

## The manifest

Start with identity and the runtime:

```yaml title="manifest.yaml"
api_version: selfhost/v1alpha1
kind: application-release

metadata:
  name: demo
  version: 1.2.0
  description: Demo self-hosted bundle used by the test suite
  vendor: example

runtime:
  project: demo
  files:
    - compose/compose.yaml
  profiles:
    embedded: [compose/compose.embedded.yaml]
    external-db: [compose/compose.external-db.yaml]
```

`metadata.name` is load-bearing: it becomes `/etc/demo`, `/var/lib/demo`,
`/run/demo`, `/opt/demo` and the `DEMO_*` prefix every hook sees. It is
validated as a path component, not merely as a string, because it comes from
your file and lands in the operator's filesystem.

**Profiles** are how one bundle serves "database included" and "bring your own
database" without two bundles. Each names *additional* Compose files layered
over the base.

### Images

```yaml
images:
  app: registry.example/demo/app@sha256:0000…0001
  db: registry.example/demo/db@sha256:0000…0002
```

Digest-pinned, always. The manager exports each as `DEMO_IMAGE_APP`,
`DEMO_IMAGE_DB` and so on, which is how your Compose file gets them:

```yaml title="compose/compose.yaml"
services:
  app:
    image: ${DEMO_IMAGE_APP:-registry.example/demo/app@sha256:…}
```

Write it that way. Hardcoding the digest in both places means two things to keep
in agreement, and the manifest is the one the manager verifies.

### Requirements

```yaml
requirements:
  architectures: [amd64, arm64]
  os:
    - {id: ubuntu, version: ">=22.04"}
    - {id: debian, version: ">=12"}
  tools:
    docker: ">=24"
    compose: ">=2.30"
  memory: 2GiB
  disk: 5GiB
  ports: [18080]
```

Checked in preflight, before anything is written, and reported by `doctor`. Be
honest here: a requirement you did not declare is a support ticket, and one you
declared too tightly is an installation that refuses for no reason.

Declaring `ports` is what lets the manager tell an operator their port is taken
*before* the containers fight over it.

### Configuration templates

```yaml
configuration:
  - template: templates/application.yaml
    target: /etc/demo/application.yaml
    mode: "0640"
```

Rendered with the installation's own facts. **Secrets appear as paths, never as
values** — there is a `secretFile` helper for exactly this:

```yaml title="templates/application.yaml"
secrets:
  db_password_file: {{ secretFile .Secrets "db_password" }}
  session_key_file: {{ secretFile .Secrets "session_key" }}
```

A rendered config in `/etc` that contained a credential would be a credential in
every backup, every support bundle and every `cat` an operator ever runs.

### Compatibility

```yaml
compatibility:
  database_schema_min: 10
  database_schema_max: 12
  rollback_safe: true
  min_manager_version: "0.0.0"
  upgrade_from: ">=1.0.0 <2.0.0"
```

The four declarations that make an update decidable without running it:

- **`upgrade_from`** — which installed versions this may be installed over.
- **`database_schema_min`/`max`** — which schemas this release can read. The
  manager knows the running schema because your migrate hook reported it.
- **`rollback_safe`** — whether returning to the previous release is safe.
- **`min_manager_version`** — the oldest manager that may install this.

Set `rollback_safe: false` when your migrations are one-way. The manager will
refuse the rollback and name the pre-update backup instead, which is the honest
answer and the one your users need.

## The secret schema

```yaml title="templates/secrets.yaml"
--8<-- "testdata/bundle/templates/secrets.yaml"
```

Declaring a `generator` means the manager can create the value itself at `init`
and rotate it on request. Declaring `services` means a rotation restarts only
those, rather than the whole product. Declaring `rotation_period` means `doctor`
reminds an operator — advisory, and never a failure.

## Validate as you go

```sh
morzer release verify ./my-product
```

Reports **every** violation in one pass rather than the first, with the line and
column from your YAML. Run it in your own CI; it needs no installation on the
machine.

There is also a JSON Schema for editor completion — see
[Validating a manifest](../reference/manifest.md#validating-a-manifest).

## The complete manifest

```yaml title="testdata/bundle/manifest.yaml"
--8<-- "testdata/bundle/manifest.yaml"
```
