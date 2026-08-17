# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **A release can set per-runtime options with `runtimes.<name>.options`.** The compose runtime reads `project` there — the namespace its volumes, networks and containers live in — and defaults to the product name. The manager carries these without interpreting them, and the runtime refuses a key it does not understand.

- **`morzer doctor` reports the runtime an installation is fixed to**, and fails when this manager drives a different one. There is no fix on the machine, since a runtime never transitions, so the check names the two ways out instead. With no installation yet, it asks the runtime which tools `init` will need.

- **A support bundle is signed, and `morzer support inspect` reads one back.** The signature is this installation's own key and lands beside the archive as a `.minisig`, checkable with `minisign -Vm <archive> -P <key>` like any release. It covers the file that leaves rather than the plaintext inside it, so an encrypted bundle's origin can be established by whoever receives it *before* anything decrypts — and by an intake holding no recipient key at all. A machine that has never minted a key still writes the archive, unsigned and saying so, because withholding evidence from the installation that has the least of it is the wrong failure.

- **`morzer support inspect` will not tell you a bundle is authentic on the bundle's own say-so.** The archive names the key that signed it, and that name proves nothing: whoever wrote the archive wrote the name beside the signature they made. So the check runs against this installation's recorded keys when you inspect your own archive, or against `--key` when somebody sent you one — a key you got from the operator rather than from the file. With neither, it prints the claimed key and says the signature was not checked. Nothing is extracted: an encrypted bundle is read in memory, so inspecting one leaves no readable copy behind.

### Changed

- **`morzer release new` scaffolds a bundle using `runtimes:` rather than the deprecated `runtime:` block**, and declares the manager version that spelling needs. A bundle scaffolded now therefore requires 0.3.0 or newer, which is stated by the manifest rather than discovered as a decoding error.

- **A release that changes a runtime option an installation was created with is refused.** Under Compose the project prefixes every volume, so such a release would bring the product up against storage nothing has written to and leave the real data unreferenced. The way through is a backup, a fresh `init` and a `restore`.

- **`<PRODUCT>_COMPOSE_PROJECT` is supplied by the runtime rather than by the core hook ABI.** Unchanged for every Compose installation, and absent under a runtime that has no projects instead of carrying a value it cannot mean. Hooks that need it should test for it.

- **`morzer installation import` refuses an export whose runtime this manager does not drive**, before anything on the new host is created. A runtime is fixed when an installation is created and never transitions, so importing one this binary cannot operate would rebuild a machine no command could use.

### Deprecated

- **The `runtime:` manifest block will stop being read in 0.4.0.** It still works, and `morzer release verify` says so before a bundle is published, as do `init` and `update` when an operator installs one. Moving to `runtimes:` relocates the files and `project`, and raises `min_manager_version` to `0.3.0`.

### Fixed

- **A release that spells out a runtime option already in force is no longer refused.** An installation with no `project` runs under its product name, so a release naming that same value changes nothing, and dropping such a line is equally harmless. A value that really changes the namespace is refused as before.

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

[unreleased]: https://github.com/morzecrew/morzer/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/morzecrew/morzer/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/morzecrew/morzer/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/morzecrew/morzer/releases/tag/v0.1.0
