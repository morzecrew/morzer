# RFC 0017 — Recovery artifacts

- **Status:** ✅ Complete — P0 shipped 2026-08-08: `<PRODUCT>_BACKUP_DIR` is
  documented as an ABI, which is what makes P1's retirement of the `secrets`
  component legitimate rather than a removal from an unstated contract.
  **P1–P3 shipped 2026-08-09.** `TestRecoveryRebuildsAMachineFromABackupAlone`
  is the claim: a machine created, backed up once, destroyed entirely, and
  rebuilt — identity and every secret — from that backup plus the offline key,
  with no export file existing at any point.

  One thing the design did not anticipate, found by the acceptance run and not
  by any unit test: **`restore` decrypts every component with the machine's
  identity**, and decision 11 makes the export the one component the machine
  cannot read. A perfectly good backup therefore failed at the step that puts
  data back, after the services had been stopped. `restore` now skips the
  export, which §4 already implied — "identity comes from `import`, before it"
  — without anyone noticing it had to be enforced somewhere.

  Question 2 is settled by omission and question 3 stays open; see §10.
- **Scope:** Makes a backup sufficient to rebuild a machine's identity, by having
  it carry a real `InstallationExport` instead of the four-file approximation it
  copies today — one of which is the operator-facing `installation.yaml` that
  this codebase already ships a `doctor` check for *because it drifts from the
  authoritative state*. Adds `installation import --from-backup`, retires the
  now-duplicated `secrets` component, redesignates `config` as forensic rather
  than recoverable, and adds a single-file fetch to the `BackupTarget` port so
  importing from a remote backup transfers kilobytes rather than the whole
  archive. Does **not** merge export and backup into one artifact, does **not**
  change what `restore` puts back, and does **not** remove `installation export`
  — a release with no backup hook and no capturable volume is *refused* a
  backup, so some installations can never produce one.
- **Related:** [0003](0003-secrets-recovery-and-onboarding.md) (export/import,
  and the recovery procedure this changes) · [0009](0009-backup-targets.md) (the
  target port, and the credentials an export already carries) ·
  [0010](0010-compose-volume-capture.md) (the refusal that keeps `export` alive)
  · [`internal/domain/export.go`](../internal/domain/export.go) ·
  [`internal/adapters/backup/hookbackup/backup.go`](../internal/adapters/backup/hookbackup/backup.go)
  (`captureManagedComponents`) ·
  [`internal/lifecycle/ops/recovery.go`](../internal/lifecycle/ops/recovery.go)
  (`Export`, `Import`) · [`internal/ports/target.go`](../internal/ports/target.go)
  · [`internal/ports/backup.go`](../internal/ports/backup.go) ·
  [`pages/docs/operating/recovering-a-lost-machine.md`](../pages/docs/operating/recovering-a-lost-machine.md)

---

## 1. Summary

Recovering a machine takes three artifacts: a recovery key, an export, and a
backup. Two of them are produced automatically — the key at `init`, the backup
by a systemd timer this repository generates. The third is a manual command that
nothing schedules, nothing reminds anyone to re-run, and no `doctor` check
reports on.

So the artifact carrying identity is the one most likely to be missing or stale,
and the artifact everybody has is the one that cannot rebuild identity — even
though it already contains almost everything needed to.

This RFC has the backup carry a real export. For an installation with a recovery
recipient, recovery becomes **recovery key + any backup taken after this ships**
— and `installation export` remains for the installations that cannot back up at
all, and as the answer for backups that predate this or that were taken with no
recovery recipient to encrypt the component to (§5.4).

## 2. Motivation

**The backup already contains a near-copy of an export, and it is the wrong
copy.** `captureManagedComponents`
([`backup.go:415`](../internal/adapters/backup/hookbackup/backup.go)) writes
five files across three components:

