---
title: Recovering a lost machine
icon: lucide/life-buoy
summary: Rebuilding a deployment from an offline recovery key, an installation export and a backup
---

# Recovering a lost machine

The machine is gone — a failed disk, a deleted VM, a host you no longer trust.
You have three things, or you do not recover:

1. An **offline recovery key**, created at `init` and kept somewhere the machine
   could not reach.
2. An **installation export**, taken while the machine was healthy.
3. A **backup**, stored offsite.

The first two rebuild the deployment's identity and secrets. The third brings
back the data. They are separate artifacts on purpose: an export is small and
static, a backup is large and changes constantly, and one file that was both
would be a file nobody takes often enough.

!!! info "This procedure is tested, not described"

    `TestRecoveryRebuildsAMachineFromAnOfflineKey` runs it end to end against
    real age keys on every CI run: create an installation, export it, delete the
    machine's entire root, rebuild from the export and the offline key, and
    assert the secrets come back readable by the new host's own key.

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
export and the recovery key where you can reach them.

### 2. Import the identity

```sh
morzer installation import demo.export.yaml --identity ~/recovery.key
```

This restores the installation with its **original id**, generates a new age
identity for this host, re-encrypts the secret state for it, and revokes the old
machine's key.

It prints what it assumed and what to do next. If the identity you passed cannot
open the export, it says so before creating anything, and names the keys that
would work.

### 3. Reinstall the release

The export records which version was running:

```sh
morzer release verify ./demo-1.2.0     # confirm the digest matches the export
morzer update ./demo-1.2.0
```

### 4. Restore the data

```sh
morzer restore --force --confirm <installation-id>
```

The id is the original one, which is exactly why this works: backups are stamped
with the installation they came from, and a machine rebuilt with a fresh id
would have its own backups refused.

### 5. Check, then decommission

```sh
morzer doctor
```

Then **decommission the old machine** if any part of it still exists. Two live
hosts sharing an installation id will confuse every backup either takes, and the
old host's key was revoked precisely so it cannot follow along.

## If you did not take an export

You can still recover data, but not identity. Create a new installation, install
the release, and restore across installations explicitly:

```sh
morzer init --release ./demo-1.2.0 --recovery-recipient <a new key>
morzer update ./demo-1.2.0
morzer restore --force --confirm <the NEW id> --allow-cross-installation
```

`--allow-cross-installation` is a separate flag from `--force` because `--force`
is already required for every restore. Restoring another deployment's data is a
distinct decision and needs its own answer.

What you do not get back: the old installation id, and the secrets. **The
secrets are gone.** Every credential the product used has to be set again, and
anything derived from them — session keys, encrypted columns the product manages
itself — may be unreadable.

That is the outcome the recovery key exists to prevent, and the reason `init`
refuses to proceed without one unless you say `--no-recovery-recipient` out
loud.

## What is not covered

- **Restoring the machine's original age key.** There is deliberately no way to
  do this. Recovery replaces the key rather than resurrecting it, so a
  compromised host does not keep access to a deployment that has moved on.
- **Migrating between products or renaming one.** An export is the same
  installation on a different host, not a different installation.
