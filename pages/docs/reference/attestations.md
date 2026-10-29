---
title: Attestations
icon: lucide/file-signature
summary: The signed statement the manager writes after a lifecycle operation — what it claims, what it deliberately does not, and how to verify one
---

# Attestations

After `apply`, `update`, `rollback`, `restore`, `config` and
`installation remove` — on failure as well as success — the manager writes a
signed record of what it did:

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

### Which operation verified the signature

Signatures are checked when a release is **staged**: by `init` for the release it
installs, and by `update` for the one it installs. Every other operation runs
against a release already on disk and verifies nothing. So
`predicate.verification` carries two different claims, and never both:

| Field | Claim |
| --- | --- |
| `signature_verified`, `key_id` | **This** operation verified the bundle's signature, against the key with this id. Only an `update` makes it. |
| `signature_verified_when_staged` | An **earlier** operation verified it when it staged this release: `key_id`, the `operation_id` that verified it, and `at`. An `apply`, `rollback`, `config`, `backup`, `task` or `remove` statement carries it. |

Neither field is present when no signature was verified: an unsigned release, a
machine with no signing keys, or a release staged by a version of morzer that
did not record the check. A release returned to with `rollback --to` is the
fourth case, unless it is the previous release: the release store keeps no
record of how a release was verified, and the previous release is the one
other than the current that the manager keeps a record for. Absent means no claim. Neither field is ever `false`,
which would read as *"checked, and it failed"* — a much stronger claim, and one
an auditor would act on.

`key_id` is the minisign key id, as `minisign` prints it, so it can be matched to
`policy.signing_keys` by eye. `when_staged` is the manager's own record of a past
check, written on the machine that signs this statement: it says what that
machine recorded, not that the bundle on disk still matches.

## Steps carry durations, not timestamps

`predicate.steps[].duration_ms` is what the operation journal records. A
duration plus the operation's own `started` places a step in time; widening a
persisted record to feed a new artifact would be the tail wagging the dog.

## Validating one

A JSON Schema ships with every release, generated from the Go types that produce
the document rather than written alongside them:

```text
schemas/morzer-attestation-v2.json
```

The predicate type is `https://morzecrew.github.io/morzer/attestation/v2`. v2
adds `signature_verified_when_staged` and nothing else. Statements written
before it say `…/attestation/v1`, validate against
`schemas/morzer-attestation-v1.json`, which stays published unchanged, and are
still read by `attest verify`.

It pins `_type` and `predicateType` to single values, so a document from another
contract does not validate against it — notably a **SLSA provenance** document,
which describes how an artifact was *built* where this describes how one was
*deployed*. Reusing SLSA's predicate would have produced a document that
validates against a well-known schema while asserting something that schema does
not mean.

Regenerated by `just schemas`, and a test fails the build when the checked-in
copy no longer matches the types.

## Reading them back

```console
$ morzer attest verify
4 statement(s), 0 problem(s)
  ✔ op_01K2Z9X7QK8V3H4M5N6P7R8S9T.json
    apply succeeded
  ...
```

Three questions, answered separately because an operator acts on them
differently.

**Signature.** Did a key this installation knows about produce these bytes? Four
outcomes, and the third is the one worth understanding:

| Outcome | |
| --- | --- |
| signed by the current key | The machine's key today produced it. |
| **signed by a predecessor** | A key this installation has since retired produced it. **Provenance, not validity** — see below. |
| unverifiable | No key this installation accounts for produced it. This is a finding. |
| unsigned | There is no signature. The machine had no key when it emitted this, which is ordinary for an installation upgraded into schema 6. |

A predecessor match is reported as passing, because a rebuilt or rotated machine
is a normal history rather than a fault. What it establishes is that the bytes
came from an **earlier incarnation of this installation** — not that the
signature was made while that key was in service. The only date available comes
from the artifact, and the artifact is what a forger writes.

Folding that outcome into plain validity is what would make rotation useless: a
rotation after a suspected compromise would still accept whatever the old key
signs, forever, which is the one case rotation exists for.

**Chain.** Do the statements that moved between releases join up? Each one's
`from_version` should be where the previous operation left the installation. A
gap means a release was installed by something that filed no record — which is
what an audit is trying to rule out. An `apply` moves nothing and carries no
`from_version`, so it does not participate.

**Live.** `--against-live` compares the newest **successful** statement against
what the runtime reports right now:

```console
$ morzer attest verify --against-live
5 statement(s), 1 problem(s)
  ✖ live: image
    app runs registry.example/demo/app at sha256:9f2c0a1b4d5e, which no attested image matches
```

Four ways it can disagree, and each is something somebody does by hand:

- an image running at a digest the statement never mentioned;
- an attested image that is no longer running anywhere;
- an image running that the statement never attested at all;
- the rendered configuration on disk no longer digesting to what was attested.

An image that only a one-shot `migrate` service runs is not expected to be
running, and is not reported missing: the service exits by design once its
migrations are done. That holds only while nothing the deployment starts runs
the same digest. A migration that runs the application's own image does not
hide the application's disappearance.

The configuration check reports **that** it differs and never how. The digest is
salted (above), so there is nothing in the statement to diff against — which is
the trade that keeps a port number out of a document that travels.

