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

- Backup targets: somewhere a backup is kept that is not the machine that took it. `morzer backup target add` accepts `file://` for a second disk or removable media, `ssh://` for another host, and `s3://` for S3 and everything that speaks its API, including MinIO, R2, B2 and Google Cloud Storage in interoperability mode.

- Every backup is copied to every configured target after it is verified, and a push that fails fails the backup. Reporting success for data that is still only on the machine that will die is the state targets exist to end. The local copy is kept either way, so a failed push leaves an operator no worse off than before they configured one. Two exceptions warn instead: a backup taken with the push disabled, and the backup taken before an update.

- Retrying a copy that failed with `morzer backup push`, which verifies the backup again and sends it without taking a new one. The data on disk is already correct; what failed was the medium.

- Reading a target with `morzer backup list --remote` and `morzer backup fetch`. Listing transfers only each backup's manifest, which is the one file in a backup that is not encrypted, so it works from a machine that has lost every key it ever had.

- Diagnostics reporting a target that cannot be reached and a backup that never arrived on one. Both fail rather than warn: the second is the failure that hides, because the backup ran, the backup succeeded, and the copy that would survive the machine is not there.

- Retention applies on targets as well as locally, under the same policy and with the same refusal to remove the most recent backup or one taken before an update.

- Checking the copy on a target with `morzer backup verify --remote`, which streams each component through a checksum and keeps nothing. It needs no key, so an off-site archive can be checked from a machine that holds none, and it is the only thing that notices rot on a target while the local copy is still perfect.

- The backup taken before an update is copied to the targets too. A failure warns rather than blocking the update: the local copy is what a rollback on this machine uses, and refusing to install a fix because a disk was unplugged helps nobody.

- Backups now include the project's named volumes, which the manager reads for itself rather than through the release's hook. A hook is usually written by somebody thinking about the database, and the uploads volume, the generated thumbnails, the certificate store and the queue's spool are not in the dump — nobody notices until a restore produces a working database and an application with no files.

- A release that declares no backup operation at all can now produce a restorable backup, since the volumes are something the manager can read without a client for whatever wrote them. Previously such a release had a backup command with nothing behind it.

- A volume the release has not classified is captured with the services that mount it stopped, so nothing is writing while it is read. Only those services are stopped, and every such volume in one backup shares a single stop and start.

- A release can declare `consistency: hot` for a volume that may be read while the product runs, and `consistency: exclude` for one its backup hook owns. Hot is a claim the vendor makes about their own product and is recorded in every backup manifest taken under it; the manager never assumes it. Copying a live volume yields a crash-consistent copy — what a power cut would have left — so this does not replace a backup hook for anything with a transaction log.

- `morzer backup --no-downtime` never stops a service. Volumes that would need it are skipped and named in the backup manifest rather than copied live, because taking a hot copy of a volume nobody classified would be the manager making the vendor's claim on their behalf.

- The backup manifest records what was left out and why, alongside what was taken: a volume the release excludes, one skipped for downtime, and every bind mount. A backup that silently covers less than it appears to is the failure this exists to prevent.

- Bind mounts are reported and never captured. A bind mount is an arbitrary host path that may be enormous, shared, or outside anything the manager manages, so copying one is the operator's to arrange.

- Restoring a volume is refused while any service that mounts it is running, named by service. A restored volume holds exactly what the backup held, rather than merging with what was there: a volume matching no point in time, beside a database restored to an exact one, is how a record without its file is made.

- Volumes are read and written through a small helper container pinned by digest, rather than through the host's storage directory, which is an implementation detail and unreadable under a rootless or remote daemon. The source is mounted read-only. A diagnostic reports the image when it is absent, with the command to pull it, so an air-gapped machine learns about it before backup night rather than during it.

- `MORZER_VOLUME_HELPER_IMAGE` names a different image to read volumes through, for the operator whose registry does not carry busybox. Any image with a POSIX `tar`, `du`, `find`, `wc` and `sh` will do — `find` and `wc` are what the size check counts a volume's entries with. An environment variable rather than a setting, because the backup that needs it is usually the scheduled one and a systemd drop-in reaches that without regenerating a unit.

- A service that is paused counts as holding its volumes open: it is stopped before one is read, and a restore into one is refused. A paused container is frozen mid-write with its file handles open, so treating it as neither running nor stopped would let a restore write over a volume something still held.

