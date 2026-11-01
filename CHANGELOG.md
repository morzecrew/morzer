# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`morzer config diff` shows a hand edit before `apply` replaces it.** It
  prints where each configuration file differs from what the release renders,
  scrubbed of secret values. `doctor` warns as `config.drift`, and an unattended
  update leaves the release staged while an edited file would be replaced.

- **`config set` changes the signature requirement, domains and retention
  after `init`.** `release.require_signature=true` lets an installation made
  without `--require-signature` turn on `update.auto_apply`.
  `installation.domains` re-creates every running service; `retention.releases`
  and `retention.backups` take counts.

- **`update --dry-run` says what the update risks.** After the plan it says
  whether `morzer rollback` could return to the installed release afterwards,
  or a restore or a parameter the update drops would stop it, which images
  change, and the release notes. `--json` carries them as `data.preview`.

- **`morzer history` lists the operations an installation ran.** Newest
  first, with each one's status, start time, release change and duration;
  `history <operation-id>` adds its steps, flags and error. It reads only the
  journal, so it answers while Docker is down, and `--json` carries the
  journal's records.

- **Tab completes products, services, tasks, releases, config names and
  backups.** `--product` and `morzer shell` offer what `morzer ls` lists.
  `logs`, `exec`, `task run`, `release show`, `--to`, `config` and `restore
  --backup` offer what the installation holds. Under `sudo`, only products
  complete.

- **Help shows examples and links the documentation.** `init`, `update`,
  `rollback`, `restore` and `release verify` gain examples, and each command
  group's help ends with a link to its page on the documentation site.

- **`init` and `status` say what to run next.** `init` starts nothing, and now
  ends by naming `morzer apply`, or `morzer update <bundle>` without a release.
  Before the first `apply`, `status` shows that as `next`, also in `--json`,
  instead of `cannot read service status`.

- **`morzer shell <product>` starts a shell pinned to one installation.** It
  sets `MORZER_PRODUCT` after `sudo`, which drops an exported one, so
  `sudo morzer shell demo` gives a root shell where commands need no
  `--product`. Nothing is written to disk; the pin ends with the shell.

- **`morzer stop` keeps a product down until `morzer start`.** The stop is
  recorded, so a reboot leaves the product down and the update timer stages
  without installing. `apply`, `update`, `rollback` and `task run` are refused
  meanwhile, and `start` applies the release before it clears the stop.

- **`task run --input <name>=<path>` passes a file to a task.** A task declares
  `inputs`; the file is copied, hashed, mounted read-only at
  `/morzer/inputs/<name>` and removed afterwards. The journal and the
  attestation, now predicate v3, record its size and digest, never its content.

### Changed

- **A usage error is two lines, not the command's whole usage.** A mistyped flag
  printed some thirty lines of usage, on stderr even under `--json`. A wrong
  argument count now says what the command takes, such as `morzer release
  verify takes <path>`, instead of `accepts 1 arg(s), received 0`.

- **`secret set` at a terminal asks before storing what it read.** It prints
  `read 41 characters; store it? [Y/n]`, so a paste that landed twice or not at
  all is caught before any service restarts. `--yes` skips it; piped values are
  unchanged.

### Fixed

- **`config set` no longer renders from a modified release.** `config list`,
  `get`, `set` and `diff` refuse a release directory changed after install, as
  `apply` already did, instead of rendering from what the digest no longer
  vouches for.

- **A notification a target refuses is logged.** A webhook target that was down
  or answered an error was reported nowhere: the fan-out dropped the error, and
  the webhook does not log its own. Each failure is now a `notifier failed`
  warning naming the target. The operation's outcome is unchanged.

### Security

- **An image reference with a control character is refused.** Loading a bundle
  rejects an image `ref` containing a control or format character, such as a
  terminal escape sequence. Commands print references as the bundle wrote
  them, so one could otherwise send commands to the operator's terminal.

## [0.10.0] - 2026-10-30

`morzer release push` publishes a bundle archive to an OCI registry and moves
its channels, and a GitHub Action installs morzer in CI. Commands report
failure and success more honestly: `logs` and `doctor` fail when the container
runtime cannot be reached, a mistyped subcommand fails instead of printing
help, and an empty listing is an empty list. A rollback refused by preflight no
longer leaves the release pointer moved, and `task run` prints the task's
output only when asked.

Upgrading migrates nothing: the installation schema stays at 13. A bundle built
for 0.9.0 installs unchanged; one that names a `secrets`, `backup` or `health`
provider morzer does not have now draws a warning on install and a refusal from
`release verify` and `build`. Scripts may notice:

- `doctor` exits 3, not 0, when the container runtime cannot be reached, and
  `logs` exits 7, not 0, when the runtime cannot produce the stream.

- `task run` no longer prints the task's stdout and stderr. Pass `--output` to
  keep them: `morzer task run report --output > report.txt`.

- A mistyped subcommand, `release archive -o` with a name that does not end in
  `.tar.zst` or `.tzst`, and `config unset` of an undeclared parameter exit 2
  where they exited 0. `attest log` with nothing recorded exits 0, not 5.

- `release build --version-from-git` versions a bundle by the last commit that
  changed it, so a repository holding more than the bundle mints fewer dev
  versions, with different numbers.

- New installations get `inst_` ids; existing `op_` ids keep working.

Under `--json`, an empty list is `[]` at any depth where it was `null`, an error
raised before a command runs names that command, and a command group run with
no subcommand exits 2 with an error envelope instead of printing help. No
existing field changed meaning.

### Added

- **A GitHub Action that installs morzer: `morzecrew/morzer/actions/setup`.** A
  workflow that builds or tests a bundle used to copy the download, checksum and
  signature steps into its own scripts. The action runs `install.sh` with the
  version it is given and `--require-signature`, installs `minisign` first when
  the runner has none, and puts `morzer` on `PATH` for the steps after it.

- **`morzer release push` publishes an archive to an OCI registry.** It checks
  the archive as `release verify` does, refuses to overwrite a version with
  different content, moves `--channel` tags only after the push, and prints both
  digests. Re-pushing the same content succeeds, so a failed pipeline can re-run.

### Changed

- **`release build --version-from-git` names the bundle, not the repository.**
  It used to describe HEAD, so in a repository holding more than the bundle
  every commit to a workflow, script or README gave the same bundle a new dev
  version. It now describes the last commit that changed the bundle directory.
  A tag on a commit that did not touch the bundle builds as that tag, and only
  uncommitted changes inside the bundle directory make a build dirty. Even in a
  repository that holds only the bundle, a commit that changes no file, such as
  a merge whose result matches one side, no longer moves the version.

- **`doctor` fails when the container runtime cannot be reached.** With the
  Docker daemon stopped or unreachable, the service check was downgraded to a
  warning, so `doctor` exited 0 and monitoring that watches its exit code saw a
  healthy machine. It now exits 3. A stopped service, or a deployment with no
  containers yet, still only warns.

- **Bundle commands refuse a provider name morzer does not have.** The
  `secrets`, `backup` and `health` provider names were never read, so a manifest
  asking for `vault` installed and ran sops-age. `release verify`, `build`,
  `pack` and `archive` now refuse any name but `sops-age`, `hooks` and `http`.
  `init` and `update` only warn, so a release already installed keeps working.

- **New installation ids start with `inst_`.** They were minted like operation
  ids, so an installation's `op_…` id was easy to mistake for one of the
  journal's operations. An installation created before this keeps its `op_` id:
  backups, fleet keys and `--confirm` go on accepting it.

- **`task run` prints the task's output only when asked.** Pass `--output` to
  get its stdout and stderr when it finishes, as before:
  `morzer task run report --output > report.txt`. `--verbose` still shows them
  as they arrive, and a task that fails shows the last 10 lines of its stderr.
  `--json` still carries both.

### Fixed

- **`morzer logs` fails when the runtime cannot produce the stream.** With the
  Docker daemon down, or a service name that does not exist, it printed nothing
  and exited 0, and `--json` reported `"ok": true`. It now exits 7 with the
  runtime's own reason.

- **Nothing to list is an empty list.** `attest log` on an installation that
  has recorded nothing exited 5; it now lists no statements and exits 0, while
  `attest verify` still refuses. Under `--json`, a listing with no entries, such
  as `backup list` or `release list`, carried `"data": null`; it is now `[]`.

- **An empty list inside a `--json` report is `[]`.** `release prune` with
  nothing to remove reported `"removed": null`. Every list in every report is
  now `[]` when empty.

- **A mistyped subcommand fails instead of printing help.** `morzer release
  verfy` printed the group's help and exited 0, even under `--json`. It now
  exits 2 with `unknown command` and suggests a subcommand when one is close;
  `morzer release` alone still prints its help.

- **A command group with no subcommand fails under `--json`.** `morzer release
  --json` printed help text and exited 0. It now exits 2 and lists the
  subcommands; without `--json` it still prints help.

- **`--json` errors name the command.** A mistyped flag or command reported
  `"command": ""` in its envelope. It now names the command the arguments
  resolve to, such as `morzer release verify` for an unknown flag.

- **`morzer doctor -v` works.** Doctor's own `--verbose` hid the global `-v`, so
  `doctor -v` was refused as an unknown flag. Both spellings now expand every
  check.

- **Flag help no longer shows an example as the flag's value.** A backticked
  word in a flag's help became its value name, so `--help` printed
  `--since 10m` and `--mode dev`. Those lines now show the value's type, or
  nothing for a switch.

- **`release show --help` names a flag that exists.** It pointed at
  `release verify --bundle`; it now names `release verify <bundle-dir>`.

- **`release archive -o` refuses a name no source would read.** The archive is
  always zstd, and morzer recognises one only by a `.tar.zst` or `.tzst`
  suffix, so `-o demo.tar.gz` wrote a file nothing could install. It now exits 2
  before writing.

- **`update --check` says what to set when it cannot list versions.** A release
  installed from a file or an HTTPS URL, with no channel, was refused with
  `file cannot list available versions`. The refusal now says where the
  reference came from, and the hint names `config set update.channel=oci://…`.

