# RFC 0041 — Files an operator passes to a task

- **Status:** 📝 Draft — proposed, and ruled by the author 2026-10-30: §11's
  `OPEN` rows as recommended, except that a task finds its input through both
  the placeholder and an environment variable (decision 2). **P1 is ready to
  build**
- **Scope:** Named file inputs for a release's tasks. A task declares `inputs`
  in the manifest, and `morzer task run <task> --input <name>=<path>` copies
  each file into a per-operation directory, mounts it read-only into the
  one-off container, and puts its in-container path where the task's `command`
  names `{inputs.<name>}`. It covers the manifest shape and its `release
  verify` checks, staging and cleanup, size limits, placeholder substitution,
  validating the operator's path, what the journal and the attestation record,
  `--dry-run`, and the manager-version requirement. It refines RFC 0035
  decisions 1, 4, 5, 8 and 13 and reverses none: there is still no stdin and no
  TTY. It does not add directory inputs, stdin, streaming from another machine,
  or files for `exec`.
- **Related:** [`internal/domain/manifest.go`](../internal/domain/manifest.go)
  (`TaskSpec`, `checkTask`);
  [`internal/lifecycle/ops/task.go`](../internal/lifecycle/ops/task.go);
  [`internal/ports/runtime.go`](../internal/ports/runtime.go) (`RunOptions`);
  [`internal/adapters/runtime/compose/compose.go`](../internal/adapters/runtime/compose/compose.go)
  (`RunOneShot`);
  [0018](0018-the-pre-1-0-manifest-surface.md) decision 1;
  [0032](0032-secrets-a-non-root-container-can-read.md);
  [0035](0035-tasks-an-operator-runs-on-demand.md).
- **Discussion:** issue #215.

---

## 1. Summary

```yaml
tasks:
  restore:
    service: worker
    command: [restore, "{inputs.snapshot}"]
    inputs:
      snapshot:
        description: Catalogue snapshot (JSONL)
        max_size: 2GiB
```

```sh
morzer task run restore --input snapshot=./catalog-snapshot.jsonl
```

The manager copies `./catalog-snapshot.jsonl` into
`<var>/manager/inputs/<operation-id>/snapshot`, hashing it as it copies, and
mounts that file read-only at `/morzer/inputs/snapshot` in the task's one-off
container. The command runs as `restore /morzer/inputs/snapshot`. When the
operation ends, the copy is removed. The journal records the input's name,
size and sha256, never its content; whether the attestation does too is
decision 12.

## 2. Motivation

