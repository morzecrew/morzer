---
title: Managing from elsewhere
icon: lucide/git-pull-request-arrow
summary: Point a machine at an authority, publish a signed and encrypted document for it, and let the machine apply it to itself
---

# Managing from elsewhere

You run the same product on several machines and want to change a value on all
of them from one place: a list of servers, a key, the profile. Logging in to
each and running `config apply` works until there are twelve.

Instead, each machine can follow an **authority**: a key you hold. You publish
one document per installation, signed with that key and encrypted to the
machine, to a bucket the machine reads. The machine pulls it on a timer,
checks it, and applies it to itself within the names its release lets be
managed. Nothing listens on the machine, and nothing outside it acts on it.

The document format and every outcome are in the
[desired-state reference](../reference/desired-state.md).

## Before you start

- **The release must opt in.** Only names listed in its
  [`managed`](../reference/manifest.md#managed) section can change, and a release
  without one lets nothing be managed.
- **One target holds both directions**: the machine publishes its
  [fleet row](../reference/fleet.md) there, and reads its documents from
  `state/<product>/<installation-id>` on it. The machine's sync credential needs
  only to list and read that prefix.
- **A [roster](../reference/fleet.md#the-roster)** names each installation and
  the key its row is signed with. A document is encrypted only to a machine
  whose row verifies against it.

## 1. Make an authority key

```sh
morzer state key generate ./authority.key
```

It prints the public key the machines pin. Keep `authority.key` off the
machines: a key a managed machine holds lets that machine manage the rest. A key
from `minisign -G -W` works too; one with a password does not.

## 2. Point the machine at it

On the machine, store the target's [credential document](../reference/backup-targets.md#the-credential-document)
as a secret, then name the target, the credential and the key:

```sh
morzer secret set sync_s3 < sync-credential.yaml
morzer config set sync.target=s3://acme-state/demo
morzer config set sync.credentials=sync_s3
morzer config set sync.authority=RWQ...
morzer config set sync.enabled=true
```

`sync.enabled=true` installs a timer that runs a cycle every 15 minutes;
`sync.interval` changes that. `morzer init --sync-target --sync-authority`
records the first and third at install time, and sync stays off until you turn
it on.

The machine must also publish its row to the same target, which it does on an
hourly timer once the target is one of its backup targets, or with
`morzer fleet publish`. The row carries the age recipient documents are
encrypted to.

## 3. Publish a document

Write the whole state you manage for this installation, not only what changed:

```yaml title="demo.yaml"
parameters:
  log_level: debug
secrets:
  db_password: |
    a-value-from-elsewhere
```

```sh
morzer state publish --target s3://acme-state/demo --expect ./roster.yaml \
    --product demo --installation inst_01M72B61SSWFT54B6KTJC26YD0 \
    --file ./demo.yaml --key ./authority.key
```

It refuses an installation the roster does not list, and a row that does not
verify against the roster's key. It stamps the serial, the time in milliseconds,
and an expiry seven days out (`--valid-for` changes it), encrypts the document
to the recipient the row carries, and signs it. It writes the ciphertext first
and the signature last, and removes all but the newest three documents.
`--dry-run` does everything except the writes.

## 4. Watch it apply

The next tick applies it, or run one on the machine now:

```sh
morzer sync now
morzer sync status
```

From where you publish, `state ls` shows every installation's newest document
beside what its row says it applied and refused:

```sh
morzer state ls --target s3://acme-state/demo --expect ./roster.yaml
```

A document is refused whole when it is not yours, is older than the one applied,
has expired, or names what the release does not manage. `sync status` on the
machine says at which step and why. A refused serial is not tried again:
publish a corrected document, which takes a newer serial.

## Learn what a new machine needs

A machine can tell you what documents for it, and documents for other machines
about it, need to carry, such as its public key and the address it is reached
at. Its release names a task that prints those under
[`published`](../reference/manifest.md#published). Give the machine your age
recipient:

```sh
morzer config set sync.recipient=age1...
```

It then runs the task after every successful apply, start, update, rollback,
restore and configuration change, and publishes the output, encrypted to you
and signed with its key, beside its row. Read it with
your age identity:

```sh
morzer state facts --target s3://acme-state/demo --expect ./roster.yaml \
    --product demo --installation inst_01M72B61SSWFT54B6KTJC26YD0 \
    --identity ./authority.age
```

It prints the facts only when the machine's key signed them and they are the
ones its newest row names.

## Crossing a release that adds or renames a managed name

A release may add a parameter you manage with no default, or rename one, and a
machine cannot install it without a value. Give the value before the machine
installs the release: the machine's channel stages it, and you publish a
document for the release it staged. You do not choose the release or when it
is installed; the machine's channel and its tick do.

1. Upgrade the manager you publish and read the fleet with first. Rows that
   name a staged release are schema 3, and an older manager refuses them.
2. Wait for the machine to stage the release. Its row names it:

   ```sh
   morzer fleet ls s3://acme-state/demo --expect ./roster.yaml --json | jq '.data.rows[].row.staged'
   ```

3. Publish a document that names the new release's names beside the ones the
   installed release manages. The machine applies the second kind now and keeps
   the first [pending](../reference/desired-state.md#names-for-the-staged-release)
   for the staged release. For a rename, give both the old name and the new.
4. The tick installs the release with the pending values, as an
   [unattended update](unattended-updates.md) does. On a machine that does not
   install unattended, `morzer update --to <version>` takes them.

The row's `sync.pending` names what is waiting. A document published before the
release is staged is refused as `unmanageable`, and the next one, once the row
names the release, is taken.

## Keep the authority heard from

`doctor` warns when the document in force expires within a day, and when the
target has not answered for three intervals. A target that withholds your
documents can do nothing else, and that warning is how you see it. Publish
before `expires_at`.

## After rebuilding a machine

A machine rebuilt with `installation import` keeps its id and the serial it
applied, but it has a new age identity and a new signing key. Until the
authority hears of both, the machine refuses each new document as
`undecryptable`, and `state publish` refuses its new row as signed by a key the
roster does not name.

1. Bring the release back with `morzer update <bundle>`, as after any
   [recovery](recovering-a-lost-machine.md).
2. Take the new key off the machine, as for a
   [new one](../reference/fleet.md#getting-the-three-fields-off-a-machine), and
   replace the old key in the roster:

   ```sh
   ssh box 'morzer fleet publish --dry-run --json' | jq -r .data.row.signing_key
   ```

3. Publish again. The row the rebuilt machine publishes names its new
   recipient, and the document is encrypted to it.

## Changing a managed value on the machine

While sync is on, `config set` and `config apply` refuse to change a name the
authority manages, and name the authority. `--force` changes it here, and the
authority's next document sets it back.

## Changing the authority

```sh
morzer config set sync.authority=RWQ...
```

The change is journalled and clears the serials the machine applied and
refused, so the new key's documents are not held back by the old key's. A
document can never change `sync.*`.