| Backup file | Export field | Same thing? |
| --- | --- | --- |
| `installation.yaml` | `Installation` | **No** — the backup takes `paths.InstallationFile()`, the operator-facing YAML in `EtcDir`. The authoritative state is `installation.json` under `ManagerDir` ([`paths.go:87,99`](../internal/domain/paths.go)) |
| `application.yaml` | — | No equivalent; a rendered config file, not identity |
| `secrets.sops.yaml` | `Secrets.State` | Yes, byte for byte |
| `secrets.recipients.yaml` | `Secrets.Recipients` | Yes — including the roles, which is why the sidecar travels ("without it a restore loses which key was the recovery key") |
| `manifest.yaml` | `Release` | Partly — version yes, **digest no** |

The first row is the defect. The codebase already knows those two files
diverge: `doctor` ships a `config.installation-file` check for exactly that
disagreement, added because "nothing reads that file back, so an edit to it
changes nothing" ([0007](0007-operator-parameters.md)). A backup therefore
carries a copy of the installation that is *documented as possibly wrong*, in
the artifact an operator would reach for during recovery.

**The duplication is not free either.** Two producers of the same recovery
payload means two things to keep correct, and only one of them is exercised by
the recovery tests.

**The scheduling asymmetry is the operational half.** `backup` has a generated
`OnCalendar` timer and a `backup.target-freshness` diagnostic. `installation
export` has neither. The result is predictable and is the failure this RFC was
written after being asked about: an operator with a year of nightly backups in a
bucket, a recovery key in a password manager, and no export — who reads the
documentation and concludes their data is unrecoverable.

**It very nearly is not.** The data comes back today via
`restore --allow-cross-installation`. What is lost is the installation id and
the secret state — and the secret state is *sitting in the backup*, encrypted to
the same recovery key, simply never reinstated. The gap between "we have the
bytes" and "the tooling can use them" is this RFC.

## 3. Current state

Verified against `b40d067`.

| Fact | Where |
| --- | --- |
| Backup captures `installation.yaml` + `application.yaml` (`config`), `secrets.sops.yaml` + `secrets.recipients.yaml` (`secrets`), `manifest.yaml` | [`backup.go:415-467`](../internal/adapters/backup/hookbackup/backup.go) |
| The captured installation is the **operator-facing** file, not the authoritative state | [`paths.go:87`](../internal/domain/paths.go) vs [`paths.go:99`](../internal/domain/paths.go) |
| `doctor` has a check for those two disagreeing | `config.installation-file` in [`doctor.go`](../internal/lifecycle/ops/doctor.go) |
| Every component is age-encrypted to the secret store's recipients and gains a `.age` suffix | [`backup.go:570`](../internal/adapters/backup/hookbackup/backup.go), `agecrypt.Extension` |
| `restore` stages components for decryption, restores volumes, runs the hook — and **never writes the secret state back** | [`backup.go:729-820`](../internal/adapters/backup/hookbackup/backup.go) |
| `Import` consumes a `domain.InstallationExport` value; the file is parsed by the caller | [`recovery.go:232`](../internal/lifecycle/ops/recovery.go), [`installation.go`](../internal/cli/installation.go) (`LoadExport`) |
| A release with no backup operation and no volumes in scope is **refused** a backup | [`backup.go:246`](../internal/adapters/backup/hookbackup/backup.go) |
| `BackupManifest` carries a `SchemaVersion`, and schema 1 (plaintext components) is still readable | [`backup.go:114`](../internal/ports/backup.go), `stage()` |
| **`BackupTarget.List` already reads one named object per remote backup** without transferring the rest | [`target.go:41-43`](../internal/ports/target.go), [`blob.go:175-205`](../internal/adapters/target/blob/blob.go) |
| `Fetch` copies a whole backup; there is no single-file fetch | [`target.go:45-46`](../internal/ports/target.go) |
| The export carries backup target credentials, so a rebuilt machine can reach the bucket | [0009](0009-backup-targets.md), `ExportedSecrets` |

The last two rows are why §5.3 is small rather than speculative: reading one
named file out of a remote backup is not a new capability, it is `List`'s
existing mechanism with a different filename.

## 4. Goals / Non-goals

**Goals**

- A backup taken by an installation with a recovery recipient is sufficient to
  rebuild identity and secrets, without an export.
- One producer of the recovery payload, not two.
- Importing from a remote backup does not require downloading the data.
- The roles of every artifact in a backup are stated, so nothing looks
  recoverable that is not.

