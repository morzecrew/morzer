# Wave 29 · Deprecating a field

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

Branch `feature/wave-29-deprecating-a-field`. RFC 0023 P2's last item — D-017,
the field-deprecation gap — and the second thing carried with it, a named
removal release for `runtime:`.

**Drift count: 0.** Nothing the RFC settled was built otherwise. Decision 9 said
`runtime:` "stays readable and deprecated"; it stays readable, and it is now
deprecated in the software rather than in a document.

**The readiness gate fired for the third wave running**, and for the third time
it was the whole of the wave's design. "Close the deprecation gap" needed three
decisions the RFC does not settle: when `runtime:` stops being read, whether the
project's own scaffold keeps writing it, and where a field-level warning is
allowed to appear. All three were put to the author before any code existed. Two
came back as recommended and one did not — the removal release is **0.4.0**, not
the 1.0.0 that was proposed — which is the argument for asking rather than
assuming: the wave would otherwise have shipped a date nobody chose, written
into a warning vendors are meant to plan against.

## D-021 — A field deprecation names its removal release and warns in three places

- **Touches:** RFC 0023 decision 9 (`LOCKED`), RFC 0018 decision 1.
- **RFC said:** `runtime:` "stays readable and deprecated", with the cost
  recorded as "two spellings to maintain until a named removal release". The
  release was never named and nothing warned (D-017).
- **Built:** `domain.FieldRemovalRelease = "0.4.0"` and
  `Manifest.DeprecatedFields()`, surfaced by `release verify`, `init` and
  `update` and by nothing else.
- **Because:** the only deprecation mechanism this project had is keyed by
  `api_version`, and a field cannot be a map key — a field is deprecated by
  being written at all, which only the manifest can answer. The three surfaces
  are the moments somebody can act: a vendor before publishing, an operator
  while choosing. Every other command meets the same manifest again with no
  choice available, and `release.Load` already refuses to be that place for the
  api_version warning, in prose, for this reason.
- **Class:** `spec-gap`.
- **Consequence:** a manifest surface now carries an expiry date that the
  software states. An operator who never runs `init` or `update` between now and
  0.4.0 is never told — which is the accepted cost of not warning on every load,
  and is bounded by the fact that 0.4.0 refuses at `update` rather than breaking
  a running deployment.
- **Deliberately not applied:** a `doctor` check, put to the author. Refused
  because every installation that exists runs a `runtime:` bundle, so the check
  would warn on every machine, permanently, about a file the operator cannot
  change — which is how a project teaches people to ignore `doctor`.
- **Proposed row (RFC 0023, row 18):** `LOCKED`.

## D-022 — The scaffold writes the current spelling and declares the manager it needs

- **Touches:** RFC 0023 decisions 8–10 and 15 (`LOCKED`), RFC 0013 §5.5, RFC
  0018 decision 1.
- **RFC said:** nothing about the scaffold. `runtimes:` was specified; that
  `morzer release new` still emitted `runtime:` was noticed by nobody.
- **Built:** the scaffold emits `runtimes.compose` with `project` under
  `options`, and stamps `compatibility.min_manager_version: 0.3.0`. The
  authoring tutorial, which taught the deprecated block, teaches the current
  one.
- **Because:** a project that warns about a field its own scaffold writes has
  deprecated nothing. The floor is not bookkeeping beside it: `runtimes:` is an
  unknown field to every released manager, and under strict decoding an unknown
  field refuses the whole manifest — so without the floor a vendor's customer is
  told about a typo instead of an upgrade requirement, which is precisely the
  failure RFC 0018 decision 1 exists to convert.
- **Class:** `spec-gap`.
- **Consequence:** a bundle scaffolded today cannot be installed by any released
  manager, because the manager it needs is not released. That is correct and it
  is also new: `release new` previously produced something 0.1.0 could install.
- **Proposed row (RFC 0023, row 19):** `LOCKED`.

## D-023 — A manager built between tags cannot be compared against a version floor

- **Touches:** RFC 0018 decision 1. **Against an earlier unit, found by this
  one.**
- **RFC said:** a lenient preamble reads `min_manager_version` before strict
  decoding, so "a future manifest field is a legible upgrade requirement rather
  than a report about a typo". It says nothing about what the manager's own
  version is.
- **Built:** `checkManagerVersion` declines the comparison when the running
  manager's version carries a prerelease.
- **Because:** the version is `git describe --tags`, which derives from the
  *last* tag — so the build that first understands a new field reports itself as
  a prerelease of the release *before* the one that ships it. Measured on this
  tree: it added `runtimes:` and calls itself `0.2.0-9-g8c5a81c`, which semver
  orders below the `0.3.0` floor its own scaffold writes. The shipped binary
  therefore refused the bundle `morzer release new` had just written, reporting
  it as "a bug in the scaffold", and `release verify` exited 9.
- **Class:** `spec-gap`. The mechanism was correct for every case that existed
  when it was designed, because until this wave nothing in the tree declared a
  floor at all.
