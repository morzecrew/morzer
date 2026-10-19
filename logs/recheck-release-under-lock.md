# Recheck release under lock · an operation that waited plans for the release now installed

On branch `recheck-release-under-lock` from `167af1a`. Found in review on #122:
`update` and `rollback` read the current release before taking the deployment
lock and never read it again. With `--wait`, the operation they queued behind
can move it. An update queued behind another update to 1.3.0 journalled
`from 1.2.0`, and checked compatibility, took its backup and its restore point,
and told its hooks `PREVIOUS_VERSION` against a release no longer installed.

`apply`, `backup` and `restore` read it the same way. An `apply` queued behind a
rollback converged the release that had just been rolled off, with the pointer
naming the other one.

`TestAnOperationThatWaitedForTheLockRefusesAMovedRelease` was red for all five
before the fix: each ran its plan for 1.3.0 after a rollback to 1.2.0 had
taken the lock first.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Refused rather than re-planned. A plan's checks ran outside the lock, and an
update's against a bundle it fetched, so re-planning means running the
operation's whole front half again under the lock. That is the operator
re-running the command, which the refusal says. The refusal uses `usage`, as
the existing refusal for a resumable operation that changed while waiting does.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-19T11:59:35Z
attempt: 1
claim: apply, update, rollback, backup and restore planned against the release read before the deployment lock and ran that plan after --wait let another operation move it
evidence: internal/lifecycle/ops/apply.go:1169
action: decided
proposal: ASSUMED — an operation that acts on the current release re-reads it under the deployment lock and refuses, with the release it found named, when its root or digest moved; recorded as RFC 0001 decision 24
```

**Drift count: 0.**
