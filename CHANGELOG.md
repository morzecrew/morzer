# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Command-line manager for the lifecycle of a self-hosted product on a single Linux machine running Docker Compose. Provides init, apply, update, rollback, status, doctor, backup, restore, secret and release commands.

- Moving between releases with `update`, which verifies the bundle, gates it on the compatibility the manifest declares, takes a pre-update backup and converges to the new release. A failure returns the release pointer to what was running, keeping both the staged release and the backup.

- Returning to the previous release with `rollback`, which reports three things separately before acting: whether the containers can be reversed, whether the database schema is still readable by that release, and whether a restore is required instead. A failed rollback returns the pointer to what was running.

- Selecting a specific installed release with `--to` on both update and rollback. Without it a second rollback returns to where the first started, since each one promotes the release it displaced, so reaching a release two steps back needs naming it.

- Refusal to roll back when the answers do not permit a safe return, naming the backup to restore from instead. Forcing does not override it, and the database is never rolled back automatically: an old release reading a newer schema corrupts data quietly.

- Refusal to install a release whose declared compatibility does not admit the installed version, the running database schema, or the manager's own version. Forcing does not bypass it: a release stating it cannot be installed over what is running is stating a fact about its migrations.

- Release bundle contract under manifest version `selfhost/v1alpha1`, covering providers, runtime topology, requirements, images, configuration templates, operations, health checks, compatibility and retention. Decoding is strict, so an unknown or misspelled field is an error rather than a silent fall back to a default.

- Release parameters: a manifest declares the knobs an operator may set, each with a type, an optional default and the services to restart when it changes. Set them with repeated `--set name=value` on init. A value the release does not declare, or does not accept, is refused by name before anything is created.

- A parameter's value reaches Compose files and hooks as a namespaced environment variable and configuration templates as a template field, always under the same name. Every declared parameter is exported, holding the release's default when the operator set nothing.

- A release's port requirements and health-check URLs can follow a parameter, so changing a published port moves the conflict check and the health probe with it. Previously the two were fixed in the manifest, and changing a port produced a deployment that worked and a converge that failed waiting for health.

- Reading and changing parameters after install with `morzer config`: list every declared parameter with its value and where that value came from, get one value alone for a script, set one or several, and unset one back to the release default.

- Changing a parameter re-creates the services the release says depend on it, rather than restarting them. A published port is fixed when a container is created, so a restart would report success and leave the old port in place.

- A diagnostic reporting when the operator-facing installation file disagrees with the recorded state, naming the fields that differ. Nothing reads that file back, so an edit to it changes nothing; the check turns a silent no-op into a diagnosis.

- A three-tier example bundle with a frontend, a backend and a database. Each application tier publishes its own port from its own parameter, credentials reach only the tier that needs them, and changing one tier's port leaves the others running. It is installed and exercised against real Docker on every change, so the worked example in the documentation is a bundle that runs.

- Complete reference for everything a bundle may reference: the variables Compose files interpolate, the fields a configuration template can use, and where each is available. All three surfaces are now gated, so a name that exists in the code and not in the documentation fails the build, and so does a documented name that does not exist.

- Container images must be pinned by digest. A bare tag is rejected at load time, because an unpinned image makes a release mutable and a mutable release makes rollback meaningless.

- Releases are identified by name and version together with the content digest of the bundle. The same version appearing with a different digest is reported as an error rather than a warning.

- Step engine behind every mutating command, giving idempotent runs, dry-run planning, resume after interruption, and automatic rollback of completed work when a step fails. Each transition is journaled before and after execution, so a crash mid-operation stays diagnosable.

- Dry-run planning shows the intended step list, marks steps whose work is already done, and renders a diff of any configuration file that would change.

- Operations that cannot be undone automatically, such as a partially applied migration, end in a state that keeps surfacing in status and doctor until an operator acknowledges it explicitly.

- Read-only diagnostics covering host, tools, installation, secrets, runtime, systemd units and backup freshness. Every result that is not ok carries a suggested remedy, and the exit code reflects the worst finding.

- Stable exit codes distinguishing usage errors, preflight failures, a held lock, missing installation, secret problems, runtime failures, failed health checks, incompatible releases, backup failures, rolled-back operations, and operations needing manual intervention.

