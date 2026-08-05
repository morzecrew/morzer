---
title: Backup target URLs
icon: lucide/hard-drive-upload
summary: The URL grammar for backup targets, the credential document each transport takes, and what is refused
---

# Backup target URLs

A target is somewhere backups are kept that is not this machine. See
[backups](../operating/backups.md) for what pushing one means; this page is the
grammar and the options.

## The URL

| Scheme | Form | Credential |
| --- | --- | --- |
| `file` | `file:///absolute/path` | none |
| `ssh` | `ssh://user@host[:port]/absolute/path` | required |
| `s3` | `s3://bucket[/prefix]` | usually |

Three slashes in `file://`, because two would name a host. `file://nas/backups`
is refused rather than quietly interpreted: it is legal URL syntax that means
something under SMB, and accepting it here would write somewhere other than the
path you read in your own configuration.

A URL that carries a password is refused. It is stored in `installation.yaml`,
printed by `morzer doctor`, and quoted in error messages — so a password in one
is a password in your support tickets. Credentials go in a secret.

`https://` is refused with a specific message: it is a release *source* scheme.
There is no standard way to push over HTTPS; use `s3://` for an object store.

## The credential document

One secret per target, holding a small YAML document. One document rather than
one secret per field, because a target needs several values at once and three
secrets that must be rotated together is three chances to rotate two of them.

```sh
morzer secret set backup_s3
morzer backup target add s3://acme-backups/demo --credentials backup_s3
```

| Field | Used by | Meaning |
| --- | --- | --- |
| `access_key_id` | `s3` | object store access key |
| `secret_access_key` | `s3` | its secret half |
| `session_token` | `s3` | for temporary credentials |
| `region` | `s3` | defaults to `us-east-1` |
| `endpoint` | `s3` | host, or a full `https://` URL; defaults to AWS |
| `private_key` | `ssh` | an OpenSSH private key, in PEM form |
| `passphrase` | `ssh` | when `private_key` is encrypted |
| `known_hosts` | `ssh` | the pinned host key, required |

Supplying an access key with no secret — or the reverse — is refused. Supplying
neither is fine: that is an instance role.

## What is always verified

**TLS**, for `s3://`. No flag disables it. A bare host in `endpoint` means TLS;
`http://` is the only way to ask for plaintext and you have to write it out.

**The host key**, for `ssh://`. `known_hosts` is not optional and no flag skips
checking it. An impostor cannot read your backups — they are encrypted to your
own recipients — but it can accept every push and answer every listing, and you
would believe you had off-site backups you do not have.

The algorithms offered during the handshake are derived from what you pinned. A
host with both an ed25519 and an RSA key offers whichever it prefers, so a pin
covering only one of them would otherwise produce a mismatch against a server
doing nothing wrong — a refusal indistinguishable from a real attack.

An `ssh-rsa` pin is accepted with `rsa-sha2-256` and `rsa-sha2-512` signatures,
because SHA-1 is refused by every current OpenSSH.

## Object stores that are not AWS

`s3://` speaks to anything that speaks the S3 API. Point `endpoint` at it:

| Store | `endpoint` |
| --- | --- |
| MinIO | `minio.internal:9000`, or `http://` for a plaintext one |
| Cloudflare R2 | `<account>.r2.cloudflarestorage.com` |
| Backblaze B2 | `s3.<region>.backblazeb2.com` |
| Google Cloud Storage | `storage.googleapis.com` (interoperability mode) |

There is no native GCS adapter. Interoperability mode covers it, and a second
large SDK for a second API waits until somebody needs a feature that mode lacks.

## The bucket must already exist

The manager does not create buckets. A typo would silently make a new one and
your backups would go somewhere nobody is watching, which is worse than the
error you get instead.

## Layout on a target

```text
<path or prefix>/
  20260804T174743Z/
    backup.json            plaintext
    database.sql.age       encrypted to your recipients
    secrets.sops.yaml.age
```

The same layout on all three, and the same two rules everywhere:

- **The manifest is written last.** A push interrupted halfway leaves a
  directory nothing lists and nobody can restore from.
- **The manifest is deleted first.** A removal interrupted halfway leaves the
  same, rather than a backup that looks whole and is missing a component.

Only what the manifest names is uploaded. A backup directory can hold other
files — an interrupted restore leaves decrypted components in a staging
directory beside the encrypted ones — and copying those would put a plaintext
database dump on a second machine.
