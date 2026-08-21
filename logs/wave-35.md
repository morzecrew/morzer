# Wave 35 · What ships from a source tree

> **Migrated from `rfcs/EXECUTION-LOG.md`, verbatim.** These entries predate the
> ```divergence``` block format and are reproduced in the prose form they were
> written in. They are deliberately *not* rewritten to satisfy the current
> schema: this record is append-only, and retrofitting `at` stamps, `attempt`
> numbers and evidence citations that resolve against today's line numbers would
> be editing what was decided to match what a checker wants — the one thing the
> format exists to prevent.
>
> `log_check.py` runs against logs that have a task file in `tasks/`. These have
> none, on purpose.

## Classes

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the RFC was right to be silent |
| `spec-gap` | Could have been known; the RFC was silent or at the wrong altitude | The design process missed something |
| `drift` | The RFC covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

---

Branch `feature/wave-35-what-ships-from-a-source-tree`. RFC 0014, and the two
defects wave 34 found while looking for something else.

Scheduled as its own unit rather than folded into wave 34, which is the ruling
recorded there: the exclusion list, the verifier's symmetry, `.gitignore`, and
what a stricter builder does to already-published bundles are four decisions,
and a security fix reviewed under a debt wave's title is reviewed by nobody.

**Drift count: 1** — D-060, against RFC 0014, pre-existing and shipped in every
release to date.

## D-060 — The RFC never said which files are in a bundle

- **Touches:** RFC 0014 §4, §8; rows 18 and 19 proposed and accepted
- **RFC said:** nothing. It is careful about ordering (rows 2, 16),
  determinism (row 4), version derivation (rows 5–9) and where `build` writes
  (row 10), and nowhere about *what the set being summed is*. The set was
  whatever `filepath.WalkDir` returned.
- **Built:** `domain.IsExcludedFromBundle`, applied at `release.ArchiveEntries`
  — the one enumeration `WriteSums` and `WriteArchive` share — and at the
  verifier's completeness walk and `atomicfs.CopyTree`.
- **Because:** row 10 has `build` write **in place** so a multi-gigabyte
  `images/` layout is not copied, and a bundle is authored in a working copy. The
  two together mean the working copy was the input to the enumeration. Measured:
  **42 of 55 `SHA256SUMS` entries under `.git/`**, `.git/config` among them, and
  the archive packed all of them.
- **Class:** `drift` against RFC 0014 — the design admits a shape it never
  considered, and the code implemented the design faithfully.
- **The shape worth keeping, and it is not "somebody forgot":** the omission is
  invisible from inside the document. Every sentence in it about the bundle is
  about a bundle that already contains the right files, so no section reads as
  incomplete and no review of the RFC would have found it. **A specification can
  be complete about everything it mentions and silent about its own input.** The
  question "what is in it" was never asked, which is different from being
  answered wrongly.
- **Consequence:** the verifier's symmetry is forced rather than chosen, and
  that is the part a fix could most easily have got wrong — excluding on the
  producing side alone makes every vendor's own `verify` fail against the tree
  they just built in, which is a worse failure than the leak because it reaches
  everyone rather than everyone-who-uses-git.

## D-061 — Already-published bundles were checked rather than reasoned about

- **Touches:** D-060; the compatibility question the author raised when ruling
- **The question:** `unlisted` is fail-closed both ways, so does a stricter
  builder make an already-published bundle unverifiable?
- **Answered by measurement, both directions.** A bundle whose sums list
  `.git/config` with the file present **verifies** — the listed side is
  digest-checked as before and the completeness side no longer looks. Strip that
  listed file and it fails with `missing or unreadable`, which is pre-existing
  behaviour and unchanged.
- **Class:** not a departure. Recorded because the plan called this benign from
  a code read, and a compatibility claim that decides whether published
  artefacts keep working is the wrong place to stop at reading. It is also the
  cheapest kind of measurement: two files and a verify.

## D-062 — One manifest reader for both shapes of bundle

- **Touches:** D-054, carried from wave 34; RFC 0014 rows 2 and 19
- **Built:** `release.ManifestAt`, reading an archive's first entry without
  extracting, refusing an archive that does not lead with the manifest.
- **Because:** decision 2 locks `manifest.yaml` first *so that this read is
  possible*. Spending that guarantee costs a few kilobytes of decompression;
  extracting to a temp directory to learn a product name would have cost the
  whole bundle, and refusing archives without `--product` would have made the
  published shape the awkward one.
- **Refuses rather than scans**, following decision 3's reasoning at a second
  reader: a guarantee one reader enforces and another works around is a
  convention, and the lenient reader is the one that admits a non-conforming
  archive to the strict one.
