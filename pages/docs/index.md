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

    [:octicons-arrow-right-24: First deployment](get-started/first-deployment.md) ·
    [Updating](operating/updating.md) ·
    [Commands](reference/commands.md)

-   :lucide-package:{ .lg .middle } **Bundle vendors**

    ---

    You ship the product. You write the manifest that declares what a release
    needs, and the hooks that carry the product-specific logic the manager
    deliberately does not know.

    [:octicons-arrow-right-24: Authoring a bundle](authoring/index.md) ·
    [Manifest](reference/manifest.md) ·
    [Hook ABI](reference/hooks.md)

</div>

## Install

See [Installing morzer](get-started/installation.md) for a verified download,
or from a clone:

```sh
just build          # ./morzer
```

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

## How this is organised

| Section | For |
| --- | --- |
| [Get started](get-started/installation.md) | A first deployment, start to finish |
| [Operating](operating/updating.md) | The tasks an operator does: updating, rolling back, secrets, backups, recovery |
| [Authoring](authoring/index.md) | Shipping your own product as a release bundle |
| [Reference](reference/commands.md) | Every command, flag, exit code, manifest field and hook variable |
| [Explanation](explanation/architecture.md) | Why it is shaped this way |

Design records for work that has not shipped live in
[`rfcs/`](https://github.com/morzecrew/morzer/tree/main/rfcs), not here — they
carry statuses that change, and mixing draft designs into documentation of
shipped behaviour would make both harder to trust.
