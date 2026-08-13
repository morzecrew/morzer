---
title: Attestations
icon: lucide/file-signature
summary: The signed statement the manager writes after a lifecycle operation — what it claims, what it deliberately does not, and how to verify one
---

# Attestations

After an `apply`, the manager writes a signed record of what it did:

```text
/var/lib/<product>/attestations/op_01K2Z9X7QK8V3H4M5N6P7R8S9T.json
/var/lib/<product>/attestations/op_01K2Z9X7QK8V3H4M5N6P7R8S9T.json.minisig
```

The name is the operation id, so a statement lines up with the journal entry
for the same run, and the ids sort in the order the operations happened.

The manager already verifies signatures, pins digests, refuses unverified
images and journals every step. Without this, that evidence stays on the
machine in a format nothing else reads, and an operator asked to demonstrate
that production runs what the vendor shipped reconstructs it by hand.

The document is an [in-toto](https://in-toto.io/) Statement. The signature is
minisign's, detached, so verifying one is a command you already know:

```console
$ cd /var/lib/demo/attestations
$ minisign -Vm op_01K2Z9X7QK8V3H4M5N6P7R8S9T.json -P RWQf6L...
Signature and comment signature verified
Trusted comment: morzer demo apply op_01K2Z9X7QK8V3H4M5N6P7R8S9T
```

The key is this installation's own — see
[the security policy](https://github.com/morzecrew/morzer/blob/main/SECURITY.md)
for what it is and what losing it costs. `morzer status --json` reports it as
`signing.public_key`, and every statement carries it too, so a reader holding
only the file can check it.

## What a signature proves

Every statement carries this sentence in a `bound` field, and it is in the
document rather than only in this page because the reader who most needs it is
the one who was handed the file without it:

!!! warning "The bound on the claim"

    This signature proves that a process holding this installation's signing key
    produced these bytes. It does not prove the bytes are true, it does not
    prove the machine was uncompromised when it signed, and it does not identify
    the operator.

A statement is what the manager *observed and recorded*. An attacker who owns
the machine owns the key, and their statements verify too.

## What is in one

| Field | |
| --- | --- |
| `subject` | The release the operation was about: name, version, and the content digest. |
| `predicate.bound` | The sentence above, verbatim. |
| `predicate.operation` | Id, kind, start, end, and outcome. |
| `predicate.installation` | Id, product, mode, manager version, the signing key, and any predecessor keys. |
| `predicate.release` | Versions moved between, the source scheme, the content digest. |
| `predicate.verification` | What was checked before the manager acted — see below. |
| `predicate.images` | Each image by reference and digest, and whether it came from a registry or the bundle. |
| `predicate.config` | Parameter **names**, and a digest of the rendered configuration. |
| `predicate.steps` | Each step, its outcome, and how long it took. |

### Configuration: names always, values never

Parameter values do not appear. They include ports and hostnames, and this is an
artifact deliberately readable by somebody outside the organisation.

`config.rendered_digest` is what makes drift detectable without publishing what
drifted, and it is **salted per installation**. Two consequences, both
deliberate:

- It detects change on **one machine over time**, which is the audit question.
- It is **not comparable across machines**, which is a capability given up on
  purpose: the input is a handful of ports and booleans, and an unsalted digest
  over that space can be brute-forced back to its inputs by anyone holding the
  document.

A machine with no salt — one that reached schema 6 by upgrade and has not run
`init` since — emits **no digest** rather than an unsalted one.

### `signature_verified` is absent, not false

`predicate.verification.signature_verified` is missing from an `apply`'s
statement, and that is the honest answer rather than a gap.

Signatures are checked when a release is **staged**. An `apply` converges onto a
release already on disk, so it verifies nothing and the document says nothing.
Writing `false` there would read as *"checked, and it failed"* — a much stronger
claim, and one an auditor would act on.

So: absent means no claim. Present and `false` would mean a check that failed.

## Steps carry durations, not timestamps

`predicate.steps[].duration_ms` is what the operation journal records. A
duration plus the operation's own `started` places a step in time; widening a
persisted record to feed a new artifact would be the tail wagging the dog.

## Validating one

A JSON Schema ships with every release, generated from the Go types that produce
the document rather than written alongside them:

```text
schemas/morzer-attestation-v1.json
```

It pins `_type` and `predicateType` to single values, so a document from another
contract does not validate against it — notably a **SLSA provenance** document,
which describes how an artifact was *built* where this describes how one was
*deployed*. Reusing SLSA's predicate would have produced a document that
validates against a well-known schema while asserting something that schema does
not mean.

Regenerated by `just schemas`, and a test fails the build when the checked-in
copy no longer matches the types.

## What this does not do yet

Stated here rather than discovered:

- **Only `apply` emits one, and only on success.** The failed operation is the
  one an auditor asks about, and it is the next phase of this work.
- **Nothing pushes them anywhere.** They are written locally, so a machine that
  is lost takes its own record with it.
- **There is no `morzer attest verify`.** Checking a statement today means
  `minisign -Vm` plus reading the JSON. A verifier that can compare a statement
  against the *running* deployment — and fail when somebody has swapped an image
  by hand — is the phase this design lives or dies by.

A rebuilt machine signs with a **new key**, and records its predecessor. A
signature that checks out against a predecessor is *provenance* — "signed by an
earlier incarnation of this installation" — and never plain validity.