**Non-goals**

- **Merging export and backup.** The export is small, static and safe to take at
  any moment; a backup is large, slow and refused outright for some releases.
  One artifact would be the worst of both — see §5.5.
- **Changing what `restore` restores.** It puts back data. Identity comes from
  `import`, before it. This RFC does not make `restore` write secrets.
- **Removing `installation export`.** §5.5.
- **Automatic recovery.** Nothing here rebuilds a machine unattended. Every step
  stays an operator running a command with a key in their hand.

## 5. Design

### 5.1 A backup carries an export

A new component, `ComponentExport`, writes `export.yaml` into the backup — the
same document `installation export` produces, from the same code path, encrypted
like every other component.

```go
ComponentExport Component = "export" // the full InstallationExport document
```

Because the export is authoritative where the current `config` copy is not, this
also settles what the neighbouring files are *for*:

| Component | Role after this RFC |
| --- | --- |
| `export` | **Recovery.** Authoritative installation, secret state, recipients with roles, release version *and digest* |
| `config` | **Forensic.** What the operator-facing files said at the time — useful in an incident review precisely *because* it can disagree with the state |
| `secrets` | **Retired.** §5.2 |

That distinction is the point. Today `installation.yaml` sits in a backup
looking like it is enough to rebuild a machine, and is not. Naming both roles
turns a trap into two labelled artifacts.

`BackupManifest.SchemaVersion` bumps, following the schema-1 precedent already
in `stage()`. Nothing has been released, so there are no backups in the wild to
stay compatible with — the versioning is here because it will matter after the
first tag, not because it is protecting anyone today.

**The export component is encrypted to the recovery recipients only**, not to
the full recipient list every other component gets. A running machine never
reads the export out of its own backup: it has its own state. Only a *rebuilt*
machine does, and it is holding the offline key.

The property that buys is worth stating in one line: **the export component is
unreadable by the machine that wrote it.** Compromising the live host — the one
that is online and attackable — yields the data but not the identity bundle.
Only the offline key opens that, and by construction the offline key is not on
the machine.

This is why the component's recipients are a per-component decision rather than
a property of the backup. `encryptComponents` currently takes one list for
everything ([`backup.go:570`](../internal/adapters/backup/hookbackup/backup.go));
it gains a per-record answer instead.

An installation created with `--no-recovery-recipient` has no recovery key to
encrypt to, so the component is **skipped entirely** rather than falling back to
the machine key. Falling back would produce an identity bundle readable by
exactly the key that dies with the machine — the appearance of a recovery path
and none of the substance. Such an installation has no recovery story by the
operator's own choice, and `doctor` already says so through
`secrets.recovery-recipient`.

### 5.2 `secrets` is retired, and `config` is not

`ComponentSecrets` is removed from new backups. It is subsumed **byte for byte**
— `ExportedSecrets.State` is documented as "the encrypted document, byte for
byte as it sits in `secrets.sops.yaml`", and `Recipients` carries the roles the
sidecar existed to preserve. Keeping both would put the secret state in a backup
*twice*, which is a worse duplication than the one this RFC set out to remove.

Nothing plausibly reads it: `secrets.sops.yaml.age` is an encrypted blob inside
an encrypted component, useless to a restore hook.

`ComponentConfig` **stays**, and the reasoning is deliberately different.
`installation.yaml` and `application.yaml` are human-readable, small, and useful
in an incident review. Removing them saves kilobytes and loses the forensic
record.

**And the directory's contents become an ABI in the same change.** The published
hook contract names `<PRODUCT>_BACKUP_DIR`
([`hooks.md:41`](../pages/docs/reference/hooks.md)) and stops there — it never
says what is *in* it. That is arguably the fourth bundle-facing ABI to
[0007](0007-operator-parameters.md)'s three, and it is unenumerated.

Which is why retiring `secrets` and documenting the directory belong in one
change rather than two. After the first tag, whatever vendors happen to read
becomes a contract regardless of what any page says; today there are no vendors,
so the enumeration is simply true and anything left out of it is legitimately
removable. Doing it in the other order — remove first, document later — would
be removing something from a contract that had never stated its own contents.

