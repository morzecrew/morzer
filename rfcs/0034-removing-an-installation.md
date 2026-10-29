# RFC 0034 — Removing an installation

- **Status:** ✅ Complete — written and built 2026-10-28, in one PR with
  [#158](https://github.com/morzecrew/morzer/issues/158). Every decision is
  `ASSUMED`: none was ruled before the build, and each is open to the author's
  ruling in review. Gaps the build exposed are in
  [logs/rfc-0034-installation-remove.md](../logs/rfc-0034-installation-remove.md).
- **Scope:** A verb that takes an installation off a machine:
  `morzer installation remove`. Covers what it removes, what it keeps by default
  and what `--purge` adds, how it is confirmed, its step order, how it ends with a
  record of itself, and a machine left behind by a failed first install. Not a
  final backup or export taken by the command, not anything on a backup target,
  and not the fleet roster's row for the machine. One new operation type, no
  manifest change, no installation schema change, no port change.
- **Related:** [`internal/lifecycle/ops/remove.go`](../internal/lifecycle/ops/remove.go),
  [`internal/lifecycle/ops/prune.go`](../internal/lifecycle/ops/prune.go)
  (`planImageRemoval`), [`internal/lifecycle/ops/attest.go`](../internal/lifecycle/ops/attest.go)
  (`emitAttestation`), [`internal/domain/paths.go`](../internal/domain/paths.go),
  [RFC 0025](0025-attesting-an-installation.md) decision 6, [RFC 0030](0030-unit-enablement-is-the-operators.md),
  [RFC 0031](0031-images-of-pruned-releases.md),
  [installation commands](../pages/docs/reference/installation-commands.md).
- **Discussion:** [#158](https://github.com/morzecrew/morzer/issues/158).

---

## 1. Summary

`morzer installation remove` runs as an operation, under the deployment lock,
journaled step by step and plannable with `--dry-run`. It removes the units, the
containers, the releases and the images only they named, the rendered secrets,
the configuration, the signing key and the manager's record of an installation.
By default it keeps the data and what unlocks it: named volumes, the data and
backup directories, the attestations, the journal, the secret state and the
machine key. `--purge` removes those too. It always needs `--force`, and on a
production machine `--confirm <installation-id>` as well, as `restore` does.

## 2. Motivation

Nothing removes an installation. `init` creates one across `/etc/<product>`,
`/var/lib/<product>`, `/run/<product>`, `/opt/<product>`, up to seven systemd
units, a Compose project and the images it loaded or pulled. The only way back
is by hand, and an operator has to know the layout to do it (#158):

- **Retrying a first install.** A failed `init` keeps the system directories
  and the machine age identity. From morzer, the operator cannot tell what is
  left or how to remove it.
- **Starting over.** `--mode` is fixed at creation, and so is the runtime's
  project. Changing either means a new installation.
- **Several products on one host.** A glob like `/etc/systemd/system/demo*`
  also matches `demo-admin`'s units.

A removal by hand also leaves nothing in the journal and no attestation, so
the record of the machine ends without saying why.

## 3. Current state

- **Paths.** `domain.Paths` derives every path from the product name.
  `/var/lib/<product>/manager/installation.json` is what makes an installation
  exist (`StateStore.InstallationExists`), and `installation.yaml` under
  `/etc/<product>` is what `ls` discovers (`DiscoverProducts`).
- **Units.** `Supervisor.ManagedUnitNames` names all seven per product, and
  `RemoveUnits` stops, disables, deletes and reloads, tolerating units that are
  not there. No unit has an `ExecStop`, so stopping one under the deployment
  lock waits for nothing.
- **The project.** `Runtime.Down` with `Volumes` removes named volumes, and its
  doc says the flag must trace back to an operator's confirmation.
- **Images.** `planImageRemoval` (RFC 0031) decides which images of a set of
  releases no other release on the machine names, and `PruneReleases` removes
  them after the release directories.
- **Reuse by `init`.** `create-identity` keeps an age identity that is already
  there, `init-secrets` keeps every secret already stored, and
  `create-signing-key` reads before it mints.
- **What reads `/etc` after an operation.** `emitAttestation` signs with the
  key under `/etc/<product>/signing`, and pushes to targets whose credentials
  `resolveTargets` reads from the secret state. `signingKeyForDocument` mints a
  key when there is none. The notifier is built with its secrets at wiring
  time.

## 4. Goals / Non-goals

**Goals**

- One command takes an installation off a machine, touching nothing whose path
  is not derived from that product's name.
- By default, the data survives, along with what is needed to read it again.
- The removal leaves a record of itself: a journal entry and an attestation,
  pushed to the configured targets.
- A machine left by a failed first install can be cleaned with the same
  command.
- A removal that fails part way is finished by running it again.

**Non-goals**

- **Taking a final backup or export.** `morzer backup` and
  `installation export` exist, and the docs give the sequence. A flag here
  would be a second way to configure a backup.
- **Anything on a backup target.** Remote backups and attestations are never
  touched, `--purge` included. They are the record that outlives the machine.
- **The fleet roster.** A machine that publishes to a roster leaves its last
  row there. Retiring a row is the roster's business.
- **The volume helper image.** It is shared by every installation on the
  machine, and no release names it.

## 5. Design

### 5.1 What goes and what stays

| What | Default | `--purge` |
| --- | --- | --- |
| systemd units | removed | removed |
| containers and networks | removed | removed |
| named volumes | **kept** | removed |
| images no other release on the machine names | removed | removed |
| `/opt/<product>`, the releases | removed | removed |
| `/run/<product>`, rendered secrets | removed | removed |
| `/etc/<product>` configuration | removed | removed |
| `/etc/<product>/signing`, the signing key | removed, last | removed, last |
| `/etc/<product>` secret state and `age/` | **kept** | removed |
| `/var/lib/<product>/data` and `backups` | **kept** | removed |
| `/var/lib/<product>/attestations` | **kept** | removed, except the removal's own |
| the journal | **kept**, renamed | only the removal's record, renamed |
| the manager's state files | removed | removed |

The secret state and the machine key stay with the data because they unlock it:

- **Backups.** They are encrypted to the machine key and the recovery keys. An
  installation made with `--no-recovery-recipient` can read its kept backups
  only with the machine key.
- **Volumes.** A kept database volume was initialised with credentials from
  the secret state.
- **Reinstalling.** A later `init` reuses both (§3), so a reinstall on this
  machine runs against the kept volumes with the credentials they were made
  with.

### 5.2 Confirmation

- **Always `--force`.** Removal is destructive on every machine.
- **Production also needs `--confirm <installation-id>`,** checked in the
  operation rather than the CLI, as `Restore` does.
- **A dev installation needs `--force` alone.** It was declared disposable at
  creation.
- **A machine with no installation record needs `--force` alone.** There is no
  id to type and no mode to read.

The refusals come before the lock is taken, so they leave no journal entry.

### 5.3 The steps

| Step | Does | Skipped when |
| --- | --- | --- |
| `remove-units` | `RemoveUnits(ManagedUnitNames)` | no supervisor, or none installed |
| `stop-services` | `Down`, with `Volumes` only under `--purge` | no release was ever recorded |
| `remove-releases` | plans the images, removes `/opt/<product>`, then the images | `/opt/<product>` is gone |
| `remove-rendered-secrets` | removes `/run/<product>` | it is gone |
| `remove-data` | `--purge` only: `/var/lib/<product>` except `manager/` | nothing is left |
| `remove-configuration` | `/etc/<product>`, except `signing/`, and `age/` and the secret state unless purging | nothing is left |
| `forget-installation` | the manager's directory, except `locks/` and the journal | nothing is left |

- **Units first,** so no timer starts an update or a backup halfway through.
- **The project is taken down with the release's own files** when the current
  release resolves. Otherwise it is taken down by project name alone, from an
  empty project directory: Compose finds the containers and volumes by label.
  The step says so.
- **The installation record is forgotten last.** A re-run after a failure then
  finds the same installation and asks for the same confirmation.
- **Every step is idempotent, has a `Check`, and aborts on failure.** Nothing
  is compensated: putting units back and restarting a project the operator is
  removing would undo what they asked for. Running the command again finishes
  the job, and so does `--resume`.

### 5.4 The record it leaves

After the engine, as every operation does:

1. **The attestation is filed, signed and pushed.**
   - The signing key is still on disk because `remove-configuration` leaves
     `signing/`.
   - The push uses targets resolved before the first step, while their
     credentials could still be read.
2. **The notification goes out.** The notifier was built with its secrets at
   wiring time.
3. **The signing key is removed,** and then `/etc/<product>` if it is empty.
   It is the last thing to go because it signed the removal's own record.
4. **The journal is renamed to `operations-removed-<operation-id>.jsonl`.**
   - A later `init` then starts with an empty journal.
   - It inherits no unfinished operation and nothing awaiting a human from an
     installation that no longer exists.
   - The history stays on disk, under a name that says what happened to it.

With `--purge`, step 1 recreates `attestations/` holding only the removal's
statement. Step 4 renames a journal that holds only the removal's record,
because `remove-data` took the rest. That is what `--purge` leaves behind.

A failure in steps 3 or 4 is a warning, as an attestation failure is (RFC 0025
decision 6): the removal has already happened.

### 5.5 Without an installation record

`--product` names the product, as it does for any command on a machine with
several. With no `installation.json`:

- `stop-services` is skipped, because nothing was ever applied.
- There is no attestation and no notification, because there is no
  installation to attest to and no configured target.
- Every other step runs as above.

### Alternatives considered

- **`morzer uninstall` at the top level.** Shorter, but `installation` is
  where list, export and import already live. `--help` on `installation`
  should show the whole lifecycle of the noun.
- **Removing everything by default, with `--keep-data`.** The failure mode of
  a wrong default is then lost data, rather than a directory the operator
  deletes by hand.
- **Keeping the data and deleting the secret state.** It looks cleaner and
  strands the data: kept backups without a recovery recipient become
  unreadable, and a kept database volume refuses the credentials a reinstall
  generates.
- **Leaving the journal where it is.** A reinstall would then inherit the old
  installation's unfinished and attention records through the
  unfinished-operation gate, and refuse to `apply` over a wreck that is no
  longer there.
- **Deleting `/etc` after the engine instead of in a step.** Keeps the signing
  key and the credentials available trivially. It also takes the most
  consequential deletion out of the step list, the plan and the journal.

## 6. Tests

- A default removal removes the units, takes the project down without
  volumes, and removes `/opt`, `/run`, the configuration, the signing key and
  the manager state. It keeps the data, the backups, the attestations, the
  secret state and `age/`, and renames the journal.
- `--purge` removes volumes and everything §5.1 lists, leaving only the
  removal's own statement and record.
- Production without `--confirm`, and any machine without `--force`, are
  refused before the lock, with nothing journaled.
- A dev installation is removed with `--force` alone.
- A machine left by a failed `init` (directories and an age identity, no
  record) is cleaned with `--product` and `--force`.
- `--dry-run` changes nothing and lists every step.
- The removal's attestation is signed, and pushed with credentials from the
  deleted secret state. The signing key is gone afterwards.
- After a removal, `init` succeeds for the same product with an empty journal.
- Images only the removed releases named are removed, and one another
  installation's release names is kept.

## 7. Docs

- `reference/installation-commands.md` gains `installation remove`, with §5.1's
  table and the confirmation rules.
- `operating/several-installations.md` points at it as the way to take one
  installation off a shared host.

## 8. Out of scope

- **A `--backup` or `--export` flag.** The docs give
  `morzer backup && morzer installation remove …`.
- **Removing a fleet roster row,** or anything on a target.
- **Removing a manager binary or its completion files.** They belong to the
  host, not to an installation.

## 9. Risks

- **The keep default leaves key material on the machine.** `age/` and the
  secret state stay with the data they unlock. An operator moving a product
  off a machine for good passes `--purge`, and the summary names what was kept
  and where.
- **A Compose without project-only `down`.** The fallback in §5.3 relies on
  Compose finding a project's containers and volumes by label from an empty
  directory. Compose 5.5 does. The fallback is used only when the current
  release cannot be read, so an older Compose fails that step and says why.

## 10. Unresolved questions

None open for the build. Every decision is `ASSUMED`, and the author may rule
differently on any of them.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | The command is `morzer installation remove`, beside `list`, `export` and `import`. The journal records it as a new operation type, `remove`. |
| 2 | `ASSUMED` | The default keeps the data and what unlocks it: named volumes, `data/`, `backups/`, `attestations/`, the journal, the secret state and `age/`. `--purge` removes them. Nothing on a backup target is touched in either mode (§5.1). |
| 3 | `ASSUMED` | `--force` always. A production installation also needs `--confirm <installation-id>`, checked in the operation. A dev installation, or a machine with no installation record, needs `--force` alone (§5.2). |
| 4 | `ASSUMED` | The removal is not gated on unfinished operations. Removing a wreck is one of its uses, and the deployment lock still serialises it against anything running. |
| 5 | `ASSUMED` | Every step is idempotent, has a `Check`, and aborts on failure. Nothing is compensated. The installation record is forgotten last, so a re-run asks for the same confirmation (§5.3). |
| 6 | `ASSUMED` | Images go by RFC 0031's rule: only names no release left on the machine names. A name that cannot be removed is reported, and the removal still succeeds. |
| 7 | `ASSUMED` | The removal attests and notifies as other operations do. Attestation targets are resolved before the first step, and the signing key is removed after it has signed the removal's statement (§5.4). |
| 8 | `ASSUMED` | The journal is renamed to `operations-removed-<operation-id>.jsonl`, so a later `init` inherits no unfinished or attention record. |
| 9 | `ASSUMED` | With no installation record, `--product` names the product. `stop-services` is skipped, and nothing is attested or notified (§5.5). |
| 10 | `ASSUMED` | When the current release cannot be read, the project is taken down by name alone, from an empty project directory. When no release was ever recorded, nothing is taken down (§5.3). |

## 12. Phasing

One PR: the operation, the command, the tests in §6, the docs in §7.
