---
title: Looking inside
icon: lucide/search
summary: logs, ps, stats and exec — the four commands for the moment something is running and something is wrong
---

# Looking inside a running deployment

`morzer status` says what is deployed and whether it is up. `morzer doctor` says
whether the machine is configured correctly. Neither answers *why is this
container restarting*, and that question is usually the reason somebody is
logged in at all.

Four commands answer it, all read-only, none of them taking the deployment lock:

| Command | The question |
| --- | --- |
| [`morzer logs`](#logs) | What did it say? |
| [`morzer ps`](#ps) | What is running? |
| [`morzer stats`](#stats) | What is it using? |
| [`morzer exec`](#exec) | What does it think is in the database? |

The reason they exist rather than a page telling you to run `docker compose` is
that `docker compose` needs three things you would have to reconstruct: the
project name (`runtimes.compose.options.project`, which defaults to the product
name and need not equal it), the list of Compose files the deployment profile
selected, and the environment the manager interpolates into them. Reconstructing
that by hand under pressure is how an operator ends up looking at the wrong
deployment.

None of the four takes the deployment lock, deliberately. They are what you run
*while* an update is in flight, which is exactly the case a lock would break.

## logs

```sh
morzer logs                      # the last 100 lines
morzer logs app db               # two services
morzer logs --follow             # until you press ctrl-C
morzer logs --since 15m          # the last quarter of an hour
morzer logs --tail 0             # the whole retained backlog
```

| Flag | Meaning |
| --- | --- |
| `--follow`, `-f` | Keep the stream open. Ctrl-C ends it and exits 0 — you finished reading, nothing failed. |
| `--tail` | Lines of history. Default 100, and the whole backlog with `--follow`. `0` means all of it. |
| `--since` | A duration back from now (`10m`, `2h`) or an RFC 3339 instant **with a zone** (`2026-08-10T09:12:33Z`). |
| `--no-redact` | Stream the bytes the container wrote, unfiltered. |

The scope is the Compose **project**, not the service list in the manifest. A
sidecar the vendor's own Compose file starts is included, because what an
operator debugging wants is what is there.

Nothing is stored, rotated, shipped or indexed. The Docker daemon's logging
driver owns that, and a manager keeping a second copy would be the thing filling
the disk it monitors.

### Redaction, and what it cannot do

This installation's secret values are scrubbed from the stream before it reaches
your terminal. A product that logs its own connection string — with a password
the manager itself generated and wrote into a config file — is the ordinary case,
not a pathological one.

The filter holds bytes until a line ends before matching, so a value split
across two reads is still caught. A line longer than 64 KiB is **dropped** with a
marker rather than passed through unmatched: a filter that gave up on the hard
case would leak precisely when a service prints something enormous.

It is best effort and the limit is worth stating plainly. The redactor matches
values it has been told about. A service that logs something *derived* from a
secret — a token minted from it, a hash, a partial — is not covered and cannot
be. **Logs are still not something to paste into a ticket unread.**

`--no-redact` turns it off, prints a warning to stderr saying so, and exists for
one case: debugging the secret itself, where a redacted value is the thing you
are trying to look at. If the secret state cannot be read at all — a missing age
key, a machine mid-recovery — the stream is shown anyway with a warning that
nothing could be scrubbed. Refusing would take the tool away at the moment it is
most wanted, and pretending the stream was filtered would be worse than either.

### The one streaming contract

`morzer logs --json` is the single exception to the [one-object
rule](../reference/output-modes.md): it emits one JSON object per line, and no
envelope. A stream has no end at which to write one.

```json
{"ts":"2026-08-10T09:12:33.481Z","container":"demo-app-1","service":"app","line":"listening on :8080"}
```

`ts` is the instant the **container** wrote the line, not the moment the manager
read it. `service` is empty for a line the runtime itself wrote about the stream
— `demo-app-1 exited with code 0` — which is often the line that explains why
the rest stopped, so it is passed through rather than dropped.

Redaction applies exactly as it does to the human stream: a `--json` consumer is
not more trusted than a terminal. Anything the manager needs to say about the
stream goes to **stderr**, never into it, so a consumer parsing lines never has
to tell the vendor's output from the manager's opinion of it. The exit code is
the contract for whether the stream ended cleanly.

## ps

```sh
morzer ps
```

```text
SERVICE   CONTAINER      STATE    HEALTH    IMAGE                STATUS
✓ app     demo-app-1     running  healthy   ghcr.io/demo/app     Up 3 hours
✓ app     demo-app-2     running  starting  ghcr.io/demo/app     Up 12 seconds
✓ db      demo-db-1      running  -         postgres             Up 3 hours
✗ worker  demo-worker-1  exited   -         ghcr.io/demo/worker  Exited (1) 2 minutes ago
```

The same table `morzer status` draws under its `services` heading, on its own.
It exists because `status` answers four questions at once and an operator
watching a crash loop wants one of them repeatedly.

The container column is what tells two replicas of one service apart, so it is
never dropped on a narrow terminal. A `-` in the health column means the service
declares no healthcheck, which is not a verdict.

## stats

```sh
morzer stats
morzer stats --watch --interval 5s
```

```text
SERVICE  CONTAINER      CPU     MEMORY          NET I/O          BLOCK I/O
app      demo-app-1  12.34%    67MiB / 512MiB  1.2KiB / 3.3KiB   8KiB / 0
app      demo-app-2   0.50%    32MiB / 512MiB            0 / 0      0 / 0
db       demo-db-1    3.10%            128MiB  1.2KiB / 3.3KiB          -
total                15.94%            227MiB
```

One row per **container**, never an aggregate per service. `docker stats`
reports containers, so a scaled service is several rows — and one row under the
service's name would be one replica's numbers wearing the whole service's label.

The total line covers the two figures that add: CPU and memory. A memory *limit*
does not add, so the total stops there. `--json` emits the rows and no total: a
machine reader can add and cannot un-add.

A `-` is not a zero. Block I/O is unaccounted under a rootless daemon, which is
an ordinary configuration — and a container that has written nothing also
reports zero, so the two must not print the same thing. In `--json` those fields
are `null`.

The memory limit shown for a container with no limit of its own is the host's
memory. That is what the runtime reports, and inventing "unlimited" from it
would be the manager guessing.

### Watching

`--watch` re-samples on a timer, floor 1 second — below that the reading is
mostly the sampler, since `docker stats` walks every container's cgroup. A
failed sample prints its reason and the watch continues; two consecutive
failures end it non-zero, because by then it is not a hiccup.

At a terminal it redraws one frame. **In a pipe or a journal it appends a block
per sample**, which is the opposite of what
[`status --watch`](../reference/commands.md#watching) does — that one is refused
without a terminal. The difference is deliberate: a status watch is a dashboard
with nothing worth keeping, while a sequence of samples is a time series, and
`morzer stats --watch > samples.txt` for ten minutes is a real way to catch a
leak.

`--watch` with `--json` is refused. Loop around `morzer stats --json` instead,
which is also the form that composes with `sleep`.

## exec

```sh
morzer exec db -- psql -U demo -c 'select count(*) from users'
morzer exec app -- ls -la /var/lib/demo
```

Everything after `--` is the command line inside the container and nothing else.
The exit code is the command's own, so this works in a script:

```sh
if ! morzer exec db -- pg_isready -q; then
    echo "the database is not accepting connections"
fi
```

It is **not an interactive shell.** There is no TTY and no stdin, so a command
that prompts will wait for an answer nobody can give. One command, named on the
command line.

There is no `--user`, and no shortcut for running as root. A manager flag that
made privilege escalation a keystroke would be a manager opinion about something
it cannot audit. A service that is not running is refused by name rather than
left to produce a message about a container that does not exist.

### It is written down

Every `exec` is journaled — the operation type, the service, the argv and the
exit code — because the fact that a human was inside the deployment at 03:14 and
what they asked it to do is exactly what an incident review needs, and nothing
else in the journal would carry it.

The argv is redacted first, with this installation's known secret values
scrubbed. That matters because a password in an argv is the ordinary case:

```sh
morzer exec db -- psql 'postgresql://demo:hunter2@localhost/demo'
```

On a machine whose secret state will not decrypt, the redactor knows no values —
so the record keeps the service, the exit code and the time, and carries
`argv_omitted` in place of the command line:

```json
{"type":"exec","flags":{"service":"db","exit_code":"0",
 "argv_omitted":"this installation's secret values could not be loaded, so the command line could not be scrubbed before writing it down"}}
```

Three answers rather than two, and the third is the point. Refusing to journal
would lose the fact that a human was inside the deployment, which is the
record's whole job. Refusing the command would take `exec` away on a machine
somebody is already debugging. So the record keeps what it can promise is clean
and says what it dropped.

What the redactor cannot catch is a credential the manager has never been told
about — one you pasted from a password manager. **An argv is still not the place
for a secret,** on this machine or any other: `/proc` exposes it to every local
user while the command runs, which is a separate problem from the file this
manager writes and keeps.

The output is never recorded. It is arbitrary vendor data plus whatever your
command printed, and a journal holding it would be a second copy of the
product's data in a file nobody thinks of as one.

## What these are not

- **Not a log management feature.** No storage, rotation, search or shipping;
  configure a logging driver for that.
- **Not a dashboard.** `stats --watch` redraws a table. It grows no graphs, no
  history and no thresholds.
- **Not a way into an unpublished port.** Published ports are declared in the
  manifest and already reachable; a tunnel to one that is not is a different
  security posture.
- **Not a way to restart one service.** Converging is
  [`apply`](../reference/commands.md#apply)'s job, and a restart outside it
  would leave a container running a definition the manager did not choose.
