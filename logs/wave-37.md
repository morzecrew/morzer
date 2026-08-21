# Wave 37 · The gate and its checker

Executed the `rfc-index` gate, carried out of wave 36 as "not wired into `ci`",
against branch `wave-37-the-gate-and-its-checker`. The author ruled the wave
**B then D**: fix the checker in the repository that owns it, then take whatever
mechanical win was left over. B landed. D turned out to be almost entirely
subsumed by B, and the entries below say so rather than manufacturing work to
fill the shape the plan had.

**Drift count: 1.** The entry classed `drift` is against RFC 0030, whose decision
table lost its grades between 2026-08-15 and 2026-08-17 as its questions were
answered. Found by this wave, against an earlier one.

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
class: spec-gap
at: 2026-08-21T14:48:00Z
attempt: 1
claim: the gate's checker is vendored from another repository, so a fix committed here is reverted by the next routine skills sync and the gate silently un-fixes itself
evidence: `python3 -c "import json;print(json.load(open('skills-lock.json'))['skills']['rfc-writer']['source'])"` prints morzecrew/agent-skills
action: decided
proposal: the two defects are fixed in morzecrew/agent-skills, not here. This repository takes them through a routine skills sync once that lands upstream, and not before — a vendored file edited in place is a fix with a scheduled expiry date.
```

Five `chore: update agent skills` commits already exist in this repository's
history, so the expiry is not hypothetical. The same reasoning is why nothing in
`.agents/` is touched by this branch.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-08-21T14:51:00Z
attempt: 1
claim: the plan named a third upstream change, a grade vocabulary for decisions in an RFC that has already shipped, and nothing in this wave needs it
evidence: `python3 ../agent-skills/skills/rfc-writer/scripts/rfc_index.py check --root . | grep -c "no table with"` prints 22, every one of them an RFC this wave leaves alone
action: decided
proposal: no new grade is invented. The 354 rows such a vocabulary would grade are deferred by the author's own ruling, so inventing the word now would settle, from inside a task, a question that belongs to whoever grades them — and it would be a change to a shared skill made on behalf of a caller that has not arrived.
```

`GRADES` stays `LOCKED, ASSUMED, OPEN`. That the question is real is recorded in
*Carried* below rather than answered here.

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-08-21T15:18:00Z
attempt: 1
claim: the approved plan's D reordered seven decision tables to put Grade in the second column, and after B that reorder changes no verdict at all
evidence: `python3 ../agent-skills/skills/rfc-writer/scripts/rfc_index.py check --root . | grep -c "PROBLEM 002[345678]"` prints 0, against 6 from the vendored checker
action: decided
proposal: RFCs 0023 through 0028 keep their tables exactly as written. Reading the column by its heading rather than its index is what makes the house style legal, and rewriting six tables to satisfy a positional rule would have been churn across documents that other RFCs and every log cite by row number.
```

This is a departure from the approved plan, not from a document. It is recorded
because the plan was approved as two halves and only one of them turned out to
exist: a reader comparing the ruling to the diff would otherwise find five of
D's seven tables untouched and no statement of why.

That the six pass on their merits rather than by being skipped was checked
rather than assumed — a grade in 0023 row 1 replaced with `PROBABLY` is reported
as `decision row '1' has grade 'PROBABLY'`, and the first attempt at that check
proved nothing, because the pattern it substituted assumed backticked grades and
0023 does not backtick them.

```divergence
decision: unlisted
grade: UNLISTED
class: drift
at: 2026-08-21T15:22:00Z
attempt: 1
claim: RFC 0030's decision table was written with a real Grade column, and answering its questions overwrote the grades in place instead of recording the answers beside them
evidence: `git show 6d3752d:rfcs/0030-unit-enablement-is-the-operators.md | grep -c "| OPEN |"` prints 4, and the same grep against the file today prints 0
action: decided
proposal: rows 1, 3, 4 and 5 of RFC 0030 regain a grade, and the answer they now carry moves to a column of its own. Which grade each row takes is the author's call and is not proposed here.
```

This is the failure the append-only rule exists to prevent, and it is worth
naming precisely because nobody did anything careless: each answer was written
carefully, into the only column that looked like it was for answers. Four grades
were destroyed one at a time, by five separate careful edits, and the table
still looks well-maintained — it is the *header* that is now the only evidence
the column ever held a grade.

It also explains why the old checker never reported it. Reading the second cell
positionally, 0030's Grade column at index 2 was invisible, so the RFC failed
with `no table with '#' and 'Grade' columns` — the same message as the 22 RFCs
that genuinely have no grades. A checker that cannot tell a destroyed grade from
an absent one reports the cheaper of the two.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-08-21T15:25:00Z
attempt: 1
claim: wiring rfc-index into just ci in this wave would wire a gate that fails
evidence: `python3 ../agent-skills/skills/rfc-writer/scripts/rfc_index.py check --root .` reports FAIL 30 RFC(s), 30 index row(s), 27 problem(s)
action: decided
proposal: the gate stays unwired. `log-check` was wired in wave 36 because it could go green the day it was wired; a gate committed red is a gate whose first job is to be switched off, and wave 36's own correction records what happens when a check is allowed to fail open instead.
```

