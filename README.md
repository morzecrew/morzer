# morzer

[![CI](https://github.com/morzecrew/morzer/actions/workflows/ci.yml/badge.svg)](https://github.com/morzecrew/morzer/actions/workflows/ci.yml)
[![codecov](https://codecov.io/github/morzecrew/morzer/graph/badge.svg)](https://codecov.io/github/morzecrew/morzer)
[![Docs](https://img.shields.io/badge/docs-morzecrew.github.io-blue)](https://morzecrew.github.io/morzer/)
[![Go](https://img.shields.io/badge/go-1.25%2B-00ADD8)](go.mod)
[![Licence](https://img.shields.io/badge/licence-MIT-green)](LICENSE)

A CLI that manages the lifecycle of a self-hosted product on a single Linux
machine running Docker Compose: install, configure, update, roll back, back up,
restore, diagnose.

The unit of delivery is a **release bundle** — an immutable archive containing a
manifest, Compose files, configuration templates and lifecycle hooks. The unit
of management is an **installation** — the state of one deployment on one
machine.

## What it is not

Not a container orchestrator, an infrastructure provisioner, a secret manager, a
backup engine, a workflow engine, or a CI system. It coordinates Docker Compose,
SOPS, age, systemd and the release's own hooks; it owns ordering, atomicity,
verification and reporting.

That boundary is the point. Everything it does is a sequence of steps against
tools that already exist, and every step can be planned with `--dry-run`,
journaled, verified afterwards, and undone.

## Install

```sh
curl -fsSL https://morzecrew.github.io/morzer/install.sh | sh -s -- --version 0.3.0
```

Verifies the checksum before it installs, and the signature too when `minisign`
is there. `--print-only` shows what it would do and changes nothing.

Unpacking needs `zstd` on the machine — releases are `tar.zst` and GNU tar runs
`zstd` as a filter rather than decompressing itself. Most servers have it;
minimal images often do not.

## Try it

```sh
just demo            # a throwaway installation under ./tmp, touching nothing real
just demo-plan       # what `apply` would do, as a step list with a config diff
just demo-recovery   # delete an installation and rebuild it from an offline key
```

Building needs Go 1.25+. At runtime a machine needs `docker`, `docker compose`
and `sops`; the versions a release requires are declared in its manifest,
checked in preflight and reported by `doctor`.

## Documentation

**<https://morzecrew.github.io/morzer/>**

| | |
| --- | --- |
| [Your first deployment](https://morzecrew.github.io/morzer/latest/get-started/first-deployment/) | Empty machine to running product, on a throwaway directory |
| [Operating](https://morzecrew.github.io/morzer/latest/operating/updating/) | Updating, rolling back, secrets, backups, offline installs, recovery |
| [Authoring a bundle](https://morzecrew.github.io/morzer/latest/authoring/) | Shipping your own product through this |
| [Reference](https://morzecrew.github.io/morzer/latest/reference/commands/) | Commands, exit codes, the manifest schema, the hook ABI |
| [Explanation](https://morzecrew.github.io/morzer/latest/explanation/architecture/) | Architecture, the step engine, the secrets model |

## Status

`init`, `apply`, `update`, `rollback`, `status`, `doctor`, `backup`, `restore`,
`secret`, `release` and `installation` all work against a real bundle, delivered
as a directory, a `tar.zst`, an HTTPS URL or an OCI artifact, and optionally
signed. An acceptance run installs, updates and rolls back a real deployment
against real Docker on every push.

Work that has not shipped is designed in [`rfcs/`](rfcs/) before it is built,
one numbered document per piece with its decisions recorded and its exclusions
reasoned. [`rfcs/INDEX.md`](rfcs/INDEX.md) is the table of contents.

## Contributing

[`CONTRIBUTING.md`](CONTRIBUTING.md) — the `just ci` loop, the commit
convention, the architecture rules and how they are enforced.
[`SECURITY.md`](SECURITY.md) states the threat model and the known gaps.

## Changelog

[`CHANGELOG.md`](CHANGELOG.md), in Keep a Changelog format.

## Licence

MIT — see [LICENSE](LICENSE).
