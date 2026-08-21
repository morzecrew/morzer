# Wave 26 · The manifest's runtime dimension

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

Branch `feature/wave-26-manifest-runtime-dimension`. RFC 0023 P2, partly — the
manifest and state halves; `installation import`, `doctor`, and §14's two
unspelled leaks are not in it.

**Drift count: 0.** Nothing the RFC settled was built otherwise. Two entries
below depart from §4.1's *sketch* rather than from a decision row, which is a
different thing and is said so in each.

## D-008 — Decision 8 resolved against the option the RFC preferred

- **Touches:** RFC 0023 §4.1, decisions row 8 (`OPEN` → proposed `LOCKED`).
- **RFC said:** either `runtimes:`' keys are the declaration, or
  `providers.runtime.name` stays the selector — *"The second reads better and is
  a smaller change."*
- **Built:** the first. `runtimes:`' keys are the declaration;
  `providers.runtime.name` is derived from them for a single-runtime release and
  left empty for a two-runtime one.
- **Because:** `Providers.Runtime` is a single `Provider` beside `secrets`,
  `backup` and `health`. It holds one value, and §4.1 requires a bundle to be
  able to declare two runtimes — which decision 4 then has `--render-check`
  render both of. The preferred option cannot express the case the same section
  mandates.
- **Class:** `spec-gap`. The struct has looked like this since before the RFC;
  the preference was formed against the YAML sketch rather than the type.
- **Consequence:** the manifest's hardcoded `"compose"` default is gone —
  §2.1's second expensive leak, and the one §12.2 said decided the RFC's cost.
  `providers.runtime.name` is now derived or empty, never invented.
- **Proposed row (RFC 0023, row 8):** `LOCKED` — as built above.

## D-009 — `runtimes:` is added; `runtime:` stays readable

- **Touches:** RFC 0023 §4.1. Put to the author before any code was written.
- **RFC said:** the block *"lands as a replacement of the existing block before
  the first tag rather than an addition after it."*
- **Built:** an addition after it. Both spellings parse, a manifest declaring
  both is refused, and the legacy block folds into the map on read.
- **Because:** the premise expired. 0.1.0 and 0.2.0 are cut, and under strict
  decoding a replacement makes `runtime:` an unknown field — every bundle
  already built stops parsing, to buy a tidier surface. The author ruled on the
  alternatives rather than the executor.
- **Class:** `spec-gap`.
- **Consequence:** two spellings until a named removal release, and **no
  `api_version` bump**: 0018 decision 1's `min_manager_version` carries the cost,
  which is the mechanism it exists to be. `DeprecatedAPIVersions` stays empty —
  it is keyed by api_version and this is a *field* deprecation, which has no
  mechanism today and did not grow one here.
- **Proposed row (RFC 0023, row 9):** `LOCKED` — as built above.

## D-010 — One `files` key per runtime, against §4.1's sketch

- **Touches:** RFC 0023 §4.1's YAML example; decision 7 (`LOCKED`).
- **RFC said:** `quadlet: {units: [app.container, ...]}` beside
  `compose: {files: [...]}`.
- **Built:** `files` for every runtime.
- **Because:** deciding whether `units` or `files` is the legal key means asking
  which runtime the block belongs to, and a branch on a runtime's name above
  `internal/adapters` is exactly what decision 7 forbids and what
  `tools/runtimecheck` fails the build over. A `LOCKED` row outranks a sketch in
  a design section, so this is a departure from the illustration rather than a
  conflict needing a halt.
- **Class:** `discovery`. The collision is only visible once the validator is
  written against the rule.
- **Consequence:** a vendor writes `runtimes.quadlet.files: [app.container]`.
  Those are files, so the name is honest; what they *mean* stays the adapter's.
- **Proposed row (RFC 0023, row 10):** `LOCKED` — as built above.

## D-011 — A fourth zero-caller shape, in the field this feature wanted

- **Touches:** RFC 0023 §1; `internal/domain/installation.go`. Unlisted.
- **RFC said:** nothing. §1 indicts the shape in general — 0015 found a port
  with no implementations, 0021 methods with no callers.
