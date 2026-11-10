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
- **The machine needs a target it can read**: a bucket or directory, with a
  credential that can list and read `state/<product>/<installation-id>` and
  nothing else.
- **You need [minisign](https://jedisct1.github.io/minisign/) and
  [age](https://age-encryption.org)** where you publish from.

## 1. Make an authority key

```sh
minisign -G -p authority.pub -s authority.key
```

Keep `authority.key` off the machines. The public key is the second line of
`authority.pub`.

## 2. Point the machine at it

On the machine, store the target's [credential document](../reference/backup-targets.md#the-credential-document)
as a secret, then name the target, the credential and the key:

```sh
morzer secret set sync_s3 < sync-credential.yaml
morzer config set sync.target=s3://acme-state/demo
morzer config set sync.credentials=sync_s3
morzer config set sync.authority="$(tail -n 1 authority.pub)"
morzer config set sync.enabled=true
```

`sync.enabled=true` installs a timer that runs a cycle every 15 minutes;
`sync.interval` changes that. `morzer init --sync-target --sync-authority`
records the first and third at install time, and sync stays off until you turn
it on.

## 3. Find the machine's recipient

Documents are encrypted to the machine's own age key. Its row in the
[fleet view](../reference/fleet.md) carries it as `age_recipient`, and on the
machine `morzer secret recipients list` shows it as the `machine` recipient.

Use the recipient only from a row that verifies against your
[roster](../reference/fleet.md#the-roster). Another machine that can write to
the bucket can post a row for any installation id, and a document encrypted to
its recipient is a document it can read.

## 4. Publish a document

Write the whole state you manage for this installation, not only what changed:

```yaml title="doc.yaml"
api_version: selfhost/v1alpha1
kind: desired-state
product: demo
installation: inst_01M72B61SSWFT54B6KTJC26YD0
serial: 1794310000123
issued_at: 2026-11-10T12:00:00Z
expires_at: 2026-11-17T12:00:00Z
parameters:
  log_level: debug
```

Encrypt it to the machine, sign the ciphertext with a trusted comment naming it,
and upload the ciphertext first and the signature last:

```sh
serial=1794310000123
age -r age1... -o "$serial.age" doc.yaml
minisign -S -s authority.key -m "$serial.age" \
    -t "morzer desired-state product=demo installation=inst_01M72B61SSWFT54B6KTJC26YD0 serial=$serial"
aws s3 cp "$serial.age" "s3://acme-state/demo/state/demo/inst_01M72B61SSWFT54B6KTJC26YD0/"
aws s3 cp "$serial.age.minisig" "s3://acme-state/demo/state/demo/inst_01M72B61SSWFT54B6KTJC26YD0/"
```

Use a new, higher serial for every document; the time in milliseconds works.
Never replace a pair that is already published.

## 5. Watch it apply

The next tick applies it, or run one now:

```sh
morzer sync now
morzer sync status
```

`sync status` shows the document applied and when it expires. A document is
refused whole when it is not yours, is older than the one applied, has expired,
or names what the release does not manage, and `sync status` says at which step
and why. A refused serial is not tried again: publish a corrected document with
a higher serial.

## Keep the authority heard from

`doctor` warns when the document in force expires within a day, and when the
target has not answered for three intervals. A target that withholds your
documents can do nothing else, and that warning is how you see it. Publish
before `expires_at`.

## Changing a managed value on the machine

While sync is on, `config set` and `config apply` refuse to change a name the
authority manages, and name the authority. `--force` changes it here, and the
authority's next document sets it back.

## Changing the authority

```sh
morzer config set sync.authority="$(tail -n 1 new-authority.pub)"
```

The change is journalled and clears the serials the machine applied and
refused, so the new key's documents are not held back by the old key's. A
document can never change `sync.*`.
