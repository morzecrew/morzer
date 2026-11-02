# manager-version-check · RFC 0046 P1, knowing the manager is out of date

On branch `manager-version-check` from `16e2e04`, building
[RFC 0046](../rfcs/0046-updating-the-manager-itself.md) P1 as ruled by the
author on 2026-11-02, with every `OPEN` row as recommended. P1 adds
`morzer version --check` against the GitHub release feed, the `doctor` check
`manager.version` gated by a new setting `manager.check`, and the page
"Upgrading the manager". P2, `self-update`, follows in its own PR.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Found while planning, and decided before any code.

The RFC says the check reports whether the running version "is still supported
under SECURITY.md's rule". The rule's sentence says the latest release, and its
table says the latest minor line.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T20:10:00Z
attempt: 1
claim: SECURITY.md's sentence supports "the latest 0.x release" while its table marks the whole 0.12.x line supported, so "still supported" has two readings
evidence: SECURITY.md:17-30
action: decided
proposal: ASSUMED — a running manager is still supported when it is on the newest release's minor line, as SECURITY.md's table reads: 0.12.0 is supported while 0.12.1 is the newest, and 0.11.x is not. A newer release on the same line still warns, per decision 3.
```

§5.1 asks for the newest release that is neither a draft nor a prerelease.
`install.sh` asks `/releases/latest`, which GitHub answers with the most
recently created one.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T20:10:00Z
attempt: 1
claim: `/releases/latest` returns the most recently created stable release, which is not the newest version once an older line gets a later patch, and it leaves §6's "a prerelease that is ignored" nothing to ignore
evidence: install.sh:346-350
action: decided
proposal: ASSUMED — the check lists the repository's releases, one page of 100, and takes the highest version that is not a draft, not a prerelease and parses as a version. Still one request per check, so §9's arithmetic holds.
```

The RFC stores `manager.check` "beside `update.check`" and names no field.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T20:10:00Z
attempt: 1
claim: the RFC names the setting and where it sits, not the field that records it
evidence: rfcs/0046-updating-the-manager-itself.md:149-154
action: decided
proposal: ASSUMED — `manager.check` is recorded as `update.manager_check` in the installation, omitted when false, so the update settings stay one object and `installation describe` carries it with them. The setting's name stays `manager.check`, as `backup.scheduled` names a field it does not spell.
```

Decision 12 makes a dev build one "whose version is not a release". The CLI
parses a version it cannot read as 0.0.0.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T20:10:00Z
attempt: 1
claim: what counts as "not a release" is unstated, and `dev` and snapshot builds reach the lifecycle layer as version 0.0.0
evidence: internal/cli/root.go:1448-1456
action: decided
proposal: ASSUMED — a manager whose version is 0.0.0, with or without a prerelease part, is a dev build. `version --check` still asks the feed and names the newest release, and says it has no version to compare; `manager.version` reports the same as information and never warns.
```

The RFC says what the check reports when it can ask the feed, not when it
cannot.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T20:10:00Z
attempt: 1
claim: §5.1 is silent on `manager.version` when the feed cannot be reached, and on the exit status of `version --check`
evidence: rfcs/0046-updating-the-manager-itself.md:135-147
action: decided
proposal: ASSUMED — `manager.version` warns that it cannot check, never reporting up to date, as `release.update-available` does for the vendor's registry. `version --check` exits 0 whether or not a newer release exists, and a feed it cannot read is a validation error, exit 2, as a failed https fetch is.
```

Found while building.

Every installation field has to say whether a sandbox rebuilt from a production
export may keep it, and the RFC does not say for `manager.check`.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T20:20:00Z
attempt: 1
claim: the sandbox classification test refuses an installation field nobody has classified, and the RFC is silent on whether a sandbox keeps the new consent
evidence: internal/domain/sandbox_test.go:101-102
action: decided
proposal: ASSUMED — a sandbox keeps `manager.check`, as it keeps `update.check`: the check reads a public feed and reaches nothing production owns.
```
