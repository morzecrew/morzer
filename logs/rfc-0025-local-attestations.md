# RFC 0025 · a record with nowhere to go

On branch `doctor-local-attestations` from `8b3c37e`, closing the SECURITY.md
known gap that `doctor` said nothing about an installation whose attestations can
never leave it. [RFC 0025](../rfcs/0025-attesting-an-installation.md) P4 gave
`doctor` a check "for an attestation directory that is not being pushed", and it
was registered only where backup targets exist.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

P4 names the check and says nothing of an installation with no targets. The
code that built it skipped those, on the grounds that an air-gapped machine has
chosen to keep its record local. That also hid the exposure from a machine that
never configured a target and never chose anything. Nothing in an installation
records an air-gapped choice: not the mode, not the policy, not the update
settings. So the two cannot be told apart.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T11:20:00Z
attempt: 1
claim: P4's doctor check does not say what it reports for an installation with no backup target, and nothing records whether such a machine is air-gapped by choice
evidence: internal/lifecycle/ops/doctor.go:211
action: decided
proposal: ASSUMED — on an installation with no backup target, backup.attestations-pushed reports how many statements are only on this machine and that nothing will push them, with status ok rather than warn, so a machine that chose to stay offline is not warned on every run; an explicit air-gapped setting that would allow a warning elsewhere is not added.
```

Decision 6 (LOCKED: a failed push warns and does not fail the operation) is
untouched: this changes what `doctor` reports, never what an operation does.

**Drift count: 0.**
