# RFC 0021 — Into the running deployment

- **Status:** 📝 Draft
- **Scope:** The commands an operator needs when the deployment is running and
  something is wrong: `morzer logs`, `morzer ps`, `morzer stats` and
  `morzer exec`. Consumes `ports.Runtime.Logs` and `ports.Runtime.Exec` — both
  fully specified, both with **zero callers** — and adds one port method for
  resource statistics. Touches the runtime port, one lifecycle file, four CLI
  commands and the redaction path that stands between vendor output and an
  operator's terminal. Deliberately not a log *management* feature: nothing is
  stored, rotated, shipped or indexed, and `--follow` is a pipe to the runtime,
  not a subscription.
- **Related:** [`internal/ports/runtime.go`](../internal/ports/runtime.go)
  (`Logs`, `Exec`, `Status`, `LogOptions`, `ServiceState`),
  [`internal/adapters/runtime/compose/compose.go`](../internal/adapters/runtime/compose/compose.go)
  (`Logs` at line 462, `startStream`),
  [`internal/infra/logging/logging.go`](../internal/infra/logging/logging.go)
  (the redactor),
  [0015](0015-notifications.md) (the "port with no caller" precedent, and why
  `step.output` never leaves the machine),
  [0019](0019-the-command-surface.md) (where these four commands land in `--help`)

---

## 1. Summary

The manager knows the Compose project name, every Compose file, the environment
they interpolate and which services the release declares. An operator who wants
logs knows none of that, so they read the docs, find `docker compose -p demo
down` in the teardown section, and improvise from there.

Four commands, all read-mostly, all over machinery that already exists:

- **`morzer logs [service…]`** — `--follow`, `--tail`, `--since`, streaming
  through the redactor.
- **`morzer ps`** — the service table `status` already computes, on its own,
  without the health, release and lock sections around it.
- **`morzer stats`** — CPU, memory, and I/O per container, once or `--watch`.
  The one place a new port method is needed.
- **`morzer exec <service> -- <argv>`** — a command inside a running service,
  journalled with a redacted argv like every other thing an operator does to a
  machine. Not an interactive shell: the port returns a buffered result with no
  TTY, and §5.4 says what that costs and where it would be fixed.

## 2. Motivation

### Two port methods, fully specified, never called

```text
$ grep -rn "\.Exec(ctx\|\.Logs(ctx" --include='*.go' internal/ cmd/ \
    | grep -v _test | grep -v '^internal/ports/' | grep -v 'func ('
(no output)
```

That is every call site of `Runtime.Exec` and `Runtime.Logs` outside the port
that declares them, the adapter that implements them and the tests. `Logs` is
implemented in the Compose adapter with `--no-color`, `--follow`, `--tail` and
`--since` handling and a dedicated streaming path (`startStream`, the one method
that deliberately does not go through the shared runner). `Exec` is implemented
too. Neither has ever run outside a test.

This is precisely the shape [0015](0015-notifications.md) found in
`ports.Notifier`: a careful interface, a real adapter, and no call site — so the
guarantees in the doc comments describe behaviour that has never happened on a
customer's machine. There the fix was an adapter; here the adapter exists and the
missing half is the command.

Their neighbours are called, which is what makes this an omission rather than a
port written ahead of its time: `Status` has five call sites, and `Stop`/`Start`
are used by the volume-capture path in
[`hookbackup/volumes.go`](../internal/adapters/backup/hookbackup/volumes.go) to
quiesce writers before reading a volume. Only these two were declared,
implemented, and never wired to anything.

### What an operator does instead

The documentation's only instruction for reaching the runtime directly is in the
teardown section of the first-deployment guide:

```sh
docker compose -p demo down
```

That works because the project name happens to equal the product name in the
example. In general it does not: `manifest.runtime.project` defaults to
`metadata.name` and a vendor may set it to anything. The manager exports
`<PRODUCT>_COMPOSE_PROJECT` into *hooks* — a bundle author can shell out
correctly, and the operator standing at the terminal cannot, because nothing
prints it.

So the operator's path to a log line is: find the release root under
`/opt/<product>/releases/<version>`, find which Compose files the profile
selected, reconstruct the environment the manager interpolates, and run
`docker compose` with all of it. Every step is knowledge the manager already has
and does not offer.

