# RFC 0035 — Tasks an operator runs on demand

- **Status:** ✅ Complete — proposed and **built 2026-10-28** from #159: a
  release declares named tasks under `tasks:`, and `morzer task list` and
  `morzer task run` run one as a one-shot container of a release service,
  as an operation. Six entries from the build wait in
  [logs/rfc-0035-tasks-on-demand.md](../logs/rfc-0035-tasks-on-demand.md):
  the output is shown when the task ends rather than as it arrives
  (decision 11), the process exits with the task's own status (decision 12),
  and the task's service is checked against the list `validate-runtime`
  already asks the runtime for, and by name for a service behind a profile
  (decisions 15 and 19). The self-audit added two: a failed task's error
  quotes none of its output, which the journal does not keep (decision 20),
  and the default timeout is every operation's 10 minutes (decision 16).
- **Scope:** A command a release ships for an operator to run by name on a
  deployed machine: filling a sandbox with demo data, unlocking a user,
  rebuilding an index, printing a diagnostic report. Covers how a release
  declares one, which machines may run it, how it runs, what the journal keeps
  of it, and what the operator sees. Deliberately not a host executable (a
  hook), not typed parameters, and not an interactive session.
- **Related:** [`internal/domain/manifest.go`](../internal/domain/manifest.go)
  (`Manifest.Tasks`, `TaskSpec`, `TasksMinManagerVersion`),
  [`internal/lifecycle/ops/task.go`](../internal/lifecycle/ops/task.go)
  (`RunTask`, `ListTasks`), [`internal/cli/task.go`](../internal/cli/task.go),
  [RFC 0018](0018-the-pre-1-0-manifest-surface.md) decision 1 (a field an older
  manager cannot read declares the manager that can),
  [RFC 0021](0021-into-the-running-deployment.md) (`exec`, which this is the
  release-declared counterpart of),
  [hooks reference](../pages/docs/reference/hooks.md#a-migration-service).
- **Discussion:** [#159](https://github.com/morzecrew/morzer/issues/159).

---

## 1. Summary

A release declares tasks by name under a new top-level `tasks:` map. Each names
a service from the release's runtime configuration and the command to run in a
one-shot container of it. `morzer task run <name>` runs it as an operation,
`task`, under the deployment lock: secrets and configuration are rendered as
`apply` renders them, the container runs through the image's entrypoint with the
migration service's environment, and the journal, the attestation and the
notification targets record that it ran. A task may name the machine modes it
is allowed on, so a demo-data seed is refused on production by name.

## 2. Motivation

Products ship one-off commands an operator needs on a deployed machine. Today
the only route is `docker exec` by hand, which needs the operator to know:

- the Compose project and service labels;
- that `exec` skips the image's entrypoint, so a wrapper that loads `*_FILE`
  secrets has to be named by hand;
- that the container must be running.

Nothing reaches the journal or the signed record, the deployment lock is not
taken, and nothing stops a seed meant for a sandbox from running against
production. `morzer exec` (RFC 0021) closes the first two for a command the
operator writes; it cannot close the last, because the manager does not know
what an operator's command is for. The vendor does.

## 3. Current state

- **`operations` is a fixed set.** `migrate`, `smoke_test`, `backup` and
  `restore`; Validate refuses any other name, because an operation under an
  invented name would load and never run.
- **A one-shot container already runs**, for `migrate` as a `runtime-service`:
  `Runtime.RunOneShot` starts the service's dependencies, passes the part of the
  hook ABI that means something in a container (`ports.ServiceEnvVars`), and
  returns a non-zero exit as data, with the output's last lines
  (`ExitResult.Output`).
- **`exec`** runs an operator's command in a running service, journals its argv
  redacted, takes no lock, and propagates the command's own exit code.
- **Strict decoding.** An unknown top-level field is a refusal, so a manager
  older than this one refuses a bundle with `tasks:` as malformed unless the
  bundle declares the manager it needs (RFC 0018 decision 1).

## 4. Goals / Non-goals

**Goals**

- A vendor names a task once; an operator runs it by name without knowing the
  project, the service's labels or its entrypoint.
- A task meant for a sandbox is refused on a production machine, by name, before
  anything runs.
- A task that ran is on the record: journal, attestation, notification.

**Non-goals**

- **Host executables.** A task is a container of the release. A host-side
  command is a hook, and hooks are the release's lifecycle, not an operator's
  menu.
- **Typed parameters.** Arguments pass through as written, or not at all.
- **Interactive sessions.** A shell or a REPL needs a TTY and fits the journal
  badly; `docker exec -it` remains the tool for that.
- **Scheduling.** A task runs when an operator runs it.

## 5. Design

### 5.1 The declaration

```yaml
compatibility:
  min_manager_version: "0.8.0"

tasks:
  seed:
    description: Fill an empty database with demo data
    service: backend
    command: [seed]
    args: true
    modes: [dev]
    timeout: 10m
```

| Field | Required | Meaning |
| --- | :---: | --- |
| `service` | ✅ | The service of the release's runtime configuration to run. |
| `command` | ✅ | The command, replacing the service's own; the image's entrypoint still runs. |
| `description` | | One line, shown by `task list`. |
| `args` | | Whether an operator may append arguments after `--`. Default `false`. |
| `modes` | | The machine modes the task may run on: `dev`, `production`. Absent means every mode. |
| `timeout` | | Time budget. Default `10m`, as every operation's. |

Names follow the product-name shape, `^[a-z][a-z0-9-]{0,62}$`, so a task name is
typed on a command line without quoting.

A manifest declaring tasks must declare `min_manager_version` of at least
`TasksMinManagerVersion`, 0.8.0, mirroring the rule for several migrate steps.
Without it an older manager reports an unknown field rather than an upgrade
requirement.

### 5.2 Running one

`morzer task run <name> [-- args...]`:

1. **Refusals before the lock**: an unknown task (naming the ones the release
   declares), arguments to a task without `args: true`, a machine whose mode the
   task does not list, `--resume`, and an installation with no current release.
   `--force` overrides none of them; each is a statement about safety, not a
   confirmation.
2. **The operation** `task`, under the deployment lock and the unfinished-
   operation gate, through the step engine:
   `load-secrets`, `render-secrets`, `render-configuration`, `validate-runtime`
   — `apply`'s own steps, so a task after a reboot finds the files a converge
   would have rendered — then `run-task`.
3. **`run-task`** refuses a service the runtime configuration does not define,
   naming those it does (a service behind an inactive profile is asked about by
   name first, decision 19), then calls `RunOneShot` with the task's command and
   the operator's arguments, the service environment with `OPERATION_TYPE=task`
   and `PHASE=task`, and the task's timeout. It starts the service's
   dependencies, as a migration does, so a task runs whether or not the
   deployment is up.
4. **A non-zero exit fails the step**, naming the exit code. The step aborts:
   nothing is compensated and nothing is flagged for intervention. The error
   quotes none of the output, unlike a migration service's (decision 20).
5. **The output** is shown when the operation ends, stdout to stdout and stderr
   to stderr, as `exec` shows a command's. The live view tails it while it runs,
   and `--verbose` prints it as it arrives.
6. **The exit status** is the task's own, as `exec`'s is. `--json` carries the
   output, the exit code and the operation record.

`--dry-run` lists the steps and the command the task would run, and runs
nothing.

### 5.3 What is recorded

The journal record carries the task's name, its service, and the operator's
arguments with this installation's known secret values scrubbed — or, when the
secret values cannot be loaded, `args_omitted` saying why, which is `exec`'s
rule. Never the output.

### 5.4 Listing

`morzer task list` shows each task's name, service, the modes it allows,
whether it is allowed on this machine, whether it takes arguments, and its
description. `--json` is the same rows.

### Alternatives considered

- **A fifth name under `operations`.** `operations` are the points in the
  lifecycle the manager runs by convention, and every name there is one the
  manager knows. A task is named by the vendor and run by an operator; mixing
  the two would make every vendor name a near miss of a lifecycle one.
- **No lock, as `exec`.** `exec` takes none so an operator can read state while
  an update runs. A task changes state on the vendor's behalf — a seed, a
  repair — and running one under an update's migrations is the race the lock
  exists to stop.
- **Streaming output to stdout as it arrives.** The live view owns the terminal
  while an operation runs, and raw writes underneath it corrupt the drawing; in
  plain mode stderr is the step narration. Showing it afterwards keeps stdout
  pipeable, which a diagnostic report needs, at the cost of seeing it late in
  plain mode without `--verbose`.
- **Typed parameters.** Validated values would make `args` safe to pass through
  to anything, at the cost of a second parameter system beside the release's
  own. Free arguments, opt-in per task, are what #159 asked for.

## 6. Tests

- Validation: a task with no service, no command, an unknown mode, a malformed
  name, a multi-line description, or a negative timeout is refused; tasks
  without `min_manager_version` 0.8.0 are refused.
- `AllowedOn`: absent modes allow both; `[dev]` refuses production.
- `task run` on a fake runtime: the one-shot is handed the command, the
  operator's arguments, `PHASE=task` and the task's timeout; the record is
  `task`, succeeded, with the name, service and redacted arguments.
- Refusals: unknown task, arguments to a task without `args`, a dev-only task
  on production, `--resume`, an undefined service — and none of them runs a
  container.
- A non-zero exit fails the operation, status `failed`, with none of the
  output in the journal; a dry run runs nothing.
- `task list` rows say which tasks are allowed here.
- The CLI propagates the task's exit code and passes its output through.

## 7. Docs

`reference/manifest.md` gains a `tasks` section and a top-level row.
`reference/commands.md` gains `task list` and `task run`. `reference/hooks.md`,
under "A migration service", says a task's container gets the same variables
with `OPERATION_TYPE` and `PHASE` set to `task`.

## 8. Out of scope

- **Hook tasks** (decision 3). If a vendor needs a host-side task, the shape is a
  `kind` field defaulting to the service kind, so bundles written now keep
  their meaning.
- **Completion of task names.** No command completes a dynamic argument today
  (decision 18).
- **Checking a task's service at `release verify`.** The service list comes from
  the runtime, which `verify` does not run (decision 15).

## 9. Risks

- **A task runs vendor code under the deployment lock.** A slow one holds back
  the backup timer and an unattended update for its timeout. The timeout is the
  vendor's to choose; the lock is what stops the two running against each other.
- **Arguments are the operator's.** `args: true` passes whatever is typed to the
  vendor's command. The vendor opts in per task; the journal records them
  scrubbed. They travel on the `docker compose run` command line, which other
  users on the machine can read, so the reference says a credential does not
  belong there.
- **Output seen late** in plain mode without `--verbose` (decision 11).

## 10. Unresolved questions

None. The three #159 left open are decisions 4, 5 and 6.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | Tasks are a new top-level `tasks:` map, name → `service`, `command`, `description`, `args`, `modes`, `timeout`, and not a name under `operations` (§5.1, alternatives). |
| 2 | `ASSUMED` | A manifest declaring tasks declares `min_manager_version` ≥ 0.8.0 (`TasksMinManagerVersion`), refused otherwise, as RFC 0018 decision 1 asks of a field an older manager cannot decode. |
| 3 | `ASSUMED` | A task is a one-shot container of a release service only. No hook tasks and no `kind` field; one added later defaults to the service kind (§8). |
| 4 | `ASSUMED` | Operator arguments pass through after `--` only when the task says `args: true`, and are refused by name otherwise. No typed parameters. |
| 5 | `ASSUMED` | No TTY and no stdin, as `exec`. Interactive tasks are out of scope. |
| 6 | `ASSUMED` | A task runs whether or not the deployment's services are up; the one-shot starts its dependencies, as a migration does. It needs a current release. |
| 7 | `ASSUMED` | `modes` lists `dev` and `production`; absent allows every mode. A machine whose mode is not listed is refused before the lock, and `--force` does not override it. |
| 8 | `ASSUMED` | A task is an operation, `task`, under the deployment lock and the unfinished-operation gate, through the step engine with `apply`'s render steps; journalled, attested and notified like a converge. Not resumable: `--resume` is refused. |
| 9 | `ASSUMED` | A failed task fails the operation and is not compensated or flagged for intervention. Its effects are the vendor's, the manager cannot undo them, and flagging every failed report for a human would train operators to clear the flag. |
| 10 | `ASSUMED` | Exit 2 means nothing special for a task; any non-zero exit fails it. "Nothing to do" is the migration ABI's, and a task has no caller that reads it. |
| 11 | `ASSUMED` | *(Superseded by decision 21.)* The output is shown when the operation ends, stdout to stdout and stderr to stderr, as `exec`'s; live only in the live view's tail and under `--verbose`. See logs/rfc-0035-tasks-on-demand.md (decision 11, attempt 1). |
| 12 | `ASSUMED` | The process exits with the task's own exit code, as `exec`; `--json` carries the runtime error in the envelope. See logs/rfc-0035-tasks-on-demand.md (decision 12, attempt 1). |
| 13 | `ASSUMED` | The journal keeps the task, its service and the operator's arguments scrubbed of known secret values, or `args_omitted` when they could not be loaded; never the output (`exec`'s rule). |
| 14 | `ASSUMED` | The container gets `ports.ServiceEnvVars` with `OPERATION_TYPE=task` and a new `PHASE` value, `task`. |
| 15 | `ASSUMED` | *Superseded by 19.* The task's service is checked against the services the runtime configuration defines, at run time before the container starts, and not at `release verify`. See logs/rfc-0035-tasks-on-demand.md (decision 15, attempt 1). |
| 16 | `ASSUMED` | The default timeout is `DefaultOperationTimeout`, 10 minutes, as every operation's, filled in by `ApplyDefaults` beside theirs. See logs/rfc-0035-tasks-on-demand.md (decision 16, attempt 1). |
| 17 | `ASSUMED` | No backup is taken before a task. A vendor whose task needs one says so in its description, and `morzer backup` is one command. |
| 18 | `ASSUMED` | No shell completion of task names. No command completes a dynamic argument today, and the first one is its own change. |
| 19 | `ASSUMED` | *(2026-10-28, from the build)* The task's service is checked at run time against the services `validate-runtime` gets from the runtime, and one that list leaves out is asked about by name through the optional `ServiceImager` before it is refused, because `compose config` omits a service behind an inactive profile that `compose run` starts when named. A runtime without the capability gets no check. Not at `release verify`. See logs/rfc-0035-tasks-on-demand.md (decision 15, attempt 2). |
| 20 | `ASSUMED` | *(2026-10-28, from the self-audit)* A failed task's error names the task, its service and its exit code, and quotes none of its output. The error is journalled, so a quote would put the output in the journal decision 13 keeps it out of, and the operator already has the whole of it on their streams and in the `--json` envelope. See logs/rfc-0035-tasks-on-demand.md (unlisted, attempt 1, 19:55). |
| 21 | `LOCKED` | *(2026-10-30)* **Supersedes decision 11.** `task run` prints neither of the task's streams unless asked. `--output` prints them when the task finishes, stdout to stdout and stderr to stderr, with the cut warning; `--verbose` prints them as they arrive and does not repeat them at the end. A task that fails shows the last 10 lines of its stderr, unless `--verbose` already printed them, and decision 20 still keeps them out of the error. `--json` carries both streams whatever the flags, and the exit code is still the task's (decision 12). Printed unasked, an image's entrypoint chatter and a task's progress read as the manager leaking a container's streams after the operation's own result. Ruled by the author 2026-10-30. See #204 and [logs/task-output-on-request.md](../logs/task-output-on-request.md). |

## 12. Phasing

One PR: the manifest field and its gate, the `task` operation, `task list` and
`task run`, the tests in §6 and the docs in §7.
