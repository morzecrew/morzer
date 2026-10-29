---
title: installation
icon: lucide/hard-drive-download
summary: Listing the installations on a machine, exporting or importing one installation's identity and secret state, and removing one
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
| `--install-units` | Install the systemd units when systemd is available, as `init` does. Default `true`. |

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
2. Mints a **new** signing key for this host. The lost machine's key is
   recorded as its predecessor, because its private half did not travel.
3. Writes the installation record **with its original id** and the new signing
   key.
4. Restores the encrypted secret state from the export.
5. Generates a **new** age identity for this host.
6. Re-encrypts the state for that new key plus every non-machine recipient, and
   verifies this machine can now read it.
7. Installs the systemd units, when systemd is available. A unit that already
   exists keeps whatever enablement it has.

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

## Describing an installation as a file

### installation describe

```sh
morzer installation describe                      # to stdout
morzer installation describe --output morzer.yaml # to a file
```

Writes what an operator *chose*, as a document:

```yaml
# Written by `morzer installation describe`. Nothing reads it back:
# editing this file changes nothing.
api_version: selfhost/v1alpha1
kind: installation-document
id: 01J9Z5K3QH
product: acme
release:
  name: acme
  version: 4.2.1
  digest: sha256:…
  ref: oci://registry.example.com/acme/release
domains: [acme.example]
parameters:
  http_port: "8443"
secrets:
  - db_password
policy:
  require_signature: true
backup:
  targets:
    - url: s3://bucket/prefix
      credentials: s3_creds
```

The answer to "what is this machine" becomes a file rather than four commands.
It is reviewable, diffable, and safe to commit.

#### It holds no secret value, and cannot

Every credential in an installation is already a *reference*: a backup target
names the secret holding its credentials, a notification target names the secret
holding its URL. The document carries those names. `secrets` is a list of names
too — what `secret set` would have to be run for, never what was set.

That is a property of the configuration rather than a filter applied on the way
out, which is why it can be stated flatly: there is nowhere in this document to
put a value.

#### Nothing reads it back

There is no `morzer apply -f`. Changing the file changes nothing, and the header
the document carries says so, because a file that looks like desired state and
is not is the defect [`config`](parameters.md) was built to fix.

Its uses are the ones that need no reader: documenting a machine, diffing two
that should match, and reviewing what changed after somebody ran `config set`.

#### What it leaves out, and why

| Not in the document | Why |
| --- | --- |
| `schema_version` | State bookkeeping. The document has its own `api_version`. |
| `created_at` | History, not a choice — a recreated installation has its own. |
| `signing` | Machine identity. The public half is derived from the key file, and a rebuilt machine is honestly a different signer. |
| `attestation_salt` | Minted, not chosen — and publishing it in a file meant for a git repository would make the digest it salts brute-forceable again. |

Every field of an installation is either carried by the document or excluded
with a reason, and that accounting is checked against the struct in both
directions: a field added and left unclassified fails the build, and so does a
reason naming a field that no longer exists.

## Removing an installation

### installation remove

```sh
morzer installation remove --dry-run --force --confirm <installation-id>
morzer installation remove --force --confirm <installation-id>
morzer installation remove --force --purge --confirm <installation-id>
```

Takes one installation off this machine, and touches nothing whose path is not
derived from its product name, so another installation on the same host is
safe. It runs as an operation: under the deployment lock, journaled step by
step, and plannable with `--dry-run`.

