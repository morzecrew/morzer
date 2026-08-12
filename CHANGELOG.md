# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- `install.sh` is published at the documentation site's root, which is the URL the README and the installation page tell people to `curl`. It was never published for 0.1.0 and that URL returned 404: the publication step compares the file it is about to write against the branch, and `git diff` reports nothing at all for a path the branch does not track — so on the one run that matters, the first, it announced that the file was already published and exited successfully.

- The installation examples name a release that exists. They pinned `--version 1.0.0`, which was a placeholder when they were written and is not a version anybody can install.

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

[unreleased]: https://github.com/morzecrew/morzer/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/morzecrew/morzer/releases/tag/v0.1.0
