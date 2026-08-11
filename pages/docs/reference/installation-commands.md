---
title: installation
icon: lucide/hard-drive-download
summary: Listing the installations on a machine, and exporting or importing one installation's identity and secret state
---

# `morzer installation`

## installation list

```sh
morzer installation list [--status]
morzer ls [--status]
```

Lists the installations on this machine. The two spellings are one command:
`morzer ls` is what somebody types on a host they have just logged into, and
`installation list` is where the noun hierarchy puts it.

```text
PRODUCT  RELEASE     MODE  UNITS  PATH
demo     1.4.0                 5  /etc/demo
sandbox  1.5.0-rc1   dev       0  /etc/sandbox
```

Read from the state files alone — no Docker call, no lock, no network — so it
answers on a machine whose daemon is down. It exits 0 on a machine with no
installations and says so.

`--status` adds a `SERVICES` column by asking each installation's runtime what
is running. One query per row, each bounded at five seconds on its own, so an
unresponsive daemon costs that row and no other. The counts are read without
taking the deployment lock, so a row may be a moment stale; the output says so.

An installation whose state will not load is listed as `unreadable` with the
reason beneath the table, never dropped — and its row carries no field this
manager could not read. A directory in `/etc` this process cannot open is listed
as `not counted` and marked `"skipped": true` in `--json`: on a real host most of
those belong to somebody else, so it is neither a deployment nor a fault — but a
listing that omitted it silently would be reporting a smaller machine than the
one that is there. An `/etc` that cannot be read at all is an error.

`--json` emits an array, one object per installation, with the same rows either
way:

```json
{
  "product": "demo",
  "path": "/etc/demo",
  "schema_version": 5,
  "release": "1.4.0",
  "units": 5,
  "services": {"running": 3, "total": 3}
}
```

`schema_version` is JSON-only; `mode`, `release`, `problem`, `services` and
`services_problem` are present only when they apply. `units` is always present
and is `null` when the supervisor could not be read — `0` is a real answer,
produced by `init --install-units=false`.

Selecting *which* installation every other command means is described in
[Several installations](../operating/several-installations.md).

## Exporting and importing an identity

An **installation export** carries the identity of a deployment and its
encrypted secret state, so a machine that is gone can be rebuilt. It carries no
application data: [`backup`](commands.md#backup) owns that.

Every backup now carries one too, so `installation export` is an optimisation
and a fallback rather than a prerequisite. It stays the fastest path, and it is
the *only* path for a deployment that can never take a backup — a release with
no backup hook and no capturable volume is refused one.

The procedure that uses these commands is
[Recovering a lost machine](../operating/recovering-a-lost-machine.md). This page
is the surface.

### installation export

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

#### What makes an export usable

The export is only ever as readable as the state already was. If the
installation was created with `--no-recovery-recipient`, the only recipient is
the machine's own key — and an export nobody but the dead machine can open is a
file that looks like an insurance policy and is not one.

`export` refuses to write one. Add a recovery recipient first:

```sh
morzer secret recipients generate-recovery-key ~/recovery.key
morzer secret recipients add <the printed public key> --kind recovery
```

### installation import

```sh
morzer installation import <path> --identity <recovery-key-file>
morzer installation import --from-backup [<id>] --identity <recovery-key-file>
morzer installation import --from-backup --target <url> \
    --credentials-file <file> --identity <recovery-key-file>
```

Rebuilds this machine from an installation export and the offline key that can
decrypt it. The export comes from a file, or out of a backup — every backup
carries one.

| Flag | Meaning |
| --- | --- |
| `--identity` | Private age identity that can decrypt the export. Required; there is no default, because the whole point is that this key was not on the machine that was lost. |
| `--from-backup` | Read the identity out of a backup rather than a file. With no id, the **newest** backup that carries one. |
| `--target` | Read the backup from a target rather than from this machine. Transfers the identity document and the manifest, not the archive. |
| `--credentials-file` | Credentials for `--target`, for when the secret store that held them is on the machine that died. |
| `--mode` | Rebuild as a sandbox with `dev`, which also **drops the export's backup targets**. Omitted keeps whatever the export was; a sandbox can never be imported as production. See [dev mode](../operating/unattended-updates.md#dev-mode-a-machine-that-is-a-sandbox-from-birth). |

With `--from-backup` the positional argument is a backup id rather than a path.

**No id means the newest, and that is the safer default rather than the lazier
one.** Identity and data are separate choices: a backup picked for the database
it holds is not necessarily the one whose secrets you want, and staleness in
identity only ever *loses* information. Naming an id is honoured — point-in-time
identity recovery is real — and warns when it is not the newest, naming the
newer backup.

A backup with no identity is refused, and the message says which of the two
reasons applies: it predates this manager version, or the installation that took
it had no recovery recipient to encrypt one to. The remedies differ, which is
why the messages do.

!!! info "On a rebuilt machine, pass `--product`"

    Every managed directory derives from the product name, and with
    `--from-backup` the manager has to find the backups *before* it can read the
    identity that names the product. `morzer --product demo installation import
    --from-backup …`. Importing from a file does not need it: the file is read
    first.

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

#### Refusals

- An existing installation is not replaced without `--force`.
- An identity that is not one of the export's recipients is refused **before**
  anything is created, naming the keys that would work. Discovering it after the
  directories and a new machine key exist is the worst moment for it.
- A secret provider that cannot be re-opened under another identity is refused
  by name. Recovery needs one that can; `sops-age` is it.

#### What it does not do

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
