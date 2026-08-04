---
title: Commands
icon: lucide/terminal
summary: Global flags and the lifecycle commands — init, apply, update, rollback, status, doctor, backup, restore, version
---

# Commands

Every command is `morzer <command> [flags]`. The two command groups with their
own subcommands live on their own pages:
[`secret`](secret-commands.md) and [`release`](release-commands.md).

Every mutating command takes the deployment lock, journals what it did before
and after each step, and can be planned first with `--dry-run`. Every command
maps its failure onto a stable [exit code](exit-codes.md).

## Global flags

These are accepted by every command.

| Flag | Meaning |
| --- | --- |
| `--json` | Machine-readable output. stdout carries exactly one JSON object; narration moves to stderr. |
| `--dry-run` | Plan only; make no changes. Prints the step list and, where applicable, a configuration diff. |
| `--yes` | Assume yes for confirmations. Destructive actions still need `--force`. |
| `--force` | Confirm a destructive operation. It authorises destructive actions, not incorrect ones — a refusal on safety grounds is not overridden by it. |
| `--timeout` | Overall time budget for the operation, as a Go duration (`30m`, `2h`). |
| `--verbose`, `-v` | Verbose output, including per-step detail and the subprocess log. |
| `--quiet`, `-q` | Errors only. |
| `--log-format` | `text` (default) or `json`. Logs always go to stderr. |
| `--no-color` | Disable styling. Also honoured through `NO_COLOR` and a non-TTY stdout. |
| `--plain` | Line-oriented output; no interactive rendering. Already automatic under CI, systemd, `NO_COLOR` and without a terminal — see [Output modes](output-modes.md). |
| `--resume` | Continue an interrupted operation from where its journal left off. |
| `--wait` | Wait for the deployment lock instead of failing with exit 4. |
| `--config` | Path to `installation.yaml`, when it is not in the default location. |
| `--product` | Product name. Inferred from the installation when omitted. |

## Index