### The thing this fixes is the 3 a.m. path

`status` reports which services are running and whether they are healthy.
`doctor` reports whether the machine is configured correctly. Neither answers
"why is this container restarting", and that question is the reason someone is
logged in.

## 3. Current state

| Thing | State |
| --- | --- |
| `Runtime.Logs` | Declared with `LogOptions{Services, Follow, Tail, Since}`; implemented in Compose; **no caller** |
| `Runtime.Exec` | Declared, returns `ExitResult`; implemented; **no caller** |
| `Runtime.Status` | Declared, implemented, called 5× (status, apply ×2, doctor ×2) |
| Resource statistics | Nothing. No port method, no adapter code, no CLI |
| `ServiceState` | `Name`, `Image`, `State`, `Health`, `ExitCode`, `Status` |
| Runtime config assembly | `Deps.runtimeConfig(rel, inst, profile)` — project, files, working dir, env |
| Redaction | `logging.Redactor`, registered with the installation's secret values, applied to log records |
| Event forwarding | `events.KindStepOutput` is explicitly **not** forwarded to notifiers: raw vendor subprocess output, and the no-secrets claim rests on a redactor that has been wrong once |
| Journalling | Every mutating operation gets an `OperationRecord`; read-only commands get none |

## 4. Goals / Non-goals

**Goals**

- Reach logs, process state and resource use for an installation without knowing
  the Compose project, the file list or the environment.
- One command to get a shell in a service, with the fact that it happened
  recorded.
- Output that behaves in a pipe: no colour, no spinner, line-oriented, and a
  clean exit on SIGINT.

**Non-goals**

- **Log storage, rotation, search or shipping.** The Docker daemon's logging
  driver owns that, the machine's journal owns the manager's own logs, and a
  manager that stored a second copy would be the thing filling the disk it
  monitors. *What would change this:* nothing — a customer needing central logs
  configures a driver, and that is a `doctor` check at most.
- **A dashboard.** `stats --watch` redraws a table; it does not grow graphs,
  history or thresholds.
- **Reaching a service's network from the operator's shell** (`port-forward`).
  Published ports are declared in the manifest and already reachable; a tunnel to
  an unpublished one is a different security posture and its own RFC.
- **Anything mutating beyond `exec`.** No `morzer restart <service>`: converging
  is `apply`'s job, and a restart that bypassed it would leave a container
  running a definition the manager did not choose. `exec` is included because it
  is how an operator reads state (`psql`, `redis-cli`), and its mutation risk is
  the operator's own command.

## 5. Design

### 5.1 `morzer logs`

```text
morzer logs [service…] [--follow] [--tail N] [--since DURATION|TIME] [--no-redact]
```

Resolves the current release, builds the runtime config the same way `apply`
does, and streams `Runtime.Logs` to stdout. Defaults: `--tail 100` when not
following, everything when following, all services when none are named.

**Scope is the Compose project, not the manifest's service list.** A vendor's own
Compose file may start a sidecar the manifest never names, and an operator
debugging wants what is *there*. The consequence is stated rather than avoided:
shell completion for the service argument offers the project's containers, which
is a superset of what the manifest declares.