!!! warning "Images are compared as a set, not per service"

    `predicate.images` records what the release said should run, by repository
    and digest. It does **not** record which service runs which, because the
    manifest does not say: it declares images by name, and the Compose file
    decides where each one is used — possibly in several services.

    So the comparison asks "is every attested digest running, and is everything
    running attested", and two situations slip through it:

    - **Two services swap images with each other.** Both digests are still
      running, so nothing is reported, even though both services run the wrong
      thing.
    - **Two services run the same digest and one stops.** The other keeps that
      digest present, so the stopped one is not reported missing.

    Everything else is caught: a digest nobody attested, a digest attested and
    running nowhere, and any change to the rendered configuration. Closing the
    gap means recording the service each image was deployed to, which is a
    change to the predicate — see RFC 0025 §12.

Newest **successful**, deliberately: comparing against a failed operation would
report every difference that failure caused as though a person had made it.

`attest verify` exits non-zero when it finds a problem, so it works in a cron
job or a CI step without parsing anything. A predecessor signature is not a
problem; an unverifiable one, a broken chain and a live mismatch all are.

### `attest log` — what happened, without asking whether to believe it

```console
$ morzer attest log
2 statement(s), newest first
  OPERATION                      KIND    OUTCOME    RELEASE         SIGNATURE
  op_01K2ZB4M8QF0R7V3X5Y6Z7A8Q9  config  succeeded  1.3.0           signed
  op_01K2Z9X7QK8V3H4M5N6P7R8S9T  update  succeeded  1.2.0 -> 1.3.0  signed
```

Deliberately not `verify` with less output. `signed` says a signature is
*there*, never that it checks out — an operator reading a timeline during an
incident should not have it withheld because a key is missing, and one asking
whether a record can be trusted should not be answered by a listing.

An installation that has recorded nothing yet lists no statements and exits 0.
`attest verify` refuses that same empty set, because there "nothing" would read
as "checked".

## Getting them off the machine

Statements are pushed as they are written, to the same targets this installation
keeps its backups on:

```text
<target>/attestations/op_01K2Z9X7QK8V3H4M5N6P7R8S9T.json
<target>/attestations/op_01K2Z9X7QK8V3H4M5N6P7R8S9T.json.minisig
```

They are not backups and never appear as one: `backup list` reports a directory
only when it holds a `backup.json`, so nothing here can be restored from or
counted by retention.

!!! warning "A failed push does not fail the operation — the opposite of a backup"

    `morzer backup` fails when a push fails, because reporting success for data
    that is only on the doomed disk is exactly what pushing exists to prevent.

    This inverts that on purpose. An attestation that did not leave is a gap in
    a *record* whose subject — the deployment — is fine, and whose local copy is
    still here. Failing an `update` because a log shipper was down would stop
    the security fix an operator is applying, for bookkeeping.

    So the gap is reported instead: a warning at the time, `doctor` until it is
    closed, and `morzer attest push` to close it.

```console
$ morzer attest push
pushed 3 attestation(s) to 1 target

$ morzer attest push
every one of 3 attestation(s) is already on 1 target
```

It sends only what is not already there, so it is safe from cron — and a
statement counts as there only when the target holds its signature too, because
the document goes first and a transfer that died between the two would otherwise
look complete forever.

`doctor` reports the same gap under `backup.attestations-pushed`:

```console
! attestations have left this machine
  1 of 1 attestation(s) are only on this machine
```

Both count statements, however many targets there are. With more than one,
`doctor` says how many statements are missing from at least one of them, since
a statement can be on one target and not another.

An installation with **no backup target** has nowhere to push, so what it
writes stays on the machine and is lost with it; a target removed earlier keeps
what it was sent. `doctor` says how many statements are here, as information
rather than a warning, which `doctor --verbose`, plain output and `--json`
show:

```console
$ morzer doctor --verbose
…
✓ attestations: no backup target to push them to
  4 attestation(s) on this machine, and no backup target is configured to push them to
```

Not a warning, because nothing records whether a machine is air-gapped on
purpose, and a warning on every machine that chose to keep its record local
would teach its operator to read past `doctor`. The default view folds it into
the category's tick, like every check that passed. Configuring a
[backup target](backup-targets.md) gives the record somewhere to go, and
`morzer attest push` then sends what is already here.

## What this does not do yet

Stated here rather than discovered:

- **Nothing prunes them.** One statement per operation is small, but `config`
  and `apply` are frequent — a machine applying at every boot accumulates one
  per boot. The directory has no retention policy the way releases and backups
  do.
- **`verify` reads the local directory**, or a path you give it. It will not
  fetch a target's copies to check them, so verifying what is off-site means
  fetching them yourself first.
- **Attestations go where the backups go.** There is no separate target list, so
  an installation that wants its records somewhere its data is not cannot say
  so yet.

A rebuilt machine signs with a **new key**, and records its predecessor, as does
one whose key was replaced with
[`installation rotate-signing-key`](installation-commands.md#installation-rotate-signing-key).
A signature that checks out against a predecessor is *provenance* — "signed by
an earlier key of this installation" — and never plain validity.
