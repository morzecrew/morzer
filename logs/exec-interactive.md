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

From #281's review.

The CLI detached the context and caught SIGINT before the deployment was
resolved and the service checked, so a Ctrl-C during those checks was dropped
and the command still started. Decision 7's hold belongs to the command alone.

```divergence
decision: 7
grade: ASSUMED
class: discovery
at: 2026-11-05T19:57:00Z
attempt: 2
claim: a session's hold on the signals, taken before the checks, swallowed a Ctrl-C meant to stop the command from starting
evidence: internal/lifecycle/ops/inspect.go:369-380
action: departed
proposal: ASSUMED — as decision 7, with the hold taken only once the command starts and given back when it ends: the checks before it stay the root handler's and cancellable.
```

Decision 7 caught `SIGTSTP` with `SIGINT` and `SIGQUIT`, as `morzer shell`
does. Without a TTY, a caught Ctrl-Z stopped `compose` and left the manager
waiting on it, so the shell never got the terminal back.

```divergence
decision: 7
grade: ASSUMED
class: discovery
at: 2026-11-05T19:57:00Z
attempt: 2
claim: catching SIGTSTP during a session without a TTY suspends the command and not the manager, which hangs the session
evidence: internal/cli/inspect.go:540-565
action: departed
proposal: ASSUMED — as decision 7 for `SIGINT` and `SIGQUIT`, caught and dropped; `SIGTSTP` is left at its default, so Ctrl-Z suspends the session and the manager together and `fg` resumes both. With a TTY it is a byte for the container's terminal either way.
```

From #282's review.

The `SIGTERM` the manager forwards reaches `docker`, and Docker's CLI passes a
termination signal on to its Compose plugin only when its stdout is not a
terminal; at a terminal it leaves the plugin to the signals the terminal sends
the whole foreground group (`cmd/docker/docker.go`, `tryTerminatePlugin`, in
docker/cli v28.5.2 and v29.0.0). The acceptance run's step had its output on
`/dev/null`. Run against Docker 29.7.2 with stdout on a pty, a `SIGTERM` to
`docker compose exec`'s `docker` left `docker` and the plugin both running;
through a pipe, both ended.

```divergence
decision: 7
grade: ASSUMED
class: spec-gap
at: 2026-11-05T22:58:00Z
attempt: 3
claim: a SIGTERM to the manager does not end a session whose stdout is a terminal, since Docker's CLI does not pass it to the Compose plugin; `docker` is killed after 10 seconds and the plugin is left running
evidence: internal/adapters/runtime/compose/interactive.go:41-42
action: departed
proposal: ASSUMED — refines decision 7. A SIGTERM to the manager ends a session only when `compose`'s stdout is not a terminal. At a terminal, `docker` is killed after 10 seconds, the session is journalled with the kill's 137, and the plugin is left running; a hangup or a shutdown signals the plugin itself, so only a signal sent to the manager by PID meets this. Accepted as Docker's own behaviour rather than worked around.
```

Attempt 3's entry was wrong about the plugin and too sure of the kill. Run
again on Docker 29.7.2 with Compose 5.5.1 at a pty, the plugin outlived the
`SIGTERM` but ended within 3 seconds of `docker`'s `SIGKILL`, whose death
closes the plugin socket; the exec'd command was still running in the
container. And a command that finishes inside the grace lets `docker` exit
on its own, with no kill.

```divergence
decision: 7
grade: ASSUMED
class: spec-gap
at: 2026-11-05T23:18:00Z
attempt: 4
claim: at a terminal the Compose plugin does not outlive `docker`'s kill, and the kill happens only when the command is still running after the grace
evidence: internal/adapters/runtime/compose/interactive.go:41-42
action: departed
proposal: ASSUMED — refines decision 7, replacing attempt 3's proposal. A SIGTERM to the manager ends a session promptly only when `compose`'s stdout is not a terminal. At a terminal, unless the command finishes first, `docker` is killed after 10 seconds and the session is journalled with the kill's 137; its death closes the plugin socket, which ends the plugin, and the command in the container can still be running.
```
