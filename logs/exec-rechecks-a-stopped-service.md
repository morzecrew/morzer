# Exec rechecks a stopped service · a failed command asks the runtime again

On branch `exec-rechecks-a-stopped-service` from `167af1a`, for
[#97](https://github.com/morzecrew/morzer/issues/97). The acceptance job once
got exit 1 and Compose's `service "db" is not running` from
`morzer exec db -- true` after `docker compose stop db`, where it expected
exit 7 and the manager's refusal.

**The CI failure's cause is unreproduced.** The issue named two candidates. Both
were tried against Docker 29.7.2 and Compose 5.5.1: 300 rounds of
`compose stop db` followed at once by `compose ps --all --format json`, with a
`db` service shaped like the fixture's (`restart: unless-stopped`, a
healthcheck).

- `ps` lag: `db` was never reported `running` after `stop` returned.
- A second `db` entry: `ps --all` never listed one.

A one-off container from `compose run` would also fit, since `ps` can list it
and `exec` ignores it. Nothing in the acceptance flow starts one at that point,
and the manager's own one-shots run with `--rm`.

What is reproduced is the defect the issue calls unacceptable either way: the
pre-check and the exec are two calls. A container that stops between them makes
Compose exit 1 with its own message, and the manager passed that off as the
command's exit code. `TestExecRefusesAServiceThatStoppedAfterThePreCheck` was
red before the fix.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-19T12:23:54Z
attempt: 1
claim: exec's pre-check and the exec are separate runtime calls; a service that stopped between them reached the operator as Compose's exit 1 rather than the manager's refusal with exit 7
evidence: internal/lifecycle/ops/inspect.go:400
action: decided
proposal: ASSUMED — a command that exits non-zero is followed by the same running check; a service no longer running is refused with exit 7 after the command's output is printed, and the exec stays journalled, because a command that ran until its container stopped looks the same
```

## As built

- `ExecInService` runs `confirmRunning` again when the command exits
  non-zero, and returns its refusal with the command's output attached.
- The CLI prints that output before the error, so a command that ran until its
  container stopped still shows what it printed.
- If the CI cause is stale `ps` output, the second check comes later and is
  likelier to see the stop, but it is not guaranteed to. #97 stays open for
  that.