- Plain and machine-readable output modes. The mode is resolved once at startup and honours a non-terminal, NO_COLOR, CLICOLOR, a dumb terminal, and CI without needing a flag. Machine-readable runs emit exactly one object on standard output.

- Live output at a terminal, showing every operation as a step list with the steps still to come, elapsed times, progress where a step can report it, and a tail of the current subprocess output. It carries no information the plain mode omits, and interrupting shows cancellation as a state rather than stopping the display.

- Styled diagnostics, plan and status views: doctor results grouped into a table with remedies collected underneath, dry-run configuration diffs coloured by addition and removal, and the status marked by service state. Every state is distinguishable without colour, and symbols fall back to ASCII when the terminal or locale cannot render them.

- Watching the deployment with `morzer status --watch`, redrawing on an interval until interrupted. A failed refresh leaves the last good reading on screen with the error beneath it, and the view never acts on the deployment.

- Hook ABI letting a release ship its own migrate, smoke-test, backup, restore and health-check executables. Hooks receive a documented environment, report structured results on a dedicated descriptor, and use a distinct exit code to mean nothing to do.

- Backups coordinated through the release's own hooks, wrapped in a self-describing manifest recording installation, release, schema version, component list and checksums. Backups are verified by re-reading them, and retention never removes the most recent one.

- Restore verifies the backup, stops writers, restores, re-applies the release and runs the smoke test. It requires an explicit force flag plus a typed confirmation of the installation identifier.

- Restoring a backup that belongs to a different installation is refused unless it is asked for by name. Forcing does not grant it: every restore already requires forcing, so a shared flag would mean the check could never apply.

- Rebuilding a lost machine from an offline recovery key, with `installation export` and `installation import`. An export carries the installation identity and the encrypted secret state, never plaintext and never application data. Import keeps the original installation identifier, so backups taken by the lost machine remain restorable.

- Import generates a fresh key for the rebuilt host, re-encrypts the secret state for it, and revokes the lost machine's key. An identity that cannot decrypt the export is refused before anything is created.

- Secret management covering listing, setting, generating, rotating, removing, rendering and recipient administration. Rotation restarts only the services a release declares as dependent on the changed secret.

- Release management covering listing, inspection, validation, fetching into the release store and pruning. Pruning never removes the current or previous release, since rollback depends on both.

- Installing a release from a compressed archive as well as an unpacked directory. A bundle and its archive produce the same content digest, so a digest recorded from one verifies the other and pinning a release does not pin a transport.

- Selection of a release source by reference scheme, so a reference the build cannot fetch is refused by name and told which forms are supported.

- Signature verification for release bundles, against public keys the installation configures rather than any the bundle names. The signature covers the bundle's per-file checksum manifest, so a signature and that manifest together cover every file, and both remain checkable with standard command-line tools.

- Refusal of any unsigned release when an installation requires signing, configurable at creation or by editing the installation file afterwards.

- Fetching a release over HTTPS. Transport failures and server errors are retried; a definitive answer such as not-found or unauthorised is not, because repeating the request only delays it.

- Fetching a release from an OCI registry, using the credentials the machine already holds for its container images. Listing available versions works there, which no other transport can offer, and tags that are not versions are ignored rather than presented as installable.

- A published JSON Schema for the release manifest and the secret schema, generated from the types that enforce them, so a bundle author can validate in an editor or in their own pipeline without running the manager.

- A diagnostic reporting which of a release's images are already present locally, so an operator can tell before losing network access whether the deployment would still come up.

- Reproducible signed release builds for linux amd64 and arm64, published as compressed archives with a signed checksum file.

- Editing several secrets in one editor session with `secret edit`. Rotating a related group of credentials is one logical change, and only the services that declare a dependency on something that changed are restarted.

- Diagnostics reporting secrets older than the rotation period their release declares, naming whichever command can actually replace the value. Secrets whose release states no period are not mentioned.

- Diagnostics reporting a decrypted-secret directory that is not memory-backed, since overwriting a file there does not reliably destroy it.

- An interactive first run of `init` at a terminal, which asks only for what is missing and prints the equivalent command line so the same install can be scripted afterwards. It never runs without a terminal, with assume-yes, or when the command line already answers everything.

