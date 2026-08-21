# Wave 34 · The lane and the clock

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

Branch `feature/wave-34-the-lane-and-the-clock`. No RFC phase: this unit is the
carried list at the tail of wave 33, which no wave owned.

Scheduled ahead of RFC 0023 P3 deliberately, and the argument is the lane rather
than tidiness. P4 adds a second runtime's acceptance stage to a container lane
that has gone red in three of the last five waves; a red run there would then be
unattributable between the new adapter and the old fragility, and that ambiguity
costs more to resolve than the fixes cost to write.

**Drift count: 5** — D-048, D-052 and D-054 against wave 32; D-056 against
RFC 0014, pre-existing and shipped; D-057 against this wave's own first fix.

Written as 1 when the group was opened and corrected when it stopped being true,
which is the whole of D-046's rule applied to the number that measures this
practice. Four of the five were found while chasing something else — two CI
failures that both read as flakes, a fixture migration, and a test written for
the removal — and none was in the diff. That is the argument for the wave: a
unit scoped to a carried list found more than the list contained, because the
list was the part somebody had already noticed.

## D-047 — A plan's steps say `planned`, not `pending`

- **Touches:** no decision row in any RFC; the step-status vocabulary is
  internal and no document settles it. Carried from wave 32.
- **RFC said:** nothing.
- **Built:** `domain.StepPlanned`, written by `Engine.plan()`, and an explicit
  case in `FirstIncompleteStep` that refuses to resume from it.
- **Because:** `pending` means a step has not run *yet*. A plan's steps were
  never going to run, so the record was the one document in the system still
  claiming work was owed — beneath an operation status of `succeeded`, which is
  correct and stays, because planning is what succeeded. Changing the operation
  status instead would have erased the distinction between a plan that worked
  and a plan that failed validation.
- **Class:** `spec-gap`.
- **Consequence:** the value is its own rather than a reuse of `pending`, and
  that pays twice. `FirstIncompleteStep` treats `pending` as resumable, so a
  record whose steps are all pending reads as resumable from step 0 — a plan
  offered to `--resume` would run every step while reporting that it continued
  something. Unreachable today, because the engine returns from `plan()` before
  journaling and no record on disk can carry the status; now refused by
  construction rather than by that accident continuing to hold.
