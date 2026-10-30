# task-output-on-request · `task run` prints a task's output only when asked

On branch `task-output-on-request` from `1776f1f`, for #204. Operating 0.9.0,
the author saw a seed task's entrypoint chatter and summary printed after the
operation's own result and read it as the manager leaking the container's
streams. They ruled on 2026-10-30 that `task run` prints neither stream unless
asked, superseding [RFC 0035](../rfcs/0035-tasks-an-operator-runs-on-demand.md)
decision 11, which is now decision 21.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Both entries were written before the code they govern.

```divergence
decision: 11
grade: ASSUMED
class: spec-gap
kind: departed
at: 2026-10-30T07:40:00Z
attempt: 1
claim: printing a task's whole output unasked once the operation ends reads as the manager leaking the container's streams; the author ruled it shown only with --output or --verbose, or as a failure's last stderr lines
evidence: internal/cli/task.go:108-123
action: departed
proposal: LOCKED — decision 21 supersedes decision 11, as the ruling in #204 states. Accepted by the author 2026-10-30.
```

The ruling says a failed task "still shows the tail of its stderr" and leaves
two things to the build: how long the tail is, and what happens when the
runtime cut the stream. Ten lines, the runtime's own bound on what an operator
watching the run read last (`outputTailLines`). And a cut stream's last lines
are the last of what was kept rather than of what the task printed, so they
carry the same warning `--output` gives.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-30T07:45:00Z
attempt: 1
claim: the failure tail needed a length and an answer for output the runtime cut
evidence: internal/cli/task.go:118-122
action: decided
proposal: ASSUMED — a failed task shows the last 10 lines of its stderr, followed by the cut warning when the runtime kept only the start; folded into decision 21.
```
