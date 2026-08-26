# Wave 40 · What a plan says it did

Executed against branch `wave-40-what-a-plan-says-it-did`. Carried out of wave
39, where it was found in `just demo-plan`'s output rather than in a diff.

**Drift count: 0.** Nothing a document settled was built otherwise. No RFC
settles the tense a summary speaks in; `init` established the practice and this
extends it to the two commands that share its shape.

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
at: 2026-08-26T13:40:00Z
attempt: 1
claim: the defect carried into this wave as one command's was two commands', because `update` shares `apply`'s shape — one summary function called on both paths that never learns which one it is on
evidence: `go test ./test/clitest/ -run TestProbeApplyPlanOutput -v` printed `demo 1.2.0 applied` for a planned apply and `updated demo from 1.2.0 to 1.2.0` for a planned update, both under `this is a plan; nothing was changed`
action: decided
proposal: ASSUMED — both summaries take the operation's `DryRun` and speak in the tense of what happened. Fixing only the command the defect was reported in is what left it live here in the first place.
```

The finding was carried as *the `apply` summary*. It was checked before it was
fixed, and `update` had it too — measured rather than assumed, from a probe that
printed what each plan actually says.

Every other summary in this package already distinguishes: `config` takes a
`dryRun`, and `settings`, `recovery` and `backup` build the two sentences at
separate call sites. **These two were the outliers**, and the reason they stayed
outliers is the next entry.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-08-26T13:52:00Z
attempt: 1
claim: nothing anywhere asserted either summary string — no test, no acceptance script, no reference page — so the sibling fix in `init` could not have been caught not reaching them
evidence: `grep -rn "is already applied" --include="*.go" --include="*.md" --include="*.sh" .` returns one hit, the line that produces it
action: decided
proposal: ASSUMED — both sentences are pinned, in both tenses, at both levels. An operator-facing sentence with no assertion behind it is not a contract, it is a coincidence that has held so far.
```

**Both tenses, deliberately.** A summary that said "would" everywhere satisfies
every plan test while describing finished work as hypothetical, and that is not
a hypothetical failure mode: it was sabotaged, the whole `clitest` suite passed,
and only the unit tests caught it.

That is also why the tests sit at two levels rather than one. The end-to-end
tests assert what an operator reads; they cannot reach the past-tense half,
because a real `apply` in `clitest` exits 11 for want of containers. The unit
tests reach every branch and read nothing an operator sees. Neither is
sufficient, and the sabotage sweep is what demonstrated it rather than the
argument.

## Rules distilled

- **Fix the class, not the instance.** This defect was fixed once in `init` and
  survived two more waves in two more commands. The carried item named one of
  them; checking before fixing found the other, and the check was one probe.
- **A sentence nobody asserts is not a contract.** Both summaries were free to
  drift because nothing anywhere read them — which is also why the earlier fix
  could not have been caught stopping short.
- **Assert the negative and the positive of a tense.** "Says would" and "does
  not say did" are one half; without the other, a summary that never speaks in
  the past tense passes everything.
- **Two levels of test, and the sweep tells you why.** Sabotaging the plan
  branch failed both levels; sabotaging the *tense itself* passed the whole
  end-to-end suite. A defect visible only to the level you did not write is the
  argument for writing both.

## Carried into the next unit

- **Whether a plan should verify signatures** (wave 39). It makes no changes, so
  `--dry-run` permits it; it is new work rather than what the plan already read.
- **The real `init` refuses a legacy bundle at exit 11, inside a step**, where
  the plan now refuses the same bundle at exit 2 with nothing started (wave 39).
- **The 354 ungraded decision rows across 22 RFCs**, and RFC 0030's four
  destroyed grades (waves 37–38).
- **`rfc-index` is not wired into `ci`**, still failing on 27 problems.
- **`FieldRemovalRelease` is a single-member design with no members** (D-052).
- **`release.draft: true` means a human publishes**, and that human is the last
  reader of the notes (release 0.3.0).
- ~~**`apply --dry-run` claims an operation in the past tense**~~ (wave 39) —
  closed by this wave, in `update` as well.

## Second attempt — 2026-08-26, the axis this wave was not looking along

Reviewed on #70. One finding, valid, and it is the same defect this wave exists
to fix rotated ninety degrees: a summary asserting something that did not happen.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-08-26T14:26:00Z
attempt: 2
claim: applySummary asked what the steps did and never what the operation did, so an apply that failed and rolled back still printed a sentence saying it applied
evidence: `go test ./test/clitest/ -run TestProbeFailedApplySummary -v` printed, in order, `failed in 0s; earlier changes were rolled back`, then `demo 1.2.0 applied`, then `error: apply failed at step "pull-images"`
action: decided
proposal: ASSUMED — an operation that did not succeed gets no summary, which is what `updateSummary` has always done. The pair existed with the guard on one side only.
```

**This wave corrected the tense and walked past the mood.** The entries above are
about a plan describing work as finished; this is a *failure* described as
finished, printed between the rollback notice and the error explaining it. Both
are `applySummary` answering a question nobody asked it — what happened to the
steps — in place of the one that matters, what happened to the operation.

It was pre-existing and it was in the function this branch already had open,
which is the only reason it is fixed here rather than carried.

**The risk in the fix was the plan path, and it was checked rather than
reasoned about.** A guard on `StatusSucceeded` suppresses every summary if a
plan's record is anything else — which would have silently deleted the sentences
this wave was written to add. A plan's record *is* succeeded, and there is now a
test saying so, because that fact is load-bearing for two features at once and
was previously written down nowhere.

Sabotaged both ways: removing the guard restores the failed-apply sentence,
and widening it to suppress everything kills four tests including the plan ones.

**Drift count: still 0.** A gap in what this wave built, found before it merged,
contradicting no document.
