# release-diff · `release diff <from> <to>`

On branch `release-diff` from `4747fe5` (`archive-inputs`, whose archive
reading it reuses), building Phase 2's fourth item of the gap report: one
command that says what changed between two releases. No RFC covers it; the area
is [RFC 0014](../rfcs/0014-building-a-release-bundle.md)'s and
[RFC 0013](../rfcs/0013-bundle-authoring-experience.md)'s, whose rows settle no
comparison. The author ruled its shape on 2026-11-01:

- what it compares: images; parameters, added, removed, or with a changed type or default; secrets; the compatibility declarations for the upgrade range, the database schema and the minimum manager; tasks and profiles; and the bundle's files, added, removed or changed by checksum, with no text patches;
- what it reads: a bundle directory, a `tar.zst` archive, or an installed version, as `release show` resolves one;
- how it exits: 0 whenever it ran, whatever differs, as `config diff` does.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

The ruling names what is compared and leaves open what a change in secrets,
tasks and profiles is.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T07:36:00Z
attempt: 1
claim: the ruling says secrets, tasks and profiles are compared, but not whether a declaration that changed is reported or only one added or removed
evidence: internal/domain/manifest.go:645
action: decided
proposal: ASSUMED — secrets, tasks and profiles are compared by name: added and removed. What an operator acts on is a secret to set, a task to run or a profile to choose, and each of those is a name.
```

The bundle's checksum list and its signature are files in it, and both change
whenever anything else does.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T07:37:00Z
attempt: 1
claim: SHA256SUMS and its signature would be listed as changed in every comparison where anything is, and say nothing the rest of the list does not
evidence: internal/release/sums.go:40
action: decided
proposal: ASSUMED — the file comparison leaves out the checksum list and its signature, the same pair WriteSums leaves out of the list itself. Every other file is compared by its content's SHA-256, as WriteSums hashes it; a change of permissions alone is not reported.
```

Nothing stops the two arguments being two different products.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T07:38:00Z
attempt: 1
claim: the ruling does not say whether two releases of different products may be compared
evidence: internal/domain/release.go:24
action: decided
proposal: ASSUMED — they may. The report names each side by product and version, so a comparison across products says so, and refusing it would stop a vendor comparing a renamed product with its predecessor.
```

**Drift count: 0.**