- **Consequence:** a developer on an untagged build gets the strict decoder's
  unknown-field error rather than the clearer one. That is the trade the
  function already makes for every other question it cannot answer honestly, and
  it is the right side of it: the alternative refuses a manifest this build can
  in fact read.
- **Proposed row (RFC 0018):** the floor is not enforced against a manager whose
  own version is a prerelease. **Not yet ruled** — proposed here and nowhere
  else.

## Decision-row outcomes

**Ruled 2026-08-17, before any code was written.** Three questions; two
accepted as recommended and one decided against the recommendation.

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | 18 | **Accepted** | `LOCKED` | `runtime:` stops being read in 0.4.0; the warning appears at `release verify`, `init` and `update` and nowhere else | D-021 |
| 0023 | 19 | **Accepted** | `LOCKED` | `release new` writes `runtimes:` and stamps the `min_manager_version` it needs | D-022 |
| 0018 | — | **Proposed** | — | A version floor is not enforced against a manager whose own version is a prerelease | D-023 |

**The recommendation that lost.** 1.0.0 was proposed for the removal, on the
argument that the deprecated spelling should go before the release that freezes
the surface. **0.4.0 was chosen.** Recorded because nothing else carries a
refusal, and because the shorter clock changes what the next wave owes: the
removal is two minors away rather than an era away.

**Two alternatives were declined.** A `doctor` check for the deprecation, and
leaving the removal release unnamed while warning anyway.

**Row 14 is still outstanding**, and was not put again — a proposal repeated
louder is still one proposal.

## Self-audit — 2026-08-17

Scope: the whole branch — the domain surface, three call sites, the scaffold,
two documentation pages, and the tests. `just ci` green at **86.5%** (floor 84),
`docs-check` 41 pages / 55 checks, `runtime-check` **17 mentions, 0 branches**,
unchanged: this wave added no runtime vocabulary above the adapters.

**Sabotage sweep: 22 mutations, 22 killed — three only after being made
killable, and five added by the review rounds.** Full acceptance passed. The container lane failed once on
`TestTCPProbeAgainstRedis` and passed on a re-run; it is a settle-window
fragility of that test rather than anything this branch touches — see *Carried*.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Major | **The shipped binary refused its own scaffold.** `morzer release new` reported "a bug in the scaffold" and `release verify` exited 9, because the floor the scaffold stamps is above the version an untagged build reports for itself. | Fixed — D-023, verified red |
| A-2 | Major | **Deleting `min_manager_version` from the scaffold killed nothing.** The half of the ruling the question was actually about had no test, so the floor could have been dropped by any later edit in silence. | Fixed — the floor is asserted from the scaffold's real output, and so is the refusal an older released manager gets |
| A-3 | Medium | Every test left `managerVersion` at zero, and zero is the one value that skips the comparison — so the entire floor mechanism was inert under `go test` while broken in the binary. This is why A-1 survived to the point of being measured by hand. | Fixed — the new tests drive the loader with a non-zero version, in three positions relative to the floor |
| A-4 | Low | `init --dry-run` reports a plan for a bundle it never opens: the product name and domain in its closing line come out empty, and the deprecation warning cannot reach it. Pre-existing, and this wave is what made it visible. | Open — recorded below and carried; the fix moves init's fetch out of the operation |

**A-1 and A-2 are one finding seen from two ends.** The sweep found that nothing
held the floor; running the real binary found that the floor was wrong. Neither
half was visible from the diff, and the sweep alone would have produced a test
pinning the broken behaviour.

**What remains distrusted.**

- **The removal itself is unbuilt.** 0.4.0 is a string in a warning and a date
  in a document; nothing fails when it arrives, and nothing tests what happens
  the day `runtime:` stops being read.
- **A scaffolded bundle cannot be installed by any released manager**, because
  the manager it declares does not exist yet. Correct, and untested end to end —
  there is no released 0.3.0 to test against.
- **`git describe` versions understate by construction**, and the fix here
  declines rather than corrects. A tagged 0.3.0 satisfies the floor; a build one
  commit later does not, and now silently skips the check instead.
- **`init --dry-run` does not warn**, and cannot. The warning is published from
  `stepStageRelease`, and the engine returns from its plan branch before any
  step runs — so an init plan says nothing about a deprecated bundle while an
  update plan does. `update` can warn because `resolveUpdateTarget` materialises
  the bundle *before* the engine; `init` fetches inside the step. Making the two
  agree means moving init's fetch out of the operation, which changes what a
  plan does on the network and is not this wave's to do.

**One process failure, recorded rather than fixed quietly.** `git checkout --`
ate the D-023 fix during the verify-red step — the **fifth** time this command
has destroyed uncommitted work on this project, and the second time in three
waves. "Commit before you sabotage" was followed for the sweep and then broken
by a fix written *after* the commit. The sweep now restores by writing back the
file's saved contents rather than by asking git what HEAD says.

## Review findings — 2026-08-17

One finding on PR #53, and it was valid.

