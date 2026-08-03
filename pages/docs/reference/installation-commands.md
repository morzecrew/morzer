---
title: installation
icon: lucide/hard-drive-download
summary: The installation command group — export and import an installation's identity and secret state
---

# `morzer installation`

An **installation export** carries the identity of a deployment and its
encrypted secret state, so a machine that is gone can be rebuilt. It carries no
application data: [`backup`](commands.md#backup) owns that.

The procedure that uses these commands is
[Recovering a lost machine](../operating/recovering-a-lost-machine.md). This page
is the surface.

## installation export

```sh
morzer installation export <path>
```

Writes the installation record, the encrypted secret state, the list of who can
decrypt it, and a note of which release was running.

It is read-only: no lock, no journal entry, safe to run at any time. That
matters more than it sounds — an export is worth nothing if taking one is
something an operator hesitates over.

| Contains | Does not contain |
| --- | --- |
| The installation id, product, profile, domains and policy | Any plaintext secret |
| The encrypted secret state, byte for byte | Any application data or database contents |
| Every recipient's public key and role | The release bundle itself |
| The release name, version and content digest | The machine's own age private key |

The file is written `0600`. The ciphertext inside is useless without a key, but
the installation record names domains, policy and the layout of a production
deployment.

!!! danger "Store it where the machine cannot reach"

    An export kept only on the host it describes protects nothing. Neither does
    one kept next to the recovery key that opens it.

`--force` overwrites an existing file. `--dry-run` reports what would be written
without writing it.

### What makes an export usable

The export is only ever as readable as the state already was. If the
installation was created with `--no-recovery-recipient`, the only recipient is
the machine's own key — and an export nobody but the dead machine can open is a
file that looks like an insurance policy and is not one.

`export` refuses to write one. Add a recovery recipient first:

```sh
morzer secret recipients generate-recovery-key ~/recovery.key
morzer secret recipients add <the printed public key> --kind recovery
```

## installation import

```sh
morzer installation import <path> --identity <recovery-key-file>
```

Rebuilds this machine from an export and the offline key that can decrypt it.

| Flag | Meaning |
| --- | --- |
| `--identity` | Private age identity that can decrypt the export. Required; there is no default, because the whole point is that this key was not on the machine that was lost. |

What it does, in order:

1. Creates the managed directory layout.
2. Writes the installation record **with its original id**.
3. Restores the encrypted secret state from the export.
4. Generates a **new** age identity for this host.
5. Re-encrypts the state for that new key plus every non-machine recipient, and
   verifies this machine can now read it.

The old machine's key is dropped. A decommissioned host must not retain the
ability to decrypt — if it is being replaced because it was compromised, keeping
its key would make the rebuild ceremonial.

!!! warning "The installation id is reused, on purpose"

    Backups are stamped with the installation id and `restore` checks against
    it, so a rebuilt machine with a fresh id could not restore its own backups —
    which is the point of having recovered at all.

    The consequence: **decommission the source machine.** Two live hosts sharing
    an installation id will confuse every backup either of them takes.

### Refusals

- An existing installation is not replaced without `--force`.
- An identity that is not one of the export's recipients is refused **before**
  anything is created, naming the keys that would work. Discovering it after the
  directories and a new machine key exist is the worst moment for it.
- A secret provider that cannot be re-opened under another identity is refused
  by name. Recovery needs one that can; `sops-age` is it.

### What it does not do

It restores identity, not software, and not data. After importing:

```sh
morzer update <bundle>                        # the export records which version
morzer restore --force --confirm <id>         # your offsite backup
morzer doctor
```

Release bundles are content-addressed and fetchable, so carrying one inside
every export would make exports enormous for no gain. The export records the
version and digest instead, which is what lets an operator confirm they got the
same bytes.

## Related

- [`secret recipients`](secret-commands.md#secret-recipients) — who can decrypt
- [`backup` and `restore`](commands.md#backup) — the data half of a recovery
- [Recovering a lost machine](../operating/recovering-a-lost-machine.md) — the
  whole procedure, in order