- **Found:** `Installation.Providers` is declared, serialised, and **never
  written and never read** by anything, tests included. It also carries two
  contradictory documented meanings: `describe.go` says *"declared by the release
  manifest, not chosen by the operator"*, and `repair_test.go` says *"from the
  flags: which adapters to use is what `init` decides"*. It comes from neither.
- **Built:** a new `Installation.Runtime`, schema 8 → 9, rather than reusing it.
- **Because:** an older manager reading `Providers.Runtime` finds a name it
  understands and no reason to stop — and it has one adapter, so it would drive
  a Quadlet installation with Compose. The bump is what makes that a refusal,
  and it is for the *read* path, which none of bumps 5–8 were.
- **Class:** `spec-gap` against the codebase rather than against this RFC.
- **Consequence:** `Installation.Providers` is still unwritten and now has a
  neighbour that does its apparent job. It should be deleted or given a meaning;
  this wave did neither, because removing a serialised field is its own schema
  question.
- **Proposed row (RFC 0023, row 11):** `LOCKED` — as built above.

## D-012 — A multi-runtime release is refused at `init`, pending P3

- **Touches:** RFC 0023 §4.1, decision 5 (`LOCKED`). Unlisted as a row.
- **RFC said:** *"A bundle declaring both carries both sets."* It does not say
  how the manager picks which one to install with.
- **Built:** `init` records the runtime when a release declares exactly one, and
  refuses when it declares several, naming P3.
- **Because:** choosing means knowing which runtime this manager can drive, and
  the only answers available today are a branch on a runtime's name — forbidden
  by decision 7 — or a name injected at the composition root that every test
  would set and no test would exercise as production leaves it. Refusing costs a
  bundle nobody ships yet; either alternative costs the architecture test.
- **Class:** `spec-gap`.
- **Consequence:** the manifest can express a two-runtime release before the
  manager can install one. That gap closes with the second adapter.
- **Proposed row (RFC 0023, row 12):** `ASSUMED` — a release declaring several
  runtimes is refused at `init`. Graded `ASSUMED` rather than `LOCKED` because it
  expires: P3 brings a second adapter and with it a real basis for choosing.

## D-013 — The state migration loop could hang rather than refuse

- **Touches:** `internal/infra/state/state.go`. Pre-existing; no RFC covers it.
- **Found by sabotage**, and not in the way a sweep usually reports: the mutation
  that stopped `case 8` advancing the schema version did not fail a test, it hung
  the run until the timeout killed it. `migrateInstallation` loops while the
  version is below current, so a case that does not raise it never terminates.
- **Built:** a progress check that refuses when a pass raises nothing.
- **Because:** every load of installation state goes through this loop. A
  mistyped case number is therefore a manager that stops responding on an
  operator's machine, not one that says what is wrong — and it would present as
  a hang with no output, which is the hardest failure to diagnose remotely.
- **Class:** `discovery`.
- **Consequence:** the failure is now a sentence naming the schema version that
  made no progress. The guard costs one comparison per migration pass.

## Decision-row outcomes

**Ruled 2026-08-16. All four proposals accepted, and a fifth row added for
D-012 at the author's direction.** D-013 proposes nothing — it is a defect fixed
in the code it belongs to.

**Recorded against this section's own process failure:** rows 8–11 were written
into the RFC's decision table as `LOCKED` in the same pass that amended the
phasing, before any of them had been put to the author. The ruling below makes
them legitimate; it does not make the sequence correct, and the entry stays here
because a log that only records outcomes cannot show that a proposal was adopted
before it was offered. D-012's row was the one written in the right order — put
first, accepted, then added.

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | 8 | **Accepted** | `LOCKED` | `runtimes:`' keys are the declaration; the provider name is derived or empty | D-008 |
| 0023 | 9 | **Accepted** | `LOCKED` | `runtimes:` added, `runtime:` deprecated and still read, no api_version bump | D-009 |
| 0023 | 10 | **Accepted** | `LOCKED` | One `files` key per runtime; per-runtime key names cannot be validated without a forbidden branch | D-010 |
| 0023 | 11 | **Accepted** | `LOCKED` | The runtime is a new installation field at schema 9, not `Providers` | D-011 |
| 0023 | 12 | **Accepted** | `ASSUMED` | A release declaring several runtimes is refused at `init`, until P3 gives a basis for choosing | D-012 |