A task cannot take a file from the operator (#215). The ai-stylist bundle needs
a 63 MB JSONL snapshot inside its worker to fill an empty catalogue. Today:

- `task run` gives the task no stdin (RFC 0035 decision 5).
- `args` carry strings, and the container cannot see host paths.
- A distroless image has no shell to `docker cp` into and run from.
- `docker cp` into a volume needs the Compose project name and a running
  container, the runtime detail RFC 0035 set out to hide.

The workaround the vendor tried, a read-only bind mount of a directory under
the product's data directory, made the operator learn a host path inside
morzer's tree and its file mode, and put a bind mount in the bundle that
backups never capture.

## 3. Current state

- **A task** is `description`, `service`, `command`, `args`, `modes` and
  `timeout` ([`manifest.go:645-664`](../internal/domain/manifest.go)), checked
  by `checkTask` ([`manifest.go:1530`](../internal/domain/manifest.go)). A
  manifest declaring tasks declares `min_manager_version` 0.8.0, because strict
  decoding makes a new field fatal to an older manager
  ([`manifest.go:681-686`](../internal/domain/manifest.go), RFC 0018 decision 1).
- **The one-off container** gets `Argv`, `Env`, a timeout and `--rm`, and
  nothing else ([`runtime.go:202-210`](../internal/ports/runtime.go),
  [`compose.go:474-485`](../internal/adapters/runtime/compose/compose.go)).
  There is no mount option. Argv and env are world-readable through `/proc`, so
  neither ever carries a secret ([`compose.go:480-482`](../internal/adapters/runtime/compose/compose.go)).
- **`task run`** joins the task's command and the operator's arguments into
  argv ([`task.go:260`](../internal/lifecycle/ops/task.go)) and journals the
  arguments scrubbed of known secret values, or `args_omitted`
  ([`task.go:226-233`](../internal/lifecycle/ops/task.go)).
- **Disk.** `/run/<product>` is a tmpfs, where secrets are rendered
  ([`paths.go:165`](../internal/domain/paths.go)); the manager's own state is on
  disk under `<var>/manager` ([`paths.go:128-129`](../internal/domain/paths.go)),
  and `freeSpace` is how an operation checks room before writing
  ([`ops.go:235`](../internal/lifecycle/ops/ops.go)).
- **Sizes** parse through `ByteSize` (`4GiB`, `512MB`;
  [`scalars.go:111`](../internal/domain/scalars.go)).
- **File ownership in a container** has a precedent: RFC 0032 gives a rendered
  secret an optional numeric `owner`.

## 4. Goals / Non-goals

**Goals**

- An operator passes a local file to a declared task with one flag, without
  knowing a host path, a project name or a file mode.
- The task sees exactly the bytes whose digest the journal records.
- The bundle declares what it accepts, and `release verify` checks it.

**Non-goals**

- **Stdin**, **directories** and **streaming from another machine**: decisions
  7, 8 and 9.
- **Files for `exec`.** `exec` runs in a running service and takes no lock; a
  staged file needs an operation to own and clean it.
- **Persisting the file.** A task that wants the data kept writes it where the
  release stores data; the input itself is gone when the operation ends.

## 5. Design

### 5.1 The manifest

```yaml
inputs:
  <name>:                 # [a-z][a-z0-9_]*, as a parameter name
    description: <text>   # single line, shown by `task list`
    max_size: <ByteSize>  # optional
```

Every declared input is required in P1 (decision 4). `release verify` refuses:

- an input name outside `[a-z][a-z0-9_]*`;
- a `description` that is not a single line, or a `max_size` that does not
  parse;
- a `{inputs.<name>}` in `command` that names no declared input;
- an input no `{inputs.<name>}` in `command` names, since the task could never
  find its path;
- a manifest whose tasks declare `inputs` without `min_manager_version` at
  least the release that reads them (decision 3).

The JSON schema gains the field through `tools/schemagen`. The manifest API
stays `selfhost/v1alpha1`: the change is additive.

### 5.2 The command line

`--input <name>=<path>`, repeatable, one per declared input. Refused before the
lock, exit 2: an undeclared name, a declared input not given, a name given
twice, a path that is not a regular file once resolved (a directory, a FIFO,
a device), and a file larger than its input's `max_size`. A symlink is followed: the operator is root already, so
following it reaches nothing they could not read. The file is opened once, and
the type check and the copy both use that descriptor. A descriptor keeps the
file's identity, not its contents, so the size checked here is only a first
answer: §5.3 bounds the copy itself.

### 5.3 Staging

A new first step of the `task` operation, **`stage-inputs`**:

1. Check `freeSpace` under `<var>/manager/inputs` for the sum of the sizes and
   a margin.
2. Create `<var>/manager/inputs/<operation-id>/` (the parent `0700`, the
   operation's directory `0555` once filled) and copy each file to
   `<name>`, mode `0444`, computing sha256 as it copies. The copy reads through
   a bound: the input's `max_size` when it declares one, and the free-space
   budget from step 1 in any case. A file that grew past the bound after it was
   checked is refused and its partial copy removed. The size and digest are
   those of the bytes copied, which are the bytes the task sees.
3. Keep `name`, `size` and `sha256` for the journal.

The directory is removed when the operation ends, success or failure, as a
deferred cleanup and as the step's compensation. A manager killed mid-task
leaves it behind. The next `task run` removes every directory under
`<var>/manager/inputs` whose operation is not running, and `doctor` reports
any it finds.

Copy rather than bind-mount the operator's file: the bytes cannot change under
the task after their digest is taken, the operator's file can live where the
container's user could never read it, and 63 MB copies in well under a second.
The cost is twice the disk for the task's duration, which the free-space check
covers.

### 5.4 Into the container

`RunOptions` gains `Mounts []Mount{Source, Target string; ReadOnly bool}`, a
runtime port change that the runtime contract suite covers. The Compose adapter
renders each as `--volume <source>:<target>:ro`. Each input is mounted at
`/morzer/inputs/<name>`, a path the manager chooses: the operator's file name
never reaches the container.

`{inputs.<name>}` in the task's declared `command` is replaced by that path
before `run-task` builds argv. Only the declared command is substituted, never
the operator's `--` arguments, so no operator-supplied text is ever templated.
Nothing passes through a shell: the command is exec form, and the substituted
value is a fixed path with no spaces.

Inside the container the file is `0444`, so the image's user reads it whatever
its uid, in a directory mounted read-only. On the host, the `0700` parent keeps
other users out (decision 5).

### 5.5 Record

The journal's flags are a string map (`OperationRecord.Flags`,
[`operation.go:146`](../internal/domain/operation.go)), so each input is one
flag, `input.<name>`, with the value `<size> sha256:<hex>`. Existing readers of
the journal see one more string key and nothing else. The host path is not
recorded: it names the operator's machine, not the content, and the digest is
what identifies the content.

The attestation predicate is closed: v2's schema sets
`additionalProperties: false` and `Predicate` has no input field
([`attestation.go:90-100`](../internal/domain/attestation.go)). Listing inputs
there means a predicate v3 and a new schema, as v1 to v2 did in 0.9.0
(decision 12). Neither record ever holds content.

### 5.6 `--dry-run`

Runs §5.2 and §5.3's checks (names, regular files, sizes, free space), copies
nothing, and the plan names each input with its size and the path it would
have.

### 5.7 Alternatives considered

- **Bind-mount the operator's file in place.** No copy, but the file can change
  after its digest is taken, and a file in `/root` or a home directory may be
  unreadable to the container's user.
- **Stage under `/run`.** A tmpfs: a 2 GiB input would sit in memory.
- **An env var per input instead of a placeholder.** Works for a task that
  reads its env, not for a vendor command that takes a path argument, which is
  the case #215 brings. A later row can add both.
- **A `docker cp` into a running service.** Needs the stack up, a shell or a
  known path in the image, and leaves the file behind.

## 6. Tests

- Domain: each `release verify` refusal in §5.1, and the `min_manager_version`
  requirement.
- CLI: each refusal in §5.2, including a FIFO and a directory.
- Suite: a task given an input sees the bytes at `/morzer/inputs/<name>`, the
  journal records name, size and digest, and the staging directory is gone
  after success and after a failed task.
- Suite: a `max_size` refusal comes before the lock, and a free-space refusal
  before anything is copied.
- Suite: a file that grows past its `max_size` while it is copied is refused,
  and no partial copy is left behind.
- Suite: `--dry-run` copies nothing.
- Runtime contract: a read-only mount is readable and not writable in the
  one-off container.
- Real Docker: a non-root image reads a 63 MB input.

## 7. Docs

- `manifest.md`: `tasks.<name>.inputs`.
- `commands.md`: `task run --input`.
- The tasks how-to gains passing a file.

## 8. Out of scope

- **Directory inputs**, **stdin**, **remote streaming**: decisions 7, 8 and 9.
- **Optional inputs**: decision 4.
- **Inputs for migrations or hooks.** They run unattended; nobody is there to
  pass a file.

## 9. Risks

- **Disk.** A large input doubles its disk use for the task's duration.
  Bounded by the free-space check and `max_size`.
- **A leftover copy after a kill.** Swept by the next `task run` and reported
  by `doctor`; until then a sensitive file sits under a `0700` root directory.
- **Another runtime.** A runtime added under RFC 0023 gets the same port field
  and must render it, or refuse a task with inputs.

## 10. Unresolved questions

The `OPEN` rows in §11 are the author's to rule. Nothing else blocks the design.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `LOCKED` | **Build named file inputs now?** Recommended yes: #215 is a vendor need with no supported workaround, and the design adds one port field and one step. It refines RFC 0035 decisions 1 (a task gains `inputs`), 4 (still no typed parameters: inputs are files), 5 (still no stdin), 8 (the attestation lists inputs, if decision 12 rules so) and 13 (the journal records them). Was `OPEN`; ruled by the author 2026-10-30: build it. |
| 2 | `LOCKED` | **How the task finds the file.** Recommended: `{inputs.<name>}` in the declared `command`, replaced by a fixed in-container path, and never in the operator's `--` arguments. Alternative: an env var per input (`MORZER_INPUT_<NAME>`), or both. Was `OPEN`; ruled by the author 2026-10-30: both. `{inputs.<name>}` in the declared `command` is replaced by the in-container path, and the one-off container also gets `MORZER_INPUT_<NAME>`, the name upper-cased, set to the same path. The variable reaches an input that no placeholder names, so §5's refusal of such an input no longer applies. |
| 3 | `ASSUMED` | A manifest whose tasks declare `inputs` declares `min_manager_version` at least the release that ships this, as `TasksMinManagerVersion` does for `tasks:` (RFC 0018 decision 1). |
| 4 | `ASSUMED` | Every declared input is required in P1. An optional input would need its placeholder to vanish when absent, which turns one argv element into a conditional. |
| 5 | `ASSUMED` | **Who can read the file in the container.** Recommended: `0444` in a read-only mount, so any image user reads it, with a `0700` root parent on the host. Alternative: an optional numeric `owner` per input, as RFC 0032 gives secrets, with `0400`. Was `OPEN`; ruled by the author 2026-10-30: as recommended. |
| 6 | `ASSUMED` | The file is copied, not bind-mounted in place, into `<var>/manager/inputs/<operation-id>/`, hashed as it is copied, after a free-space check, and removed when the operation ends. A run killed mid-task leaves it for the next `task run` to sweep and `doctor` to report. |
| 7 | `ASSUMED` | **Stdin as a single unnamed input.** Recommended no: RFC 0035 decision 5 stays, the live view owns the terminal, and a named input gets a declared path, a size check and a digest stdin cannot give before it is read. Was `OPEN`; ruled by the author 2026-10-30: as recommended, no. |
| 8 | `ASSUMED` | **A directory as an input.** Recommended no in P1: the operator archives it and the task unpacks it. A directory needs a symlink policy inside it, a total size and a tree digest. Was `OPEN`; ruled by the author 2026-10-30: as recommended, not in P1. |
| 9 | `ASSUMED` | **Streaming an input from another machine.** Recommended: out of scope. morzer runs on the machine it manages and has no remote client; `scp` then `--input` is two commands. Was `OPEN`; ruled by the author 2026-10-30: as recommended, out of scope. |
| 10 | `ASSUMED` | The journal records each input as one `input.<name>` flag, `<size> sha256:<hex>`, in the existing string map; never content and never the host path. |
| 11 | `ASSUMED` | `--input` paths are resolved, must be regular files, and are checked and copied through one open descriptor. Symlinks are followed: the operator is already root. |
| 12 | `LOCKED` | **Whether the attestation lists inputs.** The v2 predicate is closed (`additionalProperties: false`), so listing them is a predicate v3 and `schemas/morzer-attestation-v3.json`. Recommended: v3 with an `inputs` list of name, size and sha256, because an input changes the deployment's data and the statement is what an auditor reads; `attest verify` keeps reading v1 and v2. Alternative: the journal only in P1, and v3 when something else needs it. Was `OPEN`; ruled by the author 2026-10-30: as recommended, predicate v3 with an `inputs` list. |
| 13 | `ASSUMED` | The copy is bounded by the input's `max_size` and by the free-space budget, not by the size seen at the check. A file that grows past the bound while it is copied is refused, and the size and digest recorded are those of the bytes copied. |

## 12. Phasing

- **P1**: §5 in full, the decisions as ruled, the docs in §7. One PR.
- Directory inputs and remote streaming come back only on a vendor asking.
