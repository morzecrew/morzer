---
title: Commands
icon: lucide/terminal
summary: Global flags and the lifecycle commands — init, apply, update, rollback, status, doctor, backup, restore, version
---

# Commands

Every command is `morzer <command> [flags]`. The two command groups with their
own subcommands live on their own pages:
[`secret`](secret-commands.md) and [`release`](release-commands.md).

[Every command](index.md) is the generated list of all of them with the page
that documents each — start there when you know a command exists and not where
it is written up.

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
| `--no-color` | Disable colour. Equivalent to `NO_COLOR` or `CLICOLOR=0` in the environment; the live renderer still draws, since every state carries a symbol too. |
| `--plain` | Line-oriented output; no interactive rendering. Already automatic under CI, systemd, `TERM=dumb` and without a terminal — see [Output modes](output-modes.md). |
| `--resume` | Continue an interrupted operation from where its journal left off. |
| `--wait` | Wait for the deployment lock instead of failing with exit 4. |
| `--config` | Path to an installation's `installation.yaml`, which selects that installation: `/etc/demo/installation.yaml` means the `demo` layout, and a path with a prefix means that prefix. Equivalent to `--product` (and `--root`); naming both is refused when they disagree. |
| `--product` | Product name. Inferred from the installation when omitted, and required on a host with more than one. |

### Which installation a command acts on

A machine may hold several installations: every path is keyed by the product
name, so `/etc/demo` and `/etc/other` are separate deployments with separate
locks, separate secret state and separate systemd units.

Selection, in order:

| Source | Rank | Notes |
| --- | --- | --- |
| `--config <path>` | 1 | Selects the layout the file sits in. This is what the generated systemd units pass. |
| `--product <name>` | 1 | With `--root` when the layout is relocated. |
| `MORZER_PRODUCT` | 2 | For a shell session pinned to one installation. Both flags override it. |
| Discovery | 3 | Used only when the machine holds **exactly one** installation. |

`--config` and `--product` share a rank because they are alternatives rather than
a precedence: naming both is refused when they disagree, and accepted when they
name the same installation.

On a machine with more than one installation, a command that acts on one and was
given no selector is refused, and the refusal names what it found:

```console
$ morzer status
error: this machine has 2 installations, so --product is required
hint:  demo, other — pass `--product demo`, or `--config /etc/demo/installation.yaml`; `morzer ls` lists them
```

Commands that touch no installation are unaffected: `version`, `ls`, `doctor`,
`init`, `installation import`, and the bundle-authoring half of `release` —
`new`, `verify`, `pack`, `build` and `archive`.

[Several installations](../operating/several-installations.md) is the whole
picture: what two deployments share, and what `doctor` says about it.

## Index