| Flag | Meaning |
| --- | --- |
| `--force` | Required. Removal is destructive on every machine. |
| `--confirm` | The installation id, typed out. Required on a production installation, as it is for [`restore`](commands.md#restore). A dev installation needs `--force` alone, and a typed id that names another installation is refused on either. |
| `--purge` | Also remove the data and what unlocks it — see below. |

A plan is confirmed like the removal itself, because `--dry-run` refuses
whatever the operation would refuse. Pass it the same flags.

#### What goes and what stays

| What | Default | `--purge` |
| --- | --- | --- |
| systemd units | removed | removed |
| containers and networks | removed | removed |
| named volumes | **kept** | removed |
| images no release left on the machine names | removed | removed |
| `/opt/<product>`, the releases | removed | removed |
| `/run/<product>`, the rendered secrets | removed | removed |
| `/etc/<product>` configuration and signing key | removed | removed |
| `/etc/<product>` secret state and `age/`, the machine key | **kept** | removed |
| `/var/lib/<product>/data` and `backups` | **kept** | removed |
| `/var/lib/<product>/attestations` | **kept** | only the removal's own |
| the journal | **kept**, renamed | only the removal's own record, renamed |

The secret state and the machine key stay with the data because they are what
unlocks it. Backups are encrypted to the machine key, and on an installation
created with `--no-recovery-recipient` nothing else can read them. A kept
database volume was created with the credentials the secret state holds.

`init` reuses a machine key and secret state it finds, so installing the same
product again on this machine runs against the kept volumes with the
credentials they were created with. When you are moving the product off this
machine for good, pass `--purge`.

**Nothing on a backup target is removed,** `--purge` included. Remote backups
and attestations are the record that outlives the machine, and the removal adds
its own statement to them.

The command ends by saying what it kept and where; `--json` carries the same
list as `data.kept`.

#### The record it leaves

Like other operations, the removal journals its steps and files a signed
[attestation](attestations.md), which it pushes to the configured targets. The
signing key is the last thing removed, after the statement is written. A
statement that cannot be signed is filed unsigned with a warning, as any
operation's is, and the key is removed all the same.

The journal is then renamed to
`/var/lib/<product>/manager/operations-removed-<operation-id>.jsonl`. A later
`init` starts with an empty journal and inherits no unfinished operation from an
installation that no longer exists. With `--purge`, the renamed journal holds
only the removal's record, and `attestations/` holds only its statement.

#### When it fails, or there is no installation record

Every step can be repeated safely. A removal that fails part way is finished by
fixing the cause and running it again, or with `--resume`. The installation
record is the last thing it forgets, so the second run asks for the same
confirmation.

A failed first install leaves directories and a machine key, and no record to
read an id or a mode from. `--product` names it, and `--force` alone is enough:

```sh
morzer --product demo installation remove --force --purge
```

With no record there is no installation to attest to and no target configured,
so that removal is journaled and files no statement.

A product with nothing of it on the machine is refused, so a typo in
`--product` removes nothing.

!!! tip "Take a backup first"

    The command takes no backup of its own. `morzer backup` before
    `installation remove --purge` keeps a copy on your targets, and
    `installation import --from-backup` with your recovery key brings it back
    on any machine.

## Rotating the signing key

### installation rotate-signing-key

```sh
morzer installation rotate-signing-key
```

Replaces the key this machine signs its own statements with: the
[attestations](attestations.md) it files and the [fleet](fleet.md) row it
publishes. It mints a new key, records the current one as the newest entry of
`signing.previous_keys` with `reason: rotation` and the time it was retired, and
moves the new key into place. The old private key is gone afterwards; its public
half stays in installation state.

Everything the old key signed still verifies, as **signed by a predecessor** and
never as the current key: provenance, not validity, the same outcome a rebuilt
machine's predecessor gets. The rotation files its own statement, signed by the
new key, naming the old one through `previous_keys`. It is not countersigned by
the old key, because a key retired for a suspected compromise vouching for its
successor proves nothing.

!!! warning "Rotation protects the future and repairs nothing"

    After a suspected compromise, everything the old key signed stays suspect,
    including what looks old: whoever holds it can back-date a statement as
    easily as write a current one. `retired_at` is history for you, not a check
    any verifier makes. Re-sign what you still vouch for; treat the rest as
    unverified. See the [security policy](https://github.com/morzecrew/morzer/blob/main/SECURITY.md#rotation-protects-the-future-and-repairs-nothing).

It runs as the operation `rotate-signing-key`: under the deployment lock,
journaled with the public key it retires, attested, and planned by `--dry-run`
without minting anything. It asks for no confirmation beyond its name, as
`secret rotate` does.

The order is chosen so a crash cannot lose the old public key. The new key is
staged beside the current one, the old public key is recorded as retired, and
only then is the new key moved into place. A rotation that fails before the
move puts state back and removes the staged key. One that was interrupted is
finished by `morzer installation rotate-signing-key --resume`, which retires
the key the interrupted run named and mints no second one.

It refuses a machine with no signing key, which has nothing to rotate, and one
whose key file is not the key installation state records, because it is not
clear which key to retire; `morzer init --repair` records the key on disk. A key
file state records no key for at all, as on a machine migrated to schema 6 that
minted one before anything recorded it, is not a disagreement: that key is
retired like a recorded one, so what it signed stays attributable. A rotation
also refuses to start while another operation is unfinished; a rotation that
was interrupted is finished with `--resume`, never run over.

A [fleet roster](fleet.md#the-roster) binds each installation to a public key,
so after a rotation `fleet ls --expect` reports this machine's row as
`signed-by-another-key` until its entry names the new key.
`morzer fleet publish --dry-run --json` prints it.

## Related

- [`secret recipients`](secret-commands.md#secret-recipients) — who can decrypt
- [`backup` and `restore`](commands.md#backup) — the data half of a recovery
- [Recovering a lost machine](../operating/recovering-a-lost-machine.md) — the
  whole procedure, in order
