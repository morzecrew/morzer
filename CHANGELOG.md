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

- Container images must be pinned by digest. A bare tag is rejected at load time, because an unpinned image makes a release mutable and a mutable release makes rollback meaningless.

- Releases are identified by name and version together with the content digest of the bundle. The same version appearing with a different digest is reported as an error rather than a warning.

- Step engine behind every mutating command, giving idempotent runs, dry-run planning, resume after interruption, and automatic rollback of completed work when a step fails. Each transition is journaled before and after execution, so a crash mid-operation stays diagnosable.

- Dry-run planning shows the intended step list, marks steps whose work is already done, and renders a diff of any configuration file that would change.

- Operations that cannot be undone automatically, such as a partially applied migration, end in a state that keeps surfacing in status and doctor until an operator acknowledges it explicitly.

- Read-only diagnostics covering host, tools, installation, secrets, runtime, systemd units and backup freshness. Every result that is not ok carries a suggested remedy, and the exit code reflects the worst finding.

- Stable exit codes distinguishing usage errors, preflight failures, a held lock, missing installation, secret problems, runtime failures, failed health checks, incompatible releases, backup failures, rolled-back operations, and operations needing manual intervention.

- Plain and machine-readable output modes. The mode is resolved once at startup and honours a non-terminal, NO_COLOR, CLICOLOR, a dumb terminal, and CI without needing a flag. Machine-readable runs emit exactly one object on standard output.

- Hook ABI letting a release ship its own migrate, smoke-test, backup, restore and health-check executables. Hooks receive a documented environment, report structured results on a dedicated descriptor, and use a distinct exit code to mean nothing to do.

- Backups coordinated through the release's own hooks, wrapped in a self-describing manifest recording installation, release, schema version, component list and checksums. Backups are verified by re-reading them, and retention never removes the most recent one.

- Restore verifies the backup, stops writers, restores, re-applies the release and runs the smoke test. It requires an explicit force flag plus a typed confirmation of the installation identifier.

- Restoring a backup that belongs to a different installation is refused unless it is asked for by name. Forcing does not grant it: every restore already requires forcing, so a shared flag would mean the check could never apply.

- Rebuilding a lost machine from an offline recovery key, with `installation export` and `installation import`. An export carries the installation identity and the encrypted secret state, never plaintext and never application data. Import keeps the original installation identifier, so backups taken by the lost machine remain restorable.

- Import generates a fresh key for the rebuilt host, re-encrypts the secret state for it, and revokes the lost machine's key. An identity that cannot decrypt the export is refused before anything is created.

- Secret management covering listing, setting, generating, rotating, removing, rendering and recipient administration. Rotation restarts only the services a release declares as dependent on the changed secret.

- Release management covering listing, inspection, validation, fetching into the release store and pruning. Pruning never removes the current or previous release, since rollback depends on both.

- Generated systemd units for boot-time convergence and scheduled backups. The main unit will not restart on the exit code meaning manual intervention is required, so a system needing a human stops instead of looping.

- Reporting of measured sizes such as free disk space in readable units, while values declared in a manifest keep their exact written form.

### Fixed

- Secret generation no longer hangs when a release declares an alphabet whose length divides 256 evenly, such as the common 64-character case. Rejection sampling computed a cutoff that overflowed to zero, so every random draw was discarded and generation never terminated.

### Security

- Secret values never reach process arguments, logs, the operation journal or machine-readable output. The secret type renders as redacted anywhere it is printed, and a redacting log handler plus subprocess output scrubbing back it up.

- Secret state is encrypted at rest for the machine and for an offline recovery recipient. Removing the last recipient, or the machine's own, is refused because either would make the state permanently unreadable.

- Rendered secrets are written read-only to the owner inside an owner-only directory on temporary storage, and files no longer backed by a declaration are removed so stale credentials do not linger.

- Generated configuration contains paths to secrets rather than the values themselves, so a configuration file on disk never holds a credential.

- Bundle extraction and configuration rendering are confined to their target directory by the operating system rather than by string inspection, and archives are bounded by entry count and size. Symlinks and non-regular files in a bundle are rejected.

- Secret values are read from a terminal without echo or from standard input. There is no flag for supplying one, because process arguments are readable by other local users.

- An installation export is refused when the only key that can open it belongs to the machine being exported. Such a file looks like an insurance policy and is not one, and the moment to discover that is not during a recovery.

[unreleased]: https://github.com/morzecrew/morzer/commits/main
