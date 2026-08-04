# RFC 0009 — Backup targets: getting a backup off the machine

- **Status:** ✅ Complete — shipped 2026-08-04, all three phases. `file://`,
  `ssh://` and `s3://` are wired, the push step fails the backup, `doctor` has
  both checks, and the recovery scenario fetches from a target instead of
  copying a directory. Fifteen divergences and defects are recorded as amendments
  in §12 — six of them found by auditing the shipped implementation on 2026-08-05 — one of them (the push step's failure policy) a defect in this RFC
  that would have deleted the very backup it was protecting, and three of them
  defects the real-service suites found: a host-key pin that failed against
  honest servers, a listing prefix that deleted a neighbouring backup, and a
  pre-update backup that never left the machine.
- **Scope:** Adds a `ports.BackupTarget` port and a scheme registry so a backup
  can be copied to somewhere the machine that took it cannot reach: another
  host over SSH, an object store, or a directory on separate media. Covers the
  port, the registry, three adapters, the credential problem a restore hits on
  a rebuilt machine, retention and verification against a remote, and what
  `doctor` says when the last backup never arrived. Explicitly **not** in
  scope: changing what goes *into* a backup (that is RFC 0010), incremental or
  deduplicating transfer, scheduling, or a second encryption scheme — backups
  are already encrypted to the deployment's own recipients and this RFC does
  not touch that.
- **Related:** [`internal/adapters/backup/hookbackup/`](../internal/adapters/backup/hookbackup/),
  [`internal/ports/backup.go`](../internal/ports/backup.go),
  [`internal/adapters/source/registry.go`](../internal/adapters/source/registry.go)
  (the pattern this mirrors),
  [`internal/infra/agecrypt/`](../internal/infra/agecrypt/),
  RFC [0003](0003-secrets-recovery-and-onboarding.md) for the recipient model,
  RFC [0004](0004-distribution-and-verification.md) for the source registry this
  copies, RFC [0010](0010-compose-volume-capture.md) for what a backup contains

---

## 1. Summary

A `BackupTarget` port with a scheme registry, three adapters — `file://`,
`ssh://`, `s3://` — and the plumbing to push a backup to one after it is taken,
list what is there, verify it, restore from it, and prune it.

Nothing about the backup itself changes. It is already a directory of files
encrypted to the deployment's recipients with a plaintext manifest beside them,
which is precisely the shape that survives being copied somewhere else.

## 2. Motivation

**Every backup this manager takes is on the machine that will fail.**

`hookbackup` writes to `paths.BackupsDir()` — `/var/lib/<product>/backups/<id>`
— and that is where it stays. The retention policy prunes there, `doctor`
checks freshness there, and `restore` reads from there. A disk failure, a
provider deleting the instance, a `rm -rf` on the wrong host: all of them take
the backups with them.

The recovery documentation already admits this. [Recovering a lost
machine](../pages/docs/operating/recovering-a-lost-machine.md) tells the
operator to bring the backups back from wherever they kept them, and the
recovery test does the same thing by hand:

```go
offsite := filepath.Join(t.TempDir(), "offsite")
copyTree(t, origin.Paths.BackupsDir(), offsite)
require.NoError(t, os.RemoveAll(origin.Root))
```

That `copyTree` is the feature. It is currently the operator's job, done by
hand or by a cron job nobody tests, and the manager has no idea whether it
happened.

**The one thing that made this hard is already solved.** Until backups were
encrypted, copying one to a bucket meant putting a plaintext database dump in a
bucket. Now every component is encrypted to the deployment's own recipients and
only the manifest is readable, so a backup is safe to move by construction
rather than by policy.

## 3. Current state

Verified against the code.

**The backup layout** is a directory per backup under `BackupsDir()`:

```
20260804T174743Z/
  backup.json          plaintext, self-describing
  database.sql.age     encrypted to the deployment's recipients
  installation.yaml.age
  secrets.sops.yaml.age
  manifest.yaml.age
```

`BackupManifestSchemaVersion` is 2. Each `ComponentRecord` carries the stored
path, size, the SHA-256 **of the stored bytes**, and `Encryption`.

**`ports.BackupEngine`** has `Create`, `List`, `Inspect`, `Verify`, `Restore`,
`Prune`. Every one of them is local: `BackupRef.Path` is a filesystem path,
`resolve` joins it under `BackupsDir()`, `Prune` calls `atomicfs.RemoveAll`.

**`ports.ReleaseSource`** is the pattern to copy. `source.NewRegistry` maps
scheme to adapter, refuses two adapters claiming one scheme, refuses an empty
registry, and its refusal for an unknown scheme names the schemes the build
does have — asserted by `TestSourceRegistryRefusesAnUnbuiltScheme`. Three
adapters exist behind it (`local`, `https`, `oci`) and a shared contract suite
runs against each.

**`tools.Restic`** is already in the tool registry with a version probe. No
code uses it. Somebody anticipated this RFC and left a note.

**Credentials** would be secrets, and the secret store already holds arbitrary
named values. Nothing new is needed to *store* them; §5.5 is about reading them
at the one moment they are unreachable.

## 4. Goals / Non-goals

**Goals**

- A backup lands somewhere the machine cannot delete, without the operator
  writing a cron job.
- `backup list`, `verify` and `restore` work against a remote target with the
  same commands and the same refusals as a local one.
- A restore on a rebuilt machine works with the offline recovery key and
  nothing else from the lost host.
- `doctor` reports a target that is unreachable, or a last backup that never
  arrived — the two failure modes an operator otherwise discovers during a
  disaster.

**Non-goals**

- **Incremental or deduplicating transfer.** A full copy every time is
  wasteful and correct; making it clever is a second RFC and probably means
  adopting restic wholesale (§8).
- **Scheduling.** systemd timers already exist for `backup`; a push is part of
  the backup, not a separate schedule.
- **A second encryption scheme.** The backup arrives encrypted. A target that
  encrypts again would be a second answer to "who can read this", which is the
  trap `reencrypt` documents and refuses.
- **Backing up to a target the manager also restores the *release* from.**
  Sources and targets stay separate registries; conflating them would make
  `release fetch` and `backup push` share a failure mode for no benefit.

## 5. Design

### 5.1 The port

```go
// BackupTarget is somewhere a backup can be kept that is not this machine.
type BackupTarget interface {
    // Schemes are the URL schemes this target handles.
    Schemes() []string

    // Push copies a local backup directory to the target, returning the
    // reference that will find it again.
    Push(ctx context.Context, local string, id string) (RemoteRef, error)

    // List enumerates what is there, reading each backup.json without
    // transferring anything else.
    List(ctx context.Context) ([]BackupManifest, error)

    // Fetch copies one backup down to a local directory.
    Fetch(ctx context.Context, ref RemoteRef, dest string) error

    // Remove deletes one backup. Retention calls it; nothing else does.
    Remove(ctx context.Context, ref RemoteRef) error
}
```

`List` returning manifests rather than refs is the deliberate part: the
manifest is the only plaintext file in a backup, it is small, and it carries
everything `backup list` prints. So listing a bucket costs one small GET per
backup and no decryption — and it works from a machine that has lost its key.

### 5.2 The registry

`target.NewRegistry(targets ...ports.BackupTarget)`, byte for byte the shape of
`source.NewRegistry`: one adapter per scheme, a refusal naming the schemes this
build has, a refusal for two adapters claiming one scheme. The tests for the
source registry become a shared contract suite both use.

### 5.3 The three adapters

| Scheme | Adapter | Why it is in the first milestone |
| --- | --- | --- |
| `file://` | a directory: another disk, an NFS mount, removable media | The "worst case, picked up manually" answer, and the one that needs no credentials at all |
| `ssh://` | SFTP over `golang.org/x/crypto/ssh` | The commonest self-hosted answer: a second VM the operator already has |
| `s3://` | S3 and anything speaking its API — MinIO, R2, B2, GCS's interoperability mode | One adapter covers most object stores |

**Host keys are verified.** No flag disables it, for the same reason no flag
disables TLS verification: a target that accepts any host key is a target that
can be replaced by whoever is on the path, and the backup is then encrypted to
recipients an attacker chose to receive rather than to read.

**GCS is deliberately absent** from the first milestone. Its interoperability
mode speaks S3, which covers the operator who wants it, and a native adapter is
a second large SDK for a second API. Add it when somebody needs a feature
interoperability mode lacks.

### 5.4 Where the push happens

A step in the backup operation, after `verify-backup`:

```
create-backup → verify-backup → push-backup → prune-backups
```

After verification, because pushing a backup that failed its own checksums is
copying a known-bad file to a second location. `OnFailure: Compensate` and the
compensation removes the partial remote copy — a half-uploaded backup that
looks like a backup is the same hazard `Create` already guards against locally.

**A failed push fails the backup.** Not `Continue`, unlike retention: the point
of the operation is that the data is somewhere safe, and reporting success for
a backup that is still only on the machine that will die is the failure this
RFC exists to prevent. The local copy is kept either way, so a failed push
leaves the operator no worse off than today.

### 5.5 The credential problem, which is the interesting part

To restore from S3 you need S3 credentials. Credentials are secrets. Secrets
are in the encrypted state. The encrypted state is in the backup. The backup is
in S3.

On a rebuilt machine that circle has to be broken from outside. Three ways in,
and the design takes all three because they suit different disasters:

1. **From the export.** `installation export` already carries the installation
   record and the encrypted secret state, and an operator following the
   recovery guide has one. Adding the target configuration *and its
   credentials* to the export is the smallest change: the export is already
   encrypted to the recovery recipients, so the credentials are protected by
   the same key that opens the backups.
2. **From flags.** `morzer backup list --target s3://…` with credentials in the
   environment, for the operator who has the bucket and nothing else.
3. **From nothing.** `file://` on removable media needs no credential at all,
   which is why it is in the first milestone rather than treated as the toy
   case.

Route 1 is the documented path and route 2 is the escape hatch. Both are tested
in the recovery scenario, because a recovery path that only works when the
happy path worked is not a recovery path.

### 5.6 Verification and retention against a remote

**Verify** downloads the manifest, then each component, and checks the stored
checksum. That is a full transfer, which is the honest cost of the claim: a
backup nobody has read back is a hope, and the same sentence applies whether it
is on a disk or in a bucket. `--verify-remote` is opt-in per run and a
scheduled job, not part of every backup.

**Retention** applies the same policy to the target, with the same refusal:
never the most recent, never a reason on the exempt list. Two independent
retention passes over one policy, which is one policy too few to be a
contradiction and one pass too many to be free — a `--prune-remote` that
defaults on.

**`doctor`** grows two checks: `backup.target-reachable` and
`backup.target-freshness`, the second reading the remote's newest manifest and
comparing it to the local one. A local backup that never reached the target is
the failure this whole RFC is about, so it is a `fail` rather than a `warn`.

### 5.7 Configuration

In the installation, not the release manifest. Where a vendor's backups go is
the operator's decision and nothing the vendor can know:

```yaml
backup:
  targets:
    - url: s3://backups.example/demo
      credentials: backup_s3          # a secret name
    - url: file:///mnt/usb/demo-backups
```

Several targets are allowed and each is pushed to. A push that fails to one
fails the backup, per §5.4 — an operator who configured two targets wants two
copies, and "one of them worked" is a state they should be told about.

## 6. Tests

The mechanisms exist. `dockerlab` already starts containers pinned by digest
and the container suites run in CI.

| Level | What |
| --- | --- |
| Contract | One suite, run against a fake and against all three adapters: push, list, fetch, remove, idempotent re-push, a fetch of something absent |
| Container | **MinIO** for `s3://`, an **sshd** image for `ssh://`, a temp dir for `file://` |
| Failure | A target that goes away mid-push; a bucket that refuses a write; a host key that changed; credentials that are wrong |
| Recovery | The existing recovery scenario extended: the machine is destroyed and the backups are fetched from a target rather than copied by hand |
| Refusals | An unknown scheme names the schemes this build has; a push that fails fails the backup; the last remote copy is never pruned |

The recovery test is the one that matters. Today it copies a directory to
simulate an off-site backup; with this RFC it can stop pretending.

## 7. Docs

- [operating/backups](../pages/docs/operating/backups.md) gains a targets
  section: how to configure one, what a push costs, what `doctor` says when it
  fails.
- [operating/recovering-a-lost-machine](../pages/docs/operating/recovering-a-lost-machine.md)
  gains the credential story from §5.5 — this is where an operator reads it at
  the worst possible moment, so it needs to be right there.
- A new reference page for target URLs and their options, gated by
  `docs-check` like the rest.
- The claims table gains rows for the host-key verification refusal and for
  "a backup that did not arrive fails the operation".

## 8. Out of scope, and what would change that

**restic.** It does all of this — S3, SFTP, dedup, incremental, retention,
verification — and is already in the tool registry. It is out of scope because
it owns encryption and retention, and this project owns both: two retention
policies is worse than one, and restic's repository key would be a second
answer to "who can read this deployment's data" alongside the age recipients.

What would change it: evidence that full copies are actually painful. If an
operator's backup is 200 GiB and the nightly push saturates their uplink, dedup
stops being a nicety. The reconciliation, if it comes to that, is to store the
restic repository password as a managed secret so the age recipients remain the
single root and restic's key is derived from them — but that is an RFC of its
own, and it should be written when there is a measurement to justify it.

**Encrypting the manifest.** It stays plaintext so `backup list` works without
a key. It names the installation, the release version and the timestamp — an
attacker with bucket access learns what product you run and when you back it
up. That is a real disclosure and it buys the ability to enumerate backups from
a machine that has lost everything. Revisit if anyone objects; the trade is
deliberate rather than overlooked.

**Compression.** Belongs with dedup, in the restic conversation.

## 9. Risks

**A push that fails fails the backup, and some operator will hate it.** The
alternative is a green `backup` on a machine whose backups are all local, which
is the state this RFC exists to end. Mitigated by keeping the local copy: a
failed push leaves them exactly where they are today, plus an error.

**Credentials in the export.** §5.5 route 1 puts bucket credentials in a file
the operator carries around. It is encrypted to the recovery recipients, which
is the same protection the secret state gets, and the alternative is a recovery
that cannot reach the backups. Worth stating in the docs rather than burying:
an export is now enough to read your backups, so it is as sensitive as the
recovery key itself.

**SDK weight.** An S3 client is a large dependency for a module that has
deliberately stayed small. `minio-go` is the lighter option and speaks to
every S3-compatible store including R2, B2 and GCS interoperability mode.
Measure the binary before and after; if it is unacceptable, the fallback is
shelling out to a tool the way `sops` and `docker` already are.

**Two retention passes can disagree.** A local prune and a remote prune reading
one policy should keep the same set, but they run at different times against
different listings. The failure is benign — an extra copy retained — and the
test is that `Prune` on both sides is driven from the same manifest list.

## 10. Decisions

| # | Decision |
| --- | --- |
| 1 | Targets are a port with a scheme registry, mirroring `ports.ReleaseSource` exactly. A second registry shape for the same problem would be a second thing to keep honest. |
| 2 | The push happens after verification and before retention, as a compensating step of the backup operation. A backup that failed its checksums is never copied anywhere. |
| 3 | **A failed push fails the backup.** Retention failing is `Continue`; this is not. The operation's purpose is that the data is somewhere else. |
| 4 | Host keys and TLS certificates are always verified. No flag disables either, matching the existing invariant for release sources. |
| 5 | `backup.json` stays plaintext on the target, so backups can be listed from a machine that has lost its key. The disclosure this implies is named in §8 and accepted. |
| 6 | Target configuration lives in the installation, not the release manifest. Where backups go is the operator's decision and the vendor cannot know it. |
| 7 | Credentials are managed secrets, reachable at restore time from the export, from flags, or not needed at all for `file://`. All three are tested; a recovery path that works only on a healthy machine is not one. |
| 8 | `s3://` covers S3-compatible stores including GCS interoperability mode. A native GCS adapter waits for a feature that mode lacks. |
| 9 | No dedup, no incremental, no compression in this milestone, and no restic. §8 records what evidence would reopen it. |

## 11. Phasing

| Phase | What | Ships |
| --- | --- | --- |
| **P1** | The port, the registry, the `file://` adapter, the push step, `doctor` checks | The whole shape, and the "worst case" answer working end to end |
| **P2** | `ssh://`, host-key verification, the sshd container suite | The commonest real deployment |
| **P3** | `s3://`, credentials from the export, the MinIO container suite, the extended recovery scenario | Object storage and the disaster path proved |

P1 is worth landing alone: `file:///mnt/usb/backups` on a machine with a second
disk is a real improvement over everything on one filesystem, and it exercises
every part of the design except the network.

**All three shipped together on 2026-08-04.** P1 would have been a reasonable
stopping point, and the phasing was right about why — but the contract suite made
the second and third adapters cheap, and both of them found defects the first
could not have (§12). Landing them separately would have shipped a host-key check
that fails against honest servers and left it there until somebody used `ssh://`.

---

## 12. Amendments

Append-only. Recorded where execution diverged from the design, so the reasoning
survives rather than being read back out of the code.

### 2026-08-04 — the push step aborts rather than compensating

§5.4 specified `OnFailure: Compensate` with "the compensation removes the partial
remote copy". That is wrong, and the paragraph immediately after it says why
without noticing: *"The local copy is kept either way, so a failed push leaves
the operator no worse off than today."*

It would not have been. The engine's compensation walks **every** completed step
newest-first, and the oldest of those is `create-backup`, whose compensation
deletes the backup it made. A `Compensate` push would therefore have left an
operator who configured a target with *no backup at all* on a night the target
was unreachable — strictly worse than having configured nothing, and the exact
opposite of the promise decision 3 rests on.

Shipped as `OnFailure: Abort`, with the partial remote copies removed inline in
the step's own error path, where the cleanup can be scoped to what that step
did. Decision 3 is unchanged: a failed push still fails the backup.

Found by `TestAFailedPushKeepsTheBackupItTook`, which now pins it.

### 2026-08-04 — the port carries the target on every call

§5.1 gave `Push(ctx, local, id)`, which implies an adapter bound to one target.
The registry in §5.2 dispatches on scheme, so a single `file://` adapter has to
answer for every `file://` target an installation configures — two of them would
have collided.

Every method takes a `TargetRef` instead, and `RemoteRef` carries the whole ref
rather than a URL, so a retention pass authenticates the same way as the push
that created what it is pruning. This is *closer* to decision 1, not further:
`ports.ReleaseSource` takes a `Ref` on every call for the same reason.

### 2026-08-04 — the export needed no new fields

§5.5 route 1 proposed "adding the target configuration *and its credentials* to
the export". Neither was necessary. The export already carries the installation,
which now carries the target URLs, and already carries the encrypted secret
state, which carries the credentials. An operator holding an export and the
offline key can reach the backups, and nothing was added to the export format to
make that true.

The risk §9 raises — "an export is now enough to read your backups" — stands
unchanged, and is documented.

Proved end to end by `TestRecoveryFetchesTheBackupFromATarget`.

### 2026-08-04 — the installation schema moved to 3

Not in the design at all, and it should have been. `backup.targets` is a field an
older manager would ignore: it would read the state, see no targets, take a
backup, report success, and leave it on the machine the operator configured a
target to survive. That is exactly the misread the schema version exists to
prevent, and a worse one than the `settings` → `parameters` change that
established the precedent, because it is invisible until the disaster.

`InstallationSchemaVersion` is 3, with a no-op 2 → 3 migration. The bump is
entirely for the other direction: an older manager now refuses the state rather
than half-reading it.

### 2026-08-04 — `minio-go` is pinned below v7.2.0

§9 anticipated SDK weight and named `minio-go` as the lighter option. It is no
longer light: from **v7.2.0** it depends on the charmbracelet TUI stack, which
forces `x/ansi` to a version incompatible with the `lipgloss` this project
already uses. The build does not merely grow — it stops compiling.

Pinned to **v7.0.95**, the last release before that dependency. Recorded here
because the next person to run `go get -u` will hit it, and the failure message
(`cellbuf: not enough arguments in call to b.Italic`) says nothing about
buckets.

If the pin ever has to move, the fallback §9 already names — shelling out to a
tool the way `sops` and `docker` are — becomes the cheaper option rather than
the desperate one.

### 2026-08-04 — the pin decides which host-key algorithms are offered

Not a divergence from the design, which did not go into this detail, but a
defect the container suite found and worth recording next to decision 4.

A host with both an ed25519 and an RSA key offers whichever it prefers. A client
that accepts any algorithm but pins only ed25519 gets offered RSA and reports a
host-key mismatch — a refusal **identical to the one a real attack produces**,
for a server doing nothing wrong. An operator who sees that twice for no reason
learns to ignore the one check that matters.

`ssh.ClientConfig.HostKeyAlgorithms` is now derived from the pin itself, with
`ssh-rsa` expanded to the SHA-2 signature algorithms because SHA-1 is refused by
every current OpenSSH.

Found by `TestBackupTargetContract_SSH` against a real sshd, which is exactly the
class of thing RFC 0008 §5.4 argues containers are for: no fake would have
disagreed with the adapter about which key it was going to be offered.

### 2026-08-04 — remote verification is `backup verify --remote`

§5.6 named `--verify-remote` as a flag on the backup. It shipped as a mode of
`backup verify` instead, which is where an operator already looks for the
question it answers, and it takes the same `--target` and `--credentials-file`
as the other read-only commands.

The substance is unchanged: a full transfer, opt-in per run rather than part of
every backup, streaming each component through a checksum and keeping nothing.
It needs no key, because the checksums are of the stored bytes — so an operator
can verify an off-site archive from a machine that holds nothing at all.

### 2026-08-04 — the pre-update backup is pushed too, and a failure warns

Not in the design, and it should have been. §5.4 covers the backup *operation*
and says nothing about the backup `update` takes before it converges — which is
the backup an operator restores from when an update goes wrong, and the one that
was left on the machine alone.

Two consequences, both found by adding a target step to the acceptance script:
the moment the deployment is most fragile was the moment its backup was least
durable, and `backup.target-freshness` failed after every update, correctly.

It is pushed now. A failure **warns rather than failing the update**, and the
asymmetry with decision 3 is deliberate:

- `morzer backup` exists to produce a durable backup. A copy that never left the
  machine means it did not do its job, so it fails.
- `morzer update` exists to install a release. The local copy is what a rollback
  on this machine uses, and refusing to update because a USB disk was unplugged
  would block the security fix an operator is trying to apply.

The gap is reported instead — by the warning, which names `morzer backup push`,
and by `doctor` until it is closed.

### 2026-08-04 — a listing prefix is a string match, not a path one

Found while making the three stores behave alike, and it is worth recording
because the same mistake is available in any object store.

`Remove` lists everything under `<id>/` and deletes it. On S3 that is a
`ListObjects` prefix, which matches characters rather than path segments — so
removing `20260101T000000Z` also matched `20260101T000000Z-copy` and deleted a
neighbouring backup's components. Ids this manager writes are fixed-length
timestamps and cannot collide, but `Remove` is driven by ids read from a
manifest **on the target**, which is a file this manager may not have written.

All three stores now filter after trimming the prefix, and the contract suite
holds them to it: `removing a backup does not take a neighbour whose id shares
its prefix`, which fails against the unfixed adapter.

### 2026-08-05 — six defects found by auditing the shipped implementation

Recorded together because they were found in one pass over code that had already
passed CI, the container suites and the acceptance run. Each is fixed and pinned.

**`--dry-run` mutated.** `backup target add` and `remove` ignored it and changed
the installation for real. A global flag documented as "plan only, make no
changes" that lies about one command is a flag nobody can trust on any of them.
The reachability probe still runs under a dry run, because it reads nothing and
it is the only question worth asking before adding a target.

**Neither command took the deployment lock.** Every other command that writes
the installation does. Adding a target concurrently with `config set`, or with
the installation write an update makes while rolling back, was a lost update:
both load, both mutate, one change disappears.

**A second `ssh://` target on one host was never handshaked.** Connections were
cached under `user@host`, so the second target reused the first's connection --
and therefore its credentials, and therefore **never checked its own host key**.
An operator could configure one target with a correct pin and a second with a
wrong one and nothing would object, which defeats decision 4 on the exact path
it matters. The cache key now covers everything that authenticated the
connection, hashed, because a map key holding a private key is a private key in
a heap dump. The same applied, less severely, to the S3 client cache, where a
rotated secret under an unchanged access key id was ignored.

**Credentials from `--credentials-file` were never registered for redaction.**
The secret-store path armed the redactor and the file path did not -- and the
file path is the one a recovery uses. No leak was found, because nothing on that
path logs or execs the values; the second line of defence was simply not armed.

**The traversal guard was in the wrong place.** A manifest on a target decides
where a fetch *writes*, and the guard lived in each adapter's key resolution --
the *read* side. All three adapters had it, so nothing was exploitable, but the
contract suite never proved it and a fourth adapter could omit it. It now lives
in `blob.Fetch`, where no adapter can forget it.

Two of the tests written for these initially passed against the unfixed code,
for the same reason an earlier one did: they asserted on a fetch, and a fetch
refuses before it writes. Both were rewritten to assert where the decision is
actually made, and both were re-verified by breaking the fix and watching them
fail.
