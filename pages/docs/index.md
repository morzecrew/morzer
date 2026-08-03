---
title: Home
icon: lucide/house
summary: Lifecycle management for a self-hosted product on a single machine
---

# morzer

A CLI that manages the lifecycle of a self-hosted product on **one** Linux
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

That boundary is the point. Everything the manager does is a sequence of steps
against tools that already exist, and every one of those steps can be planned
with `--dry-run`, journaled, verified after the fact, and undone.

## Two audiences

<div class="grid cards" markdown>

-   :lucide-server:{ .lg .middle } **Operators**

    ---

    You run the product on a machine you own. You install it, converge it, watch
    it, update it, and put it back when an update goes wrong.

    [:octicons-arrow-right-24: Commands](reference/commands.md) ·
    [Exit codes](reference/exit-codes.md)

-   :lucide-package:{ .lg .middle } **Bundle vendors**

    ---

    You ship the product. You write the manifest that declares what a release
    needs, and the hooks that carry the product-specific logic the manager
    deliberately does not know.

    [:octicons-arrow-right-24: Manifest](reference/manifest.md) ·
    [Hook ABI](reference/hooks.md)

</div>

## Install

```sh
just build          # ./morzer
just build-all      # dist/morzer-linux-{amd64,arm64} + SHA256SUMS
```

Building needs Go 1.25 or newer. At runtime the target machine needs `docker`,
`docker compose` and `sops`; the versions a release demands are declared in its
manifest, checked in preflight, and reported by `doctor`.

## Try it without touching /etc

Every managed path derives from a single root, so the whole thing can be
exercised against a throwaway directory:

```sh
just demo
```

That runs `init`, `status`, `doctor` and `secret list` against the example
bundle in `testdata/bundle/`, writing everything under `tmp/demo/`.
`just demo-plan` then shows what `apply` would do as a step list with a
configuration diff, and `just demo-json` shows the machine-readable output
contract.

The hidden `--root` flag relocates *every* managed path — including the absolute
configuration targets a manifest declares — so nothing touches the real `/etc`.

## Status of these docs

!!! warning "Partly written"

    This site currently carries the **reference** material only: the command
    surface, the exit-code table, the manifest schema and the hook ABI. The
    tutorial, operator how-tos and the architecture explanation still live in
    the [README](https://github.com/morzecrew/morzer#readme) and are being moved
    page by page, as designed in
    [RFC 0006](https://github.com/morzecrew/morzer/blob/main/rfcs/0006-documentation-site.md).