| Command | What it does |
| --- | --- |
| [`init`](#init) | Create a new installation. |
| [`apply`](#apply) | Converge the system to the installed release. |
| [`update`](#update) | Install a new release over the current one. |
| [`rollback`](#rollback) | Return to the previous release. |
| [`status`](#status) | Show what is deployed and whether it is working. |
| [`doctor`](#doctor) | Run read-only diagnostics. |
| [`logs`](#logs) | Read the deployment's logs. |
| [`ps`](#ps) | List the deployment's containers. |
| [`stats`](#stats) | Show CPU, memory and I/O per container. |
| [`exec`](#exec) | Run a command inside a running service. |
| [`backup`](#backup) | Back up the database, files, configuration and secret state. |
| [`restore`](#restore) | Restore from a backup. |
| [`version`](#version) | Print version, commit, and supported manifest API versions. |
| [`completion install`](#completion-install) | Put the shell completion script where your shell reads it. |
| [`config`](parameters.md#changing-one-after-install) | Read and change the release parameters. |
| [`secret`](secret-commands.md) | Manage the encrypted secret state. |
| [`release`](release-commands.md) | Inspect and manage release bundles. |
| [`ls`](installation-commands.md#installation-list) | List the installations on this machine. |
| [`installation`](installation-commands.md) | List, export and rebuild installations. |

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
| `--mode` | What this machine is for: `dev` for a sandbox, or production (the default). Fixed at creation and never changeable — see [unattended updates](../operating/unattended-updates.md#dev-mode-a-machine-that-is-a-sandbox-from-birth). |

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
| `--check` | Report whether a newer release exists, without installing anything. |
| `--stage` | Follow the configured channel: fetch and verify what it points at, without installing it. |
| `--unattended` | One scheduled tick: follow the channel, stage, and install only what declares a failure cannot need a database restore. This is what the update timer runs; see [unattended updates](../operating/unattended-updates.md). |

`--check` and `--stage` are alternatives, and both stop short of installing.
See [following a channel](../operating/updating.md#following-a-channel) for what
staging leaves behind and what a poll costs.

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
| `runtime.images-local` | Some of the release's **pulled** images are not present locally, so this machine would not come up without network access. Images that travel in the bundle are not counted here — they are either loaded or the deployment is refused, which is `images.bundled` below. See [Installing without a network](../operating/installing-offline.md). |

The rotation remedy names the command that will actually work: `secret rotate`
for a secret the release declares a generator for, and `secret set` for one it
does not — pointing at `rotate` there would point at a command that fails.

One check about images does **fail** rather than warn:

| Check | What it means |
| --- | --- |
| `images.bundled` | An image the release marks `from: bundle` is not in the local image store. Fatal, and `apply` refuses on it: a bundled image is deployed under a tag the manager creates, and letting a converge proceed without it would send the deployment to the vendor's registry for whatever that tag pointed at. `morzer release ingest` loads it out of the bundle, with no network. |

## logs

Streams the deployment's logs, resolving the Compose project, its files and its
environment. Scoped to the project rather than to the manifest's service list,
so a sidecar the vendor's Compose file starts is included.

| Flag | Meaning |
| --- | --- |
| `--follow`, `-f` | Keep the stream open. Ctrl-C exits 0. |
| `--tail` | Lines of history before following. Default `100`; `0` is the whole retained backlog, which is also the default with `--follow`. |
| `--since` | A duration back from now (`10m`, `2h`) or an RFC 3339 instant **with a zone**. A timestamp with no zone is refused rather than assumed local. |
| `--no-redact` | Do not scrub this installation's secret values. Prints a warning to stderr. |

Takes no lock: reading logs must never queue behind an update, since during one
is when they are most wanted. Nothing is stored, rotated or shipped.

Secret values are scrubbed by default, holding bytes to a line boundary so a
value split across two reads is still caught, and dropping any line over 64 KiB
rather than passing it through unmatched. It is best effort — a value the
service *derived* from a secret is beyond any redactor. See
[Looking inside](../operating/looking-inside.md#redaction-and-what-it-cannot-do).

`--json` is the one exception to the [single-envelope
contract](output-modes.md#the-one-streaming-exception): one object per line, no
envelope, and the exit code is what says whether the stream ended cleanly.

## ps

The service table `status` draws, on its own: what is running, whether it is
healthy, which image, and the runtime's own sentence about it. No flags, no
lock, and nothing changed.

The container column is never dropped on a narrow terminal — it is the only
thing that tells two replicas of one service apart.

## stats

One sample of CPU, memory and I/O, one row per container.

| Flag | Meaning |
| --- | --- |
| `--watch` | Re-sample until interrupted. Refused with `--json`. |
| `--interval` | How often `--watch` re-samples. Default `2s`, floor `1s`. |

Never an aggregate per service: a scaled service is several containers, and one
row under the service's name would be one replica's numbers wearing the whole
service's label. The total line covers CPU and memory, which add, and not the
memory limit, which does not; `--json` emits the rows and no total.

A `-` in a column — `null` under `--json` — is a figure this host does not
account for, which is what a rootless daemon reports for block I/O. It is not a
zero, because a container that has written nothing reports one of those.

At a terminal `--watch` redraws; elsewhere it appends a block per sample, unlike
[`status --watch`](#watching), which is refused without a terminal. A sequence
of samples is a time series worth keeping; a redrawn status table is not.

A runtime that cannot report statistics refuses by name with
[exit 7](exit-codes.md) rather than returning an empty table, which would be
indistinguishable from an idle deployment.

## exec

```sh
morzer exec <service> -- <command> [args...]
```

Runs one command inside a running container of the named service and propagates
its exit code, so a failed command fails the invocation. Everything after `--`
is the container's command line and nothing else; the terminator is required.

Not an interactive shell: there is no TTY and no stdin, so a command that
prompts waits for an answer nobody can give. There is no `--user` and no
shortcut for running as root.

A service that is not running is refused by name, with the state it is in.

Journaled with its type, service, argv and exit code — and never its output. The
argv is redacted with this installation's known secret values first, because a
password in an argv is the ordinary case; when those values cannot be loaded at
all the record keeps everything else and carries `argv_omitted` saying why,
rather than writing down a command line it could not scrub. A credential the
manager has never
been told about is beyond that; see
[Looking inside](../operating/looking-inside.md#it-is-written-down).

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

This is the retry for a push that failed. A backup whose push failed is still on
this machine, verified and correct — what failed was the network or the medium,
and the remedy should not be taking another backup.

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

## completion install

```sh
morzer completion install            # the shell $SHELL names
morzer completion install zsh
morzer completion install --print-path
sudo morzer completion install bash --system
```

Generates the completion script and puts it where that shell reads completions
from, creating the directory when it is missing.

| Flag | Meaning |
| --- | --- |
| `--print-path` | Print where the file would go and write nothing. |
| `--system` | Write the distribution's own completion directory instead of one under your home. Needs the privileges for it. |

| Shell | Where it goes | What else |
| --- | --- | --- |
| `bash` | `${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions/morzer` | bash reads that directory through the bash-completion package, 2.8 or newer. Without it, source the file from `~/.bashrc`. |
| `zsh` | `${XDG_DATA_HOME:-$HOME/.local/share}/zsh/site-functions/_morzer` | the directory must be on `fpath` before `compinit`; the command prints the line to add. |
| `fish` | `${XDG_CONFIG_HOME:-$HOME/.config}/fish/completions/morzer.fish` | read on the next start. |

Those paths are a table in the code rather than a guess, because the failure of
a completion is silence — no error, no warning, just a Tab key that does
nothing. It is also the only place this project knows them: the
[install script](../get-started/installation.md) calls this command rather than
learning the paths itself, so there is no second copy to drift.

Writing is idempotent: the same path and the same bytes every run.

**A shell it cannot place a file for is not a failure.** No `$SHELL`, an
unrecognised one, or something exotic: the script goes to stdout with a note
naming the shells it can place, and the command exits 0. That keeps
`morzer completion install > somewhere` useful, and keeps an installer's
optional completion step from failing an install.

PowerShell is generated by `morzer completion powershell` and not installed by
this: its location is a profile script this program has no business editing.