- **A refused HTTPS or OCI download names its URL, not a temporary file.** A
  corrupt download, or one whose digest did not match, was refused with the path
  of an already-deleted temporary file. It now names the reference.

- **`config unset` refuses a parameter the release does not declare.**
  `config unset typo` reported success where `config set typo=1` was refused.
  It now exits 2 and lists the declared parameters; a stale value left by an
  older release still clears.

- **`config unset` clears a stale value while another parameter has none.** A
  recorded value the release no longer declares, beside a parameter with no
  default and no value, refused every `config` change. Clearing the stale value
  no longer waits on the other.

- **Command output no longer cites RFCs an operator does not have.**
  `config settings`, `installation describe` and two refusals named an RFC by
  number, such as "See RFC 0027". The text now stands without the citation.

- **A rollback refused by preflight leaves the installation where it was.** The
  release pointer moved to the previous release before its preflight ran, so a
  refusal there left it moved while the newer release kept running. Preflight
  now runs before anything moves, and the refusal exits 3.

- **A rollback no longer refuses over a parameter only the newer release
  declares.** A value set for it made every resolve against the older release
  fail with `the release declares no parameter`. The rollback now retires that
  value, as an update retires one a new release dropped.

- **A statement no longer names a parameter its operation retired.** After an
  update or a rollback that retired a value, the signed statement still listed
  that parameter beside a configuration digest rendered without it. It now
  names only the parameters left recorded.

- **`release verify --signing-key` explains an unsigned bundle to a vendor.**
  It refused with the installation's message, telling a build machine to clear
  `require_signature`, a setting it does not have. It now says the key was
  given and the bundle is not signed, and shows the `minisign -Sm` command.

- **A value set while an update or rollback starts is retired with the rest.**
  A `config set` landing between the operation's first read and its lock, for
  a parameter the target release does not declare, stayed recorded and showed
  as stale. The retire step now decides from what is recorded when it runs.

### Security

- **A signed URL's token no longer appears in a download error.** A 404 or an
  unreachable host quoted the HTTPS URL with its query, where a signed URL
  carries its token, into the operation journal and support bundles. Errors now
  drop the query; rotate a token used this way.

## [0.9.0] - 2026-10-29

A machine can now replace the key it signs its own statements with, and its
statements say which operation verified a release's signature. A password in a
bundle URL no longer reaches the journal or a support bundle, `doctor` says when
attestations have no target to go to, a task's output no longer carries
Compose's progress, and a rollback keeps the source `update --check` asks.

Upgrading moves the installation schema from 12 to 13, with nothing to migrate.
Once a 0.9.0 manager has written an installation's state, a 0.8.x manager
refuses it rather than rewriting its release record without the new signature
check, so that machine cannot go back to 0.8.x. A bundle built for 0.8.0
installs unchanged. Four things behave differently:

- Attestation statements use the predicate `…/attestation/v2`. `attest verify`
  reads v1 and v2; a consumer validating statements against
  `schemas/morzer-attestation-v1.json` needs `morzer-attestation-v2.json` for
  new ones.
- After `installation rotate-signing-key`, a fleet roster that pins the
  machine's key reports its row as `signed-by-another-key` until the roster
  entry names the new key.
- A journal written by an earlier `update` given a URL with a password still
  holds that password on the machine. Support bundles strip it from now on;
  rotate such a token.
- With Compose 2.36 or later, the output of a task or a migration service no
  longer includes Compose's progress for the services it starts.

Statements gain `signature_verified` and `key_id` for an `update`'s own check
and `signature_verified_when_staged` for every later one. `doctor`'s
`backup.attestations-pushed` has a new description, and reports on a machine
with no backup target where it said nothing. A support bundle's journal count
includes URL passwords it removed, and `installation rotate-signing-key` has a
payload of its own. No existing field changed meaning.

### Added

- **`installation rotate-signing-key` replaces the key a machine signs its own statements with.** Retiring a key suspected of being stolen meant rebuilding the machine with `installation import`. The command mints a new key, records the old one under `signing.previous_keys` and moves the new one into place, so everything the old key signed still verifies as a predecessor's signature and never as the current key's; it repairs nothing the old key signed, and a fleet roster naming the machine needs the new key.

### Changed

- **An attestation says where a release's signature was verified.** Signatures are checked when a release is staged, and no statement said so, not even the `update` that did the checking. An `update`'s statement now sets `signature_verified` and `key_id`, and every later statement about that release carries `signature_verified_when_staged`, naming the operation that verified it. The predicate type is now `…/attestation/v2`, validated by `schemas/morzer-attestation-v2.json`; v1 statements are still read. The installation schema moves to 13, so an older manager refuses the installation rather than rewriting the release record without the check.
- **`doctor` says when a machine's attestations can never leave it.** An installation with no backup target keeps what it writes on the machine, and `doctor` said nothing about it. It now reports how many statements are on the machine and that no target is configured to push them, as information shown by `doctor --verbose` and in `--json` rather than as a warning, since nothing records whether a machine is air-gapped on purpose. The check's description now reads "attestations have left this machine", or, with no target, "attestations: no backup target to push them to".

### Fixed

- **A rollback keeps where the release it returns to came from.** The release record a rollback writes left out its source, so `update --check` on that machine afterwards had no reference to ask and needed one typed. The source the release was installed from is now carried back with it.
- **A task's output no longer carries Compose's progress for the services it starts.** `task run` printed lines such as `Container erp-postgres-1 Healthy` with the task's own stderr, and a failed migration service could quote them, because `docker compose run` writes them to the same stream. One-shot runs now set `COMPOSE_PROGRESS=quiet`, which Compose reads from 2.36; an older Compose ignores it and prints them as before.

### Security

- **A password in a bundle URL no longer reaches the journal or a support bundle.** `morzer update https://user:token@host/…` recorded the reference as typed, so the token sat in the operation journal and travelled in any support bundle sent to a vendor. The journal now keeps the URL without its password, and a support bundle strips URL credentials from every journal record, including ones written before this release. Rotate a token that was used this way.

## [0.8.0] - 2026-10-29

Three things done by hand now have commands. A release can declare tasks an
operator runs by name, `installation remove` takes an installation off the
machine, and `update` generates a secret the new release adds. A bundle author
working with an AI coding agent can give it a skill for writing bundles. Two
writes to the secret state at once no longer lose one of them, `init` keeps the
recovery key when it has no secret to write, and `exec` says when it cut a
command's output.

Upgrading changes no schema: the installation stays at 12, and a bundle built
for 0.7.0 installs unchanged. A manifest that declares `tasks:` needs
`compatibility.min_manager_version: "0.8.0"`. Six things behave differently:

- `update` generates each secret the new release declares with a generator
  and has no value for, before it converges, and an unattended update does the
  same. Such an update used to stop at `load-secrets` until somebody ran
  `morzer secret generate`; `update --generate-secrets=false` keeps that, for
  one run.
- Writes to the secret state take turns under a new lock,
  `/var/lib/<product>/manager/locks/secrets.lock`. A `secret` command run while
  another write is under way waits for it, a second or two, instead of racing
  it.
- `init` with `--recovery-recipient` on a release that declares no secrets
  creates an empty secret state for the machine's key and the recovery key,
  where it used to create none and drop the recovery key. A machine initialised
  that way by an earlier version, that has written a secret since, has no
  recovery recipient: `morzer doctor` warns, and
  `morzer secret recipients add <age-public-key> --kind recovery` adds one.
- `exec` and `task run` print a warning on stderr after output the runtime
  cut at 256 KiB a stream. Stdout and exit codes are unchanged.
- A command that prints a line longer than 4 MiB runs to completion. It used to
  block on a full pipe and be killed at its timeout.
- The journal, notifications and attestations can carry two new operation
  types, `task` and `remove`, and a task's container sees `PHASE=task`.

`exec --json` carries `truncated` when the output was cut, and `task list`,
`task run` and `installation remove` have payloads of their own. No existing
JSON field changed.

### Added

- **A release can declare tasks an operator runs by name.** Filling a sandbox with demo data, unlocking a user or rebuilding an index meant `docker exec` by hand: knowing the Compose project's labels, naming the entrypoint wrapper that loads `*_FILE` secrets, and having the container running, with nothing journalled and nothing stopping a sandbox's seed on production. A manifest's new `tasks:` names a service, a command, whether an operator may append arguments, and the machine modes it may run on. `morzer task list` shows them and whether this machine may run each; `morzer task run <task> [-- args...]` runs one as a one-shot container of the service, through its entrypoint, starting its dependencies, as the operation `task`: under the deployment lock, journalled with its arguments scrubbed of known secret values, attested and notified. A task whose `modes` leave out this machine's is refused by name before anything runs, and `--force` does not override it. The task's output is printed when it finishes, its stdout on stdout, and the invocation exits with the task's own exit code. A manifest with tasks needs `compatibility.min_manager_version: "0.8.0"`, so an older manager says it is too old.
- **`installation remove` takes an installation off the machine.** Nothing removed one, so retrying a failed first install, changing a mode fixed at creation, or retiring one of several products on a host meant deleting units, containers and directories by hand, with a glob that could reach another product's units and no record that it happened. The command runs as an operation, under the deployment lock, journaled step by step and plannable with `--dry-run`. It removes the units, the containers, the releases, the images no release left on the machine names, the rendered secrets, the configuration, the signing key and the installation record. By default it keeps the named volumes, the data and backup directories, the attestations, the journal, the secret state and the machine key, which unlock the kept data, and says where each is. `--purge` removes those too, leaving only the removal's own journal record and attestation. Nothing on a backup target is removed, and the removal's own attestation is pushed there. It needs `--force`, and on a production installation `--confirm <installation-id>` as well. A machine left by a failed `init` is cleaned with `--product` and `--force`. The removal is attested and pushed like other operations, and the journal is renamed to `operations-removed-<operation-id>.jsonl`, so a later `init` starts with an empty one.
- **`update` generates a secret the new release adds.** Only `init` generated secrets, so a release declaring a new secret with a `generator` stopped every existing machine at `load-secrets` with `required secret(s) not set`, and an unattended machine retried the same failure every tick until somebody ran `morzer secret generate` by hand. An update now generates, before it converges, each secret the new release declares with a generator that has no value and that the release being replaced did not declare; the step names them, a plan names what it would generate, and no value is printed or journaled. A secret both releases declare and nobody set stays unset, a new one with no generator still stops the update, and `update --generate-secrets=false` leaves the new ones to you. A value generated by an update that then fails is kept, so the retry uses the same one. `apply` and `rollback` generate nothing.
- **A skill for AI coding agents that write a release bundle.** An agent asked to author a bundle guessed at fields and conventions the documentation covers. The repository now ships `morzer-bundle-authoring` in the Agent Skills format, installed with `npx skills add`; it gives the agent the verify loop and the decisions a passing verify cannot catch, and sends it to the right page for each part of the bundle. The documentation build checks every command, long flag and link it names.

