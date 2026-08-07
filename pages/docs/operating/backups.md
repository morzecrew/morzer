---
title: Backups and restore
icon: lucide/archive
summary: Taking backups, what is in one, and the restore that is the way back from a failed update
---

# Backups and restore

```sh
morzer backup --reason "before the 1.4 upgrade"
morzer backup list
morzer backup verify <id>
```

The manager does not know how to back your product up — it coordinates the
release's own `backup` hook, wraps what that produces in a self-describing
manifest, and verifies the checksums by re-reading what was written. It does
know how to read the project's **volumes**, which is the part the hook usually
forgets.

## What a backup contains

| Component | Comes from |
| --- | --- |
| `database` | the release's backup hook |
| `files` | the release's backup hook |
| `volumes` | the project's named volumes, read by the manager |
| `config` | the rendered configuration |
| `secrets` | the encrypted secret state, as it is on disk |
| `manifest` | the release identity, schema version, checksums, installation id |

The manifest is what makes a backup self-describing: which release took it,
which database schema was current, and which installation it belongs to.

## Volumes

A backup hook is usually written by somebody thinking about the database. They
do not think about the uploads volume, the generated thumbnails, the certificate
store, or the queue's spool directory — and nobody notices until a restore
produces a working database and an application with no files.

So the manager reads the project's named volumes itself. Each becomes one
component.

Every example below is the same deployment: four named volumes — `uploads`,
`caddy_data`, `spool` and `pgdata` — and one bind mount at `/srv/legacy`. The
release excludes `pgdata`, which is why three volumes end up in the backup and
the bind mount is not one of them:

```
20260805T174743Z/
  backup.json
  database.sql.age            from the hook
  volumes/caddy_data.tar.age  read by the manager
  volumes/spool.tar.age
  volumes/uploads.tar.age
```

This also means a release that ships **no backup hook at all** can still produce
a restorable backup, which it previously could not — as long as there is
something for the manager to capture. A release with no hook *and* no named
volume produces no backup: `morzer backup` refuses rather than writing a
directory holding your configuration and none of your product's data. Bind
mounts and excluded volumes do not count towards it.

!!! warning "This does not replace your database backup"

    Copying a volume while something is writing to it gives you a
    **crash-consistent** copy — byte-for-byte what a power cut would have left,
    not what a clean shutdown would have. Postgres will usually replay its WAL
    and come up, because that is what it is built to do. *Usually* is not a
    property a restore should have, and other engines vary.

    Anything with a transaction log stays the backup hook's job. Volumes cover
    what the hook does not.

### Cold by default, hot only when the release says so

A volume the release has **not** classified is captured **cold**: the services
that mount it are stopped for the duration of the copy, and started again
afterwards. Nothing is writing, so the copy is exactly what a clean shutdown
would have left.

A release can declare that a volume is safe to read live:

```yaml
backup:
  volumes:
    uploads:    { consistency: hot }     # write-once files
    caddy_data: { consistency: hot }
    pgdata:     { consistency: exclude } # the backup hook owns this
```

Nothing is said about `spool`, so `spool` is the one this deployment stops for.

`hot` is a claim the **vendor** makes about their own product, not a guess the
manager makes on their behalf — which is why the default is the slow one. The
backup manifest records which claim applied to each volume, so a post-incident
review can see what was promised.

Only the services that mount a cold volume are stopped, and they are all stopped
once rather than once per volume — so capturing a certificate store stops the
web server, not the database. Services that are already stopped are left alone:
a backup never starts something you had deliberately taken down.

A **paused** service is not stopped, but it is not ignored either. It still
holds the volume open, frozen mid-write, so it is stopped for the copy like a
running one — and comes back running rather than paused.

!!! tip "If the pause is longer than you expect"

    A container is stopped with `SIGTERM` and then killed after two minutes. A
    process that does not handle `SIGTERM` — a shell loop, or anything running
    as PID 1 without a signal handler — never sees it, so the stop waits out the
    whole two minutes before killing it.

    That cost is per backup, not per volume. If a cold capture takes minutes
    where it should take seconds, the service is ignoring `SIGTERM`, and
    `init: true` or a `stop_signal` in the release's Compose file is the fix.

### When you cannot afford the downtime

```sh
morzer backup --no-downtime
```

`--no-downtime` skips volumes that would need their services stopped, and
**names them in the backup manifest** rather than capturing them live. They are
never silently downgraded to a hot copy: that would be the manager making the
vendor's claim for them, which is the one thing this design refuses to do.

