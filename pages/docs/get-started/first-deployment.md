---
title: Your first deployment
icon: lucide/rocket
summary: From an empty machine to a running product, and how to take it back down
---

# Your first deployment

Fifteen minutes, on a throwaway directory, with nothing installed into the real
`/etc`. What you end up with is a genuine deployment — real containers, real
encrypted secrets, real health checks — that you can delete with `rm -rf`.

## Try it without touching /etc

Every path the manager owns derives from a single prefix, and the hidden
`--root` flag moves all of them — including the absolute configuration targets a
manifest declares. It exists for testing, and it is the fastest way to see what
this does.

From a clone of the repository:

```sh
just demo
```

That runs `init`, `status`, `doctor` and `secret list` against the example
bundle in `testdata/bundle/`, writing everything under `tmp/demo/`. Three
variants go further:

```sh
just demo-plan      # what `apply` would do, as a step list with a config diff
just demo-json      # the machine-readable output contract
just demo-recovery  # delete an installation and rebuild it from an offline key
```

Everything below uses the same `--root` so you can follow along without a
dedicated machine. Drop it when you mean it.

## 1. An offline recovery key, first

```sh
morzer secret recipients generate-recovery-key ~/demo-recovery.key
```

It prints a public key and writes the private half at mode `0400`.

**Move the private half somewhere else** — a password manager, an offline drive.
A recovery key stored on the machine it is meant to recover protects nothing,
and this is the one step that cannot be done later: without a second recipient,
losing the machine loses its secrets permanently.

## 2. Create the installation

```sh
morzer --root ./demo init \
    --release ./testdata/bundle \
    --profile embedded \
    --domain demo.example \
    --recovery-recipient age1…
```

At a terminal with something missing, `init` asks instead of refusing, and
prints the equivalent command line when it is done — so the interactive run
produces the script for next time.

What it created:

```text
demo/etc/demo/        installation.yaml, secrets.sops.yaml, the machine age key
demo/var/lib/demo/    data, backups, the operation journal
demo/opt/demo/        releases, and the `current` symlink
demo/run/demo/        where decrypted secrets will be rendered
```

It has **not** started anything. That separation is deliberate: a half-finished
install leaves a machine with directories and keys, not with a partially-running
deployment.

## 3. Look before you leap

```sh
morzer --root ./demo apply --dry-run
```

The step list `apply` would run, with each step marked as it would go, and a
diff of any configuration file that would change. Nothing is written.

## 4. Converge

```sh
morzer --root ./demo apply
```

Eleven steps: preflight, decrypt, render secrets, render configuration, validate
the Compose project, pull images, migrate, start, wait for health, smoke test,
record the release.

It is **idempotent**. Run it again and every step reports `skipped (already
satisfied)` — each one answers a question about the world before doing anything,
so a converged system converges to itself in milliseconds.

## 5. Check on it

```sh
morzer --root ./demo status
morzer --root ./demo doctor
```

`status` is what is deployed and whether it is working. `doctor` is read-only
diagnostics with a suggested remedy for every result that is not ok, and it
exits [3](../reference/exit-codes.md) when a check fails — which is what makes
it usable from a monitoring system.

Some results are warnings by design, including the one you will see here: your
`--root` directory is not tmpfs, so decrypted secrets are on disk. That is
correct and expected when trying things out.

## 6. Take a backup

```sh
morzer --root ./demo backup --reason "before I break something"
morzer --root ./demo backup list
```

The release's own backup hook runs, the result is wrapped in a self-describing
manifest, and the checksums are verified by re-reading what was written.

## Teardown

```sh
docker compose -p demo down
rm -rf ./demo ~/demo-recovery.key
```

That is all of it. The whole installation was under one directory, which is what
`--root` is for.

For a real machine, without `--root`, there is no teardown command on purpose:
removing `/etc/<product>` and `/var/lib/<product>` deletes the secret state and
every backup, and a tool that made that a one-liner would eventually make it an
accident.

## Where to go next

<div class="grid cards" markdown>

-   :lucide-arrow-up-circle:{ .lg .middle } **Move to a new release**

    ---

    [:octicons-arrow-right-24: Updating](../operating/updating.md) ·
    [Rolling back](../operating/rolling-back.md)

-   :lucide-key-round:{ .lg .middle } **Manage credentials**

    ---

    [:octicons-arrow-right-24: Secrets](../operating/secrets.md)

-   :lucide-package:{ .lg .middle } **Ship your own product**

    ---

    [:octicons-arrow-right-24: Authoring a bundle](../authoring/index.md)

-   :lucide-book-open:{ .lg .middle } **Understand the design**

    ---

    [:octicons-arrow-right-24: Architecture](../explanation/architecture.md) ·
    [The step engine](../explanation/the-step-engine.md)

</div>