### Fixed

- **`exec` says when it cut a command's output.** The runtime keeps the first 256 KiB of each of a command's streams, and `exec` printed that much as if it were all of it, so `morzer exec db -- pg_dump > dump.sql` could write a dump cut short with nothing on the terminal to say so. A warning on stderr now follows output that was cut, and `--json` carries `truncated: true`; `task run` does the same. The exit code is still the command's own.
- **A command that printed a line longer than 4 MiB no longer stalls until its timeout.** The manager stopped reading a command's output at such a line, so a hook, migration service, `exec` or task that went on printing blocked on a full pipe and was killed at its timeout. The rest of the output is now read and dropped, and the output is reported as cut.
- **Two writes to the secret state at once no longer lose one of them.** Every write decrypts the whole state, changes it and encrypts it back, and nothing stopped two from overlapping, so the later one saved over the earlier one's change: a `secret set` while an update generated a secret its release adds, two `secret` commands in parallel, or a script setting several values at once. Writes now take turns under a `secrets` lock, and reading takes none.
- **`init` keeps the recovery key when it has no secret to write.** On a release that declares no secrets, `init` put off adding `--recovery-recipient` until the first secret was written and kept the key nowhere, so the state that first write created could be opened by the machine's key alone. `init` now creates the state for both keys at once. A machine initialised that way by an earlier version, with secrets written since, has no recovery recipient, and `morzer doctor` warns about it; `morzer secret recipients add <age-public-key> --kind recovery` adds one.

## [0.7.0] - 2026-10-26

Fixes found by rehearsing the whole lifecycle on rebuilt and unattended
machines. An unattended update now takes its pre-update backup, a timer runs
when it is installed rather than at the next boot, and a networked machine
pulls the image its backups read volumes through. A rebuilt machine needs no
`init --repair` after its import. A release that migrates with a service now
has a known database schema, and keeps a failed migration's last lines of
output in the error.

Upgrading changes no schema: the installation stays at 12, and a bundle built
for 0.6.0 installs unchanged. Five things behave differently:

- `installation import` installs the systemd units, as `init` does. On a host
  running systemd where the manager does not run as root, it now fails at that
  step; `--install-units=false` opts out.
- An `apply` or `update` that pulls the release's images from a registry also
  pulls the pinned volume helper image, and an `update` pulls it before its
  pre-update backup. A registry or mirror that does not carry it is a warning,
  not a failure.
- A release that migrates with a service and declares
  `database_schema_produces` has that schema recorded after its next
  successful converge. The schema checks of `rollback`, `update` and the
  unattended gate, skipped until now, then run against it, so an unattended
  update that went ahead on 0.6.0 can now be held for a person.
- Timers installed by 0.6.0 or earlier are enabled and not started, and
  `config set` does not start them. `morzer init --repair`, or a reboot, does.
- `release verify` prints a warning, and still exits 0, for a release that
  migrates with a service and declares no `database_schema_produces`.

No JSON output changed.

### Fixed

- **A release that migrates with a service never had a known database schema.** A migration run as a one-shot container cannot report the schema it leaves, so the installed schema stayed 0, and `rollback`, `update` and the unattended gate skipped their schema checks. When a migration service succeeds and no step reported a schema, the release's `database_schema_produces` is now recorded as the schema it left. A schema a hook reports still wins over the declaration. `release verify` warns, and still exits 0, when a release migrates with a service and declares no `database_schema_produces`. (#143, RFC 0033)
- **A re-apply reset a known database schema to 0.** The schema recorded after a converge came from that run's migrations alone, and a migration that reported none, such as one run as a service, recorded 0. So an `apply`, including the one at every boot, could forget the schema an earlier migration had reported, and `rollback`, `update` and the unattended gate then skipped their schema checks. A converge of the installed release that reports no schema now keeps the recorded one. An update still records only what its migrations report, because a new release's migrations may have moved the schema. How a migration service reports the schema it leaves is proposed in RFC 0033.
- **A networked machine's first backup could refuse for want of the volume helper image.** Backups read volumes through a pinned `busybox`, and nothing pulled it: `apply` pulled only the release's images, and a backup refuses rather than pulls on purpose. So unless somebody had acted on `doctor`'s warning, the first backup failed with `the volume helper image … is not on this machine`, the pre-update backup included. An `apply` or `update` that pulls the release's images from a registry now pulls the helper as well, and an `update` pulls it before its pre-update backup, so a machine that lost it, to `docker image prune -a` for example, is not refused. A failed helper pull is a warning and does not fail the operation. A release whose images all travel in the bundle pulls nothing, so an offline machine still preloads it by hand, as `doctor` says.
- **A failed migration service's output was lost unless `--verbose` was passed.** A `runtime-service` migrate step that exited non-zero reported only its exit code, and the hint sent the operator to a log that did not hold the output. The container is removed when it exits and the journal keeps no output, so a unit started by systemd, which runs without `--verbose`, lost the migration's error entirely. The error now quotes the last lines the container printed, as a failed hook's does, taken from both streams in the order they arrived, so a migration that reports its failure on stdout is not quoted as Compose's own progress on stderr.
- **`secret set` exited 6 before the first `apply`, though the value was stored.** `init` makes the release current, so the restart after a secret change rendered every secret the release declares, and refused over a required one the operator had not set yet. A release needing two values only the operator has could not be given the first without exit 6, which a script under `set -e` reads as the value not being saved. `set`, `generate`, `rotate` and `edit` now restart nothing, and render nothing, when the deployment has no containers at all. When the render or the restart fails on a deployment that has them, the error now says the change was stored, and the exit code still says which failed.
- **A timer that `init` or `config set` installed did not fire until the next reboot.** The backup, update and fleet timers were enabled and never started, so `systemctl list-timers` showed no next run and the schedule an operator had just set waited for a boot. A timer is now started when it is enabled. The product's own service is still only enabled, because starting it runs `apply --startup`. A timer an operator disabled stays stopped. On a machine set up by an earlier version, `morzer init --repair` starts its timers, and so does a reboot.
- **A failed `init --repair` removed the units it had found installed.** When installing the units failed, the undo removed every unit the product manages rather than only what the failed install had created, so a timer that would not start, or an `enable` that failed, took the product's service file with it. Only a completed install is undone now; a failed one already reverses its own changes. Separately, an install interrupted part-way now still stops and disables what it had started and enabled before removing the files it wrote.
- **`update --check` refused on a machine installed from a registry until its first update.** `init --release oci://…` recorded no release source, so the bare check said `this installation has no recorded release source`, even with `update.channel` set. `init` now records the ref it installed from, as `update` does. A machine with no recorded source, or one that cannot list versions such as a local path, now checks the repository of `update.channel` when one is set. A ref passed to `--check` is still used as given. A machine installed before this change still has no recorded source; setting `update.channel`, or one update from a ref, gives it one.
- **Attestation counts mixed two units with more than one target.** `doctor` and `attest push` counted each statement once per target that lacked it, and reported that against a total of statements, so two targets after five operations read `10 of 5 attestation(s) are only on this machine`, and the push that closed the gap reported ten. Every count is now of statements: `5 of 5 attestation(s) are missing from at least one of 2 targets`, then `pushed 5 attestation(s) to 2 targets`. With one target, `doctor` still says `only on this machine`. The per-target `missing` and `pushed` fields of `attest push --json` were already per target and are unchanged.
- **`attest verify --against-live` reported a migration service's image as missing.** A release that migrates with one-shot services, `flyway` in a container for example, attests their images, and those services exit by design once the migrations are done. So the check failed with exit 3 on every healthy machine running such a release, and could not serve as a cron or CI gate. An image that only the release's `migrate` services run is no longer expected to be running. An image that a service the deployment starts also runs is still reported when nothing runs it.
- **A machine rebuilt by `installation import` needed an `init --repair` nobody was told to run, and the remedy `doctor` printed for it failed as printed.** The import minted no signing key, so the first `update` or `restore` minted one that installation state never recorded, and it installed no systemd units, so the service and the timers stayed missing. It now mints and records the signing key, keeping the lost machine's as its predecessor, and installs the units as `init` does; `--install-units=false` opts out. Separately, `init --repair` now finds the installation the way every other command does, so `--product` is needed only on a machine with several, and secret state that already has a recovery recipient needs neither `--recovery-recipient` nor `--no-recovery-recipient`. (#135)
- **An unattended update could never take its pre-update backup.** `update --unattended`, which the update timer runs, did not wire the backup engine that a manual `update` wires. With `update.auto_apply` on and the pre-update backup left on, every tick that found a release to install failed at that step with `no backup engine is configured`, exit 10. A tick now takes the same pre-update backup a manual `update` does.

## [0.6.0] - 2026-10-20

Two things that live on the machine. A pruned release's images now leave the
store with it, on `release prune` and on dev mode's automatic prune, so a dev
machine following a channel no longer fills its disk one build's images at a
time. And a secret can be given to the user its container runs as, so an image
that does not run as root reads its Compose file secret without `user: "0:0"`.

