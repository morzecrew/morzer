# attestation-retention · RFC 0049, pruning the attestations a machine keeps

On branch `attestation-retention` from the RFC's branch at `4071dd7c`,
building [RFC 0049](../rfcs/0049-pruning-the-attestations-a-machine-keeps.md)
as ruled by the author on 2026-11-05: a count, no default, and the targets'
copies left alone.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/domain/installation.go`: `Policy.RetainAttestations`, and the
  installation schema to 17 (decision 7).
- `internal/infra/state/state.go`: the 16 -> 17 step, nothing to convert
  (decision 7), with its migration test.
- `schemas/selfhost-v1alpha1-installation.json`: regenerated.
- `internal/lifecycle/ops/settings.go`: `retention.attestations`, a count of
  at least 1 through `retentionCount`, unset keeps everything (decisions 1, 2,
  6).
- `internal/lifecycle/ops/attest_prune.go`: the prune. Oldest first in name
  order; stops at the first statement a target is missing, at every statement
  when a target cannot be listed, and at the newest successful one; signature
  before statement; warns on failure (decisions 4, 5, 8).
- `internal/lifecycle/ops/attest.go`: `emitAttestation` prunes after the
  push (decision 5).
- Tests in `test/suite`, the attestations reference, the settings table in
  the configuration page, the changelog.

The newest successful statement is chosen by `newestSucceeded`, the function
`--against-live` uses, so the prune keeps exactly the statement that command
compares against (decision 4).

Found while planning, and decided before any code.

The RFC does not say whether the prune reports what it removed. It deletes
audit records, and an operator who reads the operation's output should see
that it did.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-11-05T10:05:00Z
attempt: 1
claim: the RFC is silent on whether a prune that removed statements says so
evidence: rfcs/0049-pruning-the-attestations-a-machine-keeps.md:151-166
action: decided
proposal: ASSUMED — a prune that removed statements publishes one informational message naming how many and the setting that asked for it. A prune that removed nothing says nothing.
```
