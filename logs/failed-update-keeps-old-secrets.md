# Failed update keeps old secrets · the restore point renders them back when nothing was started

On branch `failed-update-keeps-old-secrets` from `167af1a`, for
[#106](https://github.com/morzecrew/morzer/issues/106). Left open in
`logs/apply-keeps-running-secrets.md`, attempt 3, which halted on RFC 0001
decision 19 with the proposal carried below.

An update that fails before `start-services`, on an image pull for example,
leaves the previous release's containers running. Since #104, render-secrets'
compensator keeps the directory while the runtime reports anything running.
That directory holds the new release's secrets, and rendering prunes what the
release being rendered does not declare, so a secret only the previous release
declares was gone. The restore point would have rendered the previous release's
back, but it returned early when nothing had been started.

`TestAFailedUpdatePutsBackASecretOnlyTheRunningReleaseDeclares` was red before
the fix: 1.3.0 stops declaring `smtp_password`, the pull fails, the update
reports `compensated`, and 1.2.0's `smtp_password` is not on disk.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

```divergence
decision: RFC 0001 D-19
grade: LOCKED
class: spec-gap
at: 2026-10-19T12:10:32Z
attempt: 1
claim: decision 19 scopes the restore point to updates that reached start-services; one that stopped earlier kept the new release's secrets for the previous release's running containers, pruned of any only the previous release declares
evidence: internal/lifecycle/ops/update.go:949
action: halted
proposal: LOCKED — when an update stops before start-services and the secrets were kept, the restore point renders the previous release's secrets over them and brings nothing up; a failure to render ends at requires-manual-intervention
```

## Ruled 2026-10-19

The author delegated the ruling. Decided as proposed, and recorded as RFC 0001
decision 25. The containers reading that directory are the previous release's,
so its secrets are the only correct contents. The cost is that a failure to
render them turns what would have been a clean compensation into exit 12. That
is the honest outcome: the previous release would otherwise run against
secrets that are not its own, and report rolled back.

## As built

- render-secrets' compensator records `secrets-kept` when it leaves the
  directory. The restore point, when nothing was started, renders the previous
  release's secrets only in that case. When the directory was removed, nothing
  reads it.
- Both restore paths share `renderReleaseSecrets`.
- `TestAFailedUpdateThatCannotPutBackTheSecretsNeedsAHuman` pins the cost: a
  render that fails during compensation ends at requires-manual-intervention.