Upgrading changes no schema: the installation stays at 12, and a bundle built
for 0.5.0 installs unchanged. Three things behave differently:

- The first prune after upgrading also removes the images of the releases it
  prunes. It removes only names that no release left on the machine uses, in
  any installation, and never forces a removal. While any release directory
  on the machine cannot be read, it removes no image at all. Images left by
  earlier prunes stay; the `release prune` reference says how to clear them
  by hand.
- `release prune` takes the deployment lock. A script that runs it while an
  `update` of the same installation holds the lock is now refused with exit 4,
  unless it passes `--wait`.
- A bundle whose secret schema declares `owner` needs
  `compatibility.min_manager_version: "0.6.0"`, and a manager running as root
  to apply it. A manager on macOS refuses `owner`, and user-namespace
  remapping and rootless Docker are not handled.

The JSON changes are additive: `release prune --json` gains `dry_run` and
`images`, and `secret render --json` gains `uid` and `gid`.

### Added

- **`release prune` removes the images of the releases it prunes.** A release directory went and every image the runtime pulled or loaded for it stayed, so a dev machine following a channel, which prunes after every update, filled its disk one build's images at a time. The pruned releases' images now go with them, on both `release prune` and dev mode's automatic prune. A name stays when any release left on the machine, in any installation and whatever its role, still names it; spellings the daemon treats as one name, such as `postgres@sha256:…` and `docker.io/library/postgres@sha256:…`, count as one. Nothing is forced: a name a container uses is reported as kept, and the prune still succeeds. If any release directory on the machine cannot be read, no image is removed and the report names the directory. Images left by earlier prunes are not reclaimed; the `release prune` reference says how to clear them by hand.
- **A secret can be given to the user its container runs as.** Every rendered secret was `0400` and the manager's, which is root, and Compose ignores `uid`, `gid` and `mode` on a file secret, so a service whose image runs as another user, as distroless and most hardened images do, could not read the secret it was given. A declaration in the secret schema now takes `owner: "65532"` or `"65532:65532"`, numeric only, and every render gives the file to that user: `apply`, an update and its restore, `secret render`, and a rotation. The mode stays `0400` and the directory `0700`. A manager that cannot give the file away refuses with exit 6, naming the secret, rather than render a file the container is refused; a manager on macOS refuses every `owner` for now. A schema using `owner` needs `compatibility.min_manager_version: "0.6.0"`, so an older manager says it is too old. `secret render` and its `--json` show each file's owner as `uid` and `gid`.

### Changed

- **`release prune` takes the deployment lock**, so it waits for or refuses beside an `update` of the same installation, and honours `--wait`. Its `--json` output gains `dry_run`, without which a plan and an act read the same, and `images` with what was removed, what was kept and why, and why removal was skipped.

## [0.5.0] - 2026-10-19

Most of this release is about the data the manager decides to remove. A failed
`update` no longer deletes a secret the running release still reads,
`backup --no-push` no longer prunes the backup targets, and an operation queued
behind the deployment lock refuses a plan made for a release that has since
moved. Only the newest three pre-update backups are kept past retention, where
every one was, which filled the disk of a machine that updates often. A manifest that names an operation the manager
never runs, or gives a `runtime-service` step to anything but `migrate`, is
refused when it loads instead of doing nothing or failing at `apply`.
Notification targets are set with `notify target add`, and a migrate step is
told which operation is running it.

Upgrading moves the installation schema 11 → 12 on the first read and converts
nothing. Going back is not automatic, because a 0.4.x manager declines the
installation rather than dropping `update.randomized_delay` and
`retention.pre_update_backups`. Three more things change on upgrade:

- The first backup or update after upgrading prunes the pre-update backups
  beyond the newest three, locally and on backup targets. To keep more, run
  `morzer config set retention.pre_update_backups=<n>` straight after
  upgrading, before the next scheduled backup or update.
- A bundle that loaded under 0.4.0 may now be refused. `operations` may name
  only `migrate`, `smoke_test`, `backup` and `restore`, and only `migrate` may
  be a `runtime-service` step. Either mistake was a step that never ran or one
  that failed every time it was reached.
- A migrate step or smoke test that branched on `OPERATION_TYPE=apply` now
  sees `update` or `rollback` on those operations, with `PREVIOUS_VERSION` set.

### Added

- **A migration service is told what is running it.** A `runtime-service` migrate step received none of the hook ABI's variables, so it could not tell an update from a re-apply or learn the version it was migrating to. It now gets the ones that mean the same inside a container: the product, installation and operation ids, `OPERATION_TYPE`, `PHASE`, `RELEASE_VERSION`, `PREVIOUS_VERSION`, `LOG_LEVEL` and `DRY_RUN`. Host paths and `RESULT_FD` are left out, so a service still reports its schema through `database_schema_produces`.
- **`backup.before_update`: a dev-mode machine can stop taking pre-update backups.** The installation's `skip_backup_before_update` was honoured by `update` but no command could set it, so a sandbox following a channel every ten minutes took a full backup before every update. `morzer config set backup.before_update=false` turns it off on a dev-mode machine. A production machine refuses, because that backup is what a failed migration is recovered from; `update --skip-backup --force` still skips it for one update.
- **`update.randomized_delay` sets how late an update tick may run.** A production update timer always waited up to 1800 seconds at random, whatever `update.schedule` said, and a dev-mode one never did. Set a duration in whole seconds, such as `10m`, or `0` for none; unset keeps those defaults. `morzer status` shows the delay beside the schedule.

### Changed

- **Only the newest pre-update backups are kept past retention.** Every pre-update backup was exempt from pruning forever, so a machine that updates every few minutes kept a full backup per update until its disk filled. Now the newest three are exempt, and older ones are pruned like any other backup, locally and on backup targets. A successful update now applies retention too, after its pre-update backup, so a machine that only updates is pruned as well. **The first backup or update after upgrading prunes the older ones already there**, unless it is a backup run with `--no-prune`. Change the three with `morzer config set retention.pre_update_backups=<n>`; the newest is never pruned. Installation state moves to schema 12, so an older manager refuses it rather than dropping either setting.
- **`morzer notify target add|remove|list`: notification targets are set by a command.** The notifications page said to configure them in `installation.yaml`, which is a report the manager rewrites, and nothing else wrote them. A target is checked when it is added, and nothing is sent to it: the URL must be `https` with no password in it, and every secret it names must be set and well formed. A URL kept in a secret is named by the target's `--name` wherever it is shown. `status` names the targets, and `doctor` warns about one whose secret has gone, which was dropped with only a log line.

### Fixed

- **`morzer exec` passed a stopped service's failure off as the command's.** The check that the service is running and the exec itself are two calls, so a container could stop between them. Compose then exited 1 with `service "db" is not running`, and `exec` exited 1 as if the command had failed. A failed command now checks the service again, and a service that is no longer running is refused with exit 7 and its state, as it is before the command runs. Whatever the command printed is still printed.
- **`morzer exec` refused a scaled service whose first instance was down.** The check read only the first instance listed, so a stopped `app-1` refused the command although `app-2` was up and Compose would have run it there. A scaled service is now refused only when none of its instances is running.
- **A failed update could delete a secret the running release still reads.** An update that failed before starting anything, on an image pull for example, left the old release's containers running against the new release's rendered secrets. Rendering removes any secret the release does not declare, so one only the old release declares was gone, and an old container that restarted could not read it. The update now renders the old release's secrets back as it rolls back. If that fails, the update stops at exit 12 instead of 11.
- **An operation that waited for the deployment lock ran a plan made for a release no longer installed.** `apply`, `update`, `rollback`, `backup` and `restore` read the current release before taking the lock. With `--wait`, the operation they queued behind could move it, so an update queued behind another update to 1.3.0 journalled `from 1.2.0` and told its hooks `PREVIOUS_VERSION=1.2.0`, and an `apply` queued behind a rollback re-converged the release just rolled off. Each now reads the release again once it holds the lock, and refuses with exit 2 if it changed, naming the release now installed. So does a change to the database schema the release recorded, and, for a rollback without `--to`, a change to the previous release. Run the command again to plan against what is there now.
- **A migrate step and the smoke test were always told the operation was `apply`.** `OPERATION_TYPE` was `apply` and `PREVIOUS_VERSION` was absent on every run, so an update from 1.2.0 looked like a re-apply. They are now told `update` or `rollback` and the version being replaced. A first install is still `apply`, as the hook ABI reference now says.
- **`backup --no-push` pruned the backup targets.** A local-only backup copied nothing to the targets but still applied retention there, removing their oldest backups to make room for one that never arrived. Retention on a target now runs only when this run's backup reached it, as an update's already did.
- **A backport release would have replaced the installer at the site root with its older copy.** Every docs release published `install.sh`, and now `schemas/`, at the site root, so tagging 0.3.5 after 0.4.0 would have served 0.3.5's installer and schemas to everyone. Only a release that moves the documentation's `latest` alias publishes them now. The nightly check compares the published schemas with the newest release, as it already did `install.sh`.
- **The update timer fetched a private `oci://` channel without credentials.** systemd sets no `HOME` for the manager's units, and the registry client looked for the Docker configuration only under `HOME`, so it went anonymous and the registry refused it, while `docker compose pull` in the same run found root's login through the password database. The manager now finds the configuration the way the `docker` CLI does: `DOCKER_CONFIG`, then `HOME`, then the password database.
- **An operation under a name the manager never runs is refused.** `operations` accepted any name, so a misspelled `smoke-test:` or an invented `post_migrate:` loaded cleanly and never ran, and the bundle's customer got no smoke test and no error. `preflight`, which the manifest reference listed as looked up, was never run either. A manifest may now declare only `migrate`, `smoke_test`, `backup` and `restore`, and **a bundle declaring any other operation, `preflight` included, is now refused** with the field named and the four listed. The published manifest schema closes the names the same way, so an editor underlines one before the manager sees it.
- **The schema URL in every scaffolded manifest returned 404.** `release new` writes a `yaml-language-server` modeline naming `https://morzecrew.github.io/morzer/schemas/selfhost-v1alpha1-manifest.json`, which is also the schema's own `$id`, and nothing published the schemas there: the documentation site is versioned, and they lived only in the repository. Each docs release now publishes `schemas/` at the site root beside `install.sh`, so an editor following the modeline gets the schema.
- **The publishing page told you to configure signing keys by editing `installation.yaml`.** That file is a report the manager writes, and an edit to it changes nothing, so a machine set up from the page required no signature from your key. The page now shows `init --signing-key … --require-signature` and `morzer release trust add`. The updating, backups, parameters and release-command pages treated the same file as configuration and are corrected too, and the hook ABI page no longer lists `preflight`, `pre-update` and `post-update` as phases a hook runs in: they are reserved, and no release runs one.
- **A `runtime-service` smoke test, backup or restore is refused when the bundle loads.** Only `migrate` can run a one-shot container. The other three handed the step to the hook runner, which a `runtime-service` step gives no command, so a `runtime-service` smoke test failed every `apply` that reached it with an internal error, a restore failed the same way, and a backup was refused only once one ran. The manifest now refuses a `runtime-service` under any operation but `migrate`, naming `hook` as what the others support.