### 5.3 `import --from-backup`

```text
morzer installation import --from-backup --identity ~/recovery.key
morzer installation import --from-backup <id> --identity ~/recovery.key
morzer installation import --from-backup --target s3://… --credentials-file ./creds.yaml \
    --identity ~/recovery.key
```

`Import` already takes a `domain.InstallationExport` **value** and the CLI
parses the file ([`recovery.go:232`](../internal/lifecycle/ops/recovery.go)), so
this is a second way to obtain that value and no change to `Import` itself. The
component is decrypted with the supplied identity — the same key the operator is
already passing — and parsed exactly as a file-based export is.

**The identity source and the data source are separate choices, and the design
must not fuse them.** An export is taken deliberately; a backup is picked for
what its database holds. If `--from-backup` demanded an id, an operator
restoring to a point in time would silently get that moment's *identity* too —
a backup from before a secret rotation carries the superseded secrets, and
nothing about choosing it for its data says so.

So they are decoupled: **no id means the newest backup's export**, which is
almost always the right identity, because staleness here only ever loses
information. `restore` then independently takes whatever backup the operator
wants for its data. An explicit id is honoured — point-in-time identity recovery
is a real need — and **warns when it is not the newest**, naming the newer
backup and the gap between them.

**And it warns when identity and data are far apart.** Decoupling the two
choices creates a pairing the first draft did not guard: a *newer* export with an
*older* data backup. `Import` installs the export's secret state unchanged, while
`update` only compares the installed release against the incoming bundle — so a
secret rotated after the chosen backup was taken comes back in its new form
beside a database that still expects the old one, and every compatibility check
passes. So `import --from-backup` names both timestamps and warns when the
export it used is newer than the backup the operator intends to restore from.
It does not refuse: point-in-time recovery is legitimate, and the operator is the
only one who knows which secrets the restored data depends on.

`import` already "prints what it assumed and what to do next"
([`recovering-a-lost-machine.md`](../pages/docs/operating/recovering-a-lost-machine.md)),
so the chosen backup and its timestamp appear without a new mechanism.

**From a remote target, it must not download the backup.** A 4 KB document
should not cost 50 GB and an hour, least of all during an incident. So
`BackupTarget` gains:

```go
// FetchFile copies one named file out of a remote backup into destDir.
//
// List already reads backup.json this way; this is the same mechanism with
// the filename as a parameter.
FetchFile(ctx context.Context, ref RemoteRef, name, destDir string) error
```

This is a small addition precisely because `List` proves the capability:
[`blob.go:175`](../internal/adapters/target/blob/blob.go) enumerates keys and
reads only the one named `backup.json` per backup. `file://`, `ssh://` and
`s3://` all route through the same helper.

**A named file is not a bound file.** `FetchFile` returns whatever object sits
at that key, and nothing in "fetch `export.yaml.age` from backup X" establishes
that the bytes belong to backup X — an attacker with write access to the target,
or a botched sync, can put another installation's export there. So the fetch is
checked against the backup's own manifest before it is used: `backup.json` names
every component with its `SHA256`, `List` already reads that manifest without
transferring anything else, and the digest is of the *stored* bytes, so the check
needs no key ([`backup.go`](../internal/ports/backup.go), `ComponentRecord`).
A component whose digest does not match the manifest of the backup it was
requested from is refused. The test swaps two individually valid exports between
two valid backups and expects both to be rejected — a test that fetches and
decrypts successfully would pass without the binding.

`backup list --remote` already needs no key, so an operator can see what exists,
pick one, and pull a few kilobytes out of it before deciding whether to spend the
bandwidth on the data.

### 5.4 A backup with no export component

Refused by `--from-backup`, naming the alternative:

> backup 01J8Z… carries no installation export, because the installation that
> took it has no recovery recipient. Configure one with
> `morzer secret recipients add`, then take a new backup — or recover identity
> from `installation import <export>`.

and, for a backup that merely predates this change:

> backup 01J8Z… predates installation exports in backups. Take a new backup on a
> machine that still runs, or recover identity from
> `installation import <export>`.