`morzer backup` tells you either way. The ordinary run, which stops whatever
mounts `spool` for as long as the copy takes:

```
backup 20260805T174743Z created (1.4GiB), 3 volume(s): 1 cold, 2 hot,
  not captured: pgdata, /srv/legacy
```

and the same deployment with the flag, which stops nothing and says what that
cost:

```
backup 20260805T181102Z created (1.2GiB), 2 volume(s) captured hot,
  not captured: pgdata, spool, /srv/legacy
```

### What is never captured

- **Bind mounts.** A bind mount points at an arbitrary host path — it can be
  `/`, it can be a network mount, it can be shared with something the manager
  knows nothing about. They are reported in the backup manifest and in
  `morzer doctor`, never copied. Copying one is yours to arrange.
- **Volumes the release excludes.** `consistency: exclude` is the vendor saying
  their backup hook owns that data.
- **`tmpfs` mounts**, which hold nothing that outlives the container.
- **Anonymous volumes** — a mount written `- /data` rather than `- data:/data`.
  These *do* persist, which is what makes them worth reporting: the runtime
  invents a name that changes when the container is recreated, so there is no
  stable target a restore could write back into. `morzer doctor` names them, and
  the remedy belongs to the vendor — give the volume a name.

`morzer doctor` reports all of this, so you find out while you can do something
about it:

```text
[warn] every named volume is covered by a backup
       3 of 4 named volume(s) captured -- pgdata excluded by the release;
       /srv/legacy are bind mounts and are never captured
       → an excluded volume is the vendor saying its backup hook owns that
         data; a bind mount is yours to copy. Make sure something does.
```

Three of four, not two: `doctor` counts what a backup is configured to capture,
and `--no-downtime` is a decision made per run rather than something the
deployment records. A volume that the flag skips still counts as covered here.

### Restoring a volume

A volume is **replaced**, not merged: after a restore it holds exactly what the
backup held. A volume left holding files the backup does not contain, beside a
database restored to an exact moment, is how a record without its file is made.

The volume is emptied *before* the backup is extracted into it, so a failure
partway through leaves neither the old contents nor all of the new ones, and
there is nothing to roll back to. The backup is verified and decrypted before
anything is emptied, so the usual answer to a failure here is to run the same
restore again.

Restoring into a volume is **refused while any service that mounts it still
holds it open**, named by service and by the state it is in — untarring into a
volume a container has open is how a restore corrupts the thing it was
restoring. Paused counts as holding it open. `morzer restore` stops the services
for you, so seeing this message means something was still up.

### The helper image

Volumes are read and written through a small container (`busybox`, pinned by
digest) rather than through the host's storage directory, which is an
implementation detail and is unreadable under a rootless or remote daemon.

That is one more image to have locally. `morzer doctor` reports it when it is
absent, with the command to pull it — ask **before** you disconnect the machine,
not during a backup:

```sh
docker pull busybox@sha256:...   # doctor prints the exact reference
```

If your registry does not carry busybox, name a different image. Any image with
a POSIX `tar`, `du`, `find`, `wc` and `sh` will do — `find` and `wc` are for the
size check, which counts a volume's entries to bound what `tar` adds on top of
them. An image missing them measures nothing, and a backup that cannot be
measured is refused rather than started:

```sh
MORZER_VOLUME_HELPER_IMAGE=registry.internal/toolbox@sha256:... morzer backup
```

An environment variable rather than a setting, because the backup that needs it
is usually the scheduled one — add it to the timer's unit with a drop-in:

```ini
# /etc/systemd/system/demo-backup.service.d/helper-image.conf
[Service]
Environment=MORZER_VOLUME_HELPER_IMAGE=registry.internal/toolbox@sha256:...
```

Pin it by digest. It runs with your data mounted.

See [installing offline](installing-offline.md).

### Size

Volume backups are much larger than database dumps, and retention counts
backups rather than bytes. Two things follow:

- A backup that would not fit is **refused before anything is written or
  stopped**, naming both figures — `needs about 140GiB and 60GiB is free` is a
  better message than `no space left on device` halfway through.
- A volume the manager cannot **measure** is refused the same way and for the
  same reason — starting the copy anyway means finding out it does not fit once
  the services are already stopped. The message names the volume and the remedy,
  which is nearly always the helper image. A measurement that simply did not run
  is not this case: that volume goes unbudgeted and the backup is still taken.
- `morzer doctor` warns when keeping `retention.backups` of them will not fit.

