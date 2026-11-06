# resume-keeps-credit · RFC 0051 P0, credit across repeated resumes

On branch `engine-resume-keeps-credit` from `main` at `cd0e4f35`, building
[RFC 0051](../rfcs/0051-resuming-an-interrupted-restore.md) P0: decision 15,
as ruled by the author on 2026-11-06.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/lifecycle/engine/engine.go`, `resumePoint`: carry forward the
  record of every completed step, succeeded or skipped, wherever it sits,
  not only those before the resume point (decision 15).
- `internal/lifecycle/engine/engine.go`, the run loop: on a resume, skip a
  completed step that is not idempotent wherever it sits, not only before
  the resume point (decision 15). A completed idempotent step still runs
  again, as today.
- `internal/lifecycle/engine/engine_test.go`: the sequence §5.2 names, a
  resume interrupted while running an earlier idempotent step again, red
  before the fix.
- The changelog, under Fixed.

Decisions the plan needs that the RFC does not settle: none load-bearing. A
step credited after the resume point is not compensated when this run
fails, as a step credited before it is not today: compensation unwinds what
this run did.

From #284's review.

Decision 15 skips a completed step "that is not idempotent", read off the
current build alone. A manager upgrade that reclassified a step as
idempotent would then run again a completed effect the journal recorded as
not safe to repeat, and `main` already did so before the resume point.
`resumePoint` asks both sides of the step it resumes; the skip now does too.
Every journal carries the declaration: `StepRecord.Idempotent` is in
v0.1.0.

```divergence
decision: 15
grade: ASSUMED
class: spec-gap
at: 2026-11-06T07:58:00Z
attempt: 2
claim: a completed step journaled as not idempotent ran again on a resume because the current build declares it idempotent
evidence: internal/lifecycle/engine/engine.go:166-172
action: departed
proposal: ASSUMED — refines decision 15. A resume skips a completed step unless it is idempotent both in the code and in the journalled record, as resumePoint already asks of the step it resumes.
```