The failure mode this exists to prevent is a partial import that looks like it
worked. A machine with an id and no secrets is worse than a refusal, because it
passes `restore`'s installation-id check and then runs a product whose
credentials are all missing.

Two cases reach it, and neither is a compatibility burden: a backup taken by a
pre-0017 build, and one from an installation with no recovery recipient (§5.1),
where the component was skipped rather than written unreadably. The two need
different messages, and the first draft had only one. Telling an operator whose
installation has no recovery recipient to "take a new backup" prescribes the
action that reproduces the failure exactly — the next backup omits the component
for the same reason. That message names the missing recipient instead, which is
also what `doctor` reports as `secrets.recovery-recipient`.

### 5.5 Why `installation export` survives

Three reasons, and the first is decisive:

- **Some installations can never produce a backup.** A release with no backup
  operation and no volumes in scope is *refused*
  ([`backup.go:246`](../internal/adapters/backup/hookbackup/backup.go)), because
  [0010](0010-compose-volume-capture.md) decided an empty backup is worse than a
  refusal. A product with no data still has an identity and secrets worth
  recovering.
- **An export can be taken before a release is installed.** `init` then export
  is a valid state; there is nothing to back up yet.
- **It is read-only, lock-free and safe at any moment** — which is why it is the
  one thing an operator can be told to run without qualification.

What changes is its *status*: from a prerequisite for recovery to an
optimisation and a fallback. That is also why this RFC does not add the
`secrets.export-freshness` diagnostic that a previous discussion proposed —
`backup.target-freshness` already covers every installation that backs up, and
adding a second freshness warning for an artifact that is no longer required
would be noise. An installation that *cannot* back up is the one case where an
export-freshness check would say something true, and it is left as unresolved
question 3.

#### Alternatives considered

**Make the export contain a backup.** Rejected, and it is the intuitive shape of
the idea. It inverts the property that makes an export useful: small, static,
takeable at any time, storable in a password manager. An export that contained
50 GB of volumes would be taken never.

**Leave both and schedule exports on their own timer.** Rejected as the primary
answer: it fixes the freshness half and leaves the duplication, so the backup
keeps carrying a `installation.yaml` that looks recoverable and is not. It also
adds a second timer, a second retention policy and a second freshness check for
a payload the backup is already carrying.

**Have `restore` reinstate the secret state.** Rejected — it conflates two
operations with different preconditions. `restore` runs *after* a release is
installed and requires the product to be stoppable; identity has to exist before
any of that. Recovery is `import` → `update` → `restore`, and moving secrets
into the last step would reverse the order it depends on.

## 6. Tests

- **The recovery path with no export at all**, end to end and against real age
  keys, as the existing `TestRecoveryRebuildsAMachineFromAnOfflineKey` does with
  one: create, back up, destroy the root, `import --from-backup`, assert the
  installation id and every secret come back. This is the claim of the RFC, so
  it is the test that must exist before anything else.
- **Equivalence, compared after decryption.** The export inside a backup and the
  one `installation export` writes on the same machine at the same moment must
  be the same document. It cannot be asserted on the stored bytes: §5.1 encrypts
  the component to a *different recipient set*, and age is non-deterministic
  anyway, so two identical documents have different ciphertexts. Decrypt both,
  compare the `InstallationExport`. Without this the "one producer" claim decays
  quietly into two — and with the wrong version of it, the test fails for a
  reason that has nothing to do with the claim.
- **Recipient access, asserted separately** from equivalence, because they are
  different properties and one test cannot carry both: the component decrypts
  with the recovery identity and not with the machine identity (§5.1).
- **The machine cannot read its own export component.** Decrypt it with the
  machine identity and assert failure; decrypt with the recovery identity and
  assert success. The first half is the whole point of §5.1 and the half a
  refactor would quietly drop.
- **An installation with no recovery recipient produces a backup with no export
  component** — not one encrypted to the machine key. The failure this prevents
  is an identity bundle that looks recoverable and is readable only by the key
  that died.