**Two alternatives were declined and are recorded here, since nothing else
carries a refusal.** Keeping §4.1's `units:` key per runtime, which would have
let `compose: {units: [...]}` pass domain validation and fail only at the
adapter — refused because it moves the error further from the vendor who caused
it. And deleting `Installation.Providers` in this wave rather than leaving it
beside its replacement — refused because removing a serialised field is its own
schema question, and it is carried forward instead.

## Audit findings — 2026-08-16

Scope: the whole branch as of `8fc06c2`, six commits. The findings below were
produced by the project's own guards and by the sabotage sweep, not by reading.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | High | Normalising the legacy block into a field during `ApplyDefaults` made it a snapshot: `Validate` called on its own saw an empty map and checked no paths, so `/etc/passwd` in `runtime.files` passed. A path-escape check that holds only when another method ran first is not a check. | Fixed — `DeclaredRuntimes` derives on every call; test asserts the check without `ApplyDefaults` |
| A-2 | High | `buildInstallation` set the runtime from the release on **every** `init`, including `--repair`, so a vendor changing runtimes between releases would have a repair re-point an installation whose volumes belong to the old one — decision 3's transition, by the back door. | Fixed — carried from existing state; behavioural test added |
| A-3 | Medium | `migrateInstallation` hangs rather than refusing when a case fails to advance the version (D-013). | Fixed — progress guard |
| A-4 | Low | The refusal for a manifest declaring no runtime named only `runtimes`, sending a vendor using the legacy spelling to look for a block they do not have. | Fixed — names both spellings |

**Sabotage sweep: 8 mutations, 8 killed** — but only after two of them were
made killable. `repair rebuilds the runtime` **survived**: the repair
classification table asserts that somebody wrote a *reason*, not that the code
matches it, so the carry had no behavioural test. And `case 8 does not advance`
killed by timeout rather than by assertion, which is A-3 demonstrating itself.

**What remains distrusted.**

- **P2 is not finished.** `installation import` does not carry the runtime,
  `doctor` does not report it, and §14's two unspelled leaks are untouched. Each
  is named in §10's P2 bullet rather than left to be discovered.
- **No adapter has ever been selected by this machinery.** Every path is
  exercised with one runtime present, so "refuses the runtime it cannot run" is
  tested against a release that declares a runtime nobody can run, not against a
  machine that lacks one.
- **`Installation.Providers` is still unwritten**, and now sits beside a field
  doing its apparent job (D-011).

## Review findings — 2026-08-16

Six findings on PR #50, all valid, none refuted. Two were raised by both bots.
The first two matter most, because each meant an accepted decision did not hold
in code.

| # | Severity | Finding | Status |
|---|---|---|---|
| R-1 | Critical | `stepWriteInstallation` ran before `stepStageRelease`, the only thing that puts the release into engine state — so `runtimeForNewInstallation` found none and **every installation recorded an empty runtime**, read as the legacy one. The feature recorded nothing. | Fixed — staging moved first; ordering asserted on the step list |
| R-2 | Critical | `Validate` required `providers.runtime.name` unconditionally while `ApplyDefaults` deliberately leaves it empty for two runtimes, so **a two-runtime manifest could not validate** and decision 8's shape did not exist. | Fixed — required only where one name can be true |
| R-3 | Major | An empty runtime key validated; `runtimeForRelease` recorded `""`; `RuntimeName()` read that as the legacy runtime, so a release declaring something else installed as Compose with every later message agreeing. | Fixed — empty and whitespace keys refused |
| R-4 | Major | Nothing refused an installation whose runtime this manager has no adapter for, so the recorded runtime was a label (decision 5). | Fixed — `ports.Runtime` gains `Name()`; the comparison is two values, not a literal |
| R-5 | Major | `checkReferencedFiles` walked only the deprecated block, so a `runtimes:` release loaded clean with a missing file and failed three steps into a deployment. | Fixed — walks every declared runtime, naming the vendor's own spelling |
| R-6 | Medium | The no-progress guard's test started at schema 1, which falls to the switch default and returns before the loop body runs twice — it never reached the guard, so a case that stopped advancing would still hang while the test stayed green. | Fixed — every supported schema migrated forward; the timeout is the assertion |

