# Wave 32 · A plan that names what it plans

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

Branch `feature/wave-32-a-plan-that-names-what-it-plans`. RFC 0001 decision 12
applied to `init`, and the reconciliation backlog: RFC 0018's proposal from
wave 29's D-023.

**Drift count: 1** — D-031, against whichever wave first gave `init` a
`--dry-run`, found by this one. RFC 0001 decision 12 settled that a plan reads
the bundle at its **source**; `init --dry-run` read nothing at all.

## D-031 — `init --dry-run` planned against a bundle it never opened

- **Touches:** RFC 0001 decision 12, RFC 0002 §"Plan (`--dry-run`)"
- **RFC said:** `--dry-run` plans the convergence steps against the bundle at
  its source rather than its release-store destination, because nothing is
  staged during a plan.
- **Built (before this wave):** `init --dry-run` closed with this, captured
  rather than paraphrased — the trailing space is the evidence, so it is fenced
  rather than spanned:

  ```
  installation  created for 
  ```

  Two empty slots, and a creation claimed in the past tense printed directly
  beneath *this is a plan; nothing was changed*. In `--json`, `data.product`
  was `""`.
- **Because:** the summary read the installation out of engine state, and a plan
  runs no steps, so nothing had populated it.
- **Class:** `drift`. Decision 12 covered it and `init` was built otherwise —
  the only non-zero drift entry in this file, and it is recorded as such rather
  than softened into a gap. What makes it drift and not a gap: the rule existed,
  was written down, and applied to exactly this situation.
- **Consequence:** the product was never unknown. The CLI resolves it *before*
  the operation, from `--product` or from the manifest at the bundle's source,
  because every managed path derives from it — so it was already in `opts`. The
  fix reads what was in hand rather than adding a way to obtain it.
- **Measured, and what corrected the plan for this wave:** the same `--json`
  object reported `etc_dir` ending in `/etc/web` for the `web` bundle while
  `product` was empty. One value derived from the manifest and one blank, in one
  object — which is what proved the manifest had already been read and killed
  the assumption that this needed a way to read a manifest without staging.

## D-032 — A warning withheld from the only person still choosing

- **Touches:** RFC 0023 decision 18
- **RFC said:** the deprecation warns at `release verify`, `init` and `update` —
  the moments somebody can still act.
- **Built:** the warning lived inside `stepStageRelease` and read the *staged*
  copy, so `init --dry-run` — an operator deciding whether to install the bundle
  at all — was the one path that never carried it.
- **Because:** decision 18's whole argument is that the warning belongs where a
  choice is available. A plan is that moment in its purest form: nothing has
  been done yet and the operator is deciding.
- **Class:** `spec-gap`. The row named commands, and a plan is a mode rather
  than a command, so following it literally left the mode uncovered.
- **Deliberately not applied:** moving the warning out of the step for the real
  path too. There it reads the bundle *after* verification, and a bundle whose
  signature does not check out should not hand out advice about its fields on
  the way to being refused. A plan has no verified copy and is already a
  statement about the source, so the two paths read different copies for a
  stated reason rather than by accident.

## D-033 — Two verbs in one clause

- **Touches:** wave 29's D-021 work, found by this wave
- **Built:** `warnDeprecations` published `"this bundle uses " + f.Message()`,
  and `Message()` already opens with the field name — composing *this bundle
  uses `runtime` is deprecated and will stop being read in 0.4.0*.
- **Because:** the same `Message()` is printed bare by `release verify` and has
  always read correctly, which is what located the defect in the join rather
  than in the sentence. It shipped in wave 29 and is in that wave's own
  acceptance log verbatim, unread by its author.
- **Class:** `spec-gap`.
- **Consequence:** nothing parsed either string — no test, no script, no doc —
  which is why it survived. The summary line and the warning are both now
  asserted.

## Reconciliation — 2026-08-18

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0018 | 13 | **Accepted, narrowed** | `ASSUMED` | A version floor is not enforced against a manager stamped by `git describe` between tags (`<N>-g<sha>`, optionally `-dirty`); a deliberate prerelease such as `rc.1` stays subject to it | D-023 |