### Security

- **A refusal no longer sends a bundle's control characters to the terminal.** A manifest, installation or secrets refusal names the field at fault, and a field under a map, such as an operation, a runtime or an extension, is named by its key as the bundle wrote it. A key carrying an escape sequence reached the terminal raw, so a release could clear the screen or rewrite what the operator read. A refusal holding a character that is not printable is now written escaped, `\x1b` rather than the character itself.

## [0.4.0] - 2026-10-17

Most of this release is about what a failed `update` leaves behind. The previous
release's services now come back up with their own secrets; an update whose
migrations left a database the old release cannot read stops at exit 12 instead
of reporting a rollback that did not happen; an unattended update that failed is
tried once rather than on every tick; and a failed `apply` or `update` no longer
removes the secrets running containers read. The update timer gets a real
maintenance window, `update.schedule`, after the documented drop-in turned out to
add a trigger rather than narrow one. `release trust` rotates signing keys
without re-creating the installation, and `operations.migrate` may list several
steps.

Upgrading is automatic: the installation schema moves 10 → 11 on the first read
and converts nothing. Going back is not, because a 0.3.x manager declines the
installation rather than dropping `update.schedule`. If you followed the old
unattended-updates page and added an `OnCalendar=` drop-in to the update timer,
remove it and set `update.schedule` instead. A bundle whose `operations.migrate`
is a list declares `min_manager_version: "0.4.0"`, which a 0.3.x manager refuses
as needing a newer manager.

### Added

- **`morzer config set update.schedule`**: when the update timer follows the channel — and installs, if `update.auto_apply` allows — as a systemd `OnCalendar` expression. This is the maintenance window RFC 0016 always said `OnCalendar` was, and nothing let you set it: every installation ticked daily at 03:30. Unsetting it returns to that default. A sandbox that should follow `main` within minutes sets `*:0/5`.
- **`morzer status` shows the update channel a machine follows**: when the timer looks at it, and whether it installs what it finds or only stages it. A channel set while `update.check` is off is reported as not polled, since no timer exists then. Until now the schedule was visible only in the unit file.
- **`morzer release trust add|remove|list`: the keys a release must be signed with can change after `init`.** Rotating a vendor key used to mean re-creating the installation. Trust the new key, install a release it signed, then remove the old one. A key is parsed before it is trusted, adding one already trusted changes nothing, and the last key cannot be removed while a signature is required, which `update.auto_apply` also needs.
- **`operations.migrate` may list several steps**, run in order before services start, on `apply` and every `update`: migrations, then the application's own bootstrap, for example. A failure in any of them stops for manual intervention as a failed migration always has. A manifest using a list declares `compatibility.min_manager_version: "0.4.0"`, so an older manager refuses it as needing a newer manager instead of reporting a malformed operation.

### Changed

- **Building from source needs Go 1.26 or newer.** The `golang.org/x` modules moved their `go` directive to 1.26.0 together, and that directive is a requirement rather than a preference, so taking any of their updates raises this floor. Published binaries are unaffected.
- **A dev-mode installation's update timer has no `RandomizedDelaySec`.** The half-hour spread exists so a vendor's registry does not see every customer at once, and a sandbox polls its own vendor's registry; with it, a five-minute schedule ticked every five to thirty-five minutes. Production timers keep it whatever the schedule.
- **Installation schema 10 → 11**, for `update.schedule`. Migration is automatic and converts nothing. The bump is for the write path, as schema 7 was for `policy.backup_schedule`: without it, an older manager's `config set` would drop the field and its next reconciliation would put the timer back on 03:30 without saying so. With it, an older manager declines the installation instead.
- **`init --dry-run` refuses a bundle the real run would refuse.** It reported "would create an installation" for a manifest the next command rejected. It now validates a local bundle's manifest and exits 2 naming it, as the run would, and `--json` carries `manifest_validated`. A remote reference is still not fetched for a plan, and the plan now says it did not validate the manifest instead of staying silent.
- **Release notes print as the vendor wrote them.** Where a bundle's `RELEASE.md` is shown, `update --check` among them, it is no longer styled or reflowed: reflowing broke the fenced command blocks an operator copies. Dropping the renderer took ten modules out of the build and 6.7 MB out of the binary.

### Fixed

- **A failed `apply` or `update` removed the secrets of running services.** When an `apply` failed after starting services, for example on a health check that never passed, its rollback deleted the rendered secrets while those containers kept running, so any that restarted could not read them. Every boot runs an `apply`, so a database slow to come up after a reboot was enough. The same happened to services already running when an `apply` or `update` began, if it failed before starting anything, for example on an image pull. The secrets now stay whenever services may be reading them; an operation that fails with nothing running still removes them.
- **A failed unattended update was installed again on every tick.** The staged release stayed staged and nothing recorded that installing it had failed, so each tick took another pre-update backup, which retention never removes, ran the migrations again, and failed again. The tick now tries a release once: after a failure it leaves the release alone until the channel moves or an operator runs `morzer update --to <version>`, and `status` says so. A tick also does nothing while an operation waits for manual intervention, where it used to fetch and stage first.
- **`init` accepted a `--signing-key` that is not a minisign public key.** Only its presence was checked, so a typo surfaced at the first update, after the installation had been created around it. The key is now parsed before anything is created.
- **A migration run as a `runtime-service` could not reach the database on a first install.** It ran with `docker compose run --no-deps`, and migrations run before services start, so nothing had started the database it migrates. Its `depends_on` services are now started first, and any condition it names is waited for. On an update, a dependency whose configuration changed is now recreated before the migration rather than after it.
- **A migration service that exited 2 stopped the deployment for manual intervention.** Exit 2 means "nothing to do" under the hook contract, and the runtime adapter already returned it as data for that reading. It is now treated as such.
- **A failed update left the new release's containers running.** Compensation put the release pointer, configuration and secrets back, but not the services, so the new release's containers ran against the old release's configuration until somebody ran `apply` — on an unattended machine, until the next boot. When the update got as far as starting services, it now brings the previous release back up with its own secrets and waits for its health checks; if that fails too, the update ends at exit 12 with both failures in its error. Restored configuration files also keep their own mode instead of coming back as 0640.
- **An update that failed after its migrations reported a rollback that had not happened.** When a step after `migrate` failed — a service that would not start, a health check that never passed — the update compensated back toward the release that was running and exited 11, "earlier changes were rolled back", although the database stayed migrated. The old release was left recorded at its old schema over a database it could not read, nothing was flagged, and the next `apply`, including the one every boot runs, ran its migrations against it. Now, when the migrations leave a database that release cannot read (judged as `rollback` judges it), the update stops at exit 12 without rolling back, leaving the new release's configuration and containers in place, and the error names the way forward (`update --to <version>`) and the way back (the pre-update backup).
- **A schedule systemd cannot parse is refused instead of stored.** `config set backup.schedule`, `config set update.schedule` and `init --backup-schedule` accepted any single line, so a typo such as `every 5m` exited 0 and left a timer systemd would not load: no scheduled backups, or no unattended updates, and nothing said so. The expression is now checked with `systemd-analyze calendar` on the machine that will run it, before anything is written. A host without `systemd-analyze` accepts the value unchecked, as before.
- **The published manifest schema rejected every bundle this manager accepts.** `schemas/selfhost-v1alpha1-manifest.json` still required the `runtime:` block that stopped being read in 0.3.0, and did not require the `runtimes:` the manager refuses a manifest without, so an editor following `$schema` or a CI validating against the file failed a valid bundle. It now requires `runtimes` and no longer offers `runtime`, and refuses an empty `runtimes`, `images` or runtime `files` as the loader does. Tests remove each top-level key of a manifest in turn, and empty each of those three, and fail when the loader and the schema disagree.
- **The documented maintenance window did not narrow anything.** The unattended-updates page told you to add `OnCalendar=Sun *-*-* 04:00:00` to the update timer, and a drop-in's `OnCalendar=` *adds* a trigger to the one in the unit — so updates ran on Sunday morning and every night as well. Remove any such drop-in and use `update.schedule` instead.
- **`apply --dry-run` and `update --dry-run` described a plan in the past tense.** They printed "demo 1.2.0 applied" and "updated demo from 1.2.0 to 1.2.0" beneath the line saying nothing was changed. They now say what they would do, as `init --dry-run` already did.
- **A manifest refusal named a temporary copy instead of the bundle you passed.** A plan, or an install from an archive, read the manifest from a staging directory and named `/tmp/morzer-plan-…/manifest.yaml`, which was gone before you could look. It now names the `--release` path or the archive.
- **`installation.yaml` could describe a change the manager never recorded.** It was written before the state store, so a refused state write left the report ahead of the record, and every backup copies the report, so a later restore brought back as true an installation that was never recorded. The state is written first now: a failure can only leave the report stale, and the next successful save rewrites it.

### Security