- **Class:** `spec-gap`, closed.
- **Consequence:** the join existed in three places. Two were fixed a wave
  apart; the third was found by grepping for the first two. That is the argument
  for the surface being one function rather than a corrected line.

## Rules distilled

- **A specification can be complete about everything it mentions and silent
  about its own input.** RFC 0014 settled ordering, determinism, version
  derivation and where `build` writes, and never said which files are in a
  bundle. No section reads as incomplete, because every sentence about the
  bundle assumes one that already contains the right files. (D-060)
- **When a producer and a checker walk the same tree, a change to one is a
  change to both.** The exclusion had to be symmetric or every vendor's own
  `verify` would fail against the tree they built in — a worse failure than the
  leak, because it reaches everyone rather than everyone-who-uses-git. (D-060)
- **A compatibility claim about published artefacts is not a code-read claim.**
  "Already-published bundles still verify" was right, and it cost two files and
  one command to know rather than believe. (D-061)
- **Exact names beat patterns where being wrong is silent.** A `*~` rule decides
  the fate of names nobody has looked at, and the direction it fails in is a
  release that is missing a file and still looks complete. (D-060)

## Carried into the next unit

- ~~**The `.git` leak**~~ — closed (D-060).
- ~~**The path-join in `commands.go` and `init_wizard.go`**~~ — closed (D-062).
- **A plan does not validate the bundle it plans against** (D-055, wave 34).
- **`FieldRemovalRelease` is a single-member design with no members** (D-052).
- **`Installation.Providers`** — RFC 0027's question, not gated on P3. Wave 36.
- **`saveInstallation` writes its report before the state store** (wave 31), the
  oldest item in this file.
- **Cutting 0.3.0** — *open until the `v0.3.0` tag exists.* The scaffold stamps
  `min_manager_version: 0.3.0`, so until that release exists `morzer release
  new` writes bundles no released manager can install. The `release/v0.3.0`
  branch prepares the cut and measures what it will fix, both directions: a
  build stamped `0.3.0` plans a freshly scaffolded bundle (`would create an
  installation for demo`), and the same code stamped `0.2.0` refuses it —
  *requires morzer 0.3.0 or newer, and this is 0.2.0*. Strike it when the tag
  is cut, not when this branch merges: what the item is about is a released
  manager existing, and a branch that prepares one is not one. Never a defect
  in the code, only a consequence of the tag not being cut.

## Reconciliation — 2026-08-19

| RFC | row | outcome | grade | decision | from |
|---|---|---|---|---|---|
| 0014 | 18 | **Accepted** | `LOCKED` | A bundle source tree is not what ships from it; the exclusion is symmetric across producer and verifier | D-060 |
| 0014 | 19 | **Accepted** | `LOCKED` | The manifest is read from an archive's first entry and an archive that does not lead with it is refused rather than searched | D-062 |

**RFC 0014's decision table carries no grade column**, unlike 0023's. The grades
above are this file's reading of how the two rows should be treated on a later
conflict, not a quotation from the document — which is worth saying, because a
reader checking the row against the RFC will not find the word there. Whether
that table should grow grades is 0014's question and not this wave's.

## Self-audit — 2026-08-19

Scope: the whole branch — one predicate, three walks that had to agree, one new
archive reader, the RFC amendment and two changelog entries. `just ci` green at
**86.6%** (floor 84), acceptance passed, container lane passed
(`./test/suite` 187.2s).

**Sabotage sweep: 6 mutations, 6 killed** — one of them only after being made
compilable, which is not a kill until it is.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Major | The producer and the verifier walk the same tree, so excluding on one side alone fails **every vendor's own `verify`** against the working copy they just built in. Caught in the plan rather than in code, and it is the failure this change could most easily have shipped. | Designed against — one predicate, both walks, and a test that builds sums inside a `.git` tree and verifies in place |
| A-2 | Medium | Already-published bundles: `unlisted` is fail-closed both ways, so a stricter builder could have made them unverifiable. | Measured both directions rather than reasoned about — D-061 |
| A-3 | Low | `ArchiveEntries` now descends into no excluded directory at all (`fs.SkipDir`) rather than filtering per file. Deliberate: an object store on a busy repository is both large and **being rewritten under the walk**, which is how the first sighting of this leak arrived — as a lock file that existed when the walk saw it and not when the open came. | By design, recorded here because a reader could mistake it for an optimisation |
| A-4 | Low | `CopyTree`'s new comment claimed "the one caller", and there is a contract-test caller too. | Fixed — "one production caller" |
| A-5 | Low | The reconciliation table grades rows 18 and 19, and **RFC 0014's table has no grade column**. A reader checking the row against the document will not find the word there. | Fixed — the grade is named as this file's reading, and whether 0014 should grow grades is left to 0014 |