If a volume is large enough for this to bite, the answers are lower retention,
pushing to a target and pruning locally, or a vendor `exclude`.

## Everything in a backup is encrypted except its manifest

Each component is encrypted to the same recipients as the secret state — this
machine's age key plus whatever offline and operator keys `secret recipients`
knows about. So a backup you copy to another machine, upload to a bucket, or
leave on a disk carries no credential and no data with it.

The manifest stays readable on purpose. `morzer backup list` works on a machine
whose key is gone, and an operator looking at a directory of ciphertext can
still tell what it is and which installation it belongs to.

```sh
morzer secret recipients list   # who can read this deployment's backups
```

Two consequences worth knowing before you need them:

- **A backup is readable by the recipients it had when it was taken.** Adding
  a recovery key today does not make yesterday's backups readable by it. Add
  the key first, then take a backup.
- **Verification needs no key.** The checksum is of the stored bytes, so
  `backup verify` detects rot without decrypting anything. Tampering is caught
  separately and more strongly: the encryption is authenticated, so an altered
  backup fails to decrypt rather than restoring altered data.

```sh
morzer backup --component database --component files
```

Limits it, when a full backup is not what you need.

## Verification is not optional

Every backup is re-read and checksummed after it is written, unless you pass
`--no-verify`. A backup nobody has read is a hypothesis, and the moment to test
it is not during a restore.

```sh
morzer backup verify        # the most recent
morzer backup verify <id>   # a specific one
```

Worth running on a schedule against your oldest retained backup, which is the
one most likely to have rotted.

## Retention

Governed by `retention.backups` in the manifest, or `policy.retain_backups` in
your installation when you want a different number than the vendor chose. **The
most recent backup is never pruned**, whatever the number says.

`--no-prune` skips the retention pass for one run.

## Restoring

```sh
morzer restore --force --confirm <installation-id>
morzer restore --backup <id> --force --confirm <installation-id>
morzer restore --force --confirm <id> --identity ~/demo-recovery.key
```

Destructive, and it asks for two things:

- `--force`, which authorises destroying what is currently there.
- `--identity`, when the backup was taken by a machine that no longer exists.
  A rebuilt machine has a new key that was never a recipient of the old
  machine's backups; the offline recovery key is what opens them. See
  [recovering a lost machine](recovering-a-lost-machine.md).
- `--confirm <installation-id>`, typed out. A y/n prompt can be answered by
  reflex; an identifier you have to go and look up cannot.

What it does, in order: verify the backup, **stop the services** so nothing is
writing, run the release's restore hook, re-apply the release over the restored
data, and run the smoke test.

Stopping first is the part that matters. Restored data underneath containers
still holding stale state in memory is a combination that corrupts quietly.

!!! warning "A failed restore needs a human"

    Restore declares that it requires manual intervention on failure. A
    half-restored database is a state no automatic action can repair, so a
    failure exits [12](../reference/exit-codes.md) rather than pretending
    compensation succeeded — and keeps surfacing in `status` and `doctor` until
    you clear it with `morzer status --clear-intervention`.

!!! note "Ctrl-C during a restore leaves the product stopped"

    An interruption is taken literally: the operation stops where it is and
    nothing is brought back up, because a long automatic recovery is not what
    "stop" means. Since the services are stopped before anything is written,
    ctrl-C in the first moments leaves a deployment that is down and a database
    that is untouched.

    The refusal says so, and there are two roads forward:

    ```sh
    morzer apply     # start the current release again, restore abandoned
    morzer restore --force --confirm <installation-id>   # try again
    ```

    Restore is deliberately not resumable. Its middle step overwrites a database
    through the release's own hook, no automatic check can tell how far that got,
    and guessing is the one thing this tool will not do. `morzer status` shows
    where the interrupted operation stopped.

### Restoring another machine's backup

Refused by default. Backups are stamped with the installation they came from,
and restoring one deployment's data over another is almost always a mistake.

If the machine is a rebuild of the one the backup came from, the right answer is
[`installation import`](../reference/installation-commands.md), which restores
the original installation id so the guard never fires.

If you genuinely mean it:

```sh
morzer restore --force --confirm <this machine's id> --allow-cross-installation
```

A separate flag from `--force` on purpose: every restore already requires
forcing, so a shared flag would mean this check could never apply.

## Keeping them somewhere else

A backup on the same disk as the thing it protects is not a backup. A **target**
is somewhere else: another host over SSH, an object store, or a directory on
separate media.

