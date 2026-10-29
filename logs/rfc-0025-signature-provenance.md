# RFC 0025 · where a release's signature was verified

On branch `attest-signature-provenance` from `8b3c37e`, closing the SECURITY.md
known gap that no attestation said a release's signature was verified. It
follows [RFC 0025](../rfcs/0025-attesting-an-installation.md), whose §4.1
predicate already had `signature_verified` and `key_id` and never filled them
in, not even for the `update` that checks a signature itself.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

The predicate gains a field, and decision 1 says the predicate is versioned by
URI. The published v1 schema sets `additionalProperties: false` on
`verification`, so a v1 document carrying the new field would fail the schema an
auditor validates it with. The change therefore takes a new URI, which is what
the decision prescribes, rather than widening v1 in place.

```divergence
decision: 1
grade: LOCKED
kind: resolved
at: 2026-10-29T10:40:00Z
attempt: 1
claim: touched the predicate; the new field went into a new predicate version, attestation/v2, as decision 1 requires, and v1 is unchanged
evidence: internal/domain/attestation.go:36-48
action: decided
```

§4.1 lists the verification fields and §3 says a signature is checked when a
release is staged. Neither says what a statement about a later operation, which
verifies nothing, reports about the check made at staging. Reporting nothing
left an apply's statement silent on whether the release it ran was ever
verified; reporting `signature_verified` there would credit the apply with a
check it did not make.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T10:40:00Z
attempt: 1
claim: §4.1 has no field for a signature an earlier operation verified when it staged the release a later operation runs against
evidence: internal/domain/attestation.go:166-175
action: decided
proposal: ASSUMED — the release record keeps the check init or update made (key id, operation, time); an update's statement reports its own check as signature_verified and key_id, and every later statement about that release reports the recorded one as signature_verified_when_staged, never both, never false.
```

A new predicate version raises what a reader does with the old one. Statements
already filed say v1, on machines and in buckets, and `attest verify` refused any
predicate type but the current one. Refusing v1 would turn every statement filed
before this change into an unreadable file.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T10:40:00Z
attempt: 1
claim: the RFC does not say whether a reader accepts statements of an earlier predicate version once a new one exists
evidence: internal/lifecycle/ops/attest_verify.go:131
action: decided
proposal: ASSUMED — attest verify reads v1 and v2 statements, since v2 only adds a field; any other predicate type is still refused by name; schemas/morzer-attestation-v1.json stays published and is never regenerated.
```

**Drift count: 0.**
