---
title: Hook ABI
icon: lucide/plug
summary: The contract between the manager and the executables a release ships — environment, result descriptor, exit codes, timeouts
---

# Hook ABI

Hooks are executables inside a release: **the only way to add product-specific
logic without changing the manager**. That makes the ABI a public, versioned
contract, and everything in it is deliberately conservative.

- The working directory is the **release root**, so a hook can use relative
  paths to its own files.
- The command is resolved against the release root: a hook named `backup` runs
  the bundle's `hooks/backup`, never something on `PATH` that shares its name.
- Hooks run **only from a release that has already been verified**.
- A declared-but-missing hook, or one without the executable bit, is a
  validation error — caught before the deployment lock is taken.

## Environment

Variables are namespaced per product rather than under a fixed prefix. Hooks
ship inside a product's own release, so the author always knows the name, and
namespacing keeps two products' hooks from colliding.

The prefix is `metadata.name` upper-cased with `-` and `.` replaced by `_`. For
the example bundle, whose product is `demo`, `DATA_DIR` is `DEMO_DATA_DIR`.

| Variable | Meaning |
| --- | --- |
| `<PRODUCT>_PRODUCT` | The product name itself. |
| `<PRODUCT>_INSTALLATION_ID` | Identifies this deployment on this machine. |
| `<PRODUCT>_OPERATION_ID` | The operation the hook is running inside. Appears in the journal and in error output. |
| `<PRODUCT>_OPERATION_TYPE` | `init`, `apply`, `update`, `rollback`, `backup`, `restore`. |
| `<PRODUCT>_PHASE` | Where in the lifecycle this invocation sits — see below. |
| `<PRODUCT>_RELEASE_VERSION` | Version of the release the hook belongs to. |
| `<PRODUCT>_RELEASE_DIR` | Absolute path to the release root. Same as the working directory. |
| `<PRODUCT>_PREVIOUS_VERSION` | The version being replaced, during an update or rollback. |
| `<PRODUCT>_DATA_DIR` | Persistent product data. |
| `<PRODUCT>_BACKUP_DIR` | Where a backup hook writes, and a restore hook reads. Its contents are an ABI — see [below](#what-is-in-backup_dir). |
| `<PRODUCT>_SECRETS_DIR` | The tmpfs directory secrets are rendered to. |
| `<PRODUCT>_CONFIG_FILE` | The rendered configuration file. |
| `<PRODUCT>_COMPOSE_PROJECT` | The Compose project name, for a hook that shells out to `docker compose`. |
| `<PRODUCT>_LOG_LEVEL` | The manager's current log level. |
| `<PRODUCT>_DRY_RUN` | `1` during a plan, `0` otherwise. |
| `<PRODUCT>_RESULT_FD` | The descriptor to write the structured result to. Always `3`. |

`DRY_RUN` is **always present**, including as `0`. A hook that tested for the
variable's existence rather than its value would otherwise mutate during a plan.

### Phases

`PHASE` is one of `preflight`, `pre-update`, `post-update`, `migrate`,
`smoke-test`, `backup`, `restore`, `health-check`.

The same executable may be invoked in more than one phase; branching on `PHASE`
is how one hook serves several.

## What is in `BACKUP_DIR`

`<PRODUCT>_BACKUP_DIR` names a directory, and this page once said nothing about
what is in it. That gap was closed before the first release rather than after,
because once bundles exist in the wild whatever their hooks happen to read
becomes a contract regardless of what any page says.

So this is the contract. It is deliberately narrow.

### What a hook may rely on

| Path | Phase | Meaning |
| --- | --- | --- |
| Whatever the backup hook writes | `backup` | The directory is empty when the hook starts, and yours to fill. Nothing else writes into it during the hook. |
| The same files, by the same names | `restore` | Exactly what your backup hook wrote, decrypted if it was encrypted at rest. |

That is the whole of it. **A restore hook may rely on reading back precisely
what the matching backup hook wrote, and on nothing else.**

By convention a database dump is called `database.sql`, and the example bundle
uses that name — but the manager never reads it, so the name is an agreement
between your two hooks rather than something this tool enforces.

### What a hook may not rely on

The manager also places its own files in the backup directory, alongside the
hook's. **They are not part of this ABI**, they are not guaranteed to be
present, and their names and contents may change:

- `backup.json` — the backup's manifest, which the manager reads. Never
  encrypted, so `backup list` needs no key.
- `installation.yaml`, `application.yaml` — a copy of the operator-facing
  configuration, for an incident review.
- `export.yaml` — the installation export: the authoritative installation
  record, the encrypted secret state, and who can decrypt it. Encrypted to the
  **recovery keys only**, so unlike every other component the machine that
  wrote the backup cannot read it — and neither can a hook running on that
  machine.
- ~~`secrets.sops.yaml`, `secrets.recipients.yaml`~~ — **retired.** `export.yaml`
  carries the same encrypted state byte for byte, and the recipient roles the
  sidecar existed to preserve. Backups taken by earlier versions still have
  them, and a restore still reads them.
- `manifest.yaml` — the release manifest as it was at the time.
- `volumes/<name>.tar` — captured named volumes.
- `.age` suffixes on any of the above, since components are encrypted at rest.

A hook that reads one of these is reading the manager's private bookkeeping.
The reason to say so explicitly is that the alternative is to say nothing and
discover later, from a support ticket, that a vendor built something on a file
that moved.

### Restore is staged

During `restore` the directory a hook is pointed at is **not** the stored
backup — it is a temporary directory holding the decrypted components, created
beside the backup and overwritten however the hook exits. A hook must not write
anything there expecting it to persist, and must not assume the path is stable
between runs.

## The result descriptor

Structured results are written as a single JSON object to file descriptor
**3**, not to stdout. stdout goes to the log and the live view, and a hook
forced to keep its human output free of JSON would be one whose logging is
constrained by the manager's parsing.

Every field is optional. A hook that writes nothing is not in error — but bytes
that are not a JSON object are: the hook tried to report something the manager
cannot hear, and the field that matters most is `schema_version`. Writing
`{"schema_version": "42"}` — a string where the ABI says number — would
otherwise record no schema at all, and a later `rollback` would run without the
check that stops it crossing a migration.

| Field | Type | Meaning |
| --- | --- | --- |
| `message` | string | One-line summary for the operator. Quoted verbatim in a failure. |
| `skipped` | bool | The hook did nothing, while still exiting 0. |
| `schema_version` | int | The database schema this hook left behind. |
| `artifacts` | list | Files the hook produced — see below. |
| `data` | map | Free-form, for hooks with something else to say. |

Each artifact carries `name`, `path`, `size` and `sha256`, so a backup manifest
can be self-describing and verifiable without re-reading the product's own
tooling.

!!! tip "`schema_version` is what makes rollback decidable"

    A migrate hook reporting the schema it left behind is how `rollback` later
    knows whether the previous release can read the database. Asking the product
    afterwards would mean running its tooling to pose a question it already
    answered.

The manager reads at most 1 MiB from the descriptor. A hook that streams
gigabytes into fd 3 is broken, and must not take the manager's memory with it —
the read stops at the bound, which leaves a truncated object, which fails the
hook like any other unreadable result.

## Exit codes

| Exit | Meaning |
| ---: | --- |
| `0` | The hook did its work. |
| `2` | **Nothing to do.** Distinct from success, so `apply` can report "migrations: nothing to run" rather than implying work happened. |
| anything else | Failure. |

A failure quotes what the hook actually said: the `message` from the result
descriptor, or failing that the last few lines of stderr, then stdout.

## Timeouts

Timeouts come from the manifest — `operations.<name>.timeout`, default `10m`.

On expiry the manager sends `SIGTERM` to the **process group**, then `SIGKILL`.
The group, not the process: a hook that is a shell script has almost certainly
started children, and signalling only the shell leaves them running against a
database the manager is about to declare untouched.

## A complete example

The migrate hook from the example bundle. It reports its schema version on the
result descriptor and uses exit 2 to say the schema was already current:

```sh title="testdata/bundle/hooks/migrate"
--8<-- "testdata/bundle/hooks/migrate"
```

Note the shape of the two exits — both write a result, and the `skipped` path
takes exit 2. `apply` will report "nothing to run" rather than claiming a
migration happened.