**The proposal could not be accepted as written, and that is the finding.**
D-023 proposed "the floor is not enforced against a manager whose own version is
a prerelease". True of the wave 29 implementation — and **PR #53's review killed
that form as too wide**, because `0.2.0-rc.1` is a prerelease that genuinely *is*
below a `0.3.0` floor. The code was narrowed to the exact `git describe` shape
two waves before this row was written into the RFC. Accepting it verbatim would
have made the document claim something wider than the code does.

## Self-audit — 2026-08-18

Scope: the whole branch — three commits, `init`'s summary and warning paths,
the RFC row, and the tests. Sabotage sweep of six mutations against the changed
surface.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Medium | Blanking `data.product` in the `--json` output killed no test. The text summary was asserted and the machine-readable field was not — and of the two, only the second is a contract. | Fixed — `TestAPlansJSONNamesTheProductAndNoInstallation`, re-run against the same mutation and killed |

**Sabotage sweep: 6 mutations, 6 killed** — one only after A-1 made it killable.

**Two lanes, and the first container run was red.** `TestTCPProbeAgainstRedis`
failed under the full lane and passed alone in 0.64s, which is the carried
fragility from wave 29 reproducing its own measurement (0.6s alone, 30.8s under
load). Nothing on this branch is near a health probe. Recorded rather than
replaced by the green re-run: it is now the **third** sighting of the shape, and
a fragility seen three times across three waves is a defect the project keeps
deciding not to fix.

## Review round, PR #56 — appended after the group closed

Four findings, all valid. Two were defects in this wave's own new code, and the
most useful one is D-035: it found that this wave fixed half of the thing it
existed to fix and wrote the other half down as acceptable.

**Drift count: still 1** — no new drift; D-034 and D-035 are gaps, D-036 is a
limitation now named rather than a change.

## D-034 — `--repair` reported as a creation, on both paths

- **Touches:** wave 32's own summary work, and whatever wave first added `--repair`
- **Built:** `init --repair` said `installation <id> created for <product>`, and
  the plan said `would create an installation` beside an empty installation id —
  for a record that already exists.
- **Because:** the summary had one sentence for two operations. This wave made
  the plan half state it more explicitly, which is what made two reviewers see it.
- **Class:** `spec-gap`, and **pre-existing**: the real path said "created" on
  `main` before this branch. Fixed on both paths rather than only the one
  reported, because fixing the plan alone would have left the operation lying
  and called the review answered.
- **Consequence:** an operator reading a plan to check they are repairing the
  right machine was reading the one line that did not distinguish repair from
  first install.

## D-035 — The plan warned about directories and stayed silent about archives

- **Touches:** D-032, this wave, found in review
- **Built:** `warnPlannedDeprecations` joined `manifest.yaml` onto the release
  path. `--release` names a directory *or* a `tar.zst`, so for an archive that
  produced `demo.tar.zst/manifest.yaml`, the load failed, and the error was
  swallowed.
- **Because:** measured, with a real archive: the plan printed no warning while
  the operation printed one, about the same bundle. Two answers to one question,
  decided by which shape the vendor happened to publish.
- **Class:** `spec-gap`, and the worst kind — **the limitation was written down
  as intentional.** The function's own comment said an archive "gets no warning
  rather than an error". D-032 exists because a plan withheld this warning; this
  wave fixed the directory case and documented the archive case as acceptable,
  which is a gap converted into prose instead of into code.
- **Consequence:** now routed through `ports.ReleaseSource`, which reads either
  shape. Local references only: a registry would mean a plan pulling a bundle
  over the network to phrase an advisory. **A remote reference still gets no
  warning** — carried below, and named rather than commented away this time.

## D-036 — The between-tags exemption also exempts a build that is behind

- **Touches:** RFC 0018 row 13, written this wave
- **Built:** row 13 now names the hole.
- **Because:** `isUntaggedBuild` matches `N-g<sha>`, and a build from an *older*
  branch is stamped identically — `0.1.0-5-gabc1234` — while being genuinely
  behind rather than ahead. `git describe` cannot separate them: both are "N
  commits past some tag", and which tag is the last one is the question.