```sh
morzer backup target add file:///mnt/usb/demo-backups
morzer backup target add ssh://backups@nas.internal/srv/demo --credentials backup_ssh
morzer backup target add s3://acme-backups/demo --credentials backup_s3
morzer backup target list
morzer backup target remove file:///mnt/usb/demo-backups
```

Every backup is copied to every configured target after it is verified.
`morzer backup target add` checks the target answers before recording it, so a
typo fails at your terminal rather than during a backup three weeks later, and
`morzer backup target list` shows which of them are reachable right now.

`morzer backup target remove` stops using a target. It deletes nothing that is
already there.

### A push that fails fails the backup

Not a warning. The point of a target is that the data is somewhere your machine
failing does not reach, and a green `backup` on a machine whose backups are all
local is the state this exists to end.

**The local backup is kept.** A failed push leaves you exactly where you were
before you configured a target, plus an error — so the remedy is not to take
another backup. `morzer backup push` retries the copy of a backup you already
have, verifying it again on the way:

```sh
morzer backup push          # retry the most recent
morzer backup push <id>
```

`--no-push` takes a local-only backup for the run, when you already know the
medium is disconnected. `--no-prune-remote` skips the retention pass on the
targets; retention there follows the same policy as locally, and never removes
the most recent backup.

### Credentials

`file://` needs none, which is why it is the target a recovery can always reach.

The others take the `--credentials` flag, naming a secret that holds a small
YAML document. Set it first:

```sh
morzer secret set backup_s3
```

```yaml
access_key_id: AKIA...
secret_access_key: ...
region: eu-central-1      # optional
endpoint: minio.internal  # optional; for anything that is not AWS
```

For `ssh://`:

```yaml
private_key: |
  -----BEGIN OPENSSH PRIVATE KEY-----
  ...
known_hosts: |
  nas.internal ssh-ed25519 AAAA...
```

A name rather than the values, because the URL lives in `installation.yaml`,
which `doctor` prints and support tickets quote. A URL that carries a password
is refused for the same reason.

!!! warning "The host key is required"

    `known_hosts` is not optional and no flag disables checking it. An
    impostor cannot read your backups — they are encrypted to your own
    recipients — but it can accept every push and answer every listing, and you
    would believe you had off-site backups you do not have.

    Get the line with `ssh-keyscan`, then check it against the host itself with
    `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`. A keyscan is only as
    trustworthy as the network you ran it over.

`s3://` speaks to S3 and to everything else that speaks its API: MinIO,
Cloudflare R2, Backblaze B2, and Google Cloud Storage in interoperability mode.
Point `endpoint` at them. The bucket must already exist — the manager will not
create one, because a typo would silently make a new bucket and your backups
would go somewhere nobody is watching.

### Reading a target

```sh
morzer backup list --remote
morzer backup fetch            # the newest on the target
morzer backup fetch <id>
```

`--remote` reads only each backup's manifest, which is the one file in a backup
that is not encrypted — so it works from a machine that has lost every key it
ever had, which is the machine most likely to be running it.

`backup fetch` brings a backup down into this machine's backup store and
verifies it. Restoring is a separate step on purpose: a backup that has come
back from a bucket is one you should be able to look at before it overwrites a
database.

```sh
morzer backup verify --remote        # every backup on every target
morzer backup verify --remote <id>
```

Checks the copy on the target without keeping it: each component is streamed
through a checksum and discarded. That is a full transfer, which is the honest
cost of the claim — a backup nobody has read back is a hope, and copying one to
a bucket does not change that. It needs no key, because the checksums are of the
stored bytes.

Worth running on a schedule against your oldest retained backup, which is the
one most likely to have rotted, and it is the only thing that will notice: the
local copy can be perfect while the remote one is not.

`backup list`, `backup fetch` and `backup verify` all take `--target` to address
one target by URL, whether or not this installation configures it, and
`--credentials-file` to supply that target's credentials from a file instead of
from the secret store. That pair is the escape hatch for a rebuilt machine — see
[recovering a lost machine](recovering-a-lost-machine.md).

### What doctor says

| Check | When it fails |
| --- | --- |
| `backup.target-reachable` | a configured target does not answer, so every backup from now on will fail at the push |
| `backup.target-freshness` | the most recent backup is not on any target |

Both are failures rather than warnings, and both exit
[3](../reference/exit-codes.md). The second is the one worth watching: it is the
failure that hides, because the backup ran, the backup succeeded, and the copy
that would survive the machine is not there.

An [installation export](../reference/installation-commands.md) belongs on a
target too, or beside it, and separately from the recovery key that opens it.