| # | Severity | Finding | Status |
|---|---|---|---|
| R-2 | Medium | Codecov named `warnDeprecations` at 44% patch, and the uncovered lines were the **api_version branch** — dead in every test because `DeprecatedAPIVersions` is empty, on this path and on the one the code was moved from. A detection branch nothing runs is one nobody knows works, and this one only ever runs on the day it matters. | Fixed — the branch is driven by injecting a stale version, as `manifest_test.go` already does; both it and the nil-bus guard are sabotaged and killed |
| R-3 | Minor | `TestBothKindsOfDeprecationAreReported` asserted only that **two** warnings were published, which an implementation emitting the same warning twice also satisfies — the test's name claimed more than its assertion checked. | Fixed — both warnings are named; the mutation that published the api_version one twice is now killed |
| R-4 | Minor | The api_version tests overwrote a package-global map entry and deleted it unconditionally, so a pre-existing entry would be destroyed on the way out. | Fixed — a helper saves and restores. Not reproducible today, and deliberately so: the map is empty by construction, and "the map is empty" is a property of production code the test should not depend on |
| R-1 | Major | The untagged-build exemption (D-023) was written as **"any prerelease"**, which also exempts a deliberately versioned one — `0.2.0-rc.1` really is older than a 0.3.0 floor. The comment justifying it claimed the strict decode would still refuse such a bundle; that holds only when the floor stands in for an unknown field. A vendor may raise it for a *behavioural* reason, and then the manifest parses on the old manager and this check is the only thing refusing it. | Fixed — the exemption matches the shape `git describe` produces and nothing else; reproduced red first |

**R-1 is a defect in the reasoning rather than in the code**, which is the kind
worth writing down. D-023 was found by measuring the real binary, and the fix
was written against the one case measurement had produced. "Any prerelease"
covered that case and a second one nobody had asked about — and the comment
beside it asserted a safety property that was true of the measured case only.
The narrow shape was in the reviewer's first suggestion.

## Rules distilled

- **A fix written from one measurement generalises to exactly one case.** The
  untagged-build exemption was correct for the build that produced it and wrong
  for `0.2.0-rc.1`, because "the stamp understates" and "this build is older"
  wear the same syntax. Name the shape you measured, not the category it is
  in. (R-1)
- **A deprecation with no removal release is a complaint.** "Deprecated" tells a
  vendor something will happen; only a version tells them when, and the warning
  has nowhere to put a date the project never chose. (D-021)
- **Warn where the reader can still act, and nowhere else.** A vendor before
  publishing and an operator while choosing a bundle can both do something; the
  same manifest met on every later command cannot be changed by anybody reading
  the message. (D-021)
- **A project that scaffolds the field it deprecates has deprecated nothing** —
  and the assertion that keeps it honest is that the scaffold's own output
  produces no warning, not that the template looks right. (D-022)
- **A version derived from the last tag understates every build after it.**
  Anything comparing the manager's own version against a floor is comparing
  against a number that is wrong by one release for the entire development
  cycle. (D-023)
- **A check that is skipped by its zero value is inert in every test that does
  not set it.** `managerVersion` defaulted to zero, zero meant "decline", and a
  whole mechanism passed its tests while failing in the binary. Set the
  production value in at least one test, or the seam is the thing under test.
  (A-3)
- **`git checkout --` restores to HEAD, so it eats every fix written after the
  last commit.** Restore a mutated file from its own saved bytes; the sweep must
  not consult git at all. (fifth occurrence)
- **An assertion that something has gone away is a timeout, not a signal, and a
  timeout is a race with whatever else the machine is doing.** Two now in this
  project — a container count after `compose stop`, and a TCP probe after a
  shutdown — both green alone and both failing under a full lane. The published
  port outlives the process behind it. (carried from wave 28, second instance
  here)

## Carried into the next unit

- **The removal of `runtime:` in 0.4.0** — now a dated commitment rather than an
  open question, and nothing yet enforces the date.
- **Row 14, outstanding since wave 27.**
- **The RFC 0018 proposal from D-023**, unruled.
- **`Installation.Providers`** — still declared, still unwritten (D-011).
- **P1b item 4** — the `EnvironmentFile`-on-tmpfs measurement, still behind a
  bootable venue, and still the only thing before **P3, the Quadlet adapter**.
- **R-4 from wave 28** — an option made explicit with the adapter's own default
  is refused as a change; needs a resolve-options capability and a decision row.
- **The acceptance suite's `assert_running` has no settle window**, carried from
  wave 28 as a fragility — and **`TestTCPProbeAgainstRedis` is a second of the
  same kind**, found by this wave's container lane. It waits 30 seconds for a
  shut-down Redis to stop accepting connections and failed at 30.8s under a full
  lane, then passed alone in 0.6s. A published port keeps accepting while the
  proxy is torn down, so the probe is measuring Docker's teardown rather than
  the service. Neither is this branch's, and both are now two instances of one
  shape: an assertion about a service going away, with a timeout instead of a
  signal.
- **`init --dry-run` plans against a bundle it has not read** (A-4), which is why
  it cannot carry the deprecation warning and why its closing line names no
  product.
- ~~**The field-deprecation gap (D-017)**~~ — closed by this wave. **P2 is
  complete.**