## Rules distilled

- **A vendored file is a fix with an expiry date.** Check who owns a file before
  fixing it; a lock manifest naming another repository means the edit survives
  exactly until the next sync, and the gate un-fixes itself with nobody noticing.
- **A positional rule cannot tell a wrong answer from no answer.** Reading the
  grade from column two collapsed "this table grades its rows, elsewhere" and
  "this table has no grades" into one message — and the collapse hid a real
  defect behind 22 copies of a boring one.
- **A column heading outlives the column's contents.** When a table's cells are
  progressively rewritten, the header is the last evidence of what they were for;
  0030's says `Grade` and not one cell holds a grade.
- **A sabotage that assumes the formatting proves nothing.** Substituting
  `` `LOCKED` `` where the file writes `LOCKED` matched no line, and the silent
  no-op read exactly like a passing check.
- **Fixing the checker can be cheaper than reshaping what it checks.** B removed
  six sevenths of D. The measurement that showed it was one command, and it was
  available before either half was attempted.

## Carried into the next unit

- **The 354 ungraded decision rows across 22 RFCs.** Deferred by the author's
  ruling on this wave, not forgotten. Grading them is a unit of its own, and the
  open question inside it is what a grade *means* on a decision in an RFC that
  has already shipped — the vocabulary has `LOCKED`, `ASSUMED` and `OPEN`, and
  all three describe what an executor does on conflict, which is a question a
  Complete RFC no longer poses.
- **RFC 0030's four destroyed grades**, above. Smaller than the 354 and a
  different problem: those rows were never graded, these were.
- **`rfc-index` is still not wired into `ci`**, and now fails on 27 problems
  rather than 32.
- **The upstream fix is unmerged.** Until it lands in `morzecrew/agent-skills`
  and reaches this repository by a sync, the vendored checker here still reports
  the three false positives in 0019 and still fails all six graded RFCs.
- A plan does not validate the bundle it plans against (D-055, wave 34).
- `FieldRemovalRelease` is a single-member design with no members (D-052).
- `saveInstallation` writes its report before the state store (wave 31), the
  oldest item in this collection.
- `release.draft: true` means a human publishes, and that human is the last
  reader of the notes (release 0.3.0).

## Correction — wave 36's count of what this gate costs

Wave 36 carried this gate forward as **"369 decision rows across 23 RFCs carry
no grade"**. Measured again at the head of this wave, the number is **359 across
23** — 354 in the 22 RFCs whose tables carry no Grade column at all, plus RFC
0030's 5, which are a different problem and are counted separately above.

Wave 36's entry is left as written; it is append-only, and this note is the
correction rather than an edit to it. The discrepancy is not explained here
because explaining it would need the intermediate measurement, which was not
recorded — which is itself the reason a counted claim should carry the command
that produced it, as the entries above do.

## Correction — 2026-08-21, the count beneath the first entry

**The paragraph under the first entry says "Five `chore: update agent skills`
commits already exist in this repository's history". Five is wrong.** It is left
standing rather than edited, for the same reason wave 36's correction left its
own paragraph standing: a record quietly adjusted to match what is true now is
worth less than one that shows what was believed when it was written.

Measured: **9** commits in this repository sync vendored skills, **8** of them
touched `.agents/skills/rfc-writer/`, and **4** touched `rfc_index.py` itself.

Where five came from is the part worth keeping. It was read off
`git log --oneline -5 -- .agents/skills/rfc-writer/`, run to see *whether* the
file had ever been re-synced. It had, which was the question, and the `-5` was a
display limit that answered a different question than the one the number was
later used for — the second use needed a count, and the flag capping it was no
longer visible in the answer.

The claim the number was supporting survives the correction and is stronger for
it: the expiry date on a vendored edit is not hypothetical, and `rfc_index.py`
specifically has been overwritten four times.

**Rule distilled:** a number lifted out of a command run for a yes-or-no
question carries that command's limits with it. Re-run it without the cap before
it becomes a count.

**Drift count: still 1.** This correction is against this wave's own prose, not
against a document any RFC settled, and it changes no entry's class.
