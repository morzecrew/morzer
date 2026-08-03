---
title: secret
icon: lucide/key-round
summary: The secret command group — list, set, generate, rotate, remove, render, recipients
---

# `morzer secret`

Secrets live encrypted in `secrets.sops.yaml` and are rendered to tmpfs for the
product to read.

**Values are never printed, never passed in argv, and never written to the
journal.** `domain.Secret` renders as `[redacted]` through both `String` and
`LogValue`; a redacting log handler and the subprocess output scrubber are the
second and third lines of defence.

```text
/etc/<product>/secrets.sops.yaml     SOPS + age, encrypted at rest
        ↓
/run/<product>/secrets/*             tmpfs, 0700 directory, 0400 files
        ↓
/run/secrets/*                       inside the container
```

Rendered configuration in `/etc` contains *paths* to secrets, never values.

## secret list

Lists secret names, fingerprints and metadata — never values.

The fingerprint is what lets an operator confirm two machines hold the same
value without either of them revealing it.

## secret set

```sh
morzer secret set <name>
```

The value is read without echo from the terminal, or from stdin when it is
piped. **There is no flag for the value**: argv is world-readable through
`/proc`, so a credential passed that way is a credential published.

## secret generate

```sh
morzer secret generate <name>
```

Generates a value using the generator the release declares for that secret in
its [secret schema](manifest.md#the-secret-schema).

| Flag | Meaning |
| --- | --- |
| `--kind` | Override the generator: `password`, `hex`, `base64`, `uuid`, `age-key`. |
| `--length` | Override the declared length. |
| `--alphabet` | Override the password alphabet. |

## secret rotate

```sh
morzer secret rotate <name>
```

Generates a new value of the same shape and restarts **only** the services the
release declares as depending on it — the difference between a blip and a full
outage.

## secret remove

```sh
morzer secret remove <name>
```

Deletes a secret. A secret the release declares as required will then be
reported missing by `doctor`.

## secret render

Renders secrets to the tmpfs directory the product reads. `apply` does this as
one of its steps; running it directly is for recovering a `/run` that was
cleared by a reboot without a full converge.

## secret recipients

Manages who can decrypt the secret state.

The state is always encrypted for at least two recipients: this machine, and an
offline recovery key. **Removing the last recipient, or the machine's own, is
refused** — either would produce a state nothing on the machine could read.

### secret recipients list

Lists recipients, with their kind and comment.

### secret recipients add

```sh
morzer secret recipients add <age-public-key>
```

Adds a recipient and re-encrypts the state for it.

| Flag | Meaning |
| --- | --- |
| `--kind` | Recipient kind: `recovery` or `operator`. Default `operator`. |
| `--comment` | Note recorded alongside the key. |

### secret recipients remove

```sh
morzer secret recipients remove <age-public-key>
```

Removes a recipient and re-encrypts without it.

### secret recipients generate-recovery-key

```sh
morzer secret recipients generate-recovery-key <path>
```

Generates an offline recovery identity, writes the private half to `path` at
mode `0400`, and prints its public key on stdout — so it can be fed straight to
`init --recovery-recipient`.

!!! danger "Move it off the machine"

    A recovery key kept on the machine it is meant to recover protects nothing.
    The private half exists so a *rebuilt* machine can read the old state.

## Why `sops` is a subprocess

`sops` is executed rather than imported. The library pulls in the AWS, GCP and
Azure KMS SDKs for a deployment that only ever uses age. It sits behind a port,
so the decision is reversible: replacing it is a new adapter, not a change to
the lifecycle layer.

Values reach it over stdin, never as arguments.