| Command | What it does |
| --- | --- |
| [`init`](#init) | Create a new installation. |
| [`apply`](#apply) | Converge the system to the installed release. |
| [`update`](#update) | Install a new release over the current one. |
| [`rollback`](#rollback) | Return to the previous release. |
| [`status`](#status) | Show what is deployed and whether it is working. |
| [`doctor`](#doctor) | Run read-only diagnostics. |
| [`backup`](#backup) | Back up the database, files, configuration and secret state. |
| [`restore`](#restore) | Restore from a backup. |
| [`version`](#version) | Print version, commit, and supported manifest API versions. |
| [`config`](parameters.md#changing-one-after-install) | Read and change the release parameters. |
| [`secret`](secret-commands.md) | Manage the encrypted secret state. |
| [`release`](release-commands.md) | Inspect and manage release bundles. |
| [`installation`](installation-commands.md) | Export and rebuild an installation's identity. |

---

## init

Creates the directory layout, the machine's age identity, the installation
configuration, the encrypted secret state and, optionally, the systemd units.

It never overwrites an existing installation, and it does not start the product:
run `apply` afterwards.

```sh
morzer init --release ./bundle --profile embedded --domain example.com \
    --recovery-recipient age1...
```

| Flag | Meaning |
| --- | --- |
| `--release` | Release bundle to stage during init. The product name is taken from its manifest. |
| `--profile` | Deployment profile from the release manifest. |
| `--domain` | Public domain. Repeat for several; the first is canonical. |
| `--recovery-recipient` | Offline age public key that can also decrypt the secret state. |
| `--no-recovery-recipient` | Proceed without one. Losing this machine then loses its secrets. |
| `--generate-secrets` | Generate every secret the release declares a generator for. Default `true`. |
| `--install-units` | Install systemd units when systemd is available. Default `true`. |
| `--backup-schedule` | systemd `OnCalendar` expression for scheduled backups. |
| `--signing-key` | minisign public key a release signature must verify against. Repeat for several. |
| `--require-signature` | Refuse any release that is not signed by one of those keys. |
| `--product` | Product name, when no `--release` is given to take it from. |
| `--set` | Set a release parameter, as `name=value`. Repeat for several. Needs `--release` to validate against. See [Parameters](parameters.md). |
| `--repair` | Restore missing directories on an existing installation. |

### The interactive first run

At a terminal, with something required still missing, `init` asks for it —
product name, deployment profile (offering what the release declares), domains,
and the recovery key, with an option to generate one on the spot.

It is a front-end over the flags and never a second path. It fills the same
options the flags do, and prints the command line that would have produced the
same result:

```text
equivalent to:
  morzer init \
      --product demo \
      --profile embedded \
      --domain demo.example \
      --recovery-recipient age1vm6ncva…
```

Put that in your provisioning script and you never need the wizard again, which
is the point of printing it.

It never runs with `--yes`, `--json` or `--quiet`, without a terminal, or when
the command line already answers everything. A CI job, a systemd unit and a
scripted install all take the flags exactly as given.

`--require-signature` without `--signing-key` is refused: no bundle could
satisfy it, and a policy nothing can satisfy is a configuration error rather
than something to discover on the next update. Both are written into
`installation.yaml` under `policy`, and can be edited there afterwards.

!!! warning "Keep the recovery key off this machine"

    A recovery key stored on the machine it is meant to recover protects
    nothing. `init` insists on one, or on `--no-recovery-recipient` said out
    loud.

## apply

Renders configuration and secrets, pulls images, runs migrations, starts
services and waits for health.

Idempotent: applying an unchanged system runs nothing and says so. Each step
answers a `Check` before it runs, and a satisfied postcondition marks it skipped
rather than repeating the work.

| Flag | Meaning |
| --- | --- |
| `--profile` | Override the installation's deployment profile. |
| `--startup` | Boot-time mode: skip pulls when images are local, skip migrations when the schema is current. |

`--startup` is what the systemd unit uses. It exists so a machine rebooting
without network connectivity still comes back up.

## update

Installs a new release over the current one.

Takes a bundle path, or `--to <version>` for a release already fetched into the
release store. A pre-update backup is taken by default.

```sh
morzer update ./bundle-1.3.0
morzer update --to 1.3.0
```

| Flag | Meaning |
| --- | --- |
| `--to` | Install a version already in the release store, instead of a bundle path. |
| `--digest` | Expected bundle content digest. A mismatch refuses the update. |
| `--profile` | Override the installation's deployment profile. |
| `--skip-backup` | Skip the pre-update backup. Requires `--force` and is recorded in the journal. |

A failed update rolls back to the release that was running. **The database is
never rolled back automatically**: when a migration cannot be undone, the
release says so through `compatibility.rollback_safe`, and the answer is a
restore from the backup taken here.

## rollback

Returns to the previous release.

It is not "update in reverse". It assesses three questions first — are the
containers reversible, is the schema compatible, is a restore required — and
refuses when the answers do not permit a safe return, naming the backup to
restore from instead.

| Flag | Meaning |
| --- | --- |
| `--to` | Roll back to this installed version rather than the immediate previous one. |

Each rollback promotes the release it displaced to *previous*, so a second
rollback without `--to` returns to where the first started. Reaching a release
two steps back means naming it.

`--force` does not override a refusal. Force authorises destructive actions, not
incorrect ones, and the failure mode here is quiet data corruption rather than a
visible break.

## status

Shows what is deployed, which services are up, the last backup, the last
operation, and anything needing attention.

| Flag | Meaning |
| --- | --- |
| `--clear-intervention` | Acknowledge a `requires-manual-intervention` operation. An empty value selects the only one. |
| `--watch` | Refresh until interrupted. Needs a terminal. |
| `--interval` | How often `--watch` refreshes. Default `2s`. |

An operation that ended in [exit 12](exit-codes.md) keeps surfacing in `status`
and `doctor` until it is cleared explicitly. That is deliberate: the flag exists
to stop an operator proceeding as though nothing happened.

### Watching

`--watch` redraws the status on a timer until you press `q`. It is the only view
that takes over the screen, and it restores what was there when it exits — there
is nothing in a repeatedly-redrawn table worth keeping in the scrollback.

It observes and never acts. No key restarts a service or clears an
intervention: those take the deployment lock and are journaled, which makes them
commands rather than keystrokes.

A refresh that fails leaves the last good reading on screen with the error
underneath, because a runtime that briefly stops answering is exactly when the
previous reading matters most. Refreshes never overlap: if one is still in
flight when the timer fires, the tick is skipped rather than queued against a
runtime that is already struggling.

It is refused without a terminal, rather than falling back to printing the table
in a loop — a `--watch` left in a unit file would otherwise fill a journal with
thousands of copies of the same output. For scripts, poll `morzer status --json`
instead.

## doctor

Read-only diagnostics, with a suggested remedy for every non-ok result. It
checks tool versions against what the release requires, the installation layout,
the secret state, service and health status, disk headroom, and the journal.

Exits [3](exit-codes.md) when any check fails; warnings exit 0.

Three checks are advisory by design, and warn rather than fail:

| Check | What it means |
| --- | --- |
| `secrets.rotation` | A secret is older than the `rotation_period` its release declares. That period is the vendor's recommendation, and failing an exit code monitoring watches over a recommendation is how a team learns to ignore it. Secrets with no declared period are not mentioned at all. |
| `secrets.ephemeral-storage` | The directory decrypted secrets are written to is not tmpfs, so they touch disk and are not reliably erasable. A container with no tmpfs mounted is a legitimate way to run this. |
| `runtime.images-local` | Some of the release's images are not present locally, so this machine would not come up without network access. See [Installing without a network](../operating/installing-offline.md). |

The rotation remedy names the command that will actually work: `secret rotate`
for a secret the release declares a generator for, and `secret set` for one it
does not — pointing at `rotate` there would point at a command that fails.

## backup

Coordinates the release's backup hook, wraps the result in a self-describing
manifest, and verifies the checksums by re-reading what was written.

| Flag | Meaning |
| --- | --- |
| `--component` | Limit the backup to these components: `database`, `files`, `config`, `secrets`, `manifest`. |
| `--reason` | Why this backup was taken; recorded in its manifest. Default `manual`. |
| `--no-verify` | Skip re-reading the backup to check its checksums. |
| `--no-prune` | Skip applying the retention policy afterwards. |
| `--no-push` | Do not copy the backup to the configured targets; it stays only on this machine. |
| `--no-prune-remote` | Skip applying the retention policy on the targets. |

### backup list

Lists backups, newest first.

| Flag | Meaning |
| --- | --- |
| `--remote` | List what is on the configured backup targets instead. |
| `--target` | List one target by URL, whether or not this installation configures it. |
| `--credentials-file` | YAML file holding the target's credentials, for a machine whose secret state is not readable yet. |

### backup target

Manages where backups are kept besides this machine. See
[backup target URLs](backup-targets.md).

`morzer backup target add <url>` records a target, checking it answers first.
`morzer backup target list` shows them and whether each is reachable.
`morzer backup target remove <url>` stops using one and deletes nothing that is
already there.

| Flag | Meaning |
| --- | --- |
| `--credentials` | Name of a secret holding this target's credential document. |

### backup push

Copies an existing backup to every configured target, verifying it again first.
Takes a backup id; the most recent when omitted.

The retry for a push that failed. A backup whose push failed is still on this
machine, verified and correct — what failed was the network or the medium, and
the remedy should not be taking another backup.

### backup fetch

Copies a backup down from a target into this machine's backup store, and
verifies it. Takes a backup id; the newest on the target when omitted.

Restoring is a separate command on purpose: a backup that has come back from a
bucket is one you should be able to look at before it overwrites a database.

| Flag | Meaning |
| --- | --- |
| `--target` | Target URL to fetch from; the installation's targets when omitted. |
| `--credentials-file` | YAML file holding the target's credentials, for a machine whose secret state is not readable yet. |

### backup verify

Re-reads a backup and checks its checksums. Takes a backup id; locally, the most
recent backup when omitted.

```sh
morzer backup verify 01J8ZP...
morzer backup verify --remote
```

With `--remote` it checks the copy on the configured targets instead, streaming
each component through a checksum and keeping nothing. A full transfer, and the
only thing that notices rot on a target.

Omitting the id means something different in each mode: locally it verifies the
most recent backup, and with `--remote` it verifies **every** backup on every
configured target, which is what a scheduled check wants.

| Flag | Meaning |
| --- | --- |
| `--remote` | Check the copy on the configured backup targets; a full transfer. |
| `--target` | Verify on one target by URL, whether or not this installation configures it. |
| `--credentials-file` | YAML file holding the target's credentials, for a machine whose secret state is not readable yet. |

## restore

Restores from a backup.

Destructive: it requires `--force` **and** `--confirm <installation-id>`, typed
out. A restore that could be triggered by one mistyped command is a restore
waiting to destroy a production database.

| Flag | Meaning |
| --- | --- |
| `--backup` | Backup id. The most recent when omitted. |
| `--component` | Limit the restore to these components. |
| `--confirm` | The installation id, typed to confirm a destructive restore. |
| `--allow-cross-installation` | Restore a backup that belongs to a different installation. |

`--allow-cross-installation` is separate from `--force` because `--force` is
already required for every restore: using it for both would mean the
cross-installation guard was only ever checked after the one thing that disabled
it. Restoring another deployment's data is a distinct decision and gets its own
answer.

If the machine is a rebuild of the one the backup came from,
[`installation import`](installation-commands.md#installation-import) is the
right answer instead — it restores the original id, so the guard never fires.

Restore declares that it requires manual intervention on failure: a half-restored
database is a state no automatic action can repair, so a failure here exits
[12](exit-codes.md) rather than pretending compensation succeeded.

## version

Prints the manager version, the commit it was built from, and the manifest API
versions it can read.

Under `--json` the supported API versions are a list, so a bundle author can
check compatibility programmatically rather than by trial and error.