- A backup that would not fit is refused before anything is written or stopped, naming what it needs and what is free. A diagnostic also warns when keeping the configured number of backups will not fit, since retention counts backups rather than bytes and volume backups are much larger than database dumps.

- A volume the manager cannot measure is refused the same way, naming the volume and what to do about it — a helper image that cannot walk the volume, or a `du` whose output is not a size. Starting a copy onto a disk nobody could check means discovering it does not fit once the services are already stopped. A measurement that simply did not run leaves that one volume unbudgeted instead, so a helper that exits non-zero on one awkward volume does not stop the deployment being backed up at all.

### Changed

- The installation state format moved to schema 3 for backup targets. An older manager refuses a newer state rather than reading it, seeing no targets, and quietly leaving every backup on the machine a target was configured to survive.

- The backup manifest format moved to schema 3 for volume components. An older manager refuses such a backup rather than restoring the database, silently omitting the volumes it does not know how to write, and reporting success. Backups written under earlier schemas still restore.

- The container runtime no longer inherits the whole environment of whoever invoked the manager. It receives an allow-list of what a tool needs to run, plus the release's declared parameters. Any product-prefixed variable set in a shell used to interpolate into Compose files unvalidated and unrecorded.

- The header of the operator-facing installation file no longer claims that editing it overrides release defaults. It never did: the manager reads its own state. The file is now described as a report, and names the command that changes a parameter.

- The free-form `settings` block on an installation is replaced by declared parameters. It reached configuration templates but nothing could set it, so no deployment can depend on it.

### Removed

- The configuration-template render context no longer exposes the process environment. It offered every product-prefixed variable from whichever shell invoked the manager as an unvalidated, unrecorded input to a rendered configuration file — the same channel the container runtime stopped inheriting. Nothing set it deliberately and no template used it.

### Fixed

- A secret could reach a log line through a value whose type implements only a string method, or through a plain struct. The redacting handler scrubbed strings and errors and passed everything else through for the log handler to format, so such a value printed in full. It now scrubs the rendering of anything it does not recognise.


- Container images now come from the digests the manifest pins. Previously the pull ignored the list it was given and the compose file's image references were never substituted, so a release ran whatever its topology file defaulted to and the pinning that makes a release immutable decided nothing.

- Secret generation no longer hangs when a release declares an alphabet whose length divides 256 evenly, such as the common 64-character case. Rejection sampling computed a cutoff that overflowed to zero, so every random draw was discarded and generation never terminated.

- A registry pull refused for credentials now says to run `docker login` whatever the bundle's digest happens to be. The failure was classified by searching the error's text for `404` and `not found` before `401` and `unauthorized`, and that text carries the request URL: a digest is 64 hex characters, so roughly one release in seventy contains `404` somewhere inside it. Those releases reported an expired login as a missing artifact and sent the operator to check a reference that was correct. The status the registry returned is now read from the response itself.

### Security

- Secret values never reach process arguments, logs, the operation journal or machine-readable output. The secret type renders as redacted anywhere it is printed, and a redacting log handler plus subprocess output scrubbing back it up.

- Secret state is encrypted at rest for the machine and for an offline recovery recipient. Removing the last recipient, or the machine's own, is refused because either would make the state permanently unreadable.

- Backups are encrypted to the same recipients as the secret state, so nothing in a backup is readable without a key except its manifest. Previously a backup held the database dump in plaintext, which mattered little while backups stayed on the machine and matters a great deal once one leaves it.

- Restoring accepts `--identity` for the case the recovery design exists for: a rebuilt machine has a new key that was never a recipient of the lost machine's backups, and the offline key is what opens them.

- A second SSH backup target on the same host is handshaked in its own right, so its host key is checked rather than inherited from the first target's connection.

- An SSH backup target must pin its host key, and no flag disables checking it. An impostor cannot read a backup, which is encrypted to the deployment's own recipients, but it can accept every push and answer every listing while an operator believes they have off-site copies they do not have.

- Target credentials are named rather than written into the installation, and a target URL carrying a password is refused. The URL is stored on disk, printed by diagnostics and quoted in support requests.

- Only the components a backup's manifest names are copied to a target. A backup directory can hold decrypted files left by an interrupted restore, and copying everything found there would put a plaintext database dump on a second machine.

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
