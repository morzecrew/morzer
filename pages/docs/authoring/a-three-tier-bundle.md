---
title: A three-tier bundle
icon: lucide/layers
summary: A worked example with a frontend, a backend and a database — two published ports, per-tier credentials, and parameters scoped to the tier that uses them
---

# A three-tier bundle

[Your first bundle](your-first-bundle.md) ships one application and one
database. Most products are not shaped like that. This is the next shape up: a
frontend, a backend and a database, where each tier publishes its own port and
holds only the credentials it needs.

Everything here is drawn from
[`testdata/bundle-web/`](https://github.com/morzecrew/morzer/tree/main/testdata/bundle-web),
which the acceptance run installs against real Docker on every change — so the
example is a bundle that demonstrably works, not one that used to.

## What the tiers are

| Tier | Publishes | Holds |
| --- | --- | --- |
| `frontend` | the web port | nothing |
| `backend` | the API port | the database password, the session key |
| `db` | nothing | the database password |

The frontend holding nothing is the point, not an omission. It is the tier
exposed to the internet, and a tier that was never given a credential cannot
leak one.

## Two ports, two parameters

Each published port is a [parameter](../reference/parameters.md), and the
`services` list names the tier that consumes it:

```yaml
parameters:
  http_port:
    type: port
    default: 18080
    description: Port the web interface is published on
    services: [frontend]
  api_port:
    type: port
    default: 18081
    description: Port the API is published on
    services: [backend]
  log_level:
    type: enum
    values: [debug, info, warn, error]
    default: info
    services: [frontend, backend]
```

`morzer config set http_port=8443` then re-creates the **frontend only** — the
backend keeps serving and the database is never touched.

!!! warning "List every tier that reads the value"

    Nothing checks that `services` matches your Compose files. If both tiers
    interpolate `${WEB_PARAM_LOG_LEVEL}` but you list only the frontend,
    `config set log_level=debug` re-creates the frontend, reports success, and
    leaves the backend running the old value.

    `log_level` above lists both, which is why.

### A parameter with no services

`max_upload` declares none:

```yaml
  max_upload:
    type: bytes
    default: 25MiB
    description: Largest accepted upload
```

The backend reads it from its rendered configuration file at start-up, so
re-creating a container would not apply it — a full `apply` would. Declaring no
services is how you say that, and `config set` then tells the operator the
change waits for the next `apply` rather than claiming it took effect.

## The Compose file

Host ports come from the parameters; container ports are fixed by your images:

```yaml
services:
  frontend:
    image: ${WEB_IMAGE_FRONTEND:-…}
    ports:
      - "${WEB_PARAM_HTTP_PORT:-18080}:8080"
    environment:
      WEB_LOG_LEVEL: ${WEB_PARAM_LOG_LEVEL:-info}
      WEB_API_URL: http://backend:8080
    depends_on:
      - backend

  backend:
    image: ${WEB_IMAGE_BACKEND:-…}
    ports:
      - "${WEB_PARAM_API_PORT:-18081}:8080"
    secrets:
      - db_password
      - session_key
```

The frontend has no `secrets:` block. Tiers reach each other over the Compose
network by service name (`http://backend:8080`), so only what an operator must
reach from outside needs publishing at all.

## Requirements and health follow the parameters

Both, for both tiers:

```yaml
requirements:
  ports:
    - "{{ .Parameters.http_port }}"
    - "{{ .Parameters.api_port }}"

health:
  checks:
    - {name: web, type: http, url: "http://127.0.0.1:{{ .Parameters.http_port }}/health/ready"}
    - {name: api, type: http, url: "http://127.0.0.1:{{ .Parameters.api_port }}/health/ready"}
```

Writing a literal here is the mistake this exists to prevent: the deployment
publishes the port the operator chose, preflight checks the one you wrote, and
`apply` fails at *wait for health checks* on a system that is working perfectly.

## Per-tier credentials

The secret schema's `services` is what gets restarted on rotation, so it names
the tiers that actually hold each value:

```yaml
secrets:
  - name: db_password
    description: Database password used by the backend
    required: true
    generator: {kind: password, length: 32}
    services: [backend, db]

  - name: session_key
    description: Key the backend signs session cookies with
    required: true
    generator: {kind: hex, length: 64}
    services: [backend]
```

Rotating `session_key` bounces the backend. Rotating `db_password` bounces the
backend and the database. Neither touches the frontend, because the frontend
never had either.

!!! warning "Do not reach for a parameter here"

    A parameter is the wrong home for anything secret: its value is visible in
    `docker inspect`, in `status --json`, in the journal and in
    `installation.yaml` in the clear. Credentials belong in the secret schema,
    which encrypts them at rest and renders them to tmpfs as files.

## Profiles

The database is a profile rather than a fixed service, because whether it runs
here is the operator's decision:

```yaml
runtimes:
  compose:
    files:
      - compose/compose.yaml
    profiles:
      embedded: [compose/compose.embedded.yaml]
      external-db: [compose/compose.external-db.yaml]
```

The backend keeps `db_password` under both. Someone else's database still
requires authenticating to it; only the server moves.

## Installing it

```sh
morzer init --release ./bundle-web --profile embedded \
    --set http_port=8443 --set api_port=9443
morzer apply
```

And afterwards, one tier at a time:

```sh
morzer config set http_port=8080     # re-creates the frontend, nothing else
morzer config list                   # what is set, and where each value came from
```
