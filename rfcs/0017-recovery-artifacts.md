# RFC 0017 — Recovery artifacts

- **Status:** 📝 Draft — design proposed
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

This RFC has the backup carry a real export. Recovery becomes **recovery key +
any backup** for every installation that takes backups, and `installation
export` remains for the ones that cannot.

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

- A backup is sufficient to rebuild identity and secrets, without an export.
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

`BackupManifest.SchemaVersion` bumps. Schema 1 (plaintext components) is still
read today, so the precedent for handling an older backup is established and
this follows it: an older backup simply has no `export` component, and §5.4 says
what happens when one is used.

### 5.2 `secrets` is retired, and `config` is not

`ComponentSecrets` is removed from new backups. It is subsumed **byte for byte**
— `ExportedSecrets.State` is documented as "the encrypted document, byte for
byte as it sits in `secrets.sops.yaml`", and `Recipients` carries the roles the
sidecar existed to preserve. Keeping both would put the secret state in a backup
*twice*, which is a worse duplication than the one this RFC set out to remove.

Nothing plausibly reads it: `secrets.sops.yaml.age` is an encrypted blob inside
an encrypted component, useless to a restore hook.

`ComponentConfig` **stays**, and the reasoning is deliberately different.
`installation.yaml` and `application.yaml` are human-readable, small, and a
restore hook could be reading them — the published hook ABI names
`<PRODUCT>_BACKUP_DIR` ([`hooks.md:41`](../pages/docs/reference/hooks.md)) without
enumerating its contents, so a vendor doing so is undocumented but not
misbehaving. Removing it would be a silent break for a saving of a few
kilobytes.

### 5.3 `import --from-backup`

```
morzer installation import --from-backup <id> --identity ~/recovery.key
morzer installation import --from-backup <id> --target s3://… --credentials-file ./creds.yaml
```

`Import` already takes a `domain.InstallationExport` **value** and the CLI
parses the file ([`recovery.go:232`](../internal/lifecycle/ops/recovery.go)), so
this is a second way to obtain that value and no change to `Import` itself. The
component is decrypted with the supplied identity — the same key the operator is
already passing — and parsed exactly as a file-based export is.

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

`backup list --remote` already needs no key, so an operator can see what exists,
pick one, and pull a few kilobytes out of it before deciding whether to spend the
bandwidth on the data.

### 5.4 What happens with an older backup

A backup with no `export` component is refused by `--from-backup`, naming the
alternative:

> backup 01J8Z… was taken before backups carried an installation export
> (backup schema 1). Recover identity from `installation import <export>`, or
> take a new backup on a machine that still runs.

Fail-closed and specific. The failure mode to avoid is a partial import that
looks like it worked — a machine with an id and no secrets is worse than a
refusal, because it will pass `restore`'s installation-id check and then run a
product whose credentials are all missing.

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
- **Byte-for-byte equivalence**: the export inside a backup and the export
  written by `installation export` on the same machine at the same moment are
  identical. Without this the "one producer" claim decays quietly into two.
- **A schema-1 backup is refused by `--from-backup` and still restores data** —
  the pair, since the refusal must not become a refusal to read old backups at
  all.
- **`FetchFile` transfers one file**, asserted by byte count against the target
  fake, not by reading the result. A correct file fetched by downloading
  everything passes an equality check and fails the point.
- **The retired `secrets` component**: a restore of a backup written before this
  change still works, and a new backup does not contain `secrets.sops.yaml.age`.
- **A backup still refuses when there is nothing to capture** — the `export`
  component must not accidentally make an empty backup look non-empty, which
  would defeat [0010](0010-compose-volume-capture.md)'s refusal.

That last one is the sharpest risk in the RFC and the least obvious: adding a
component that is always present changes what "the backup captured nothing"
means.

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
  `import --from-backup --target` does.
- The `installation export` documentation reframes it as an optimisation and a
  fallback, without discouraging it: it is still the fastest path and the only
  one for installations that cannot back up.

## 8. Out of scope

- **Scheduling exports.** Made unnecessary for machines that back up, and §5.5
  explains why a second timer is the wrong shape. Reopens only for the
  cannot-back-up case, together with unresolved question 3.
- **Encrypting the export to a different recipient set than the backup.** They
  share the secret store's recipients today and there is no case for divergence;
  a separate set would mean a backup whose components need two keys.
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
  target credentials. **The marginal change is smaller than it sounds** — the
  secret state is already in every backup today, and 0009 already puts target
  credentials in it — but the authoritative installation is new, and retention
  means thirty copies of it rather than one. The documentation must say plainly
  that a backup is now an identity artifact; an operator who was storing backups
  more casually than exports has a decision to make.
- **`--from-backup` restores identity from a moment chosen for data reasons.** An
  export is taken deliberately; a backup is picked because of what its database
  contains. A backup from before a secret rotation carries the old secrets, and
  nothing about choosing it flags that. The mitigation is that the newest backup
  is usually the right one and is what an operator reaches for anyway — but it
  is a silent staleness where the export's was at least self-inflicted.
- **Retiring `secrets` could break an undocumented hook.** Judged very unlikely
  (it is an encrypted blob), and it is a real behaviour change to a file that has
  been in every backup.
- **An always-present component changes "captured nothing".** The refusal in
  [0010](0010-compose-volume-capture.md) counts what was captured. If `export`
  is counted, a backup with no data at all starts succeeding. §6 tests it; the
  implementation must exclude managed components from that count, as it already
  does.
- **This makes a bucket compromise worse in one specific way.** The target
  credentials in the export are credentials to *write* to that bucket, not only
  read it. Already true via the secret state, and worth stating rather than
  discovering.

## 10. Unresolved questions

1. **Should `--from-backup` accept no id and use the newest?** Convenient, and
   during an incident convenience is worth something. Against: identity is not a
   thing to guess at, and the newest backup may be the one that failed
   verification. Leaning toward requiring the id, with `backup list --remote`
   as the step that finds it.
2. **Does `export.yaml` need its own schema version inside the backup, separate
   from `BackupManifest.SchemaVersion`?** The export document already carries
   `api_version`; a second version would be belt and braces, and possibly the
   kind that rots.
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
| 10 | `BackupManifest.SchemaVersion` bumps; older backups stay readable | Follows the schema-1 precedent already in `stage()`. Consequence: the refusal in decision 7 must not widen into a refusal to *restore* an old backup. |

## 12. Phasing

- **P1 — the `export` component, the schema bump, and `--from-backup` from a
  local backup.** This is the whole claim of the RFC, and the recovery test
  without an export is what proves it. `secrets` is retired here, since keeping
  it would ship the duplication this phase exists to remove.
- **P2 — `FetchFile` and `--from-backup --target`.** Independently useful and
  independently testable; until it lands, importing from a remote means fetching
  the backup first, which works and is merely expensive.
- **P3 — the documentation rewrite.** Last, because
  `recovering-a-lost-machine.md` should describe the two-artifact procedure
  rather than promising it.

P1 alone changes the answer to "my VM died and I never took an export".