**What this wave did not find is worth as much as what it did.** Wave 34's
findings all arrived while chasing something else; this one was scoped to two
known defects and turned up no third. That is the difference between a unit that
follows a carried list and one that follows a lane going red, and it is an
argument for keeping both kinds in the rotation rather than only the second.

## D-063 — A wizard test wrote a private key into the source tree

- **Touches:** `internal/cli/wizard_run_test.go`; found by the author asking why
  a stray file kept coming back
- **Found:** `internal/cli/3`, mode 0400, containing a real age identity —
  `AGE-SECRET-KEY-…` under a `# created by morzer` header, a fresh one each time.
- **Mechanism, confirmed by probe rather than inferred:** the wizard tests feed
  answers positionally and **the number of questions is not fixed** — the
  profile question only appears when the bundle declares profiles. With
  `profilesFrom` returning empty, that question is skipped, every later answer
  shifts up one, and the `"3"` intended as the recovery *menu choice* becomes
  the answer to *"Where to write the recovery key"*. `go test` runs with the
  working directory set to the package, so `GenerateIdentity("3")` wrote into
  the source tree.
- **It was a real symptom before it was an artefact.** The first appearance was
  during the window in wave 34 when `profilesFrom` genuinely returned empty for
  every bundle (D-053). Afterwards it recurred only under mutations that empty
  the profile list — which is to say the sabotage sweeps kept re-minting it.
- **Class:** `drift`, and against a rule this file's own subject already knew.
  One test in that file asserted its key landed under its own `HOME`, with the
  comment *"an earlier draft of this file wrote a real age private key into the
  repository, which is a thing a test must never be able to do"*. **The rule was
  right, written down, and applied to the one test it was noticed in.** The next
  test that could do it was not covered. Third instance of that shape in two
  waves (D-054, D-058).
- **Built:** the isolation moved into `wizardApp`, so every wizard test gets a
  temporary `HOME` and a `t.Cleanup` that fails if the test created a file in
  the package directory. It reports and leaves the file rather than deleting it:
  a test that writes a key into the source tree has already done the thing worth
  knowing about, and tidying the evidence away lets the next one repeat it.
- **A limitation of the guard, found by verifying it:** it is a before/after
  diff, so a **pre-existing** stray masks a recurrence of that same name. Worth
  knowing rather than worth solving — the answer to a dirty tree is to clean it.
- **Also fixed:** the HOME isolation had two owners. One test set its own and
  was silently overridden once `wizardApp` set one; it now reads the harness's.

## D-064 — The commit that fixed a key leak committed a key

- **Touches:** this wave's own first commit
- **What happened:** `internal/cli/3` was staged by `git add internal/` and
  landed in *"🔒 fix(release): a source tree is not what ships from it"*. It sat
  in all four commits of the branch.
- **How it was missed:** it had been reported and tracked as *untracked* for
  several turns, and the check that would have caught the change —
  `git status --short` — was read as showing the file just edited. A `"??"` became
  a `" M"` and nothing looked at it again.
- **Class:** `drift`. Over-broad staging is the specific failure this project
  has now made three times, twice with `git add -A` and once with a directory.
  **The mitigation that exists is a habit, and a habit is what failed.**
- **Consequence:** the branch had never been pushed, so nothing left the machine,
  and the key was a throwaway that had encrypted nothing. Removed from all four
  commits with `filter-branch`, and the refs that still held the old history —
  `refs/original`, a safety branch, and **a stash whose parent was the old tip** —
  removed with it. That last one is the part worth remembering: purging history
  and leaving a stash pointing into it purges nothing, and the stash was
  invisible to `git stash list` because the reflog had already been expired.
  Verified afterwards: zero commits and zero objects naming the path.
- **Not covered by D-063's guard.** That guard stops a *test* writing into the
  source tree; it has nothing to say about what gets staged. Two failures, one
  fixed.

## Review round, PR #61 — appended after the group closed

## D-065 — The exclusion reached what a bundle contains, not what it hashes

- **Touches:** D-060, this wave; `internal/infra/atomicfs/copy.go`
- **Found in review** (CodeAnt), and confirmed by measurement before agreeing:
  a bundle built inside a working copy loaded with digest
  `sha256:88b5821a…` and the release extracted from its own archive loaded with
  `sha256:ded8194d…`. **The same release, two identities.**
- **Because:** `Release.Digest` comes from `atomicfs.DigestTree`, a third walk
  of the same tree that the exclusion did not reach. That digest is what `fetch`
  pins against, what `update` compares, and what an attestation records — so the
  divergence surfaces as a bundle refusing itself.
