# Wave 38 · The report and the record

Executed against branch `wave-38-the-report-and-the-record`. One defect closed:
`saveInstallation`'s write order, carried since wave 31 and the oldest item in
this collection.

**Drift count: 0.** Nothing a document settled was built otherwise. The one
entry that could have been `drift` is not: no RFC ever settled the order these
two records are written in, which is why it survived five waves of being noticed.

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
at: 2026-08-24T11:40:00Z
attempt: 1
claim: saveInstallation wrote the operator-facing report before the state store, so a refused state write left installation.yaml describing a change the manager never recorded
evidence: internal/lifecycle/ops/ops.go:665
action: decided
proposal: ASSUMED — the state store is written first and the report second. Both writes can fail and `doctor` reports the two disagreeing either way, so the order does not decide whether a disagreement is noticed; it decides which of the two files is the wrong one.
```

The argument is not that a failed write should be invisible. It is about which
direction the two records may disagree in.

Report first let it run **ahead** of the record: an installation.yaml describing
an installation that does not exist. State first can only leave it **behind** —
stale, still describing the installation as it actually stands, and reproduced
by the next successful save. One is a fiction, the other is an old fact.

What settles it is that the report travels.
[`hookbackup`](../internal/adapters/backup/hookbackup/backup.go#L503) copies
`installation.yaml` into every backup, so a report written ahead of a refused
state write outlives the failure that produced it and is restored later as
though it were true. A stale report restored is merely old.

`doctor` needed no change, and that is the confirmation rather than a
coincidence. It already tells an operator
[`the recorded state is what runs`](../internal/lifecycle/ops/doctor.go#L298)
and advises `init --repair` to rewrite the file from the state — advice that was
only sound if the state is authoritative, which is exactly what the old order
undermined. The fix makes a sentence the manager was already printing true.

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-08-24T11:45:00Z
attempt: 1
claim: this wave's first planned item, syncing the merged checker into this repository, was already done before the wave began
evidence: `git show --stat ff9d108` — .agents/skills/rfc-writer/scripts/rfc_index.py and skills-lock.json, 2 files changed
action: decided
proposal: none. The item is closed, not carried, and the wave is one item smaller rather than padded to its planned size.
```

It also answered, for free, a question this wave's plan had listed as unresolved:
whether the sync tool notices a change confined to a skill's `scripts/`, given
that `skills-lock.json` records a `skillPath` pointing at `SKILL.md`. It does —
`ff9d108` carries the script and nothing else. The lock's `computedHash` is not
the digest of `SKILL.md`, which is what made the question worth asking.

```divergence
decision: 0001 D-12
grade: UNLISTED
class: spec-gap
at: 2026-08-24T11:52:00Z
attempt: 1
claim: RFC 0001 decision 12 governs whether a plan validates the bundle it plans against, and it carries no grade, so this skill cannot say whether departing from it is a halt or a decision
evidence: `awk '/^##+ *[0-9]*\.? *Decisions/{f=1;next} f&&/^\|/{print; exit}' rfcs/0001-update-and-rollback.md` prints the header `| # | Decision |`
action: decided
proposal: D-055 is not executed in this wave. The grade is the input this practice runs on, and inventing one from inside a task would settle, for every future wave, how a listed-but-ungraded row behaves.
```

**This is the first time the ungraded rows have blocked execution rather than a
gate**, and it is worth separating from the gate argument that carried them.
Wave 37 deferred grading 354 rows because a gate that fails is a gate that gets
switched off — a cost argument. This is different: `flag-dont-flip` keys every
decision it makes on the grade, and its vocabulary covers `LOCKED`, `ASSUMED`,
`OPEN` and *unlisted*. A row that is **listed and ungraded** is none of those,
and the skill does not say what to do with one.

`UNLISTED` is recorded above because the checker's vocabulary offers nothing
nearer, and it is wrong in a way worth stating: the decision is emphatically
listed. It is row 12.

The second question D-055 needs answered is narrower and is the author's too:
**does a plan over a remote reference validate?**
`TestAPlanOverARemoteReferenceDeclinesToWarn` already declines a network pull to
phrase an advisory. Validation is a stronger claim than an advisory, so either a
plan over `oci://` pulls the bundle — a cost nobody asks a plan for — or it
validates local bundles and not remote ones, which is *two answers to one
question decided by which shape the vendor published*: the exact shape D-055
records as having already reappeared twice.

## Rules distilled

- **When two records of one fact are written in sequence, the order decides
  which one can be the fiction.** Ask which of them travels: the one copied into
  backups, shipped in a support bundle or read by another tool is the one that
  must never run ahead of the truth.