- **A release reference's password could reach the update timer's journal.** When a reference was refused, as malformed, as plaintext `http`, or as a `file://` URL naming a host, the error quoted it in full, password included, and a malformed one twice. `config set update.channel` refuses such a channel, but a state file edited by hand, imported or restored could still carry one, and the timer then logged its password on every tick. The refusal now quotes the reference without its password, or withholds a malformed one entirely when it has userinfo. The https source's own errors, a 404, a refused login, an unreachable server, quoted the URL with its password too, and now leave it out, as they do a malformed redirect that echoes it back.
- **An `oci://` reference carrying a username and password is refused.** The registry source takes its credentials from `docker login` and read userinfo as part of the repository name, so the password authenticated nothing and appeared only in error messages. Remove it from the reference and log in to the registry instead.
- **A release's notes could write to the operator's terminal and clipboard.** A bundle's `RELEASE.md` reached the terminal byte for byte, so an escape sequence in it, such as one that sets the clipboard, ran the moment you ran `update --check`, and the same bytes went into notification bodies. Terminal control sequences and bare control characters are now stripped from notes in every output mode, before they reach a notifier.

## [0.3.0] - 2026-08-21

The manifest gains a runtime dimension. `runtimes:` names the substrate a bundle
is written for and carries that runtime's files, profiles and options — Compose
is still the only runtime this manager drives, so what changed is that a bundle
now says so rather than the manager assuming it. An installation is fixed to its
runtime when it is created and never transitions.

**A bundle written for 0.2.0 does not install on this release.** The `runtime:`
block stopped being read: move its files under `runtimes.compose.files`, any
`runtime.project` under `runtimes.compose.options.project`, and raise
`compatibility.min_manager_version` to `0.3.0`. The refusal names all three, so
a bundle meeting this manager is told what to write instead of failing as an
unrecognised field. This is a withdrawn compatibility promise rather than a
moved date — `runtimes:` ships here for the first time, so 0.2.0 and earlier
read only the old spelling and this release reads only the new one. No version
ever read both, which is why the grace period first announced for 0.4.0 was a
window no release could have opened.

Upgrading is automatic — the installation schema moves 8 → 10 on the first read
and converts nothing. Going back is not, and here the refusal earns its keep: an
older manager meeting a state file that names a runtime it has never heard of
has exactly one adapter and would drive the installation with it, operating the
wrong substrate while reporting success.

### Added

- **A release declares which runtimes it supports, under `runtimes:`.** Each key names a runtime and carries that runtime's own files and profiles, so a bundle states the substrate it is written for instead of leaving the manager to assume the only one that has ever existed. An installation records the runtime it was created against and never transitions to another, which is what lets a later manager tell a machine that chose Compose from one that predates the question being askable. A release declaring more than one runtime is refused at `init` rather than chosen between, because choosing needs a second adapter to choose with and that arrives alongside it.

- **A release can set per-runtime options with `runtimes.<name>.options`.** The compose runtime reads `project` there — the namespace its volumes, networks and containers live in — and defaults to the product name. The manager carries these without interpreting them, and the runtime refuses a key it does not understand.

- **`morzer doctor` reports the runtime an installation is fixed to**, and fails when this manager drives a different one. There is no fix on the machine, since a runtime never transitions, so the check names the two ways out instead. With no installation yet, it asks the runtime which tools `init` will need.

- **A support bundle is signed, and `morzer support inspect` reads one back.** The signature is this installation's own key and lands beside the archive as a `.minisig`, checkable with `minisign -Vm <archive> -P <key>` like any release. It covers the file that leaves rather than the plaintext inside it, so an encrypted bundle's origin can be established by whoever receives it *before* anything decrypts — and by an intake holding no recipient key at all. A machine that has never minted a key still writes the archive, unsigned and saying so, because withholding evidence from the installation that has the least of it is the wrong failure.

- **`morzer support inspect` will not tell you a bundle is authentic on the bundle's own say-so.** The archive names the key that signed it, and that name proves nothing: whoever wrote the archive wrote the name beside the signature they made. So the check runs against this installation's recorded keys when you inspect your own archive, or against `--key` when somebody sent you one — a key you got from the operator rather than from the file. With neither, it prints the claimed key and says the signature was not checked. Nothing is extracted: an encrypted bundle is read in memory, so inspecting one leaves no readable copy behind.

### Changed

- **A planned operation's steps report `planned` rather than `pending` in `--json`.** A dry run's steps were never going to run, and `pending` means not yet, so the record was the one part of the output still claiming work was owed. The operation status is unchanged at `succeeded`: planning is what succeeded.

- **`morzer release new` scaffolds a bundle using `runtimes:` rather than the `runtime:` block this release stops reading**, and declares the manager version that spelling needs. A bundle scaffolded now therefore requires 0.3.0 or newer, which is stated by the manifest rather than discovered as a decoding error.

- **A release that changes a runtime option an installation was created with is refused.** Under Compose the project prefixes every volume, so such a release would bring the product up against storage nothing has written to and leave the real data unreferenced. The way through is a backup, a fresh `init` and a `restore`.

- **`<PRODUCT>_COMPOSE_PROJECT` is supplied by the runtime rather than by the core hook ABI.** Unchanged for every Compose installation, and absent under a runtime that has no projects instead of carrying a value it cannot mean. Hooks that need it should test for it.

- **`morzer installation import` refuses an export whose runtime this manager does not drive**, before anything on the new host is created. A runtime is fixed when an installation is created and never transitions, so importing one this binary cannot operate would rebuild a machine no command could use.

### Removed

- **The `runtime:` manifest block is no longer read.** A bundle carrying it is refused, naming what to write instead: files under `runtimes.compose.files`, `project` under `runtimes.compose.options.project`, and `min_manager_version` raised to `0.3.0`.

### Fixed

- **A release bundle no longer carries the vendor's version-control directory.** `release build` summed and `release archive` packed whatever sat in the bundle directory, so a bundle built inside a git repository shipped `.git` — history and `.git/config` included — under the signature. Bundles already published are unaffected and still verify.

- **`morzer init --release <bundle>.tar.zst` works without `--product`.** Reading the product name from an archive's manifest built a path inside the archive file, so the primary install path failed on a valid bundle. The manifest is now read from the archive's first entry, which the format guarantees is the manifest.

- **The init wizard offers the profiles of a bundle written with `runtimes:`.** It read the deprecated block directly, so from the moment the new spelling existed it offered nothing — indistinguishable from a release that declares no profiles.

- **A release that spells out a runtime option already in force is no longer refused.** An installation with no `project` runs under its product name, so a release naming that same value changes nothing, and dropping such a line is equally harmless. A value that really changes the namespace is refused as before.

- **`morzer init --dry-run` names the product it would create.** The summary read the installation out of engine state, which a plan never populates, so it closed with `installation  created for` — two empty slots and a creation claimed in the past tense, directly beneath the line saying nothing had changed. Under `--json`, `data.product` was empty for the same reason. `--repair` says *repaired* rather than *created* on both paths too: a repair on the wrong machine and a first install differ in exactly what that line reports.

## [0.2.0] - 2026-08-15

Three things a machine could not do before: sign statements about itself,
package the evidence for somebody who is not sitting at its terminal, and be
seen from outside without a control plane. The signing identity is underneath
the first and the third; the support archive is the second and needs nothing
but the files already on disk.

Upgrading is automatic — the installation schema moves 5 → 8 on the first read
and converts nothing. Going back is not: a manager refuses state written by a
newer one rather than misreading it, so a 0.1.x binary put back on this machine
declines the installation rather than quietly dropping what it cannot read.

### Added

- **`morzer support bundle` writes one archive an operator can hand to a stranger.** A `.tar.zst` carrying the operation journal, `doctor`'s results, the resolved manifest, the parameters, configuration drift, the version history, service state and bounded container logs — everything the conversation with a vendor needs, in a form they open with `tar --zstd -xf` and nothing from this project. Every component is scrubbed against the secret values the installation holds now, rather than trusting the redaction that ran when each file was written months earlier, and the container logs are dropped altogether rather than shipped unfiltered when those values cannot be loaded. Parameter values do travel, because a parameter is not a secret by construction and withholding them would hide the commonest cause of a support question. The age identity, the encrypted secret state, the signing key, the backup credentials, a recovery export and the directory where secrets are rendered in the clear are refused outright — checked against the archive's own bytes, not against the list of things it was asked to collect. `--preview` prints what would be collected, what was left out and why, and writes nothing.

- **`morzer support redact --check` reads a file and reports whether any of this installation's secret values appear in it**, changing nothing. The archive is safe by construction; the terminal output somebody is about to paste into a chat window is not, and that is the paste this command exists for.

- **A support bundle can be encrypted to your vendor, and to nobody else.** A release declares who its bundles are for; the archive is then encrypted to those keys alone, gets an `.age` suffix, and is unreadable by the machine that produced it — so an archive sitting in a ticket system, a mail thread or a bucket is not readable by the ticket system, the mail provider, or whoever later takes the host. A declaration the manager cannot use is a refusal before anything is collected, never a quiet fall back to plaintext, and `--preview` prints the recipients in full so the target can be checked against what the vendor published while the archive still does not exist. Declaring nobody still produces a plaintext archive on purpose: posting to a forum is the case the whole feature was built around.

- **Every installation has a signing key of its own.** Minted at `init`, kept readable only by root, and used to sign statements this machine makes *about itself* — never to verify a release, which stays the vendor's key and stays off deployment hosts. The public half is recorded in the installation state and travels in an export, and `morzer doctor` reports whether the recorded key and the key on disk still agree. A rebuilt machine mints a fresh key and records its predecessor's, so a signature from before the rebuild reads as *signed by a predecessor* rather than as plainly valid — collapsing the two would make rotating after a suspected compromise pointless.