- **Class:** `drift` against this wave. The plan named "one choke point" and
  found two walks that had to agree; there were three. The map was built by
  grepping for the *enumeration* (`ArchiveEntries`, `unlisted`) and a digest is
  not an enumeration, so it was not in the search that produced the plan.
- **The rule, and it is a sharpening of D-058's rather than a new one:** *the
  grep that matters is for every consumer of the thing being changed, not every
  caller of the function being changed.* D-058 was a field with three readers;
  this is a directory tree with three walkers. Both times the missing one was
  the one that did something different with it.

## D-066 — Two callers of one predicate disagreed about what they hand it

- **Touches:** D-060; `unlisted`
- **Found in review:** `IsExcludedFromBundle` reads slash-separated paths.
  `ArchiveEntries` converts with `filepath.ToSlash`; the verifier passed
  `filepath.Rel`'s output straight through.
- **Not reachable today** — the separator only differs on Windows, which this
  project does not target. Fixed anyway, and recorded as the cheaper half of a
  real point: a predicate with a documented input shape and two callers, one of
  which honours it, is a defect whether or not the platform that exposes it is
  supported.
- **Class:** `spec-gap`.

## D-067 — Three smaller findings, all valid

- **A directory named like an archive was opened as one.** `ManifestAt` chose by
  suffix, and `--release` takes an arbitrary path, so a bundle directory called
  `demo-1.2.0.tar.zst` failed before staging. It now asks what the path *is*
  before what it is *named*. `spec-gap`.
- **The source-tree guard was shallow.** `os.ReadDir(".")` records only top-level
  entries, so a key written below an existing directory — `hooks/3`, anything
  with a slash — left the entry set unchanged and the guard reported nothing.
  Now a walk. `drift` against D-063, one commit old.
- **A markdownlint MD038** in D-064's own text: a code span opening with a space.
  Fixed.

**The guard had no test, which is how it shipped shallow.** A test harness is
the code least likely to be tested and the code every other test trusts. The
walk is now a function taking a root, and there is a test that it records a
nested path — sabotaged shallow, and it fails.

## D-068 — The verifier filtered without pruning, and kept the race

- **Touches:** D-060, D-065; found in review, in the **review body** rather than
  on a thread — CodeRabbit could not anchor it because it fell outside the diff.
- **Found:** `unlisted` returned early for directories *before* computing the
  relative path, so it descended into `.git` and excluded per file. The three
  other walks over a bundle — `ArchiveEntries`, `CopyTree`, `DigestTree` — all
  prune with `fs.SkipDir`.
- **Why it matters beyond speed:** an object store is being **rewritten while a
  verify runs**. The first sighting of this whole class was a build failing on a
  git lock file that existed when the walk saw it and was gone before the open.
  A verifier that enters `.git` at all keeps that race, however carefully it
  filters afterwards — so the fix for the leak would have left the symptom that
  found the leak.
- **Class:** `drift` against this wave. Four walks, and the exclusion was applied
  to the fourth with the wrong shape after being applied correctly to three.
- **Consequence:** tested through an unreadable directory rather than by
  counting entries, because "did not report its files" and "did not enter it"
  are different claims and only the second is the one that matters.

**Three of this round's six findings are the same mistake at different
distances**: the exclusion reached the enumerations (D-060), missed the digest
(D-065), and reached the verifier in a form that filtered without pruning
(D-068). The generalisation is not "grep harder" — it is that **a rule about a
tree has to be applied at every walk of that tree, and walks do not look alike**:
one enumerates, one hashes, one copies, one audits. Grepping for any of their
names finds none of the others.

## D-069 — The refusal branches of a new reader, and one that cannot fire

- **Touches:** D-062; codecov reported the patch at 75.7%, with
  `ReadFirstArchiveEntry` at 46.9%
- **Class:** `spec-gap`, closed. Codecov is informational on this repository —
  the gate is `COVERAGE_FLOOR` and it was green at 86.6% — so nothing was
  failing. Covered anyway, because the uncovered lines were **all refusals**:
  the branches that only run when the input is malformed, which is the code a
  passing bundle never reaches and a truncated download always does.
- **Found while writing them:** the branch that reports *"not a valid zstd
  archive"* **cannot fire for a file that is not one**. `zstd.NewReader` is lazy;
  it accepts the file, and the magic-number mismatch surfaces at the first read
  as `cannot read <path>` with the truncation hint. The test asserts the refusal
  a vendor actually meets rather than the one the code appears to offer.
- **The same shape exists in `ExtractTarZst`**, which this reader was modelled
  on, and is pre-existing there. Not changed: the branch is defensive, costs
  nothing, and removing it is a behaviour question rather than a cleanup. Named
  so the next reader does not take its message as evidence of what happens.
- `ReadFirstArchiveEntry` is at **84.6%**; the remainder is that branch.