- **Newest-by-default, and the staleness warning**: `--from-backup` with no id
  picks the newest; with an older id it still works *and* warns, naming the
  newer backup. Both halves, or the warning becomes a refusal or a no-op.
- **A backup without an export component is refused, and still restores data** —
  the pair, since the refusal must not widen into a refusal to read the backup
  at all.
- **`FetchFile` transfers one file**, asserted by byte count against the target
  fake, not by reading the result. A correct file fetched by downloading
  everything passes an equality check and fails the point.
- **The retired `secrets` component**: a new backup does not contain
  `secrets.sops.yaml.age`, and a restore of one that does still works.
- **The "captured nothing" refusal stays volume-specific.** Not a test that the
  `export` component fails to satisfy it — it structurally cannot, since the
  predicate counts `capturedVolumes`. A guard instead: a release with no hook
  and no volumes must still be refused a backup after this change, so a later
  refactor that generalises the predicate into a component count fails here
  rather than in production.

## 7. Docs

- `operating/recovering-a-lost-machine.md` is substantially rewritten. It
  currently opens with "You have three things, or you do not recover" — which
  becomes two for anyone taking backups. The manual two-step decrypt recipe
  added in `b40d067` becomes the fallback for pre-schema-2 backups rather than
  the main path for operators without an export.
- `operating/backups.md`: what a backup now contains, and that it is an identity
  artifact as well as a data one — which changes where a reader should be willing
  to store one.
- `reference/backup-targets.md`: `FetchFile` has no operator surface, but
  `import --from-backup --target` does. The page also gains a hardening note
  that is available today and documented nowhere — give the manager put+get on
  the bucket, withhold `DeleteObject`, run with `--no-prune-remote`
  ([`commands.go`](../internal/cli/commands.go)), and let bucket lifecycle rules
  apply retention.

    **Withholding `DeleteObject` is necessary and not sufficient**, and the page
    must say so rather than implying a guarantee it does not give. On S3 a
    `PutObject` to an existing key *replaces* it, so credentials that can write
    can still destroy history without ever calling delete. The property an
    operator actually wants is immutability — Object Lock, or versioning with a
    lifecycle policy the backup credentials cannot alter. Without one of those,
    put+get bounds the damage rather than preventing it.
- `reference/hooks.md`: the contents of `<PRODUCT>_BACKUP_DIR`, enumerated as an
  ABI for the first time (§5.2), naming what a hook may rely on and what it may
  not. This ships **with or before** the retirement of `secrets`, not after.
- The `installation export` documentation reframes it as an optimisation and a
  fallback, without discouraging it: it is still the fastest path and the only
  one for installations that cannot back up.

## 8. Out of scope

- **Scheduling exports.** Made unnecessary for machines that back up, and §5.5
  explains why a second timer is the wrong shape. Reopens only for the
  cannot-back-up case, together with unresolved question 3.
- ~~**Encrypting the export to a different recipient set than the backup.**~~
  **Reversed 2026-08-09.** This item said there was "no case for divergence" —
  and then §5.1 and decision 11 made exactly that divergence the RFC's central
  security property. The export component is encrypted to the **recovery
  recipients only**; every other component keeps the full set. The consequence
  this item worried about is real and accepted: a backup's components need two
  keys, and that is the point — the machine holds one of them and cannot read
  its own identity bundle. Left visible rather than deleted, because an
  implementer reading the original wording would remove the isolation.
- **Restoring across products or renaming one.** Unchanged from
  [0003](0003-secrets-recovery-and-onboarding.md).
- **Pruning old exports from a target.** There are none — the export lives inside
  a backup and follows its retention.
- **A `--from-backup` that also runs `update` and `restore`.** Chaining the three
  steps is a script, and the steps have genuinely different failure modes; one
  command would report a single outcome for three decisions.

## 9. Risks

- **Every backup becomes a complete identity artifact.** An attacker with backup
  read *and* the recovery key gets the installation, the secrets and the backup
  target credentials. Two things bound this. The marginal *data* exposure is
  close to zero — the secret state is already in every backup, and so are the
  target URLs via `installation.yaml`; what genuinely changes is attacker
  ergonomics, since work that needed manual `sops` fumbling becomes one command.
  And §5.1's recovery-only encryption removes the more likely half of the threat
  entirely: compromising the live machine no longer yields the identity bundle,
  because the machine cannot read it either. What remains is the offline key,
  which is the thing the whole design already asks operators to protect. The
  documentation must still say plainly that a backup is an identity artifact.