- **Every lifecycle operation writes a signed record of what it did.** `apply`, `update`, `rollback`, `restore` and `config` produce an in-toto statement with a detached minisign signature beside it: the release and its content digest, the images by digest, the names of the parameters that changed, and a digest of the rendered configuration. Parameter *values* never appear; what a failing step said does, scrubbed against the installation's secret values, stripped of control characters and truncated at 300 bytes, because a record that cannot say why an operation failed is not worth pushing anywhere. Failures attest too, since the operation an auditor asks about is the one that went wrong — and an operation refused before it ran files nothing, there being nothing yet to describe.

- **`morzer attest log`, `morzer attest verify` and `morzer attest push`.** `log` reads the record back without asking whether to believe it; `verify` answers three questions separately, because an operator acts on them differently — did a key this installation knows about produce these bytes, do the version-moving statements join up, and, with `--against-live`, does the deployment running right now match the newest statement. Statements are pushed to the backup targets as they are written, `morzer doctor` reports the ones still only on this machine, and `attest push` sends those. A push that fails does not fail the operation: a record that did not leave is a gap whose local copy is still here, and failing an update over bookkeeping would stop the fix an operator was applying.

- **`morzer fleet publish` and `morzer fleet ls`: several machines visible without a control plane.** Each installation publishes one small signed document to a stable key on a backup target it already uses, and a stateless command reads them back — no agent, no listener, no database, and no inbound connection to a managed machine. The row says which installation it is, the release it runs, its mode, what the runtime reports or why that could not be taken, how many configuration targets differ, and what the last operation was — a count of drifted targets and never the diff, no parameter values, no hostnames, no logs and no configuration. `--dry-run` prints the document that would be published rather than a description of it.

- **A roster answers absence and authenticity at once.** `morzer fleet ls --expect roster.yaml` takes the file naming which installations are the fleet and which key may speak for each, so a machine that has stopped publishing is reported as absent instead of quietly missing from a list, and a row signed by another key is reported as a mismatch. A row whose signature has been *removed* is its own verdict rather than an unsigned machine: forging a signature is hard and deleting one is an unlink, so folding the two together would let anyone with write access to the target forge a row and then take away the evidence.

- **An installation with a backup target publishes its fleet row hourly.** A generated systemd timer runs `fleet publish`, so `fleet ls` sees machines nobody has logged into. `morzer backup target add` and `remove` install and take away that pair as the target arrives and goes, and a reconciliation that fails warns rather than failing the command — the target is on disk either way, and re-running would meet "already a backup target" and refuse before reaching the step that failed, leaving an operator an error with nothing to type. The timer shipped after the payload was settled rather than beside it, deliberately: a scheduled publisher built earlier would have put badly shaped objects into every bucket, and objects in buckets are the one thing this design cannot recall.

- **`morzer config set backup.schedule`**, which the schedule never was: it arrived as an `init` flag and could not be changed afterwards without re-running `init --repair`. Unsetting it returns the nightly default rather than turning backups off, because an empty `OnCalendar` is a unit systemd refuses to load.

- **`morzer config set backup.scheduled=false`, for a machine whose backups are handled elsewhere.** The backup service and timer are then not generated at all, and an existing pair is removed — the same mechanism that already takes the update timer away when you stop following a channel. `morzer doctor` and `morzer status` stop asking how old the last backup is, because you have told them the job belongs to something else rather than left them guessing. Taking one by hand still works, and a backup that exists is still expected to reach a target. Unsetting returns to scheduled backups, which is also what an installation that never mentioned the field gets.

- **The tree builds for macOS.** `go build ./cmd/morzer` with Go 1.25 or newer produces a working binary for `darwin/amd64` and `darwin/arm64`, which is a CLI for authoring bundles and looking around. There is no macOS release build and running a deployment stays a Linux server's job, so this is a development host rather than a supported target.

### Changed

- **`systemctl disable` on a morzer timer now sticks.** Every reconciliation — any `config set`, any backup-target change — re-ran `systemctl enable` on the units it manages, so an operator who switched a timer off had it switched back on by the next unrelated command, with no message. Reconciliation now enables only units it has just created; `morzer init --repair --install-units` is the one command that re-asserts enablement, which is the command whose job is to put a machine right. `morzer doctor` still reports a unit that is installed and switched off, and now names both ways out of that warning: repair it, or declare that this machine's backups are handled elsewhere.

- **Installation schema 5 → 8**, for the machine's signing key and attestation salt, `policy.backup_schedule`, and `policy.skip_scheduled_backups`. Migration is automatic on the first read and converts nothing — an installation arriving at schema 6 has no signing key and mints one the first time something asks it to sign, because acquiring a key is not something loading a file should do. Each number moved for the write path rather than the read path: an older manager rewriting this state would drop the fields it does not know, which is how an operator's maintenance window silently becomes nightly again and how a machine that declared it wants no backup timer gets one. The bump is what stops that happening quietly — a 0.1.x binary put back on this machine declines the installation instead.

### Fixed

- **`morzer init --repair` no longer drops the backup targets, the notification targets and the update channel.** It rebuilt the installation record from the flags on *that* command line, so everything an operator arranged after `init` was silently discarded — by the command they run precisely because something is already wrong. An operator found out at the next backup, during a recovery, or never. The repair now carries what it did not create, and every field of an installation is classified in a test as carried or rebuilt, so a field added later cannot be forgotten the way these three were.

- **An unrelated `morzer config set` no longer rewrites the backup window.** The schedule was an `init` flag that nothing persisted, so every later reconciliation of the systemd units rendered the *default* instead: an operator's `Mon *-*-* 04:00:00` became nightly `02:30`, with exit zero and no warning. It is now `policy.backup_schedule` in the installation, which is also what makes it settable.

- **`morzer doctor` no longer reports units this machine should not have.** It checked against the supervisor's *removal* superset, so the conditional update and fleet pairs read as `not installed` on every ordinary machine, on every run, with a remedy that could not clear them.

- **`install.sh` on macOS no longer gives advice that could not work.** It refused to install, as it still does — the release matrix is Linux only — and told the reader to build from source with Go 1.25 or newer, while the tree did not compile for darwin at all. That is the sentence somebody acts on, and it cost them an hour before failing. The refusal now says what a source build actually gets them.

### Security

- **A backup schedule can no longer add a directive to a systemd unit.** The guard trimmed the value and then inspected the trimmed copy, so a schedule whose *first* character was a newline passed validation and was stored and rendered unchanged — producing `OnCalendar=` followed by a second line of the operator's choosing in a root-owned unit file. `Unit=` in a `[Timer]` section names what the timer starts, so that is a way to have root run something else on a schedule, reachable from an `init` flag or from the manager's own state file. The value is now inspected as given, trimmed before it is stored so what was validated is what is written, and refused a third time by the renderer itself — the guard nearest the file holds for a caller that does not exist yet. Never released: `policy.backup_schedule` is new in this version.

- **`morzer installation import --mode dev` now drops the notification targets as well as the backup targets.** An import keeps the original installation id on purpose, and an export carries credentials so a rebuilt machine can reach what it needs, so a sandbox rebuilt from a production export held both. Dropping the backup targets stopped it writing into the customer's bucket; nothing stopped it reporting into the customer's alerting, and a Slack or Teams webhook URL *is* the credential — a machine that exists in order to be broken would page the on-call every time it was. Both are dropped by one list now, and every field of an installation is classified in a test as safe for a sandbox to keep or not, so a third thing to drop does not depend on somebody remembering.

## [0.1.1] - 2026-08-13

Repairs the documented way to install this. 0.1.0 is unaffected as an artifact —
its binaries, checksums and signature are what they always were, and installing
from the release assets worked throughout — but the one-line command the README
and the installation page give did not.

### Fixed

- **`install.sh` is served from the documentation site's root**, which is the URL the README and the installation page tell people to `curl`. It never was: the URL returned 404 for the whole of 0.1.0. The publication step decides whether to commit by comparing the file it is about to write against the branch, and `git diff` reports nothing at all for a path the branch does not yet track — so on the one run that matters, the first, into a root with no `install.sh`, it announced that the file was already published and exited successfully. It could only ever have worked where the file already existed.

- The installation examples name a release that exists. They pinned `--version 1.0.0`, a placeholder from before there was anything to install.

## [0.1.0] - 2026-08-12

First release. Everything here is new, so there is nothing to record as changed,
removed or fixed: entries of that kind would describe churn between unreleased
commits rather than anything an operator installing this can observe.

The manifest contract is `selfhost/v1alpha1`. It is an alpha API version on
purpose — it may change, and the mechanisms for changing it without breaking a
bundle in the field are in place.

### Added

- A command-line manager for the whole life of a self-hosted product on one Linux machine running Docker Compose: install, converge, update, roll back, back up, restore, inspect and diagnose. No agent, no control plane, and no network dependency beyond fetching a release.

- A step engine behind every mutating command, giving idempotent runs, dry-run planning, resume after interruption, and automatic compensation of completed work when a step fails. Each transition is journalled before and after it executes, so a crash mid-operation stays diagnosable.

- Dry-run planning that shows the intended steps, marks the ones whose work is already done, and renders a diff of any configuration file that would change.

- Operations that cannot be undone automatically, such as a partially applied migration, end in a state that keeps surfacing in `status` and `doctor` until an operator acknowledges it explicitly.

- `update` verifies the bundle, gates it on the compatibility the manifest declares, takes a pre-update backup and converges. A failure returns the release pointer to what was running and keeps both the staged release and the backup.

- `rollback` reports three things separately before acting: whether the containers can be reversed, whether the database schema is still readable by that release, and whether a restore is required instead. It refuses rather than guessing, forcing does not override the refusal, and the database is never rolled back automatically — an old release reading a newer schema corrupts data quietly. `--to` reaches a specific installed release, since each rollback promotes the release it displaced.

- Refusal to install a release whose declared compatibility does not admit the installed version, the running database schema, or the manager's own version. Forcing does not bypass it: a release stating it cannot be installed over what is running is stating a fact about its migrations.

- The release bundle contract under manifest version `selfhost/v1alpha1`, covering providers, runtime topology, requirements, images, configuration templates, operations, health checks, compatibility and retention. Decoding is strict, so an unknown or misspelled field is an error rather than a silent fall back to a default.

