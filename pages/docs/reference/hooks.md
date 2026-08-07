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
| `<PRODUCT>_BACKUP_DIR` | Where a backup hook writes, and a restore hook reads. |
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