- Generated systemd units for boot-time convergence and scheduled backups. The main unit will not restart on the exit code meaning manual intervention is required, so a system needing a human stops instead of looping.

- Reporting of measured sizes such as free disk space in readable units, while values declared in a manifest keep their exact written form.

### Changed

- The container runtime no longer inherits the whole environment of whoever invoked the manager. It receives an allow-list of what a tool needs to run, plus the release's declared parameters. Any product-prefixed variable set in a shell used to interpolate into Compose files unvalidated and unrecorded.

- The header of the operator-facing installation file no longer claims that editing it overrides release defaults. It never did: the manager reads its own state. The file is now described as a report, and names the command that changes a parameter.

- The free-form `settings` block on an installation is replaced by declared parameters. It reached configuration templates but nothing could set it, so no deployment can depend on it.

### Removed

- The configuration-template render context no longer exposes the process environment. It offered every product-prefixed variable from whichever shell invoked the manager as an unvalidated, unrecorded input to a rendered configuration file — the same channel the container runtime stopped inheriting. Nothing set it deliberately and no template used it.

### Fixed

- A secret could reach a log line through a value whose type implements only a string method, or through a plain struct. The redacting handler scrubbed strings and errors and passed everything else through for the log handler to format, so such a value printed in full. It now scrubs the rendering of anything it does not recognise.


- Container images now come from the digests the manifest pins. Previously the pull ignored the list it was given and the compose file's image references were never substituted, so a release ran whatever its topology file defaulted to and the pinning that makes a release immutable decided nothing.

- Secret generation no longer hangs when a release declares an alphabet whose length divides 256 evenly, such as the common 64-character case. Rejection sampling computed a cutoff that overflowed to zero, so every random draw was discarded and generation never terminated.

### Security

- Secret values never reach process arguments, logs, the operation journal or machine-readable output. The secret type renders as redacted anywhere it is printed, and a redacting log handler plus subprocess output scrubbing back it up.

- Secret state is encrypted at rest for the machine and for an offline recovery recipient. Removing the last recipient, or the machine's own, is refused because either would make the state permanently unreadable.

- Rendered secrets are written read-only to the owner inside an owner-only directory on temporary storage, and files no longer backed by a declaration are removed so stale credentials do not linger.

- Generated configuration contains paths to secrets rather than the values themselves, so a configuration file on disk never holds a credential.

- Bundle extraction and configuration rendering are confined to their target directory by the operating system rather than by string inspection, and archives are bounded by entry count and size. Symlinks and non-regular files in a bundle are rejected.

- Secret values are read from a terminal without echo or from standard input. There is no flag for supplying one, because process arguments are readable by other local users.

- Archive extraction refuses entries that escape the destination, links of any kind, device nodes and other non-regular files, and anything beyond the entry, per-file or total size limits. The limits apply while files are being written, so an archive that expands enormously is refused before it fills the disk rather than after.

- Extracted files carry either an executable or a plain permission set, so a release cannot arrive world-writable regardless of how it was packed.

- Requiring signatures without configuring any signing key is refused when the installation is written. The policy could not be satisfied by any release, and reporting that as a failure of each later operation would point at bundles instead of at configuration.

- Editing secrets writes plaintext only inside the memory-backed render directory, in a directory of its own that is overwritten and removed however the editor exits — including a crash or a non-zero exit. The whole directory goes, because editors leave swap and backup files beside the one they were given.

- An installation export is refused when the only key that can open it belongs to the machine being exported. Such a file looks like an insurance policy and is not one, and the moment to discover that is not during a recovery.

- Transport-layer encryption is not optional when fetching over the network, and a redirect out of it is refused. Otherwise a server could hand over a release unencrypted by asking politely, defeating the refusal of plaintext references.

- Response and layer sizes are bounded while they are read rather than trusted to match what the server declared, since the declaration comes from the same server as the content.

- Content fetched from a registry is checked against the digests that registry advertises for it, which the client library does not do on its own. A registry serving bytes other than the ones it named is refused rather than discovered later.

- A registry reference naming no version is refused. Such a reference resolves to whatever a moving tag points at today, which is what content-addressed release identity exists to prevent.

[unreleased]: https://github.com/morzecrew/morzer/commits/main
