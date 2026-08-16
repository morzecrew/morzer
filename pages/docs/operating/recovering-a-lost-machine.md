---
title: Recovering a lost machine
icon: lucide/life-buoy
summary: Rebuilding a deployment from an offline recovery key and a backup
---

# Recovering a lost machine

The machine is gone — a failed disk, a deleted VM, a host you no longer trust.
You need **two** things:

1. An **offline recovery key**, created at `init` and kept somewhere the machine
   could not reach.
2. A **backup**, stored offsite.

Every backup carries the deployment's identity — the installation record, the
encrypted secret state, and who can decrypt it — encrypted to the recovery keys
alone. So the artifact a timer produces every night is the one that rebuilds the
machine, and the recovery key is the only thing you have to keep by hand.

!!! warning "This changed, and older backups did not get the benefit"

    A backup taken before this manager version carries no identity, and neither
    does one from an installation with **no recovery recipient** — there is no
    offline key to encrypt it to, so the component is skipped rather than
    written in a form only the dead machine could read. `morzer doctor` reports
    that as `secrets.recovery-recipient`.

    For those, recovery still needs an [installation
    export](#if-your-backups-carry-no-identity) — which is also the answer for a
    product that can never take a backup at all.

An **installation export** is still supported and still the fastest path. What
changed is its status: from a prerequisite you had to remember to take, to an
optimisation and a fallback.

!!! info "This procedure is tested, not described"

    `TestRecoveryRebuildsAMachineFromABackupAlone` runs the two-artifact
    procedure end to end against real age keys on every CI run: create an
    installation, take one backup, delete the machine's entire root, and rebuild
    identity and every secret from that backup plus the offline key. No export
    file exists at any point in it.

    `TestRecoveryRebuildsAMachineFromAnOfflineKey` does the same for the export
    path, so both remain proven rather than one being described.

    `TestRecoveryFetchesTheBackupFromATarget` runs the whole of it, including
    step 4: the backup is pushed to a target, the machine is destroyed, and the
    rebuilt one lists and fetches it back with nothing copied by hand. That test
    used to copy a directory and call it "offsite"; it does not have to pretend
    any more.

    A recovery procedure nobody has executed is a procedure you find out about
    during an incident.

## Before anything goes wrong

### Keep the recovery key off the machine

```sh
morzer secret recipients generate-recovery-key ~/recovery.key
```

It prints a public key and writes the private half at mode `0400`. **Move the
private half somewhere else** — a password manager, an offline drive, a safe. A
recovery key stored on the machine it is meant to recover protects nothing.

The public key is what `init --recovery-recipient` takes.

### Take exports, and store them apart from the key

```sh
morzer installation export /media/backup/demo.export.yaml
```

Read-only, takes no lock, safe at any time. Take one after every change to
secrets or recipients, and keep it with your backups — but **not** next to the
recovery key. An attacker with both has the secrets; with either alone, nothing.

Check what you have before you need it:

```sh
morzer secret recipients list
```

If nothing is listed with kind `recovery`, this procedure will not work and
`installation export` will tell you so rather than writing a file that cannot be
opened.

## When the machine is gone

### 1. Prepare the replacement

Install `morzer`, `docker`, `docker compose` and `sops` on the new host. Put the
recovery key where you can reach it.

### 2. Import the identity

From the backup target directly, which transfers a few kilobytes rather than
the archive:

```sh
morzer --product demo installation import --from-backup \
    --target s3://backups.example/demo \
    --credentials-file ./bucket.yaml \
    --identity ~/recovery.key
```

`--product` is needed because nothing on this host knows what it is yet: every
managed directory derives from the product name, and on a rebuilt machine the
identity you are about to read is the only place it is written down.

`--credentials-file` breaks a circle: the bucket credentials live in the secret
state, the secret state is in the backup, and the backup is in the bucket. An
operator with an access key can start from outside it.

From a backup already on this machine — one you fetched, or copied off a disk:

```sh
morzer --product demo installation import --from-backup --identity ~/recovery.key
```

With no id it uses the **newest** backup that carries an identity, which is
almost always what you want: staleness here only ever loses information, so the
newest export is the most complete one. Naming an id is honoured and warns when
it is not the newest, because a backup chosen for the *data* it holds is not
necessarily the one whose *secrets* you want.

From an export file, if you have one:

```sh
morzer installation import demo.export.yaml --identity ~/recovery.key
```

All three restore the installation with its **original id**, generate a new age
identity for this host, re-encrypt the secret state for it, and revoke the old
machine's key.

It prints what it assumed and what to do next. If the identity you passed cannot
open the export, it says so before creating anything, and names the keys that
would work.

It also refuses, before creating anything, if the lost machine ran a runtime
this manager does not drive. An installation is fixed to its runtime when it is
created and never moves, so a rebuild here would produce a machine no command
could operate. The refusal names both runtimes: install a manager built for the
one your export needs, or `init` a new installation and `restore` the data into
it.

### 3. Reinstall the release

The export records which version was running:

```sh
morzer release verify ./demo-1.2.0     # confirm the digest matches the export
morzer update ./demo-1.2.0
```

### 4. Get the backup back

If your backups go to a [target](backups.md), the import already brought its
address with it — the target lives in the installation, and the installation was
in the export. You do not have to remember where they went, which is the thing
nobody remembers during an incident.

```sh
morzer backup list --remote
morzer backup fetch <id>
```

Listing needs no key at all: it reads each backup's manifest, and the manifest is
the one file in a backup that is not encrypted.

??? note "If the target needs credentials the machine cannot read yet"

    The circle to break: the bucket's keys are secrets, the secrets are in the
    encrypted state, and on a machine where import has not run there is nothing
    to decrypt them with. Three ways through, in the order to try them.

    **From the export.** `installation import` restored the secret state, so the
    credentials are already there. This is the ordinary path and it needs
    nothing extra.

    **From a file.** When the export is gone or the credentials in it are stale:

    ```sh
    morzer backup list --remote --target s3://acme-backups/demo \
        --credentials-file ./creds.yaml
    morzer backup fetch <id> --target s3://acme-backups/demo \
        --credentials-file ./creds.yaml
    ```

    A file rather than a flag, because a flag is visible in `ps` to everyone on
    the machine.

    **From nothing.** A `file://` target on removable media needs no credential.
    That is why it is worth having one even if your real backups go to a bucket.

If you copied backups off by hand instead, put them under
`/var/lib/<product>/backups` now.

### 5. Restore the data

```sh
morzer restore --force --confirm <installation-id> --identity ~/recovery.key
```

The id is the original one, which is exactly why this works: backups are stamped
with the installation they came from, and a machine rebuilt with a fresh id
would have its own backups refused.

`--identity` for the second reason the recovery key exists. Import generated a
**new** age identity for this host, and that key was never a recipient of the
backups the lost machine took — the offline key was. So the same key opens both
the export and the backups, which is why keeping one off the machine is not
optional.

### 6. Check, then decommission

```sh
morzer doctor
```

Then **decommission the old machine** if any part of it still exists. Two live
hosts sharing an installation id will confuse every backup either takes, and the
old host's key was revoked precisely so it cannot follow along.

## If your backups carry no identity

Two kinds of backup have none: those taken before this manager version, and
those from an installation with no recovery recipient. `--from-backup` says
which case you are in and what to do about it — they need different answers,
and telling the second kind to "take a new backup" would prescribe the action
that reproduces the problem.

If you have an export file, use it: the procedure above works unchanged.

If you have neither, you can still recover data, but not identity. Create a new
installation, install the release, and restore across installations explicitly:

```sh
morzer init --release ./demo-1.2.0 --recovery-recipient <a new key>
morzer update ./demo-1.2.0
morzer restore --force --confirm <the NEW id> --allow-cross-installation
```

`--allow-cross-installation` is a separate flag from `--force` because `--force`
is already required for every restore. Restoring another deployment's data is a
distinct decision and needs its own answer.

What you do not get back automatically: the old installation id, and the
secrets. `restore` brings back the database and the volumes; it does not
reinstate the secret state.

**The secrets are not necessarily lost, though — that depends on the recovery
key, not on the export.** A backup from before this change carries
`secrets.sops.yaml` and `secrets.recipients.yaml`, encrypted to the same
recipients as everything else in it. If the recovery key was one of them, you
can get the values back by hand and set them again with `morzer secret set`.

Backups taken since carry `export.yaml` instead, which holds the same encrypted
state and the recipient roles beside it — and `--from-backup` reads it for you,
so the recipe below is only for the older shape.

There are two layers, because the SOPS document is itself stored as an
encrypted backup component:

```sh
cd /var/lib/demo/backups/<id>

# 1. the backup component
age --decrypt -i ~/recovery.key secrets.sops.yaml.age > secrets.sops.yaml

# 2. the SOPS document inside it
SOPS_AGE_KEY_FILE=~/recovery.key sops --decrypt secrets.sops.yaml
```

The same key opens both, because it was a recipient of both. Tedious, and much
worse than having an export, but not the same as gone — and delete the
intermediate file when you are done with it.

### Without a recovery key, nothing is recoverable

This is the case worth understanding before it happens. Backups are encrypted to
the secret store's recipient list. With no recovery recipient, that list is the
machine's own age key and nothing else — and that key was on the machine you no
longer have.

So it is not merely the secrets that are unreadable. **The database dump and the
volumes are too.** Every backup you took is ciphertext nobody holds a key for.

That is the outcome the recovery key exists to prevent, and the reason `init`
refuses to proceed without one unless you say `--no-recovery-recipient` out
loud. `morzer doctor` reports it as `secrets.recovery-recipient`; if that check
is not passing on a machine you care about, it is the most urgent thing on this
page.

## What is not covered

- **Restoring the machine's original age key.** There is deliberately no way to
  do this. Recovery replaces the key rather than resurrecting it, so a
  compromised host does not keep access to a deployment that has moved on.
- **Migrating between products or renaming one.** An export is the same
  installation on a different host, not a different installation.
