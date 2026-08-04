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

The manager writes backups under `/var/lib/<product>/backups` and does not
transport them. Copying them offsite is a job for whatever you already use —
what matters is that a backup on the same disk as the thing it protects is not
a backup.

An [installation export](../reference/installation-commands.md) belongs offsite
too, and separately from the recovery key that opens it.
