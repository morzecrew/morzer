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
manifest, and verifies the checksums by re-reading what was written.

## What a backup contains

| Component | Comes from |
| --- | --- |
| `database` | the release's backup hook |
| `files` | the release's backup hook |
| `config` | the rendered configuration |
| `secrets` | the encrypted secret state, as it is on disk |
| `manifest` | the release identity, schema version, checksums, installation id |

The manifest is what makes a backup self-describing: which release took it,
which database schema was current, and which installation it belongs to.

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

Both take `--target` to address one target by URL, whether or not this
installation configures it, and `--credentials-file` to supply that target's
credentials from a file instead of from the secret store. That pair is the
escape hatch for a rebuilt machine — see
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
