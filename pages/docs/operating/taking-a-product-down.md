---
title: Taking a product down for a while
icon: lucide/pause-circle
summary: Stopping a product for maintenance, a move or an incident, and keeping it down until you start it
---

# Taking a product down for a while

```sh
morzer stop --reason "moving the data disk"
morzer start
```

`morzer stop` stops every service and records the stop in the installation's
state. The product stays down across a reboot and an unattended update until
`morzer start` brings it back. Containers, networks and volumes are kept, and
nothing about either command can remove a volume.

Stopping it with `docker compose stop` instead does not stay down: the boot-time
unit runs `morzer apply --startup`, which starts every service, and an
unattended update with `auto_apply` installs a release and starts it.

## Stop it

```sh
morzer stop --reason "moving the data disk"
```

`--reason` is optional and one line. It is shown by `status`, `doctor` and `ls`
until the product is started again, so the next person on the machine knows why
it is down. Known secret values are scrubbed out of it before it is written
down.

`stop` writes the record before it stops anything. A `stop` killed half-way
leaves the product marked stopped and still running; `morzer stop --resume`
finishes it, and the next boot leaves the product alone either way.

## While it is stopped

| What | While stopped |
| --- | --- |
| A reboot | Leaves the product down. The unit exits 0 and logs `stopped by the operator since <time>: <reason>`. |
| The update timer | Fetches, verifies and stages a new release, and never installs it, whatever `update.auto_apply` says. |
| `apply`, `update`, `rollback`, `task run` | Refused with [exit 2](../reference/exit-codes.md) and a hint to run `morzer start`: each would start the product. |
| `restore` | Runs, and leaves the product stopped. `morzer start` applies the release over the restored data. |
| The scheduled backup | Skipped, with a line in the unit's journal. `morzer backup` still runs when you run it, and a backup hook that needs a running service will fail then. |
| `config set`, `secret set`, `secret generate`, `secret rotate`, `secret edit` | Store the change and restart nothing. It takes effect at `morzer start`. |
| `exec` | Refused, as for any service that is not running. |
| `status` | Shows the stop above the services, and `--json` has a `stopped` object. |
| `doctor` | `runtime.services` reports the stop as information, however long ago it was, and the health endpoints are not probed. A Docker daemon that cannot be reached still fails, because `start` needs it. |
| `ls` | Adds a `stopped` column with the time it stopped, and `stopped` in `--json`. |

`installation import` does not carry a stop to the machine it rebuilds: the stop
belongs to the machine that was stopped.

## Start it again

```sh
morzer start
```

`start` runs `apply` for the current release and clears the stop only once that
has converged and the health checks pass. A `start` that fails keeps the stop,
so a reboot or the update timer cannot bring up a half-converged machine; fix
the cause and run `start` again.

A release the update timer staged during the stop is not installed by `start`.
The next tick of the timer, or `morzer update`, installs it as it would have.
On a product that is not stopped, `start` is `apply` and says nothing was
stopped.