- **Deliberately not applied:** the plan already knows which steps will not run
  (`WillRun`, from each step's `Check`), and that could have been folded into
  the record as `StepSkipped`. It was not: `skipped` in a journaled record means
  a check reported the postcondition already held *during a run*, and reusing it
  for a prediction would put a claim about what happened into a document about
  what would. The event carries `WillRun` and `Reason` for the reader who wants
  it.
- **Proposed row:** none. A status vocabulary that no RFC settles does not
  become a decision table entry because one value was added to it; the argument
  for the value belongs where the value is, and it is in the doc comment.

## D-048 — The deferral rested on a premise that was false, and checkable

- **Touches:** wave 32's carried item, and its stated reason
- **Wave 32 said:** the dry-run status is *"a machine-readable field RFC 0026's
  read model may consume, so changing it is a design question rather than a
  bugfix"*, and left it.
- **Found:** 0026's read model cannot reach it. `Engine.Run` returns from
  `plan()` before `e.journal()` — under a comment that says so in as many words,
  *"a dry run plans and prints; it must not touch the journal"* — and
  `fleetLastOperation` sources the row from `State.LastOperation`, the journal.
  No dry-run record has ever been journaled, so no read model can consume one.
- **Class:** `drift` against wave 32. Not a wrong fix, a wrong reason: the
  premise was two file reads away in code that wave had open, and deferring on
  it converted a contained defect into a standing design question that was
  carried into two subsequent waves' *Carried* lists.
- **Consequence:** the fix was smaller than the deferral implied, which is the
  general shape worth noticing. **A deferral is a claim, and it is the kind
  nothing later re-examines** — a fix gets reviewed, a `Carried` bullet gets
  copied forward. This is the same failure as RFC 0023 §12 item 5, where "a host
  is needed for this" sat on the list defining what was unknown and went
  unattacked for four days; here it was "a consumer may read this", and it sat
  for two waves.

## D-049 — The flake was not a settle window, and the first diagnosis was wrong

- **Touches:** the fragility carried since wave 29; waves 29, 32 and 33
- **Planned:** the wave-34 plan named the *second* poll loop as the suspect —
  that the test waits up to 30s for Docker to stop accepting on the published
  port, and that teardown under load exceeds it.
- **Measured, and refuted:** teardown is **105ms idle and 119–133ms under CPU
  saturation** (24 spinners on 16 cores, five runs each). It is not the cause,
  and no amount of widening that window would have fixed anything.
- **Built:** `dockerlab.WaitGone`, and the test asks the container whether it
  stopped before asking the port.
- **Because:** the real cause is an ambiguity, not a window — the test had two
  windows already. `redis-cli shutdown` drops its own connection as the server
  goes down, so a non-zero exit is the ordinary outcome; the test therefore
  discarded the error, and in doing so made a `docker exec` that never reached
  the container indistinguishable from a shutdown that worked. When the exec
  missed, the test spent its whole 30s deadline watching a perfectly healthy
  Redis and then reported *"a stopped service was still reported healthy"*.
- **Reproduced:** by making the shutdown miss — **31.3s and those exact
  messages**, against the **30.8s** wave 32 recorded from the real failure. The
  duration is what identifies it: 30s is the deadline, and a test that fails
  because a service is genuinely unhealthy fails in under a second.
- **Class:** `discovery`.
- **Consequence:** the poll is still there and still 30s, because the port
  mapping does outlive the container. What changed is what a failure names. It
  had been reporting the prober for a fault in the fixture, which is why three
  waves each looked at it, found nothing wrong with health probing, and carried
  it. **An assertion that cannot tell which of two things failed will name the
  wrong one, and it will name the one under test** — that is what made this
  cost three waves rather than one.

## D-050 — `assert_running` counts before Docker has finished

- **Touches:** the fragility carried since wave 28
- **Built:** a 30s settle window inside the helper, at all seven call sites.
- **Because:** `docker compose stop` returns when it has *asked*; a container is
  reported running until its process is actually gone. The helper sampled once,
  immediately.
- **Class:** `spec-gap`.
- **Consequence:** the assertion is not weakened — a wrong count still fails, it
  merely has to still be wrong thirty seconds later. What it stops reporting is
  a fact about timing dressed as a fact about the deployment.

## D-051 — A plan over a remote reference will not warn, and that is now settled

- **Touches:** D-035, carried from wave 32
- **Put to the author, and refused:** whether `init --dry-run` should fetch an
  `oci://` or `https://` bundle so it can phrase a deprecation warning.
- **Because:** wave 32's reason holds and was never a compatibility argument —
  a plan is the cheap, side-effect-free path, and making it pull a remote
  artifact to phrase an advisory inverts what it is for. The measured absence of
  users, which decided the other two questions this wave, does not reach this
  one.
- **Class:** not a departure. Recorded so the item stops being carried.
- **Consequence:** closed as won't-fix rather than left open. Three waves of
  *Carried* lists is long enough for a bullet nobody intends to act on, and an
  item carried indefinitely is indistinguishable from one nobody has read.

## D-052 — `runtime:` stops being read in 0.3.0, and the grace period never existed

- **Touches:** RFC 0023 decision 18 (`LOCKED`), superseded by row 23
- **RFC said:** 0.4.0, so that 0.3.0 would be a release in which both spellings
  worked and a vendor could publish one bundle across the upgrade.
- **Found:** there is no such release and never was. `git show
  v0.2.0:internal/domain/manifest.go` has no `Runtimes` field — `runtimes:`
  ships for the first time in 0.3.0 — so 0.1.0 through 0.2.0 read only the old
  block and 0.3.0 reads only the new one. **No version reads both.** The window
  row 18 was buying did not exist when row 18 was written.
- **Built:** the removal, in 0.3.0, and put to the author as a *withdrawal*
  rather than a reschedule. Ruled on and accepted the same day.
- **Class:** `drift` against wave 32, and specifically against row 18's
  reasoning rather than its date. The row got the important half right — a
  deprecation without a clock is a word in a document — and then assumed the
  half it did not check.
- **Consequence:** priced rather than asserted. 11 amd64 downloads and 0 arm64
  across three releases, read from the release assets on 2026-08-19, most of
  them this project's own installer validation. The cost of the break is
  therefore near zero and the cost of carrying two spellings was not.
- **Also:** the author's ruling was "keep the mechanism, delete only the member",
  and the mechanism kept a global `FieldRemovalRelease` that only ever worked
  because there was exactly one member. Two fields deprecated in different
  releases cannot share it. **Left as it is deliberately** and recorded here
  instead: the shape to choose is the next deprecation's to force, and building
  it now would be a mechanism designed for a caller that has not arrived — which
  is the thing RFC 0015 and 0021 were both findings about.

## D-053 — Every fixture in the tree was written in the spelling being removed

- **Touches:** the whole test suite; RFC 0023 row 23
- **Found:** all three `testdata/*/manifest.yaml` bundles and every Go manifest
  fixture used `runtime:`. Nine packages went red on the removal.
- **Class:** `discovery`, and the most useful thing this wave measured. The
  fixtures being on the old spelling is the same fact as no released manager
  reading the new one, seen from inside the repository — the project had shipped
  `runtimes:` in wave 32 and was still testing almost everything through the
  block it had deprecated.
- **Consequence, and it is not bookkeeping:** two tests were passing for the
  wrong reason and one of them was hiding a live defect. `profilesFrom` read
  `manifest.Runtime.Profiles` directly, so from the moment `runtimes:` existed
  the `init` wizard offered **no profiles at all** to any bundle written in it —
  silently, because an empty list is also what a release with no profiles looks
  like. `TestProfilesComeFromTheBundle` passed throughout, because its fixture
  was written the old way. Migrating the fixture failed the test immediately;
  the fix is in the same wave.
- **The rule underneath it:** *a fixture written in the deprecated form tests
  the deprecated path.* A project that deprecates a surface and leaves its
  fixtures on it has not started migrating, it has only announced one — and its
  suite is measuring the path it intends to delete.

## D-054 — The path-join bug was fixed in one place and left in two

- **Touches:** wave 32's D-034; `internal/cli/commands.go`,
  `internal/cli/init_wizard.go`
- **Wave 32 found and fixed:** `warnPlannedDeprecations` joined `manifest.yaml`
  onto `--release`, which for an archive produces
  `demo.tar.zst/manifest.yaml` — a path that does not exist.
- **Found here:** the same join, unchanged, at `commands.go:62` and
  `init_wizard.go:293`. Byte-identical in `v0.2.0`, so it is shipped.
  Measured: **`morzer init --release <bundle>.tar.zst` without `--product`
  fails with `cannot read <archive>/manifest.yaml`** — on a valid archive, for
  both a plan and a real install. The archive is the shape a vendor publishes,
  so this is the primary install path.
- **Class:** `drift` against wave 32. Not for fixing the instance it found —
  that was right — but for not grepping for the others, which is **the rule this
  file distilled one wave later** as D-046: *a claim that changes is a claim to
  grep for.* The same failure, one wave apart, in the opposite direction: D-046
  was a number restated in prose, this is a mechanism restated in code.
- **Consequence:** not fixed here. It is a third defect found by a wave that was
  scoped to a carried list, and fixing it inside this one would make the wave
  unreviewable. Carried, named, and measured, which is the most a wave that
  cannot afford it should do.

## D-055 — A plan does not validate the bundle it plans against

- **Touches:** RFC 0001 decision 12; found while writing the removal's tests
- **Found:** `init --dry-run --product demo --release <legacy bundle>` reports
  *"would create an installation"* for a release the very next command refuses.
  The plan is refused only when `--product` is **absent** — because the CLI then
  has to read the manifest to learn the name, and validation comes with the
  read. An incidental mechanism, not a check.
- **Class:** `spec-gap`. RFC 0001 decision 12 settled that a plan reads the
  bundle at its source, which wave 32 built; it did not settle that a plan
  *validates* what it read, and nothing does.
- **Consequence:** the shape wave 32 named — *two answers to one question,
  decided by which shape the vendor published* — has reappeared as *decided by
  which flags the operator passed*. Recorded in the test that meets it, so the
  next reader finds it at the assertion rather than in this file.

## D-056 — `release build` copies the vendor's `.git` into the bundle it publishes

- **Touches:** RFC 0014; `internal/infra/atomicfs/copy.go`
- **Found:** chasing a CI failure that read as a settle-window flake —
  `cannot open .../bundle/.git/objects/maintenance.lock`, racing git's own
  background maintenance. The lock was the symptom. **The bundle walk does not
  exclude `.git`**, and nothing in the tree does.
- **Measured, end to end, with a seeded credential:** `release build` on a
  git-tracked bundle wrote a `SHA256SUMS` of 55 entries of which **42 were
  `.git/`**, including `.git/config` and the whole of `.git/objects`; `release
  archive` then packed all 42 into the published `tar.zst`. The signature chain
  is *signature → SHA256SUMS → every file*, and "every file" had come to mean
  the vendor's repository.
- **Not exotic:** `--version-from-git` requires the bundle to be a git repo, so
  this is the workflow the flag exists for, and this project's own test creates
  a repo at the bundle root — which is how CI met it.
- **Class:** `drift` against RFC 0014, which never distinguished a bundle
  *source tree* from what *ships* from it. Pre-existing: `v0.2.0`'s `copy.go`
  has no filter either.
- **Consequence:** wave 35, with an RFC 0014 amendment, ruled by the author on
  2026-08-19. Not fixed here — the exclusion list, whether `.gitignore` is
  honoured, and what a stricter builder does to bundles already published are
  four decisions, and a security fix reviewed under a debt wave's title is
  reviewed by nobody.
- **What the download counts do and do not bound.** An earlier draft of this
  entry said *"nobody has leaked anything"*, and review was right that the
  counts cannot carry that. They are worse evidence than the objection allowed,
  in fact: 11 amd64 and 0 arm64 are downloads of **morzer**, not of bundles
  built with it, so they bound how many people could have run the builder and
  say nothing whatever about what any resulting bundle contained. The honest
  statement is that exposure is bounded by a small number of manager downloads
  and is not otherwise established.

## Rules distilled

- **A deferral is a claim, and nothing later re-examines it.** A fix gets
  reviewed; a *Carried* bullet gets copied forward. Wave 32 deferred the dry-run
  status because a read model "may consume" it, and the read model reads the
  journal a dry run never enters — two file reads away, carried for two waves.
  (D-048)
- **A grace period is a claim about what some released binary can read, and it
  is checkable against the tags.** Row 18 named a removal release on sound
  reasoning and assumed the release before it was a migration window. `git show
  <tag>:<file>` settles that in one command. (D-052)
- **A fixture written in the deprecated form tests the deprecated path.** A
  project that deprecates a surface and leaves its fixtures on it has announced
  a migration rather than started one, and its suite is measuring the path it
  means to delete. Migrating the fixtures is what turns the announcement into a
  test — here it exposed a wizard that had offered no profiles to any current
  bundle since the day the spelling landed. (D-053)
- **An assertion that cannot tell which of two things failed will name the
  wrong one — and it names the one under test.** The Redis probe reported "a
  stopped service was still reported healthy" when the shutdown had not landed,
  so three waves each looked at health probing, found it correct, and carried
  the finding. (D-049)
- **Fixing the instance is half the fix; the other half is the grep.** D-046
  distilled this for a number and this wave found it true of a mechanism: wave
  32 fixed one path-join and left two, one of which breaks the primary install
  path. (D-054)
- **A flake in a lane is a hypothesis about the lane, not a fact about it.**
  Both of this wave's flakes were real defects wearing a flake's clothes — a
  swallowed error, and a `.git` directory in a published archive. (D-049, D-056)

## Carried into the next unit

- **The `.git` leak — wave 35**, with an RFC 0014 amendment (D-056). Ruled.
- **The path-join in `commands.go` and `init_wizard.go`** (D-054). `init
  --release <archive>` without `--product` is broken on a shipped release.
- **A plan does not validate the bundle it plans against** (D-055).
- **`FieldRemovalRelease` is a single-member design with no members** (D-052).
  The next field deprecation is what should force its shape.
- **`Installation.Providers`** — still declared, still unwritten (D-011). Ruled
  2026-08-19: **not gated on P3**, which was this file's own framing and did not
  survive checking. The state decodes with plain `json.Unmarshal`, so removing
  it needs no migration; what gates it is whether `installation describe`'s
  output may lose a field, which is RFC 0027's question. Wave 36.
- ~~**The removal of `runtime:` in 0.4.0**~~ — done, one release early (D-052).
  The oldest carried item in this file, closed.
- ~~**Two settle-window fragilities**~~ — both closed (D-049, D-050), and
  D-049's first fix was itself wrong (D-057): it named the cause correctly and
  left it in place.
- ~~**A plan over a remote reference carries no deprecation warning**~~ — closed
  as won't-fix (D-051).
- ~~**`operation.status` reports `succeeded` for an all-`pending` dry run**~~ —
  fixed (D-047).
- **`saveInstallation` writes its report before the state store** (wave 31).
  Untouched, and now the oldest carried item in this file.

## Reconciliation — 2026-08-19

| RFC | row | outcome | grade | decision | from |
|---|---|---|---|---|---|
| 0023 | 23 | **Accepted** | `LOCKED` | `runtime:` stops being read in 0.3.0; a withdrawn compatibility promise rather than a moved date, because no released manager reads `runtimes:` | D-052 |
| 0023 | 18 | **Superseded** | `LOCKED` | Left in the table unedited. An append-only table that rewrites the row it replaces has stopped recording that a decision changed | D-052 |
| 0014 | — | **Deferred to wave 35** | — | A bundle source tree is not what ships from it; the exclusion list, `.gitignore`, and already-published bundles are wave 35's to settle | D-056 |
| 0027 | — | **Ruled, unscheduled** | — | `Installation.Providers` is 0027's question, not 0023's, and not gated on P3 | D-011 |
| — | — | **Refused** | — | Making `init --dry-run` fetch a remote bundle to phrase a deprecation advisory | D-051 |

No row is proposed for `StepPlanned` (D-047): a step-status vocabulary that no
RFC settles does not become a decision row because one value was added to it.
The argument for the value belongs where the value is.

## Self-audit — 2026-08-19

Scope: the whole branch — the engine's plan record, two test lanes, the manifest
removal and every fixture behind it, three documentation pages and the
changelog. `just ci` green at **86.6%** (floor 84), `docs-check` 41 pages / 55
checks, acceptance passed, container lane run.

**Sabotage sweep: 9 mutations, 8 killed.** The survivor is `case StepPlanned:`
in `FirstIncompleteStep`, and the why is the finding rather than the survival:
it is behaviourally identical to the `default:` branch beside it, which also
refuses. It exists so that branch's comment — *"a status this build does not
recognise"* — stays true of a status this build defines. Recorded rather than
deleted, and recorded rather than counted as covered.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Major | **The `init` wizard offered no profiles to any bundle on the current spelling**, since the day that spelling landed. `profilesFrom` read the deprecated block directly, and an empty list is also what a release with no profiles looks like, so it failed silently. | Fixed — D-053, verified red the moment the fixture moved |
| A-2 | Major | **`init --release <archive>.tar.zst` without `--product` is broken on a released binary** — the same path-join wave 32 fixed in one place and left in two. | Open — D-054, carried |
| A-3 | Major | **`release build` writes the vendor's `.git` into the bundle it publishes**, `.git/config` included, and `release archive` ships it. | Open — D-056, wave 35, ruled |
| A-4 | Medium | **A plan does not validate the bundle it plans against**, and is refused only when `--product` is absent, by accident of the CLI needing the manifest for the name. | Open — D-055, recorded at the assertion that meets it |
| A-5 | Medium | The legacy refusal emitted a **second error contradicting the first**: a vendor who wrote `runtime:` was told they had declared no runtime. | Fixed, with the test that was missing for it |
| A-6 | Low | Four symbols left with no callers by the removal — `legacyProjectOption`, `RuntimeSpec.isZero`, and the suite's `warnings`/`contains`. **Found by lint, not by reading the diff**, which is the argument for the linter: none appears in any hunk that stopped using it. | Fixed |
| A-7 | Low | `DeclaredRuntimes` now returns the manifest's own map rather than a fold-built one, and `RuntimeConfig.Options` carries it to every adapter method uncloned. **Pre-existing and unchanged in practice**: every manifest that still loads was already on the spelling that took this path, and the fold's fresh map only ever protected the spelling being deleted. | Open — observation, not this wave's to decide |

**A-1 is the wave's most useful finding and it was free.** Nothing looked for it;
migrating a fixture off a deprecated spelling failed the test that had been
guarding it, and the defect was underneath. The generalisation is in *Rules
distilled*, and it is the one worth carrying: a suite whose fixtures use the
deprecated form is measuring the path the project intends to delete.

**Three of the four Majors were found while chasing something else** — two CI
failures that both read as flakes, and a fixture migration. None was in the
diff. That is the argument for treating a red lane as a hypothesis rather than
an inconvenience, and it is why this wave was scheduled before RFC 0023 P3
rather than after.

## D-057 — The first fix made the flake diagnosable, not fixed

- **Touches:** D-049, this wave, corrected by this wave
- **D-049 said:** `dockerlab.WaitGone` was the fix — ask the container whether
  it stopped before asking the port.
- **Found, by running the lane it was written for:** the test failed again, and
  the message was the new one — *"the request to stop it did not land, which is
  a fault in the fixture and not in whatever is being probed"*. So the diagnosis
  was **confirmed in the wild rather than only in simulation**, and the fix
  addressed the wrong half: it made the failure name its cause and left the
  cause in place.
- **Built:** `dockerlab.Stop`, which stops the container from outside. Nothing in
  the probe's claim needs the service to stop itself — what is asserted is that
  a TCP check reports a vanished port as refused, and how it went is the
  fixture's business. `docker exec` has to schedule a process in a container on
  a busy host; `docker stop` has no such step.
- **Class:** `drift` against this wave. The evidence for the *diagnosis* was
  strong — a simulated miss reproduced the recorded 30.8s to within half a
  second — and I let that stand in for evidence about the *remedy*, which it
  never was. Attributing a failure correctly and preventing it are two changes,
  and the first one feels like both.
- **Consequence:** the full lane is green and **one green run is weak evidence
  for a flake that failed in three of six waves.** What is worth more is that
  the mechanism is gone rather than widened: there is no longer an in-container
  step that can fail to land, so the failure mode is removed by construction
  instead of given a longer deadline. If it returns it will be something else,
  and it will say so.

## Review round, PR #60 — appended after the group closed

## D-058 — The wave distilled "grep for the rest" and then did not

- **Touches:** D-053 and D-054, both of this wave; `internal/ui/views/release.go`,
  `internal/adapters/render/gotemplate/synthetic.go`
- **Found in review:** `releaseDoc` still read `Manifest.Runtime.Profiles`, so
  `release show` listed no profiles for any bundle on the current spelling.
  Grepping for the rest — which the reviewer did not do and I should have —
  turned up **a third site**: `syntheticProfile`, which feeds `release verify
  --render-check`. That one is the worst of the three, because it is a
  *verification* feature and it had stopped exercising profile branches
  entirely while continuing to report success.
- **Class:** `drift` against this wave, and against its own distilled rule.
  D-054 in this same group records wave 32 fixing one instance of a repeated
  mechanism and not grepping for the others; the fix for D-053's wizard defect
  was written after that entry and did exactly the same thing. The rule was
  not merely available, it was **written down in the file, in this group, by
  me, in the commit before**.
- **Why it went unseen, which is the part worth keeping:** the search that found
  the wizard was `\.Runtime\.\(Files\|Project\)` — the fields the *removal*
  touched. Profiles was not on that list because nothing in the removal read it;
  the sites were found by a test failing, and only one test failed. **The grep
  that matters after a removal is for every read of the removed thing, not for
  the reads the removal happened to break.** A read that silently returns the
  empty value breaks no test, which is precisely why it needs the grep.
- **Consequence:** one `Manifest.ProfileNames`, three callers, and the
  render-check's own fixture moved off the deprecated block — it had been
  declaring profiles in `runtime:`, which is why the regression was invisible
  from inside its test. Sabotage: 4 mutations, 4 killed.

## D-059 — Download counts bound who could run the builder, not what shipped

- **Touches:** D-056
- **Written:** *"Nobody has leaked anything: the same 11 downloads that priced
  D-052 price this."*
- **Found in review, and it is worse than the objection said:** the counts are
  downloads of **morzer**, not of bundles built with it. They bound how many
  people could have run the builder at all; they say nothing about what any
  resulting bundle contained. The same number was sound evidence for D-052,
  where the question really was "how many people have this binary", and carrying
  it one entry across to a question about *artefacts* made it look like evidence
  for something it cannot reach.
- **Class:** `drift` against this wave.
- **Consequence:** a number that was load-bearing in one entry is not
  transferable to the next just because the entries are adjacent. **Re-derive
  what a measurement measures before reusing it**, especially when reusing it is
  what makes a security claim comfortable.
