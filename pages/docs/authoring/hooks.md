---
title: Writing hooks
icon: lucide/plug
summary: The product-specific logic a bundle ships, and what the manager does with what it says
---

# Writing hooks

Hooks are executables inside your bundle. They are the only way to add
product-specific logic without changing the manager, which is why the
[ABI](../reference/hooks.md) is a stable contract rather than an implementation
detail.

This page is how to write one. The reference is what you can rely on.

## The shape of a hook

```yaml title="manifest.yaml"
operations:
  migrate:
    kind: hook
    command: ["hooks/migrate"]
    timeout: 10m
```

A hook is a file in your bundle with the executable bit set. A declared hook
that is missing, or one that is not executable, is a **validation error** —
caught by `release verify` in your CI, before an operator ever sees it.

The working directory is the release root, so relative paths to your own files
work. The command is resolved against the release root too: a hook named
`backup` runs *your* `hooks/backup`, never something on `PATH` that shares the
name.

## What a hook is told

Everything arrives as environment variables namespaced with your product name:

```sh
"${DEMO_DATA_DIR:?DEMO_DATA_DIR is required}"
"${DEMO_BACKUP_DIR:?}"
"${DEMO_SECRETS_DIR:?}"
"${DEMO_RESULT_FD:-3}"
```

Use `:?` as above. A hook that silently does nothing because a variable was
empty is much worse than one that fails loudly with the variable's name.

The full list is in the [ABI reference](../reference/hooks.md#environment).

## What a hook says back

Three exit codes, and one file descriptor.

| Exit | Meaning |
| ---: | --- |
| `0` | It did the work |
| `2` | **Nothing to do** |
| other | Failure |

Exit 2 is worth using. It is how `apply` reports "migrations: nothing to run"
rather than implying work happened, and it is what makes an idempotent run
readable.

Structured output goes to **file descriptor 3**, not stdout — stdout is your
log, and a hook whose logging is constrained by the manager's parsing is a hook
you will fight with:

```sh
printf '{"message":"migrated %s -> %s","schema_version":%s}' \
    "$current" "$target" "$target" >&"${DEMO_RESULT_FD:-3}"
```

## The migrate hook

The most consequential one, because what it reports decides whether a rollback
is allowed later:

```sh title="testdata/bundle/hooks/migrate"
--8<-- "testdata/bundle/hooks/migrate"
```

Three things to copy from it:

1. **It reports `schema_version`.** That is how the manager knows what the
   database is at, and it is the number a future rollback checks against your
   `database_schema_max`. A migrate hook that does not report one leaves every
   later rollback decision uninformed.
2. **It exits 2 when the schema is already current**, so a re-`apply` says
   "nothing to run" instead of claiming a migration.
3. **It is idempotent.** `apply` is idempotent, so everything it calls has to
   be. Check the current state and do nothing if it already holds.

!!! warning "A migration cannot be undone by the manager"

    `migrate` is deliberately not compensable. If it fails partway, no automatic
    action the manager knows about can put the database back, so the operation
    ends in `requires-manual-intervention` — exit 12 — and keeps surfacing until
    a human clears it. Write migrations that fail cleanly.

## The backup and restore hooks

```sh title="testdata/bundle/hooks/backup"
--8<-- "testdata/bundle/hooks/backup"
```

The manager creates and owns `BACKUP_DIR`; you write into it and report what you
produced, with checksums, so the backup manifest is self-describing and
verifiable without your tooling.

Restore is the mirror image, and by the time it runs the manager has already
verified the backup, stopped every writer, and obtained a typed confirmation
from the operator:

```sh title="testdata/bundle/hooks/restore"
--8<-- "testdata/bundle/hooks/restore"
```

## Health checks

```yaml
health:
  checks:
    - {name: api, type: http, url: "http://127.0.0.1:18080/health/ready", timeout: 30s}
    - {name: db, type: command, command: ["hooks/check-db"], timeout: 15s}
```

An `http` check runs from the **host**, so the port has to be published by your
Compose file. A `command` check is a hook, for anything HTTP cannot answer:

```sh title="testdata/bundle/hooks/check-db"
--8<-- "testdata/bundle/hooks/check-db"
```

## The smoke test

Runs after health passes, and answers a different question: not "is it up" but
"is it behaving".

```sh title="testdata/bundle/hooks/smoke-test"
--8<-- "testdata/bundle/hooks/smoke-test"
```

A smoke-test failure fails the `apply`, and on an update that means the release
pointer goes back. Make it check something that would actually be broken by a
bad deployment — a rendered config that is missing, a secret that did not
render — rather than something that is true whenever the process started.

## Timeouts and signals

Timeouts come from your manifest. On expiry the manager sends `SIGTERM` to the
**process group**, then `SIGKILL`.

The group, not the process, and that matters to you: a shell script has almost
certainly started children, and signalling only the shell would leave them
running against a database the manager is about to report as untouched.

## Testing your hooks

They are executables with a documented environment, so they test like
executables:

```sh
DEMO_DATA_DIR=$(mktemp -d) \
DEMO_RESULT_FD=1 \
    ./hooks/migrate
```

Setting `DEMO_RESULT_FD=1` puts the structured result on stdout where you can
read it. In production it is 3, precisely so it is not mixed into the log.