**Option parsing, so it is not decided three times:** `--since` takes either a
duration (`10m`, `2h`, Go's `ParseDuration`) anchored at the moment the command
starts, or an RFC 3339 timestamp; a timestamp without a zone is refused rather
than assumed local, because "which midnight" is exactly the question a log query
must not guess. `--tail` with `--follow` is valid and means the backlog to print
before following. An unparseable value is a usage error naming both accepted
forms.

Three things this must get right, because they are where a log command is
usually wrong:

**Redaction is on by default, and it is stateful across reads.** Container logs are vendor-controlled output that
may contain what the vendor logged — including a connection string the manager
itself wrote into a config file. The redactor already exists and already knows
this installation's secret values; the stream passes through it line by line.
`--no-redact` exists because a truncated password in a log is confusing when an
operator is debugging *the secret itself*, and it requires the same confirmation
posture as any other "show me the thing" flag: it prints a warning to stderr
naming what is disabled.

This follows the rule 0015 set for `step.output`, and inverts its conclusion for
a reason: there, raw subprocess output must never leave the machine because the
destination is a chat channel; here it must reach the operator's own terminal,
which is the machine's console. The redactor is the compromise both directions
share.

A line-by-line filter over a stream is not enough, because a secret can straddle
a read boundary and neither half matches. The filter therefore buffers to a line
boundary before matching, with a bound (64 KiB) on how much it will hold for a
line that never ends — and when the bound is hit it **fails closed**: the
oversized fragment is dropped with a marker rather than emitted unmatched. A
redactor that gave up and passed the bytes through would be one that leaks
exactly when a service prints something enormous, which is not a coincidence
worth risking.

**Following is a pipe, and a pipe ends.** SIGINT closes the reader and exits 0 —
an operator pressing Ctrl-C has finished reading, not failed. A runtime that
dies mid-stream is a non-zero exit with the runtime's own message.

**No lock.** Reading logs must never queue behind an update; it is most wanted
*during* one.

`--json` streams one JSON object per line rather than the single-envelope
contract every other command uses, because a stream has no end at which to write
an envelope. This is the only exception, and it is a contract of its own:

```json
{"ts":"2026-08-10T09:12:33Z","service":"app","container":"demo-app-1","line":"…"}
```

Redaction applies exactly as it does to the human stream — a `--json` consumer is
not more trusted than a terminal. A diagnostic the manager itself needs to emit
(the runtime died, the stream ended early) goes to **stderr** as an ordinary
error, never as a record in the stream: a consumer parsing lines must not have to
distinguish the vendor's output from the manager's opinion about it. The exit
code is the contract for "did this end cleanly"; `0` on SIGINT, non-zero when the
runtime failed.

### 5.2 `morzer ps`

The `[]ports.ServiceState` slice `status` already computes, rendered on its own:

```text
$ morzer ps
SERVICE   STATE     HEALTH     IMAGE                        STATUS
app       running   healthy    ghcr.io/demo/app@sha256:8a…  Up 3 hours
db        running   healthy    postgres@sha256:1f…          Up 3 hours
worker    exited    -          ghcr.io/demo/worker@sha256…  Exited (1) 2 min ago
```

No new lifecycle code: `ops.GetStatus` already builds this, and `ps` is a view
over the part of it that answers "what is running". It exists because `status`
answers four questions at once, and an operator watching a crash loop wants one
of them repeatedly.

### 5.3 `morzer stats`

The one genuinely new capability, and therefore the one new port method:

```go
// Stats reports resource use per service, sampled once.
//
// A sample, not a stream: the caller decides the cadence, and a port that
// returned a channel would put the refresh policy in the adapter where a
// second implementation would choose differently.
Stats(ctx context.Context, cfg RuntimeConfig) ([]ServiceStats, error)

type ServiceStats struct {
    // Service is the Compose service. Container and Replica identify which
    // one, because `docker stats` reports containers and a scaled service
    // has several -- a row keyed by service alone would silently show one
    // replica's numbers under the service's name, or three rows that look
    // like duplicates.
    Service   string `json:"service"`
    Container string `json:"container"`
    Replica   int    `json:"replica,omitempty"`

    CPUPercent  float64 `json:"cpu_percent"`
    MemoryBytes int64   `json:"memory_bytes"`
    MemoryLimit int64   `json:"memory_limit,omitempty"`
    NetRxBytes  int64   `json:"net_rx_bytes"`
    NetTxBytes  int64   `json:"net_tx_bytes"`
    BlockRead   int64   `json:"block_read_bytes"`
    BlockWrite  int64   `json:"block_write_bytes"`
}
```

**One row per container, never an aggregate.** Summing is the caller's decision
and the sums are not all meaningful: memory adds, CPU percentages add, and a
memory *limit* does not. `morzer stats` prints one row per container and a
`total` line for the two that add; `--json` emits the rows and no total, because
a machine reader can add and cannot un-add.

The Compose adapter implements it with `docker stats --no-stream --format json`
scoped to the project's containers. `--no-stream` matters: the streaming form
emits a first sample of zeros before its first interval, and a `stats` that
printed zeros for CPU would be reporting an idle machine that is on fire.

`--watch[=DURATION]` re-samples on an interval and redraws: default 2s, floor 1s
(a lower one measures the sampler), no ceiling beyond the command's own timeout.
In plain mode it appends a block per sample instead of redrawing, because a log
that rewrites itself is not a log. A failed sample prints the error and keeps
watching — a daemon hiccup must not end a watch an operator is staring at — and
two consecutive failures exit non-zero. SIGINT exits 0, as with `logs --follow`.

`--watch` with `--json` is refused: the single-envelope contract cannot describe
a stream, and `logs` is the one exception this design is willing to carry. The
scripted form is a loop around single-shot `stats --json`, which is also the form
that composes with `sleep`.

A runtime that cannot report statistics returns `domain.ErrUnsupported` and the
command says so by name — the same shape `ChannelPeeker` uses in
[0016](0016-update-checking-and-unattended-updates.md), rather than an empty
table that looks like an idle deployment.

### 5.4 `morzer exec`

```text
morzer exec <service> -- <command> [args…]
```

Wraps `Runtime.Exec`, with the release's runtime config, and:

- **One form, and it names a command.** There is no bare
  `morzer exec <service>` opening a shell, because the port cannot deliver one:
  the Compose adapter runs `exec --no-TTY <service>` and returns a buffered
  `ExitResult` ([`compose.go:346`](../internal/adapters/runtime/compose/compose.go)),
  so there is no stdin, no TTY and no streaming. A command that printed a
  prompt nobody could answer would be worse than not having it. An interactive
  session needs `Exec` to grow a TTY and stdin lifecycle, which is P4 below and
  a port change this RFC does not smuggle in.
- **Everything after `--` is the argv inside the container, and nothing else.**
  The adapter appends it *after* the service name, so a `--user` written there
  reaches the process rather than `docker compose exec`. Runtime-level options
  therefore need typed fields on `RunOptions`-style options that the adapter
  places before the service name; none is added here, which is the same answer
  as before by a correct route.
- **The exit code is the command's.** An operator's `psql -c 'select 1'` that
  fails must fail the invocation, so `ExitResult.ExitCode` becomes the process's
  exit status rather than being flattened into "the manager succeeded".
- **It is journalled, and the argv is redacted before it is written.** The
  journal is what tells a later reader that a human was in there at 03:14. What
  it must not become is a store of the credentials they typed:
  `morzer exec db -- psql 'postgresql://u:p@host/db'` puts a password in an argv,
  and `/proc` exposure is a separate problem from a file this manager writes and
  keeps. The existing `logging.Redactor` — which already holds this
  installation's secret values — runs over the argv, and a token that is not a
  known secret is beyond what any redactor can do, which the docs say rather
  than implying the journal is safe to publish. Output is never recorded.
- **It refuses a service that is not running**, naming the state, rather than
  letting the runtime produce its own error about a container that does not
  exist.
- **No `--user root` convenience.** A manager flag that made privilege
  escalation a keystroke would be a manager opinion about something it cannot
  audit.

**Alternatives considered.** *Leave `exec` out entirely and document `docker
compose exec`.* Rejected: it is the same knowledge problem as logs — project
name, files, environment — and an operator who has to reconstruct that
by hand will reconstruct it wrong under pressure. *Name it `shell`.* Rejected:
the common case is one command, not an interactive session, and a name that
implies a session makes the scripted use look like an abuse of it.

### 5.5 What binds all four

Every one of them needs the same three things: the installation, the current
release, and a runtime config. That is `Deps.runtimeConfig(rel, inst, profile)`,
which exists and is used by five operations. The lifecycle addition is one file
holding the four operations, each of which resolves the release and delegates —
so a runtime other than Compose gets all four the moment it implements the port.

None of them takes the deployment lock. All four are things an operator does
*while* something else is happening, which is exactly the case a lock would
break.

## 6. Tests

- **`logs` reaches the runtime with what the operator asked for.** A fake runtime
  records `LogOptions`; `--tail 5 --since 10m app` arrives as those three values.
  The alternative — asserting on output — passes on a command that ignores its
  flags and streams everything.
- **Redaction, verified red.** A secret value registered with the redactor,
  emitted by the fake runtime's log stream, must not appear in stdout. Then the
  same with `--no-redact`, asserting it does appear *and* that the warning
  reaches stderr — a redaction test that only tests the on case cannot tell
  redaction from a stream that dropped the line.
- **SIGINT during `--follow` exits 0** and closes the reader; a leaked reader is
  a hung `morzer logs` in someone's terminal.
- **A runtime with no `Stats`** produces the named refusal, not an empty table.
- **`exec` propagates the exit code**, including 0, 1 and 127.
- **A secret split across two reads is still redacted.** The fake runtime emits
  the value in two writes with the boundary inside it; stdout must not contain
  it. This is the test the line-by-line implementation passes only by accident.
- **An unterminated line past the bound is dropped, not emitted.** Fail-closed,
  asserted rather than assumed.
- **`exec`'s journal entry contains no known secret value**, with one registered
  and passed in the argv.
- **`exec` on a stopped service refuses**, naming the state.
- **`exec` is journalled with its argv** and without its output.
- **Container lane:** `logs` against a real Compose project returns lines the
  fixture's service printed, and `stats` returns a non-zero memory figure for a
  running container. The fakes cannot prove either — this is the lane that owns
  "the flags we pass are flags Docker accepts".

## 7. Docs

- `pages/docs/operating/looking-inside.md` (new) — the four commands, when each
  is the right one, and the redaction default.
- `pages/docs/reference/commands.md` — flags and exit codes.
- `pages/docs/reference/output-modes.md` — the streaming exception for
  `logs --json`.
- `pages/docs/get-started/first-deployment.md` — the teardown snippet gains a
  pointer, since it is currently the only place the docs reach for `docker`
  directly.

## 8. Out of scope

- **`morzer restart <service>`.** Converging is `apply`; a restart outside it
  leaves a container the manager did not choose. *What would change this:*
  evidence that operators are running `docker compose restart` anyway, which
  would mean the gap is real and the answer is a command that converges one
  service.
- **Log filtering by pattern.** `grep` exists and composes.
- **`stats` history or alerting.** A metrics system's job; the manifest already
  lets a vendor ship an exporter.
- **Attaching to a service's stdin beyond `exec`'s TTY.** `docker attach`
  semantics on a supervised container are a way to accidentally kill it.

## 9. Risks

- **`exec` is a shell on the box, offered by the tool that manages the box.**
  It does not grant anything an operator with the manager binary lacks — they can
  already run `docker` — but it makes it easy, and easy is what gets used at 3
  a.m. under pressure. Mitigated by the journal entry and by refusing to add
  privilege-escalation conveniences. Stated here so the trade-off is recorded
  rather than discovered.
- **Redaction is best-effort against vendor output.** The redactor matches known
  secret values; a service that logs a *derived* value (a token minted from a
  secret) is not covered and cannot be. The docs must say this plainly rather
  than implying logs are safe to paste into a ticket.
- **A streaming JSON mode is a second output contract.** One exception, named in
  the reference, or every future streaming command invents its own.
- **`docker stats` is expensive on a busy daemon.** One sample per invocation
  bounds it; `--watch` at a sane floor (1s) bounds the rest.

## 10. Unresolved questions

Both of the questions this section opened with — the service scope for `logs` and
whether redaction survives a partial line — are now decided in §5.1 (the project,
not the manifest) and §5.6 (buffered to a line boundary, bounded at 64 KiB, fails
closed). What remains:

- **Does `stats` need a `--service` filter?** One row per container is right for a
  small deployment and noisy for a scaled one. A filter is trivial to add and
  trivial to add *later*; nothing in the design depends on the answer.
- **Should `exec`'s TTY support (P4) reuse `RunOneShot`'s options or grow its
  own?** Both carry "run something in a container"; only one needs a stdin
  lifecycle. This is a port-shape question that P4 must answer before it starts,
  not before this RFC is accepted.

## 11. Decisions

| # | Decision | Why |
| --- | --- | --- |
| 1 | Four commands over the existing port, not a general `morzer docker …` passthrough | A passthrough would make every Docker flag part of this project's surface and its compatibility promise. Four named operations are four contracts a second runtime can implement. |
| 2 | Redaction on by default, `--no-redact` explicit and warned | Container logs are vendor output that may echo the manager's own secrets. 0015 forbade forwarding raw output to a chat channel for this reason; the operator's terminal earns the output, the redactor is the compromise. |
| 3 | `Stats` samples once; cadence belongs to the caller | A port returning a stream puts refresh policy in the adapter, where a second implementation would choose differently. `--no-stream` also avoids Docker's zero-CPU first sample. |
| 4 | An unsupported capability is refused by name (`domain.ErrUnsupported`) | Same shape as `ChannelPeeker`. An empty table is indistinguishable from an idle deployment, which is the wrong thing to show someone diagnosing load. |
| 5 | None of the four takes the deployment lock | They are what an operator runs *while* something else is happening. A lock here would make the tools useless exactly when they are needed. |
| 6 | `exec` is journalled with its argv, never its output | The journal's job is that a human was in there and what they asked for. Recording output would put arbitrary vendor data — and whatever the operator's command printed — into the manager's own record. |
| 7 | `exec` propagates the command's exit code | A manager that returned 0 for a failed command inside the container would make `morzer exec` unusable in a script. |
| 8 | No `restart`, no `port-forward`, no log storage | Each is either `apply`'s job, a different security posture, or the daemon's. Named so the additions are decisions rather than drift. |
| 9 | `logs --json` streams one object per line; the single-envelope rule gains exactly one documented exception | A stream has no end at which to write an envelope. One exception written down beats every future streaming command inventing its own. Manager diagnostics go to stderr, never into the stream, so a consumer never has to tell the vendor's output from the manager's opinion of it. |
| 10 | `stats --watch` is refused with `--json`; the scripted form is a loop around single-shot `--json` | The exception in decision 9 is one this design will carry once. A second streaming contract for a table that redraws buys nothing `sleep` does not. |
| 11 | `stats` reports one row per container with service, container and replica; never an aggregate | `docker stats` is per-container, so a scaled service is several rows. Keying on the service alone would print one replica's numbers under the service's name. Summing is the caller's decision and not all the sums mean anything — memory adds, a memory limit does not. |
| 12 | `exec` has one form and it names a command; no bare `morzer exec <service>` shell | The port cannot deliver one: the adapter runs `exec --no-TTY` and returns a buffered result. A prompt nobody can answer is worse than no prompt, and a TTY is a port change (P4) rather than something to smuggle into a command's help text. |
| 13 | Everything after `--` is the container argv; runtime options need typed fields, and none is added | Verified against the adapter: argv is appended *after* the service name, so a `--user` written there reaches the process, not `docker compose exec`. The first draft of this RFC claimed otherwise. |
| 14 | `exec`'s argv is redacted before it reaches the journal | A password in an argv is the ordinary case (`psql 'postgresql://u:p@host/db'`), and the journal is a file this manager writes and keeps. The redactor already holds the installation's secrets; what it cannot catch is said plainly rather than implied away. |
| 15 | Stream redaction buffers to a line boundary, bounded at 64 KiB, and fails closed | A secret straddling a read boundary matches neither half. A filter that gave up and passed the bytes through would leak precisely when a service prints something enormous. |
| 16 | `--since` takes a duration or an RFC 3339 timestamp; a timestamp with no zone is refused | "Which midnight" is exactly what a log query must not guess. |

## 12. Phasing

- **P1 — `logs` and `ps`.** The two that need no new port surface, over adapter
  code that already exists. `logs` carries the redaction work, which is the
  substantive part.
- **P2 — `exec`.** Journalling, exit-code propagation, the running-service
  refusal. Separable and the one with the security discussion, so it lands on its
  own.
- **P3 — `stats`.** Port method, Compose implementation, `--watch`, the
  unsupported refusal, and the container-lane test that proves the flags are
  real.
- **P4 — An interactive `exec`, if it earns it.** `Runtime.Exec` grows a TTY and
  a stdin lifecycle, the adapter stops hardcoding `--no-TTY`, and
  `morzer exec <service>` gains its shell form. Deliberately last and
  deliberately conditional: it is the only piece here that changes a port, and
  three phases of use will say whether anyone wants it.