- **Class:** `spec-gap` in the row I had just written. The row already said the
  stamp is derived and understates; it did not say the derivation is also
  ambiguous in the other direction.
- **Consequence:** bounded to builds from source — a released binary sits on a
  clean tag and is held to the floor. Closing it needs a *declared* version
  rather than a derived one, which is a change to how this project versions
  itself, not to this check. Not attempted under review.

## D-037 — A test that passed for the wrong reason

- **Touches:** D-035's fix, found by the coverage gate on PR #56
- **Built:** `TestAPlanDoesNotReachForARemoteBundle`, asserting on a counting
  source rather than on the output.
- **Because:** codecov reported the patch at 75%, and the uncovered lines were
  `warnPlannedDeprecations`'s decline branches. The clitest written to cover the
  remote case asserted "no warning appears" — and **that passes for two
  different reasons**: the scheme guard declining, or a `Fetch` that fails
  because nothing serves `oci://` in a test. Measured: with the guard deleted,
  that test still passed.
- **Class:** `spec-gap` in my own test. The decision is *a plan does not go to a
  registry to phrase an advisory*, and the only observable that separates it
  from "the pull failed" is whether the source was asked at all — which the
  output cannot show.
- **Consequence:** the clitest keeps the user-visible claim and the internal
  test pins the mechanism. The mutation that survived now dies.

## Rules distilled

- **An output assertion cannot tell a guard from a failure downstream of it.**
  Both produce silence. When a decision is "do not attempt X", the test has to
  observe the attempt, not the result. (D-037)
- **A limitation written into a comment is a gap that has stopped being
  counted.** The archive case was documented as acceptable in the same wave
  whose whole purpose was that a plan must not withhold this warning. Ask
  whether a comment explaining a restriction is describing a decision or
  excusing an omission. (D-035)
- **Fixing the half a reviewer saw leaves the other half lying.** `--repair`
  said "created" on the real path too, and only the plan was reported. (D-034)
- **A row is written the day you know least about it.** Row 13 was authored and
  reviewed in the same wave, and review found a direction the author had not
  considered. (D-036)
- **An assertion on the sentence is not an assertion on the contract.** The
  summary and `data.product` say the same thing to two different audiences, and
  the parsed one had no test. (A-1)
- **A carried proposal ages against the code it describes.** D-023 sat unruled
  for three waves while a review invalidated its wording, and nothing in the
  log's format shows that — the entry looks as fresh as the day it was filed.
  Re-read the code before accepting a proposal, not just the proposal. (D-023)
- **A rule written about commands does not cover modes.** Decision 18 named
  `verify`, `init` and `update`; `--dry-run` is a mode of one of them, and fell
  through. Ask which *modes* a rule about commands reaches. (D-032)
- **One object holding a derived value and a blank one is the fastest proof
  available.** `etc_dir: /etc/web` beside `product: ""` settled where the defect
  was, and cost one command. (D-031)
- **Prose defects survive because nothing parses prose.** Two verbs collided in
  a warning that shipped, ran in an acceptance log, and was read by nobody —
  including the author who wrote both. (D-033)

## Carried into the next unit

- ~~**The RFC 0018 proposal from wave 29's D-023**~~ — accepted, narrowed.
- **The removal of `runtime:` in 0.4.0** — a dated commitment nothing enforces.
  Now the oldest carried item in this file.
- **`Installation.Providers`** — still declared, still unwritten (D-011).
- **P1b item 4** — still behind a bootable venue, still the only thing before
  **P3, the Quadlet adapter**.
- **Two settle-window fragilities**, carried from waves 28 and 29.
- **`saveInstallation` writes its report before the state store** (wave 31).
- **A plan over a remote reference still carries no deprecation warning** — new
  in review (D-035). Local shapes are covered; `oci://` and `https://` are not,
  because a plan that pulls a bundle to phrase an advisory is a cost nobody
  asked a plan for. Named here rather than left in a code comment.
- **`operation.status` reports `succeeded` for a dry run whose steps are all
  `pending`**, new here and deliberately not fixed: it is a machine-readable
  field RFC 0026's read model may consume, so changing it is a design question
  rather than a bugfix.
