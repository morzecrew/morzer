# Wave 39 · The answer the plan discarded

Executed against branch `wave-39-the-answer-the-plan-discarded`. D-055 closed —
*a plan does not validate the bundle it plans against*, carried since wave 34
and ruled ready in wave 38.

**Drift count: 0.** Nothing a document settled was built otherwise. RFC 0001
decision 12 governs where a plan reads from and is unchanged: the plan still
reads the bundle at its source.

Where building this disagreed with the design for it, written at the moment it
happened. Nothing here is revised afterwards to agree with what was later
settled, and nothing here has been folded back into any RFC's own text. The rows
proposed below are put forward for the author to accept or refuse; execution does
not write them into a decision table itself.

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-08-25T17:40:00Z
attempt: 1
claim: the plan was not failing to validate the bundle; it validated and discarded the answer, because LoadManifest calls Validate and every error in the plan's read path was swallowed by a bare return
evidence: internal/release/load.go:154
action: decided
proposal: ASSUMED — the validation error is propagated rather than added. D-055 named the symptom correctly and the mechanism differently: nothing needed to start validating, and one `return` needed to stop discarding.
```

Four errors were swallowed in a row — ref parse, temp directory, `Fetch`, and
`LoadManifest` — each with a bare `return` from a function whose signature could
not report anything. The last is the one D-055 met.

This is why the item read as larger than it was. *A plan does not validate* and
*a plan validates and throws the answer away* have the same symptom and
different fixes, and only the second is a two-line change.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-08-25T17:52:00Z
attempt: 1
claim: a plan that refuses inside a step reports exit 11 with the reason buried in a compensated operation record, and the plan has nothing started to compensate
evidence: internal/lifecycle/ops/init.go:156
action: decided
proposal: ASSUMED — the plan refuses before the operation is built, so an invalid manifest is exit 2 (`usage`) with nothing started, not exit 11 (`compensated`). `refuseRuntimeOptionChange` already exists for this reason and its comment argues the case.
```

The real `init` still exits **11** for the same bundle, because it discovers the
same thing inside `stepStageRelease`. So the plan and the run now name one cause
with two codes, and the plan's is the more accurate of them. **Carried, not
fixed here:** moving the real path's check before the operation is a change to
what `init` does, not to what a plan says, and D-055 is about the plan.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-08-25T18:05:00Z
attempt: 1
claim: the ruling says a plan states when it could not validate, and does not say what a plan does when it tried to read a local bundle and failed
evidence: `go test ./internal/lifecycle/ops/ -run TestAPlanDoesReachForALocalBundle` — a local reference is materialised through the source, and a source that cannot produce the bundle now returns an error rather than a plan
action: decided
proposal: ASSUMED — a plan declines to look in exactly one case, the remote reference the ruling names, and says so. Everywhere else, failing to look is the operation failing, and a plan that says "would create" there is the defect D-055 reports arriving by a different door.
```

The distinction is between *declining* and *failing*. "Could not validate" is an
honest statement about a deliberate choice; used as a bucket for every read that
broke, it becomes the silence it replaced — a plan that prints its steps whether
anything was checked or not.

## What the operator sees now

A plan over a local bundle validates it and refuses what the run would refuse.
A plan over a remote reference still does not touch the network — that decision
is unchanged and still pinned from both sides — and now says so, on both
surfaces: a line in the output, and `data.release_validated` for what parses it.

Two promises rather than one, because the sentence and the field are different
contracts, and this project has already had a release where blanking the field
while leaving the sentence intact killed no test.

## Rules distilled

- **A symptom names the defect, not the mechanism.** D-055 read as "a plan does
  not validate" for five waves. The plan validated the whole time; a bare
  `return` threw the answer away, and the fix was one line rather than a feature.
- **A function that cannot report anything will be given something to report.**
  `warnPlannedDeprecations` returned nothing, so every error inside it had one
  available ending. The signature chose the behaviour before anybody decided it.
- **Declining to look and failing to look are different, and only one of them is
  a limit worth stating.** Merge them and the statement stops meaning anything,
  because it now covers the case where something is actually wrong.
- **A name that stops being true is a defect in waiting.** `warnPlannedDeprecations`
  now refuses; keeping the name would have left every call site reading as
  advisory. Renamed with the behaviour, in the same commit.

## Carried into the next unit

- **The real `init` refuses a legacy bundle at exit 11, inside a step.** The
  plan now refuses the same bundle at exit 2 with nothing started. One cause,
  two codes; moving the run's check earlier is its own change.
- **The 354 ungraded decision rows across 22 RFCs**, and RFC 0030's four
  destroyed grades (waves 37–38).
- **`rfc-index` is not wired into `ci`**, still failing on 27 problems.
- **`FieldRemovalRelease` is a single-member design with no members** (D-052).
- **`release.draft: true` means a human publishes**, and that human is the last
  reader of the notes (release 0.3.0).
- ~~**A plan does not validate the bundle it plans against**~~ (D-055, wave 34)
  — closed by this wave.

## Found while verifying, not while building — 2026-08-25

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-08-25T18:35:00Z
attempt: 1
claim: `apply --dry-run` prints "demo 1.2.0 applied" in the past tense, directly beneath the line saying nothing was changed, because applySummary never asks whether the operation was a plan
evidence: internal/lifecycle/ops/apply.go:166
action: decided
proposal: none from this wave. Recorded and carried: it is another command's operator-visible summary, and this branch is about what `init --dry-run` validates.
```

**This is the defect an earlier wave already fixed for `init`, still live in
`apply`.** That one read "installation created for" under a plan and was
described then as "a creation claimed in the past tense, directly beneath the
line saying nothing was changed" — which is this, verbatim, one command over.
`init` grew `initVerb` to say "would create" for a plan and "created" for a run;
`applySummary` takes the record and the release and never learns which it was.

Nothing pins it: no test asserts the apply plan's summary, which is why the fix
to the sibling command did not reach it.

**How it was found is the part worth keeping.** It was not in the diff, not in
the tests, and no checker reports it. It appeared in `just demo-plan`'s output,
which this wave ran to satisfy a checklist — and it would have been just as
invisible had that recipe been run and its exit code believed instead of its
output read. The lane was already green.

**Not fixed here, deliberately.** The plan's summary is parsed, and changing a
command's output line is a decision about a published surface rather than a
tidy-up to fold into a branch about something else. It is small — `applySummary`
needs the same argument `initVerb` takes — and it is the author's to schedule.

**Drift count: still 0.** This is a discovery against an earlier wave's fix, not
a departure from anything this wave built.
