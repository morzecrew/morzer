# facts-refresh · #353, facts refreshed on a schedule

On branch `facts-refresh` from `main` at `d1a18f59`, fixing
[#353](https://github.com/morzecrew/morzer/issues/353): a machine runs its
release's facts task only after an operation that succeeded
([RFC 0057](../rfcs/0057-desired-state-an-installation-pulls-and-applies-to-itself.md)
decision 31), so facts that change on their own, such as a canary's verdict on
the relay it watches, never reach the authority. No design document covers it.
The author ruled on 2026-11-13 and 2026-11-14, in the sessions that took the issue, and the
build follows that ruling; the entries below propose RFC 0057 rows.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

```divergence
decision: 31
grade: ASSUMED
class: spec-gap
at: 2026-11-14T09:55:33Z
attempt: 1
claim: Decision 31 keeps the facts the task last printed after an operation, and every row publish writes that pair. A release whose facts change while the installation does not, such as a canary's, needs the task run again without an operation. Ruled by the author: a release opts in under `published`, and the machine then runs the task before every `fleet publish`, the hourly timer's and an operator's, and not before a sync's. The run gives way to a held deployment lock (decision 56) and records `facts_problem` on failure, as a run after an operation does, and is journaled and attested as every facts run is.
evidence: internal/lifecycle/ops/facts.go:35-41
action: departed
proposal: ASSUMED — a release may declare `published.refresh: publish`; the facts task then also runs before every `fleet publish`, and is journaled and attested as a facts run after an operation is; a sync's publish runs it only after an operation, as before
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-14T09:55:33Z
attempt: 1
claim: The ruling has the field's name and form settled in the build. `refresh` under `published`, a string with the one value `publish`, absent meaning after operations only: an enum leaves room for another trigger without a second field, and `hourly`, the issue's word, would be untrue on a machine whose fleet timer runs on another schedule. The field needs `compatibility.min_manager_version` of at least 0.17.0, because an older manager decodes the manifest strictly and refuses it, as `published` needed 0.16.0.
evidence: internal/domain/manifest.go:783-791
action: decided
proposal: UNLISTED — `published.refresh` takes `publish` alone, and a release declaring it needs a manager floor of 0.17.0
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-14T09:55:33Z
attempt: 1
claim: The ruling writes a new facts pair only when its digest changed, so an unchanged canary writes nothing new. The digest the record keeps is of the age ciphertext, and age encryption is randomised, so re-sealing the same output always changes it. The kept pair now records a digest of the task's output too, and a run whose output and recipient are unchanged keeps the pair it has. That digest lives in the kept facts file on this machine, which is not versioned state, and never in the row.
evidence: internal/lifecycle/ops/facts.go:191-198
action: decided
proposal: UNLISTED — a facts run whose output equals the kept facts' keeps them: the kept pair records a sha256 of the output, and the row's facts_digest moves only when the facts do
```

## Review

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-14T10:37:07Z
attempt: 1
claim: The fleet unit gives a publish 300 seconds, and a facts task is allowed its declared timeout, ten minutes by default, so a slow one would leave every hourly row unpublished. The run a publish makes first bounds its task at three minutes, and its record gives way to a lock another operation holds rather than waiting, as the task's own run already gave way (decision 56): the row goes out with the facts it has, and the next run tries again. Found in review of #359.
evidence: internal/adapters/supervisor/systemd/systemd.go:743
action: decided
proposal: UNLISTED — the facts run before a publish is bounded at three minutes and does not wait for the deployment lock to record, so the publish fits inside the fleet unit's timeout
```