- **A fix that requires no change to the diagnostic beside it is usually the
  right one.** `doctor` was already telling operators the recorded state is what
  runs; the code was contradicting the sentence rather than the sentence being
  wrong.
- **An ungraded decision row is not a documentation problem, it is a missing
  input.** A practice that dispatches on a grade cannot execute against a row
  that has none, and discovering that at execution time costs a wave.
- **A sabotage that does not compile is not a kill, and neither is one that
  produces a third behaviour.** Dropping the report write left `header` and
  `data` unused; it had to be made to compile before it proved anything.

## Carried into the next unit

- **D-055 — a plan does not validate the bundle it plans against** (wave 34),
  now with both of its blockers named: RFC 0001 row 12 carries no grade, and
  whether a plan over a remote reference validates is unanswered.
- **The 354 ungraded decision rows across 22 RFCs.** Deferred in wave 37 on a
  cost argument; this wave adds a second and independent one — they are an input
  execution needs, not only a gate that is failing.
- **RFC 0030's four destroyed grades** (wave 37), still the author's to restore.
- **`rfc-index` is not wired into `ci`**, still failing on 27 problems.
- **`FieldRemovalRelease` is a single-member design with no members** (D-052).
  The next field deprecation is what should force its shape.
- **`release.draft: true` means a human publishes**, and that human is the last
  reader of the notes (release 0.3.0).
- ~~**`saveInstallation` writes its report before the state store**~~ (wave 31)
  — closed by this wave, the oldest item in this collection.
- ~~**The upstream checker fix is unmerged**~~ (wave 37) — merged as
  `morzecrew/agent-skills#13` and synced here in `ff9d108`.

## Correction — 2026-08-24, the third entry's claim that row 12 governs

**The third entry says RFC 0001 decision 12 "governs whether a plan validates
the bundle it plans against". It does not, and the entry is wrong.** It is left
standing rather than edited, as this collection's corrections are.

Row 12 settles *where* a plan reads from — the bundle at its source rather than
its release-store destination — and its rationale is about planning against an
unstaged destination reporting every template and hook as missing. It says
nothing about whether a plan validates what it read.

Wave 34 had already drawn that line and this entry contradicted it:
`logs/wave-34.md` classes D-055 `spec-gap` on exactly these grounds — "decision
12 settled that a plan reads the bundle at its source, which wave 32 built; it
did not settle that a plan *validates* what it read, and nothing does."

So the consequence stated in the entry does not follow. Adding validation
contradicts no row, which makes it an **unlisted** decision, and unlisted
licenses `decided`. **Nothing halted.** D-055 was held on one real question, not
two: whether a plan over a remote reference validates.

What survives, smaller and not blocking: 354 rows across 22 RFCs carry no grade,
so if execution ever does want to contradict one of them, this practice has no
answer for it. That is latent rather than live, and the entry above dressed a
latent gap as a live blocker by finding the nearest row and assuming it governed.

**Rule distilled:** before claiming a row blocks you, check that the change
actually contradicts it. A row about the same *area* is not a row about the same
*question*, and an earlier wave's log may already have drawn that line.

## Ruling — 2026-08-24, the question D-055 was actually held on

The author ruled the remaining question, so D-055 is executable and is carried
as ready rather than blocked.

**A plan validates what it already read, and says so when it did not.** A local
bundle is validated — `warnPlannedDeprecations` already fetches it to a temp
directory and loads its manifest, so the read is paid for and the validation is
close to free. A remote reference is **not** fetched, and the plan states that
it could not validate rather than staying silent.

What this buys is that there is one answer to "does a plan validate?" — *it
validates, or it tells you it did not*. The objection D-055 raises is that a
question gets two answers depending on which shape the vendor published; a plan
that names what it could not check has one answer and a stated limit, which is
not the same thing as two answers.

Refused: fetching the bundle behind a remote reference so a plan can validate
it. That would make `--dry-run` — the cheap, safe thing an operator runs first —
a network operation that fails when a registry is down, and
`TestAPlanOverARemoteReferenceDeclinesToWarn` already pins both halves of the
current behaviour: no attempt to reach the registry, and a plan that still
succeeds promptly.

Consequence for whoever executes it: the plan's output changes, and this
project's plan output is parsed — the acceptance and CLI suites assert on
`would create an installation for demo`. The limit is stated by **adding** a
line rather than rewording that one, and by adding a key to the operation's
`Data` map rather than reshaping it.

**Drift count: still 0.** This section records an author's ruling and the
correction above corrects this wave's own prose; neither changes an entry's
class.