- **Retention multiplies the copies.** Thirty nightly backups are thirty copies
  of the identity bundle where there was one export. Mitigated by the same
  recovery-only encryption and by nothing else; an operator storing backups more
  casually than exports has a decision to make.
- **A bucket compromise yields *write* credentials, not only read.** Already
  true via the secret state and unchanged by this RFC, but worth stating rather
  than discovering. The available hardening is operational: give the manager
  put+get and withhold `DeleteObject`, run with `--no-prune-remote`, and let
  bucket lifecycle rules do retention — see §7.
- **~~An always-present component changes "captured nothing".~~ Overstated in
  the first draft of this RFC, and corrected here.** The refusal is
  `!hasHook && capturedVolumes == 0`
  ([`backup.go:342`](../internal/adapters/backup/hookbackup/backup.go)) — it
  counts *volumes specifically*, not components in general, so an
  always-present `export` cannot satisfy it. The residual risk is a future
  refactor generalising that predicate into a component count, which §6 guards
  against rather than tests for.
- **Retiring `secrets` is now free, and was never expensive.** Nothing has been
  released, so no vendor has a restore hook reading it. Recorded because the
  reasoning would be different after the first tag, and because §5.2 removes it
  from a directory whose contents §7 is simultaneously making an ABI.

## 10. Unresolved questions

1. ~~**Should `--from-backup` accept no id and use the newest?**~~ → decision 11.
   **Yes, newest by default.** The question was posed as a convenience trade and
   it is not one: identity and data are separate choices, and fusing them by
   demanding one id is what creates the silent-staleness risk in the first
   place. My earlier lean — "identity is not a thing to guess at" — had the
   emphasis wrong, since staleness in identity only ever *loses* information and
   the newest export is strictly the most complete.
2. ~~**Does `export.yaml` need its own schema version inside the backup,
   separate from `BackupManifest.SchemaVersion`?**~~ → **No, settled 2026-08-09
   by shipping without one.** The document already carries `api_version` and
   `Validate` refuses one it does not know, so a second version would have been
   a number nothing read. What the *backup* schema bump buys is different and
   was kept: it makes an older manager refuse a backup whose identity it would
   otherwise restore around silently.
3. **Does the cannot-back-up case need `secrets.export-freshness` after all?**
   It is the one installation shape where an export is still mandatory and
   nothing checks it. The check would be correct and would fire on very few
   machines — which is either well-targeted or forgettable, and the answer
   probably depends on whether such releases turn out to exist.

## 11. Decisions

