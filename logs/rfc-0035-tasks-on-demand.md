# RFC 0035 · tasks an operator runs on demand

On branch `release-tasks` from `e1ed187`, building
[RFC 0035](../rfcs/0035-tasks-an-operator-runs-on-demand.md) for #159: a
release declares named tasks under `tasks:`, and `morzer task list` and
`morzer task run` run one as a one-shot container of a release service, as the
operation `task`. The brief's recommendations are the RFC's `ASSUMED` rows; the
two it left to the build are `OPEN` here.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

All three entries were written before the code they govern.

The brief asked for a task's output to be visible live. The live view owns the
terminal while an operation runs, drawing on stderr, and the plain presenter
prints subprocess lines only under `--verbose`; a raw stream underneath either
corrupts the drawing or mixes the report into the step narration. `exec` already
answers the same question by passing the output through when the command ends.

```divergence
decision: 11
grade: ASSUMED
class: spec-gap
kind: departed
at: 2026-10-28T19:43:16Z
attempt: 1
claim: a task's output cannot be streamed to the terminal while the operation runs, because the live view owns the terminal and plain mode keeps subprocess lines for --verbose
evidence: internal/cli/render.go:142-150
action: departed
proposal: ASSUMED — the output is shown when the operation ends, stdout to stdout and stderr to stderr, as `exec`'s; live only in the live view's tail and under `--verbose`.
```

The brief left the process exit status to the build: the task's own, as `exec`
propagates, or the stable code for a failed operation. A task is a vendor
command an operator scripts around, which is `exec`'s case, and `exec` already
carries a container's status out while keeping the envelope's error.

```divergence
decision: 12
grade: OPEN
class: discovery
at: 2026-10-28T19:43:16Z
attempt: 1
claim: the brief delegates whether `task run` exits with the task's status or the failed operation's stable code
evidence: internal/cli/inspect.go:478-487
action: decided
proposal: ASSUMED — the process exits with the task's own exit code, as `exec`; `--json` carries the runtime error in the envelope.
```

The brief asked for the task's service to be checked at validation if that is
cheap, and at run time otherwise. A manifest does not list its services; the
runtime does, and `validate-runtime` asks it and then drops the answer. The run
step reads it from the engine's state instead, so `apply`'s step gains one
`st.Set` and no behaviour.

```divergence
decision: 15
grade: OPEN
class: discovery
at: 2026-10-28T19:43:16Z
attempt: 1
claim: validation cannot know a release's services without running the runtime, and validate-runtime discards the service list it gets
evidence: internal/lifecycle/ops/apply.go:630-636
action: decided
proposal: ASSUMED — the task's service is checked against the services the runtime configuration defines, at run time before the container starts, and not at `release verify`.
```

The list `validate-runtime` keeps is not every service a task can run. `compose
config` leaves out a service whose profile is not active, and a one-off tool
behind a profile is the usual shape of a task's service; `compose run` starts it
when named. Refusing every service missing from that list would refuse the
common case. So a service the list does not name is asked about by name through
the runtime's optional `ServiceImager`, which reaches a profiled service the way
`compose run` does, and is refused only when that answer does not include it. A
runtime without the capability gets no check, and its one-shot reports.

```divergence
decision: 15
grade: ASSUMED
class: discovery
kind: departed
at: 2026-10-28T19:46:09Z
attempt: 2
claim: the validated service list leaves out a service behind an inactive profile, which compose run still starts when named, so checking against it alone refuses a valid task
evidence: internal/adapters/runtime/compose/compose.go:273-276
action: departed
proposal: ASSUMED — the task's service is checked at run time against the services the runtime configuration defines, and a service that list leaves out is asked about by name through `ServiceImager` before it is refused; not at `release verify`.
```

Found by the self-audit, before the PR. The brief asked a failed task to quote
its output's last lines in the error, as a migration service's does, and
decision 13 keeps the output out of the journal, as `exec`'s rule. The two
cannot both hold: the operation's error is journalled with the record, so a
quoted tail is the task's output in the journal. A migration quotes because the
error is the only place its output survives; a task's output reaches the
operator whole, on their own streams and in the `--json` envelope, so the quote
buys nothing there and costs the rule.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-28T19:55:55Z
attempt: 1
claim: quoting a failed task's last output lines puts its output in the journal, which decision 13 keeps out, and the operator already has the whole output
evidence: internal/lifecycle/ops/inspect.go:463-468
action: decided
proposal: ASSUMED — a failed task's error names the task, its service and its exit code, and quotes none of its output: the output is passed through to the operator and carried in the `--json` envelope, and the journal keeps none of it (decision 13).
```

Also found by the self-audit: the brief left the default timeout to the build
("a migration step's 30m or shorter"), and it was decided without an entry. A
migration step's effective default is not 30 minutes: `ApplyDefaults` gives
every operation step `DefaultOperationTimeout`, 10 minutes, before the step's
own `Or(30m)` fallback is reached, and the manifest reference documents `10m`.
A task gets the same through the same function. Recorded late, after the RFC's
first draft said 30 minutes and before any code read it.

```divergence
decision: 16
grade: OPEN
class: discovery
at: 2026-10-28T19:59:05Z
attempt: 1
claim: a migration step's default timeout is DefaultOperationTimeout, 10 minutes, filled in by ApplyDefaults, not the 30 minutes its run step falls back to
evidence: internal/domain/manifest.go:823
action: decided
proposal: ASSUMED — the default timeout is `DefaultOperationTimeout`, 10 minutes, as every operation's, filled in by `ApplyDefaults` beside theirs.
```

## Self-audit, 2026-10-28

Scope: the whole branch before its first commit, 24 files, about 1,650 lines.
Passes 1–10 walked; the suite, the gates and a coverage run over the touched
packages ran.

- **Fixed — a failed task's error quoted its output into the journal**
  (entry above, decision 20). Red without the fix: re-adding the quote fails
  `TestAFailedTaskFailsTheOperationAndKeepsItsOutputOutOfTheJournal`.
- **Fixed — `RunTask`'s comment said every refusal comes before the lock.** The
  undefined-service refusal needs the runtime configuration and ends a
  journalled operation; the comment now says so.
- **Fixed — the reference did not say the arguments are on a world-readable
  command line.** `commands.md` now says a credential does not belong in them.
- **Fixed — two branches without a test.** The `args_omitted` record and the
  rows of `task list`'s view; both now have one.
- **Fixed — the timeout default was an `OPEN` choice with no entry** (above).
- Sabotage: the mode refusal, the argument refusal, the argument scrubbing, the
  operator's arguments reaching the container, and the undefined-service check
  each turn a test red when broken. One sabotage first survived; the `sed`
  pattern had not matched, and the corrected one went red.
- Not run: anything against a real Docker daemon. The profile behaviour of
  `compose config <service>` is taken from the measurement recorded on
  `ServiceImages`, not re-measured here.

**Drift count: 0.**
