# exec-interactive · RFC 0050 P2, `morzer exec --interactive`

On branch `exec-interactive` from `main` at `904d2dfe`, building
[RFC 0050](../rfcs/0050-restarting-a-service-and-an-interactive-exec.md) P2
as ruled by the author on 2026-11-05: RFC 0021 decision 12 reopened, and a
command is still required.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/ports/runtime.go`: `ExecInteractive` and `ExecIO` on `Runtime`,
  beside `Exec`, shaped as `Stats` is: a method every runtime has, which
  answers `domain.ErrUnsupported` when it cannot (decision 6).
- `internal/adapters/runtime/compose/interactive.go`: `compose exec`, with
  `--no-TTY` unless `TTY`, started with this process's streams and left in
  its process group, so the terminal's signals reach it; its context's
  cancellation sends it `SIGTERM` (decision 7).
- `test/fakes/fakes.go`: the fake records the call and returns a set code.
- `internal/lifecycle/ops/inspect.go`: `ExecInteractive`, sharing
  `ExecInService`'s refusal of a service that is not running and its
  journal, with `interactive` in the flags (decision 5).
- `internal/cli/inspect.go`: `-i`; a TTY exactly when stdin and stdout are
  terminals; `--json` refused; the session's signals and context (decision
  7).
- The acceptance run: stdin through a pipe, and a TTY through `script`.
- `exec`'s help, the command reference, `looking-inside.md`, the changelog.

Found while planning, and decided before any code.

The RFC's port method returns the command's exit code and an error, without
saying which carries a command that failed. `Exec` returns a non-zero exit
as data, and the review of #279 asked for the same here.

```divergence
decision: 6
grade: ASSUMED
class: spec-gap
at: 2026-11-05T15:05:00Z
attempt: 1
claim: the RFC does not say whether a command that exits non-zero is an error or a result
evidence: internal/adapters/runtime/compose/compose.go:704-726
action: departed
proposal: ASSUMED — as decision 6. A command that exits non-zero returns its code and a nil error, as `Exec` does; the error is for a session that could not start, a runtime failure, and an unsupported runtime.
```

The shared runner and the log stream start `compose` in a process group of
its own. A session cannot: a process outside the terminal's foreground group
is stopped when it reads the terminal, and does not receive Ctrl-C.

```divergence
decision: 7
grade: ASSUMED
class: discovery
at: 2026-11-05T15:05:00Z
attempt: 1
claim: `compose exec` started the way the adapter starts every other command, in its own process group, cannot read the terminal or receive its signals
evidence: internal/adapters/runtime/compose/stream.go:165-185
action: departed
proposal: ASSUMED — as decision 7. The session's `compose` stays in the manager's process group, and its cancellation signals `compose` alone, not the group, which holds the manager too.
```