| # | Decision | Rationale and consequence |
| --- | --- | --- |
| 1 | A backup carries a full `InstallationExport` as a new `export` component | Recovery becomes recovery key + any backup, and the automated artifact is the one carrying identity rather than the manual one. Consequence: every backup is an identity artifact, which the documentation must state. |
| 2 | It is produced by the **same code path** as `installation export` | One producer of the recovery payload was the point; two that drift is the situation being fixed. Pinned by a byte-for-byte equivalence test, without which the claim decays quietly. |
| 3 | `secrets` is **retired** from new backups | Subsumed byte for byte by `ExportedSecrets`; keeping it would put the secret state in one backup twice. Consequence: a behaviour change to a file present in every backup ever taken, judged safe because it is an encrypted blob no hook can use. |
| 4 | `config` is **kept**, and redesignated **forensic** | `installation.yaml` is the operator-facing file that `doctor` already watches for drift — useful in a review *because* it can disagree, useless for recovery. Naming both roles turns a trap into two labelled artifacts. |
| 5 | `import --from-backup <id>` is a second way to obtain an export **value**; `Import` is unchanged | It already takes a parsed `InstallationExport`, so the recovery logic, its refusals and its tests are untouched. |
| 6 | `BackupTarget` gains `FetchFile` | Importing identity from a remote backup must not transfer the data. Precedented rather than novel: `List` already reads one named object per backup through the same helper. |
| 7 | A backup without an `export` component is **refused** by `--from-backup`, naming `installation export` | A partial import that looks successful is worse than a refusal: it passes `restore`'s id check and then runs a product with no credentials. |
| 8 | `installation export` **stays**, reframed as an optimisation and a fallback | A release with no hook and no volumes is refused a backup ([0010](0010-compose-volume-capture.md)), so some installations can never produce one — and an export can be taken before any release is installed. |
| 9 | No `secrets.export-freshness` diagnostic | `backup.target-freshness` covers every installation that backs up, and a second freshness warning for a no-longer-required artifact is noise. Reopens only for the cannot-back-up case (question 3). |
| 10 | `BackupManifest.SchemaVersion` bumps; older backups stay readable | Follows the schema-1 precedent already in `stage()`. Nothing is released, so this protects nobody today — it is here because it will matter after the first tag. Consequence: decision 7's refusal must not widen into a refusal to *restore* an older backup. |
| 11 | The **export component is encrypted to the recovery recipients only**, and skipped entirely when there are none | A running machine never reads the export out of its own backup, so giving it the ability buys nothing and costs blast radius. The property: the export component is unreadable by the machine that wrote it, so compromising the live host yields the data and not the identity. Skipping rather than falling back to the machine key, because an identity bundle readable only by the key that dies with the machine is the appearance of recovery with none of the substance. Consequence: `encryptComponents` takes per-record recipients rather than one list. |
| 12 | `--from-backup` with **no id uses the newest** backup's export; an explicit id is honoured and **warns when it is not the newest** | Identity and data are separate choices and fusing them is what creates silent staleness — an operator restoring to a point in time would otherwise get that moment's secrets too. Staleness in identity only loses information, so newest is strictly the safest default. |
| 13 | `<PRODUCT>_BACKUP_DIR`'s contents are **enumerated as an ABI in the same change** that retires `secrets` | After the first tag, whatever vendors read becomes a contract whatever the docs say; today the enumeration is simply true and anything outside it is legitimately removable. Removing first and documenting later would be removing from a contract that never stated its contents. |
| 14 | The export component is validated against the backup's own `backup.json` before use, and `FetchFile` is not trusted to bind them | A named remote key returns whatever sits there; nothing in "fetch `export.yaml.age` from backup X" says the bytes are backup X's. `backup.json` already records each component's `SHA256` over the *stored* bytes, so the check needs no key. Consequence: the swap test must swap two individually valid exports between two valid backups. |
| 15 | Equivalence between the two export producers is asserted **after decryption**; recipient access is a separate assertion | Decision 11 encrypts the component to a different recipient set, and age is non-deterministic regardless — so a byte-for-byte comparison of stored bytes fails for reasons unrelated to the claim it exists to defend. |
| 16 | `import --from-backup` **warns** when the export it used is newer than the backup being restored from; it does not refuse | Decoupling identity from data (decision 12) admits a pairing where a rotated secret returns beside a database that predates it, and every compatibility check passes. Point-in-time recovery is legitimate, so the operator decides — but not silently. |

## 12. Phasing

- **P0 — enumerate `<PRODUCT>_BACKUP_DIR` as an ABI.** Documentation only, no
  code. It goes first because it is what makes P1's retirement of `secrets`
  legitimate rather than a removal from an unstated contract — and because it is
  free now and permanent after the first tag.
- **P1 — the `export` component, its recovery-only encryption, the schema bump,
  and `--from-backup` from a local backup.** This is the whole claim of the RFC,
  and the recovery test without an export is what proves it. `secrets` is
  retired here, since keeping it would ship the duplication this phase exists to
  remove.
- **P2 — `FetchFile` and `--from-backup --target`.** Independently useful and
  independently testable; until it lands, importing from a remote means fetching
  the backup first, which works and is merely expensive.
- **P3 — the documentation rewrite.** Last, because
  `recovering-a-lost-machine.md` should describe the two-artifact procedure
  rather than promising it.

P1 alone changes the answer to "my VM died and I never took an export".
