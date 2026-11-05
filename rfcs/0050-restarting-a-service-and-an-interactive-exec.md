# RFC 0050 — Restarting a service, and an interactive exec

- **Status:** 📝 Draft — proposed 2026-11-05, and ruled by the author
  2026-11-05: RFC 0021 decisions 8 and 12 reopened, and §11's `OPEN` rows
  as recommended. Ready to build.
- **Scope:** Two commands RFC 0021 left out on purpose. `morzer restart
  <service>…` re-creates named services from the installed release, under
  the deployment lock, and journals it. `morzer exec --interactive` gives
  `exec` a stdin and, at a terminal, a TTY, which is RFC 0021's P4. It
  covers the port method an interactive session needs, what the journal
  records, and what each command refuses. Not covered: `port-forward`,
  `docker attach` semantics, a `--user` flag, and restarting the whole
  deployment, which is `stop` and `start` (RFC 0040) or `apply`. One new
  port method, `Runtime.ExecInteractive`; `Runtime.Exec` is unchanged.
- **Related:** [0021](0021-into-the-running-deployment.md) (decision 8, "no
  `restart`"; decision 12, "no bare shell"; §8's condition for a restart;
  P4; §10's port question); [0040](0040-keeping-an-installation-down.md)
  (`stop` and `start`); [0025](0025-attesting-an-installation.md) (which
  operations attest);
  [`internal/ports/runtime.go`](../internal/ports/runtime.go) (`Exec`,
  `Restart`, `RunOneShot`);
  [`internal/adapters/runtime/compose/compose.go`](../internal/adapters/runtime/compose/compose.go)
  (`Exec`, `Restart`);
  [`internal/lifecycle/ops/inspect.go`](../internal/lifecycle/ops/inspect.go)
  (`ExecInService`);
  [`internal/lifecycle/ops/config.go`](../internal/lifecycle/ops/config.go)
  (`recreate`).

---

## 1. Summary

`morzer restart app` re-creates the `app` service from the installed release,
forced, converging its dependencies as any `up` does, waits for the
release's health checks,
and journals a `restart` operation. It takes the deployment lock and refuses
an installation `morzer stop` took down.

`morzer exec -i db -- psql` runs the command with this terminal's stdin, and
with a TTY when stdin and stdout are both terminals, through a new port
method. The command is still required. No timeout applies, and the journal
records the argv, redacted, as it does for `exec` today.

Both reopen decisions RFC 0021 made without grades, which count as `LOCKED`:
decision 8 (no `restart`) and decision 12 (no interactive form). The author
reopened both on 2026-11-05 and ruled §11's three questions as recommended:
`restart` re-creates (decision 1), `exec -i` still needs a command (decision
2), and `restart` is journalled, not attested (decision 3).

## 2. Motivation

The Phase 3 plan names both. RFC 0021 named what would justify each.

For `restart`, §8: "evidence that operators are running `docker compose
restart` anyway, which would mean the gap is real and the answer is a
command that converges one service." The operating docs say there is no way
to restart one service (`looking-inside.md:249-251`), and `apply` re-creates
nothing whose definition has not changed, so a hung service leaves an
operator with `docker compose restart`. That needs the project name, the
files and the environment the manager renders, the reconstruction the same
page says these commands exist to spare them (`looking-inside.md:23-29`),
and it lands nowhere in the journal.

For an interactive `exec`, P4: "if it earns it". `exec` today cannot run
`psql` without `-c`, an editor, or anything that asks a question. The help
says so ("a command that prompts will wait for an answer nobody can give"),
and the operator's remaining route is `docker compose exec`, again by hand
and unjournalled. An `exec` capped at five minutes
(`compose.go:705`) also cannot follow a long migration check or a
`pg_dump` piped to a file.

## 3. Current state

- **`exec`.** `ExecInService` refuses a service that is not running, arms the
  redactor, calls `Runtime.Exec`, and journals an `exec` record with the
  argv redacted and never the output (`inspect.go:355-420`). The Compose
  adapter runs `exec --no-TTY <service> <argv…>` with a five-minute limit and
  returns a buffered `ExitResult` (`compose.go:705`). The CLI prints stdout
  as it came back, unredacted (`cli/inspect.go:604`), and propagates the exit
  code. No lock (RFC 0021 decision 5).
- **Restarting.** `Runtime.Restart` exists and runs `compose restart
  <services…>` (`compose.go:490-496`). Its one caller restarts the services
  that declare a changed secret, from the CLI, unjournalled
  (`cli/secret.go:530`). A plain restart keeps the container, so it does not
  pick up anything that needs re-creating.
- **Re-creating.** `config set` and a domain change re-create named services
  with `ForceRecreate` through `d.recreate` (`config.go:434`), inside a
  journalled operation, after rendering. That is "converging one service" in
  everything but name.
- **Stopped installations.** `stop` records itself, and operations that would
  start something refuse while it holds (`stop.go:226`,
  `refuseIfStoppedNow`).

## 4. Goals / Non-goals

**Goals**

- An operator bounces one service without leaving the manager, and the
  journal says they did.
- An operator runs a command that needs a terminal, or reads stdin, inside a
  running service.
- Nothing scripted today changes: `exec` without the new flag behaves as it
  does now, byte for byte.

**Non-goals**

- Restarting everything. `stop` then `start`, or `apply`, already do it, with
  their own semantics.
- Running as another user. RFC 0021 decision 13 stands: everything after
  `--` is the container's argv.
- Attaching to a service's main process. RFC 0021 §8: `docker attach` on a
  supervised container is a way to kill it.

## 5. Design

### 5.1 `morzer restart <service>…`

```console
$ morzer restart app
$ morzer restart app worker
```

One or more services, by name; none is a usage error that points at `stop`
and `start`. A name the installed release does not start, an unknown one or
one behind an inactive profile, is refused by name before anything runs.

Decision 1 decides what a restart does. The recommendation is to re-create:
render nothing new, and call `d.recreate` with the installed release and
the named services, forced. Their dependencies are converged as any `up`
converges them, and not forced: one that is down is started, one whose
container no longer matches the release is re-created, and one that matches
is left alone. In the state `apply` leaves, every dependency matches, so
only the named services are replaced; a dependency changed by hand is put
back, as the next `apply` would put it back. `--no-deps` is not passed:
under it a dependency that is down is neither started nor resolved, and the
restarted service waits on it, which the Compose adapter measured when
`RunOneShot` stopped passing the flag (`compose.go:552-561`). The container is
replaced from the definition the manager chose, which is what RFC 0021 §8
asked of it. A drifted container (one changed by hand) comes back as the
release defines it, and a container's writable layer does not survive,
which is true of every re-create `apply` does. The alternative is
`Runtime.Restart`, a plain `compose restart`: faster, keeps the container
and its writable layer, and brings back whatever the container was,
including a hand change.

Either way it is an operation: journalled as `restart`, under the
deployment lock (it changes the deployment, so RFC 0021 decision 5 does not
apply), refused while the installation is stopped, and followed by the
release's health checks, as `apply`'s last step waits for them. A health
check that fails fails the operation, and there is nothing to compensate:
the previous container is gone either way. The summary names the services
and says the health checks passed.

Whether it attests is decision 3. The recommendation is no: RFC 0025
attests what moves a version or a configuration, and a restart moves
neither. The journal records it, which is what an operator reading the
history asks.

### 5.2 `morzer exec --interactive`

```console
$ morzer exec -i db -- psql -U demo
$ morzer exec -i db -- psql -U demo < migrate.sql
$ morzer exec -i app -- sh
```

`-i` (`--interactive`) attaches this process's stdin. A TTY is allocated
when stdin and stdout are both terminals, and not otherwise, so a pipe or a
redirect gets a byte stream and a terminal gets a terminal. There is no
separate `-t`: a TTY with a non-terminal stdin is an error Docker already
gives (`the input device is not a TTY`), and one without stdin has nothing
to type into.

The command is still required (decision 2). The recommendation keeps RFC
0021 decision 13 whole: everything after `--` is the container's argv, and
the operator names the shell. The alternative is a bare `morzer exec -i
<service>` that runs `/bin/sh`, which saves four characters and fails on a
distroless image with Docker's message rather than the manager's.

Unchanged from `exec`: the service must be running, the argv is journalled
redacted and the output never, the exit code is the command's, and no lock
is taken. Different: no timeout, since a session lasts as long as the
operator; `--json` is refused, since there is no result to wrap; and the
journal record carries `interactive: true`.

### 5.3 The port

RFC 0021 §10 asked whether P4 reuses `RunOneShot`'s options or grows its
own. Its own, as a separate method, so `Exec`'s buffered contract, which
scripts and the journal's output rule depend on, does not change shape:

```go
// ExecInteractive runs a command in a running service with this process's
// streams. TTY asks for a terminal; the caller sets it only when stdin and
// stdout are terminals. Returns the command's exit code.
ExecInteractive(ctx context.Context, cfg RuntimeConfig, service string,
    argv []string, io ExecIO) (int, error)

type ExecIO struct {
    Stdin          io.Reader
    Stdout, Stderr io.Writer
    TTY            bool
}
```

A runtime that cannot run one returns `domain.ErrUnsupported`, refused by
name as RFC 0021 decision 4 does for `stats`. The Compose adapter runs
`compose exec` (with `--no-TTY` unless `TTY`) and hands it the streams. With
a TTY it hands over the terminal itself: `compose` puts it in raw mode,
handles resizing, and the manager forwards no bytes. Without one it copies
the streams.

Ctrl-C belongs to the session, as it does in `morzer shell`
(`cli/shell.go:96-106`). For as long as the session runs, the manager
catches `SIGINT`, `SIGQUIT` and `SIGTSTP` and does nothing with them; it
does not ignore them, because an ignored signal is inherited through exec,
and `compose` would then shrug off Ctrl-C too. The terminal sends the
signal to the foreground process group, which `compose` is in. Without a
TTY, Ctrl-C therefore ends `compose exec`; what reaches the command inside
the container is Docker's to decide. With a TTY, `compose` holds the
terminal in raw mode, and Ctrl-C is a byte the command reads. The session
runs under a context the root handler's cancellation does not reach
(`context.WithoutCancel`), so a Ctrl-C ends the session and the manager
journals it afterwards, rather than killing `compose` through the context
mid-session.

That context drops the root handler's `SIGTERM` too, so the session handles
`SIGTERM` itself: a `SIGTERM` sent to the manager, which a terminal never
sends, is forwarded to `compose`, the session ends, and the manager journals
it and exits with the command's status. A `SIGKILL` leaves `compose` running
with nobody to journal it, as it would leave any child.

### 5.4 Alternatives considered

- **`restart` as `Runtime.Restart` (decision 1's alternative).** It is what
  the secret path already does and it is quicker. It does not converge: a
  container someone changed by hand comes back changed, which is the case
  RFC 0021 declined `restart` over.
- **Widening `Exec` with a stdin and a TTY.** One method instead of two, at
  the cost of a contract every caller and fake already depends on: a
  buffered result with output the journal must not see.
- **Docker's `-i` and `-t` as separate flags.** Every combination but one is
  either an error or useless here, and the right one can be read from the
  terminal.

## 6. Tests

- `restart` re-creates the named service with the installed release's
  definition, forced, re-creates a dependency only when its container no longer
  matches, and journals one `restart`
  record; an unknown service, a service the release does not start, and no
  service at all are refused before anything runs.
- `restart` refuses a stopped installation, takes the deployment lock, and
  fails when the health checks fail.
- `exec` without `-i` sends the runtime exactly what it sends today.
- `exec -i` passes stdin through and returns the command's exit code; asks
  for a TTY only when stdin and stdout are terminals; refuses `--json`; and
  journals the redacted argv with `interactive: true` and no output.
- A runtime without `ExecInteractive` is refused by name.
- Without a TTY, a `SIGINT` to the process group ends the session, the
  manager survives it, and the session is journalled with the command's
  exit status.
- A `SIGTERM` to the manager alone ends the session through `compose`, and
  the session is journalled.
- The Compose adapter's argv for both, against a recorded command line, and
  one container-lane test that types into `sh` through a pseudo-terminal.

## 7. Docs

- `pages/docs/reference/` for `restart` and for `exec -i`.
- `pages/docs/operating/looking-inside.md`: "not an interactive shell"
  (lines 192-194) becomes the `-i` form, and "not a way to restart one
  service" (lines 249-251) points at `morzer restart`. The same change in
  `exec`'s help.
- The changelog.

## 8. Out of scope

- **`port-forward`.** RFC 0021 §8 still holds: a different security posture.
- **A bare shell form.** Decision 2's alternative, unless ruled otherwise.
- **Restarting on a schedule, or on a failed health check.** A supervisor's
  job, and Docker's restart policy is already in the vendor's Compose file.

## 9. Risks

- **A restart the operator meant as a plain restart re-creates instead**
  (decision 1's recommendation). The help says it re-creates, and the
  summary names what was re-created. A writable layer that held something
  important was going to be lost by the next `apply` that changed the
  service anyway.
- **An interactive session is unredacted.** So is `exec` today; the
  operator's own command printed it to the operator's own terminal. The
  journal's argv is redacted as before.
- **A TTY session ties up the manager process for as long as it runs.** It
  holds no lock, so nothing else waits on it.

## 10. Unresolved questions

None. The author reopened RFC 0021 decisions 8 and 12 and ruled decisions
1–3 on 2026-11-05.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | **What `restart` does.** Recommended: re-create the named services from the installed release, forced, with their dependencies converged as any `up` converges them: started if down, re-created only when their container no longer matches (`d.recreate`), so the container is the one the release defines, as RFC 0021 §8 asked. Alternative: `Runtime.Restart`, a plain `compose restart`, which keeps the container and whatever was changed in it by hand. Was `OPEN`; ruled by the author 2026-11-05: as recommended. |
| 2 | `ASSUMED` | **Whether `exec -i` has a bare shell form.** Recommended: no; the command is required and everything after `--` is the container's argv (RFC 0021 decision 13). Alternative: `morzer exec -i <service>` with no command runs `/bin/sh`. Was `OPEN`; ruled by the author 2026-11-05: as recommended. |
| 3 | `ASSUMED` | **Whether `restart` attests.** Recommended: no; it is journalled, and RFC 0025 attests what moves a version or a configuration, which a restart does not. Alternative: attest it as a sixth operation kind, so an auditor's record shows every time a service was bounced. Was `OPEN`; ruled by the author 2026-11-05: as recommended. |
| 4 | `ASSUMED` | `restart` is an operation: journalled as `restart`, under the deployment lock, refused while the installation is stopped, followed by the release's health checks, and failed (with nothing to compensate) when they fail. One or more services by name; none, an unknown one, or one the release does not start is refused before anything runs. |
| 5 | `ASSUMED` | `exec -i` attaches stdin and asks for a TTY exactly when stdin and stdout are terminals; there is no `-t`. No timeout, no lock, `--json` refused, and the journal records the redacted argv with `interactive: true` and never the output. `exec` without `-i` is unchanged. |
| 6 | `ASSUMED` | A new port method, `Runtime.ExecInteractive`, rather than a wider `Exec`, answering RFC 0021 §10. A runtime without it returns `domain.ErrUnsupported`, refused by name. |
| 7 | `ASSUMED` | With a TTY the Compose adapter hands `compose exec` the terminal and forwards no bytes; without one it copies the streams. For the session's length the manager catches, not ignores, `SIGINT`, `SIGQUIT` and `SIGTSTP`, as `morzer shell` does, since an ignored signal is inherited by `compose`; the session runs under `context.WithoutCancel`, so the root handler's cancellation does not kill it, and it is journalled when it ends. A `SIGTERM` to the manager is forwarded to `compose`, which ends the session; it is journalled, and the manager exits with the command's status. |
| 8 | `ASSUMED` | Reopens RFC 0021 decision 8 for `restart` alone (`port-forward` and log storage stay out) and decision 12 for `exec -i` alone. Both were ungraded, so they move only when the author rules here. Ruled by the author 2026-11-05: both reopened. |

## 12. Phasing

- **P1 — `restart`.** Decisions 1, 3, 4. One PR.
- **P2 — `exec -i`.** Decisions 2, 5–7: the port method, the Compose
  adapter, the fake, the CLI flag. One PR.
