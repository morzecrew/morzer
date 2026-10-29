# Ruled behaviour fixes · doctor without a daemon, unknown providers, installation ids

Three behaviour changes from the v0.9.0 gap report (B4, B6 and B7), each ruled
by the author on 2026-10-29 before any code was written, on branch
`ruled-behaviour-fixes` from `e340210`.

Only B6 meets a design document. RFC 0018 owns the manifest surface and carries
no grades, so its rows are treated as `LOCKED`; none of them settles provider
names, and decision 15, which refuses operation names nothing runs, is the
nearest precedent. B4 changes which `doctor` finding fails, and B7 changes the
prefix of a new installation id; no RFC decides either, so neither has an entry
here.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T21:00:37Z
attempt: 1
claim: providers.secrets, backup and health names were defaulted and never read, so a manifest naming vault loaded and ran sops-age; the authoring commands now refuse a name outside the one implementation each capability has, and init and update only warn, as the author ruled
evidence: internal/domain/manifest.go:157
action: decided
proposal: LOCKED — RFC 0018 decision 19: secrets, backup and health accept only sops-age, hooks and http; release verify, build, pack and archive refuse any other name, and init and update warn rather than refuse, so a morzer upgrade never strands a running installation.
```

The refusal sits in `checkBundleIntegrity` (`internal/cli/release.go:330`), the
one function every command that blesses a bundle shares, rather than in
`Manifest.Validate`. `Validate` runs on every load, including an operator's
`apply` over a release already installed, and the ruling was that such a load
must keep working. The warning goes through `warnDeprecations`
(`internal/lifecycle/ops/ops.go:284`), which `init`, `update` and a plan call
and nothing else, for the reason that function already gives.