- Published JSON Schemas for the manifest, the secret schema and the installation document, generated from the types that enforce them, so a bundle author can validate in an editor or in their own pipeline without running the manager.

- Container images must be pinned by digest; a bare tag is refused at load time. Releases are identified by name and version together with the bundle's content digest, and the same version appearing with a different digest is an error rather than a warning.

- Releases install from an unpacked directory, a compressed archive, HTTPS, or an OCI registry using the credentials the machine already holds for its container images. A bundle and its archive produce the same content digest, so pinning a release does not pin a transport. A reference whose scheme cannot be fetched is refused by name.

- Signature verification for release bundles, against the public keys the installation configures rather than any the bundle names. The signature covers the per-file checksum manifest, so the two together cover every file and both stay checkable with standard command-line tools. An installation can require signing, at creation or afterwards.

- Operator parameters: a release declares the knobs an operator may set, each with a type, an optional default and the services to re-create when it changes. Set them at `init` with `--set name=value`; read and change them afterwards with `morzer config`, which reports where each value came from. A value the release does not declare, or does not accept, is refused by name before anything is created.

- A parameter reaches Compose files and hooks as a namespaced environment variable, and configuration templates as a template field, always under the same name. Changing one re-creates the services the release says depend on it rather than restarting them, because a published port is fixed when a container is created.

- A release's port requirements and health-check URLs can follow a parameter, so changing a published port moves the conflict check and the health probe with it.

- A hook ABI letting a release ship its own migrate, smoke-test, backup, restore and health-check executables. Hooks receive a documented environment, report structured results on a dedicated descriptor, and use a distinct exit code to mean nothing to do.

- Backups coordinated through the release's own hooks *and* the project's named volumes, which the manager reads for itself — the uploads directory, the generated thumbnails, the certificate store and the queue's spool are not in a database dump, and nobody notices until a restore produces a working database and an application with no files. A release that declares no backup operation at all can still produce a restorable backup.

- Each backup is wrapped in a self-describing manifest recording the installation, release, schema version, component list and checksums, and is verified by re-reading it. Retention never removes the most recent backup or the one taken before an update.

- Volume consistency is the release's declaration: an unclassified volume is read with the services that mount it stopped, `hot` is a claim the vendor makes about their own product and is recorded in every manifest taken under it, and `exclude` leaves the volume to their backup hook. `--no-downtime` skips what it would have to stop rather than quietly taking a live copy, bind mounts are reported and never captured, and the manifest records what was left out and why.

- A backup that would not fit is refused before anything is written or stopped, naming what it needs and what is free — as is a volume the manager cannot measure.

- Backup targets: somewhere a backup is kept that is not the machine that took it. `file://` for a second disk, `ssh://` for another host, and `s3://` for S3 and everything that speaks its API. Every verified backup is pushed to every target, and a failed push fails the backup. Retention, listing, fetching and verification all work against a target, and listing transfers only the manifest — the one file in a backup that is not encrypted — so it works from a machine that has lost every key it ever had.

- `restore` verifies the backup, stops writers, restores, re-applies the release and runs the smoke test, behind an explicit force flag and a typed confirmation of the installation identifier. Restoring a backup belonging to a different installation is refused unless it is asked for by name.

- Rebuilding a lost machine from an offline recovery key, with `installation export` and `installation import`. The export carries the installation identity and the encrypted secret state — never plaintext and never application data — and import keeps the original installation identifier so backups taken by the lost machine remain restorable, generates a fresh key for the new host, and revokes the lost machine's.

- Secret management covering listing, setting, generating, rotating, removing, rendering and recipient administration, plus `secret edit` for changing several in one editor session. Rotation restarts only the services a release declares as dependent on what changed.

- Several installations on one machine: `morzer ls` lists them from the state files alone — no Docker call, no lock, no network — and `--status` adds what is running, bounded per installation so one unresponsive daemon costs that row and no other. An installation whose state will not load is listed with the reason rather than left out. A command that acts on one installation is refused unless the operator says which, and `--config`, `--product` and `MORZER_PRODUCT` select one.

- Commands for a deployment that is already running and misbehaving: `logs` with follow, tail and since; `ps` for the service table with the container beside each service; `stats` for CPU, memory and I/O per container; and `exec` to run one command inside a running container, propagating its exit code. None of them takes the deployment lock, because they are what an operator runs *while* something else is happening. Every `exec` is journalled with its service, argv and exit code, and never its output.

- `installation describe` writes the installation as a file: the release and its digest, the parameters, the policy, the backup and notification targets, and the names of the secrets that must exist. It is reviewable, diffable and committable, holds no secret value and structurally cannot, and nothing reads it back — the header says so.

- Notifications for the operations that run with nobody watching, and update checking that is off by default because a check reveals an IP, a timestamp and by inference an installed version. Unattended updates install only what passes a gate the release and the installation both have to open.

- Read-only diagnostics covering host, tools, installation, secrets, runtime, systemd units and backup freshness. Every result that is not ok carries a suggested remedy, and the exit code reflects the worst finding.

- Stable exit codes distinguishing usage errors, preflight failures, a held lock, a missing installation, secret problems, runtime failures, failed health checks, incompatible releases, backup failures, rolled-back operations, and operations needing manual intervention.

- Plain and machine-readable output, resolved once at startup, honouring a non-terminal, `NO_COLOR`, `CLICOLOR`, a dumb terminal and CI without needing a flag. A machine-readable run emits exactly one object on stdout; `logs --json` is the single documented exception, because a stream has no end at which to write an envelope.

- A live view at a terminal showing each operation as a step list with elapsed times, progress where a step can report it, and a tail of the current subprocess output. It carries no information the plain mode omits, every state is distinguishable without colour, and symbols fall back to ASCII where the terminal or locale cannot render them.

- Generated systemd units for boot-time convergence and scheduled backups. The main unit will not restart on the exit code meaning manual intervention is required, so a system needing a human stops instead of looping.

- An install script, published at `https://morzecrew.github.io/morzer/install.sh` and as a release asset covered by that release's `SHA256SUMS`. It verifies before it installs — the archive against its own line in the checksum file, the checksum file's signature against a key embedded in the script, and the extracted binary against the version that was asked for — installs one file, and puts the prefix on `PATH` in one marked block in the files the shell actually reads. It never runs `sudo`.

- Shell completions for bash, zsh and fish via `morzer completion install`, which writes where each shell actually reads from and prints whatever else that shell needs.

- A documentation site, versioned per release, with a generated index of every command and the reference section that documents it. The build fails on drift between the code and the pages.

- A three-tier example bundle — frontend, backend and database — installed and exercised against real Docker on every change, so the worked example in the documentation is a bundle that runs.

### Security

- Secret values never reach process arguments, logs, the operation journal or machine-readable output. The secret type renders as redacted anywhere it is printed, a redacting log handler and subprocess output scrubbing back it up, and values are read from a terminal without echo or from standard input — there is no flag for supplying one, because process arguments are readable by other local users.

- Secret state is encrypted at rest for the machine and for an offline recovery recipient. Removing the last recipient, or the machine's own, is refused: either would make the state permanently unreadable.

- Backups are encrypted to the same recipients, so nothing in one is readable without a key except its manifest. `restore --identity` covers the case the recovery design exists for — a rebuilt machine whose new key was never a recipient of the lost machine's backups.

- Rendered secrets are written read-only to the owner, inside an owner-only directory on memory-backed storage, and files no longer backed by a declaration are removed so stale credentials do not linger. Generated configuration contains paths to secrets rather than the values. `secret edit` writes plaintext only inside that directory, in a directory of its own that is overwritten and removed however the editor exits — including a crash — because editors leave swap and backup files beside the one they were given.

- Backup target credentials are named rather than written into the installation, and a target URL carrying a password is refused: the URL is stored on disk, printed by diagnostics and quoted in support requests. Only the components a backup's manifest names are copied to a target, so a plaintext dump left by an interrupted restore is not pushed off the machine.

- An SSH target must pin its host key and no flag disables checking it. An impostor cannot read a backup, but it can accept every push and answer every listing while an operator believes they have off-site copies they do not have. A second target on the same host is handshaked in its own right rather than inheriting the first connection's key.

- Bundle extraction and configuration rendering are confined to their target directory by the operating system rather than by string inspection. Archives are bounded by entry count and by size *while they are being written*, so an archive that expands enormously is refused before it fills the disk. Entries that escape the destination, links of any kind, device nodes and other non-regular files are refused, and extracted files carry either an executable or a plain permission set so a release cannot arrive world-writable.

- Transport-layer encryption is not optional when fetching over the network and a redirect out of it is refused. Response and layer sizes are bounded as they are read rather than trusted to match what the server declared, and content fetched from a registry is checked against the digests that registry advertises for it. A registry reference naming no version is refused, since it resolves to whatever a moving tag points at today.

- The container runtime receives an allow-list of what a tool needs plus the release's declared parameters, rather than inheriting the environment of whoever invoked the manager. The configuration-template render context does not expose the process environment either.

- Secret values belonging to the installation are scrubbed from `logs` output by default, holding bytes to a line boundary so a value split across two reads is still caught. It is best effort and the documentation says so — a value a service *derived* from a secret is beyond any redactor — and `--no-redact` warns.

- Requiring signatures without configuring any signing key is refused when the installation is written, rather than reported as a failure of every later operation.

- An installation export is refused when the only key that can open it belongs to the machine being exported. Such a file looks like an insurance policy and is not one, and the moment to discover that is not during a recovery.

[unreleased]: https://github.com/morzecrew/morzer/compare/v0.10.0...HEAD
[0.10.0]: https://github.com/morzecrew/morzer/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/morzecrew/morzer/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/morzecrew/morzer/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/morzecrew/morzer/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/morzecrew/morzer/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/morzecrew/morzer/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/morzecrew/morzer/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/morzecrew/morzer/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/morzecrew/morzer/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/morzecrew/morzer/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/morzecrew/morzer/releases/tag/v0.1.0
