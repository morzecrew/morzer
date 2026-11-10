---
title: Desired state
icon: lucide/git-pull-request-arrow
summary: The document an authority publishes for one installation, the cycle a machine runs to apply it, what each outcome means, and the sync commands
---

# Desired state

An authority publishes a document for one installation, signed with its key and
encrypted to that machine. The machine pulls it on a timer, verifies it, and
applies it as one [`config apply`](parameters.md#several-names-in-one-change).
Nothing listens on the machine, and only names its release lets be managed can
change. For setting one up, see
[managing from elsewhere](../operating/managing-from-elsewhere.md).

## The document

```yaml
api_version: selfhost/v1alpha1
kind: desired-state
product: dotdash-relay
installation: inst_01M72B61SSWFT54B6KTJC26YD0
serial: 1794310000123
issued_at: 2026-11-10T12:00:00Z
expires_at: 2026-11-17T12:00:00Z
parameters:
  dotdash_servers: example.com,b.example.com
settings:
  installation.profile: own-site
secrets:
  vless_clients: |
    0b6c... phone pool=a
unset:
  parameters: [dotdash_address]
```

Below the envelope it is a [change document](parameters.md#several-names-in-one-change),
read by the same rules: `settings`, `parameters`, `secrets` and `unset`, each
optional, names not mentioned untouched. Every envelope field is required:

| Field | What it is |
| --- | --- |
| `api_version` | `selfhost/v1alpha1` |
| `kind` | `desired-state` |
| `product`, `installation` | which installation it is for; both must match the machine and the signature |
| `serial` | a positive whole number, greater than the last one applied; a timestamp in milliseconds works |
| `issued_at`, `expires_at` | RFC 3339 times; a document is refused at or after `expires_at`, which must be after `issued_at` |

**Each document is cumulative.** It carries every value the authority manages
for the installation, every time. A machine applies only the newest document it
finds and may miss any before it, so a document holding only the latest change
would drop one published before it and never fetched. A document whose values
are already in force records its serial and runs no operation.

At most 1 MiB once decrypted, and the ciphertext at most 1 MiB on the target.

## On the target

```text
state/<product>/<installation-id>/<serial>.age
state/<product>/<installation-id>/<serial>.age.minisig
```

- **`<serial>.age`** is the document encrypted with [age](https://age-encryption.org)
  to the machine's recipient, the `age_recipient` its
  [fleet row](fleet.md#what-the-row-carries) carries.
- **`<serial>.age.minisig`** is a minisign signature over the ciphertext, whose
  trusted comment names the document in the clear:

    ```text
    morzer desired-state product=<product> installation=<installation-id> serial=<serial>
    ```

Each pair is written once and never replaced: the ciphertext first and the
signature last. A machine takes the highest serial that has both objects, so a
publish cut off before its signature leaves the previous document in force.

The machine lists `state/<product>/<installation-id>`, so a read-only credential
with listing on that prefix is all [`sync.credentials`](parameters.md#installation-settings-are-not-parameters)
needs. A credential that can list other installations' prefixes tells the
machine which installations exist, even though it cannot read their documents.

## A cycle

Each tick, or `morzer sync now`:

1. List the prefix and take the newest complete pair. None: `absent`. No answer:
   `unreachable`.
2. Compare its serial with the watermark: the same as the last applied or
   refused is `current`, and lower than the last applied is `replay`.
3. Verify the signature against `sync.authority`, then check that the trusted
   comment names this product, this installation and the pair's serial.
   Otherwise `bad-signature`.
4. Decrypt with the machine's age identity, read the envelope, check it against
   the comment, and check `expires_at`.
5. Check every name against the release's [`managed`](manifest.md#managed) list.
   One name outside it refuses the whole document.
6. Apply it as one operation. The serial is written in the operation's record
   step, so it commits with the change and is undone with it.

**Only an authenticated serial is recorded as refused.** The serial in an
object's name is anybody's who can write to the bucket, so a pair that fails its
signature moves no watermark. A refused serial is not tried again, and a newer
one is.

## Outcomes

| Result | What happened | Recorded as refused |
| --- | --- | --- |
| `ok` | the document was applied, or its values were already in force | no; recorded as applied |
| `current` | nothing newer than the last applied or refused document | no |
| `absent` | no complete pair is published | no |
| `unreachable` | the target did not answer, or an object could not be read, as one over 1 MiB | no |
| `bad-signature` | the newest pair is not a document the authority signed for this installation and serial | no |
| `replay` | the newest document is older than the one applied | no |
| `expired` | the newest document has expired, or the one in force has and nothing newer is published | the newest, when it is the one expired |
| `undecryptable` | the document is not encrypted to this machine, as after a rebuild | yes |
| `invalid` | the document does not parse, names another installation inside, or a value is refused | yes |
| `unmanageable` | it names what the release does not let be managed | yes |
| `failed` | its operation failed and was undone | yes |

A refusal records the **step** it stopped at, a **class** and the **names**
involved, and never the error's text, which can quote the value refused. The
steps are `decrypt`, `parse`, `check`, `validate`, `plan`, or the operation's
own step. The classes are the results above, and `health-check`, `runtime`,
`secrets` and `manual-intervention` for an operation that failed. The text is
kept on the machine for `sync status`.

## Commands

### sync now

```sh
morzer sync now
morzer sync now --dry-run
```

Runs one cycle and prints its outcome. Refused while `sync.enabled` is off.
Exits non-zero when a document's operation failed and was undone; every other
outcome, a refusal included, is reported and exits 0. A failure of the
machine's own before the operation runs, such as a secret store it cannot read,
also exits non-zero: it refuses nothing, and the next tick tries the document
again.

| Flag | Meaning |
| --- | --- |
| `--unattended` | One scheduled tick: when another operation holds the deployment lock, exit 0, and the next tick tries again. Without it a held lock exits as a held lock does. |
| `--dry-run` | Fetch, verify and plan the document, and change nothing. |

### sync status

```sh
morzer sync status
morzer sync status --json
```

Prints the target, the authority's key id, the interval, the last document
applied and when it expires, the last refused with its step, class, names and
full text, and what the last cycle found.

## What it reports

| Where | What |
| --- | --- |
| [Fleet row](fleet.md#what-the-row-carries) | `age_recipient`, and a `sync` block with the applied and refused serials, steps, classes and names, never a refusal's text. A cycle publishes the row to the installation's backup targets when its outcome is news: a document applied or refused, or a result unlike the last |
| [Statement](attestations.md) | a `desired_state` block naming the document's serial, ciphertext digest and the authority's key id |
| [Notifications](../operating/notifications.md#what-is-sent) | `sync.refused` once per refused document, `sync.expired` when a cycle first finds the document in force expired |
| `doctor` | `sync.authority` warns when the document in force expires within a day or has expired, or the target has not answered for more than three intervals |
| Supervisor | `<product>-sync.timer` and `<product>-sync.service` while `sync.enabled` is on, every `sync.interval` after the last tick, with up to a third of it as spread |

## A local change to a managed name

While sync is enabled, `config set` and `config apply` refuse to change a
parameter, secret or setting the release lets the authority manage, and name the
authority's key. `--force` proceeds, and the authority's next document sets the
value back. The `secret` commands are unchanged.

## What a document can never change

Settings are limited to `installation.domains` and `installation.profile`, and
only those the release also lists. A document can never change `sync.*`, which
names whom the machine follows, nor `release.*`, `update.*`, policy, recipients,
or backup and notify targets. Changing `sync.authority` is a local act, and it
clears the serials the machine applied and refused, so a new authority starts a
watermark of its own.

An installation imported with `--mode dev` drops `sync.*`: a sandbox keeps
production's installation id and would otherwise read production's documents.