**Both critical findings are the same shape, and it is the shape this branch's
own audit was blind to.** R-1 and R-2 each passed every test written for them,
because each test exercised the piece rather than the path: `runtimeForRelease`
was tested directly and never through an `init`, and the two-runtime manifest
was asserted to leave a field empty without ever being asked to load. The seam
extracted to make a decision testable is the seam that stopped the decision
being tested where it runs.

**Port growth, recorded so §6's count stays honest.** `ports.Runtime` gains
`Name()`, its thirteenth method. §6's escape hatch counts methods forced by the
*second adapter*; this one is forced by decision 5's refusal, and the alternative
was comparing against a literal above `internal/adapters` — the branch decision 7
forbids.

**R-7, taken after the round rather than carried.** `Installation.Validate`
ignored `Runtime` entirely, so a hand-edited state file naming a runtime that
does not exist loaded clean.

The answer is that "validate the runtime" is two questions and only one of them
belongs here. **Whether a name is well-formed** is a grammar, and the domain
already has that shape for images, parameters and product names — so
`ValidRuntimeName` joins them, rejecting empty, padded, capitalised,
underscored, over-long, and anything carrying a terminal escape. **Whether a
runtime exists** is not a fact this layer has, and any answer shaped as a list
of known names is the runtime catalogue above `internal/adapters` that decision
7 exists to prevent; the well-formed-but-wrong name is refused by R-4's adapter
comparison, which is the only place that knows.

The security half is what made it worth doing now rather than later: the value
is read from a file an operator may have hand-edited and is printed back in
error messages, so a name carrying an escape sequence is a diagnostic that moves
the cursor — the same shape as the bounds on fleet rows and attested text, and
the same argument.

**The limit is asserted, not merely described.** A test requires that `quadlt`
*passes* domain validation, so a later reader who adds a catalogue here has to
delete a test that says why there isn't one.

**A process failure worth recording against this round.** Five of the six thread
replies cited commit hashes written from memory rather than read from `git log`;
four of them pointed at nothing. Corrected on the threads. A reviewer following a
fabricated reference finds no commit and has no way to tell a wrong hash from a
fix that was never made.

## Rules distilled

- **A validator that reads a normalised field is only as good as the guarantee
  that normalisation ran.** Derive on read, or the check silently becomes a
  check of stale state. (A-1)
- **A table that records a reason is documentation, not a test.** If a
  classification says "carried", something must fail when it stops being
  carried. (A-2)
- **A loop whose exit condition is only changed inside its own body needs a
  progress guard**, or a missing case is a hang rather than an error — and a
  hang on a load path is the least diagnosable failure there is. (D-013)
- **A seam extracted for testability is a seam where the production path stops
  being tested.** Both critical review findings sat exactly there: the unit
  passed, and nothing asked whether the caller reached it. (R-1, R-2)
- **A test that asserts a field is empty has not asserted the object is
  usable.** The two-runtime manifest satisfied its test and could not load.
  (R-2)
- **"Validate X" is often two questions at two layers.** Split them before
  reaching for the check: the shape of a value is usually knowable where it is
  defined, and its truth usually is not. (R-7)
- **Assert the limit of a check, not just its coverage.** A test requiring that
  a plausible-looking wrong value *passes* is what stops the next reader
  "fixing" the omission by adding the thing the architecture forbids. (R-7)
- **Read the hash, never recall it.** A commit reference is a claim like any
  other, and one written from memory is unfalsifiable to the reader who follows
  it and finds nothing.
- **A design sketch loses to a `LOCKED` row.** When the illustration cannot be
  implemented without violating a decision, the illustration is what gives way,
  and the departure is recorded rather than argued. (D-010)

## Carried into the next unit

- **The rest of P2**: `installation import`, `doctor`, and §14's two unspelled
  leaks (`RuntimeSpec.Project`, `doctor.go`'s hard-coded `tools.Docker`).
- **`Installation.Providers`** — delete it or give it a meaning. Deleting it in
  this wave was put to the author on 2026-08-16 and declined: removing a
  serialised field is its own schema question, and widening the branch after its
  audit is how an audited branch stops being audited.
- **The field-deprecation gap**: `runtime:` is deprecated and nothing warns. The
  only deprecation mechanism is keyed by `api_version`, and this is a field.
- **A named removal release for `runtime:`**, without which "deprecated" is a
  word in a doc.
