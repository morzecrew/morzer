# Wave 27 · Import, doctor, and the leak with no name in it

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

Branch `feature/wave-27-import-and-doctor-runtime`. RFC 0023 P2, the rest of it
except one item: `installation import` carrying and refusing the kind, `doctor`
reporting it, and §2.2's second unspelled leak. `RuntimeSpec.Project` is not in
it, deliberately and by the author's ruling — see D-016.

**Drift count: 0** against this wave's RFC. One finding against wave 26 is
recorded here (D-016) and it is *not* drift: the RFC never settled what a
project means under `runtimes:`, so its absence is a gap rather than a
departure. What wave 26 missed was writing the gap down, which is what this
entry is.

**The readiness gate fired before any code was written, and that is the entry
worth reading first.** "The rest of P2" as a single unit needed three
load-bearing decisions the RFC does not settle — the import refusal, the
project question, and the deprecation mechanism — which is the threshold at
which `flag-dont-flip` says an RFC is not ready to execute as drawn. All three
were put to the author; one was ruled into the wave and two were ruled out of
it. The wave that ran had one.

## D-014 — The `tools.Docker` leak is closed by a capability, not a rename

- **Touches:** RFC 0023 §2.2, §4.5, §9. No decision row governs it: the leak was
  named and classified, and no fix was ever specified.
- **RFC said:** `ops/doctor.go` hard-codes `tools.Docker` in the branch that runs
  when there is no installation — *"the lifecycle layer stating which runtime
  this machine will use, in a sentence with no runtime's name in it."*
- **Built:** `ports.ToolRequirer`, an optional capability beside `RegistryProber`
  and `ImageInspector`. `doctor` asks the wired adapter which tools `init` will
  need; the compose adapter answers `docker` and `compose`.
- **Because:** §14 files both unspelled leaks under "the renames themselves are
  not P1's", and this one cannot be renamed — the sentence has no runtime's name
  in it, which is why the inventory could not hold it. What had to move was the
  *decision*, from the layer that must not make it to the layer that owns it.
- **Class:** `spec-gap`.
- **Consequence:** `doctor` on a machine with no installation now also checks the
  Compose CLI plugin, and **fails** on a host that has the daemon and not the
  plugin. That machine could never have completed an `init`, so this is a
  refusal moving earlier rather than a new restriction — but it is a new failing
  check on an existing surface and it is in the changelog as one.
- **Deliberately not applied:** two alternatives, each refused for a reason worth
  keeping. A `runtime name → tool` table in the tool catalogue, which decision 7c
  explicitly permits as data — refused because it puts the adapter's knowledge in
  a table the adapter cannot see, so the manager would be asserting what a
  runtime needs rather than asking. And a method on `ports.Runtime` — refused
  because §6's escape hatch counts methods, and spending the fourteenth on a
  diagnostic would blunt the test the RFC exists to run.
- **Proposed row (RFC 0023, row 14):** `ASSUMED` — where the manager would
  otherwise state which runtime a machine uses, it asks the adapter; an optional
  capability is the mechanism, so an adapter that cannot answer declines rather
  than stubs. **Outstanding.**

## D-015 — `installation import` refuses a runtime it cannot drive

- **Touches:** RFC 0023 §4.2, decision 3 (`LOCKED`), decision 5 (`LOCKED`).
- **RFC said:** *"`installation import` is the second creation path (0016 found
  this) and must carry the kind."* Nothing about what an import does when the
  kind names a runtime this binary has no adapter for.
- **Built:** refused before anything is created, naming both runtimes and the two
  ways forward, exiting 9.
- **Because:** the field already travelled — an export carries the installation
  whole — so carrying it was never the open question. The open question is that
  decision 3 makes the runtime immutable, so an imported record naming an
  undriveable runtime is a machine where `apply`, `update`, `status` and
  `restore` all fail and nothing can correct it short of deleting the
  installation.
- **Class:** `spec-gap`.
- **Consequence:** this is the only thing an import refuses about the *manager*
  rather than about the document, and it sits directly beside `ManagerVersion`,
  which the same file says is *"recorded for diagnosis, never enforced: refusing
  an export because it was written by a different manager is a refusal at the
  worst possible moment."* The distinction is real and is now written in both
  places: a version mismatch still leaves a working machine.
- **Deliberately not applied:** importing with a warning, which was put to the
  author beside the refusal. Refused because the warning is read during an
  incident, by someone who then runs `apply` and gets a second, worse surprise.
- **Proposed row (RFC 0023, row 13):** `LOCKED` — **accepted 2026-08-16**, added
  with a back-link.

## D-016 — `runtimes:` cannot name a project, and one is supplied anyway

- **Touches:** RFC 0023 §2.2, §4.1, decision 10 (`LOCKED`). Against wave 26.
- **RFC said:** §4.1's sketch gives each runtime a file list and nothing else.
  §2.2 lists `RuntimeSpec.Project` as an unspelled leak and describes it as a
  field with three readers.
- **Found:** it is more than that now. `RuntimeDecl` has no `project` key, so a
  vendor on the new spelling cannot set one — and `ApplyDefaults` fills
  `m.Runtime.Project` from the product name **unconditionally**, including for a
  manifest that never wrote a `runtime:` block. That value still reaches
  `--project-name` and the `COMPOSE_PROJECT` hook ABI. The only way to name a
  project under `runtimes:` is to write a legacy block containing nothing but
  `project:`, which slips past the both-declared refusal because `isZero()`
  deliberately ignores the field.
- **Because:** decision 10 removed per-runtime key names, and `project` left with
  them without anybody saying so. Wave 26's log records the decision and not this
  consequence.
- **Class:** `spec-gap`. The RFC never settled what a project means under the new
  spelling, so this is not drift — wave 26 built nothing the RFC had decided
  otherwise. What it did was leave a surface nobody chose.
- **Consequence:** every release on the new spelling is grouped by its product
  name, silently, through a field on a deprecated block. 0021 already noticed the
  docs' teardown snippet works only because the example's project name happens to
  equal its product name; this makes that coincidence the rule, without saying so
  anywhere a vendor reads.
- **Not fixed here, by the author's ruling on 2026-08-16.** It is a published
  hook ABI, and moving it breaks bundles in the field — its own unit of work,
  with its own decision about whether the ABI moves, is renamed with the old name
  kept, or stays what it is. Widening this wave to include it would have made the
  wave the ABI change with a doctor check attached.
- **Proposed row:** none. The question is what a project *is* once a second
  runtime exists, and proposing an answer from inside a wave that is not doing
  the work would be the laundering this practice exists to prevent.

## D-017 — The deprecation of `runtime:` is deferred, with the reason

- **Touches:** RFC 0023 decision 9 (`LOCKED`), RFC 0018 decision 1. A departure
  from the execution plan rather than from a document.
- **Plan said:** wave 27 covers the rest of P2, and the carried list from wave 26
  names the field-deprecation gap as part of it.
- **Built:** nothing. `runtime:` remains deprecated in prose with no warning
  anywhere.
- **Because:** this project's only deprecation mechanism is keyed by
  `api_version` (`DeprecatedAPIVersions`), and this is a *field*. Where a
  field-level warning surfaces — bundle load, `release verify`, every operator
  command — is a design decision with a real cost attached: a warning on every
  manifest load is a warning about a file the operator did not write and cannot
  change, which is how a project teaches people to ignore its warnings.
- **Class:** `spec-gap`.
- **Consequence:** decision 9's cost — *"two spellings to maintain until a named
  removal release"* — is running with no clock on it and no signal to a vendor
  that one is running.
- **Proposed row:** none yet; the mechanism has to be chosen before a row can say
  anything. Carried.

## Decision-row outcomes

**Ruled 2026-08-16, before any code was written — which is the sequence wave
26's own log recorded itself for getting wrong.** Three questions, three
rulings, and two of them decided what the wave was rather than what it built.

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | 13 | **Accepted** | `LOCKED` | `installation import` refuses an export whose runtime this manager does not drive, before anything is created | D-015 |
| 0023 | — | **Deferred** | — | `RuntimeSpec.Project` is its own unit of work; the finding is logged and the ABI decision is not taken here | D-016 |
| 0023 | — | **Deferred** | — | The deprecation of `runtime:` waits on a field-level mechanism being chosen | D-017 |
| 0023 | 14 | **Outstanding** | `ASSUMED` | Where the manager would state which runtime a machine uses, it asks the adapter through an optional capability | D-014 |

**Three alternatives were declined and are recorded here, since nothing else
carries a refusal.** Importing with a warning instead of a refusal — refused
because the warning is read mid-incident by somebody who then runs `apply`.
Doing the `project` work inside this wave — refused because a published-ABI
change with a doctor check attached is an ABI change nobody reviewed as one.
Warning about `runtime:` on every manifest load — refused because it is a
warning about a file the operator did not write and cannot change.

## Self-audit — 2026-08-16

Scope: the whole branch, four commits, ~520 lines across 12 files, code, tests,
docs and records. `just ci` green at **86.4%** (floor 84), `just
coverage-union` **87.6%** (floor 86), `just runtime-check` **18 mentions / 0
branches** — unchanged, because closing this leak removed no *name*, which is
the whole point of D-014. Full acceptance, all three demo lanes, `just
test-docker` green on the first run.

**Sabotage sweep: 12 mutations, 12 killed — one only after being made
killable.**

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Major | Dropping `compose` from the compose adapter's tool list **survived**. Everything above the adapter sees that list through the fake, which carries its own copy, so the shipped adapter's answer was asserted by nothing. | Fixed — the adapter's own test, plus a test that every tool it names has a probe in the catalogue |
| A-2 | Major | `git checkout -- doctor.go`, undoing a mutation, restored the file to HEAD and **ate an uncommitted fix** — and the commit that followed claimed the fix as done. The comment on `drivesRuntime` said one implementation served three callers while `doctor` still had its own copy. | Fixed and recorded in the commit that restored it |
| A-3 | Medium | The import refusal raised an installation error, the kind the same comparison uses mid-operation, while the schema-from-the-future refusal on the same path exits 9. Two exit codes for "this document needs a different manager". | Fixed — incompatible, with the exit code asserted |
| A-4 | Low | `TestDoctorAsksTheRuntimeWhichToolsInitWillNeed` asserted against the fake's own list, so a fake advertising nothing would have passed by never entering the loop. | Fixed — the list is required non-empty first |

**A-1 is the one to carry.** It is the injected-seam failure this project has
already recorded once, arriving from the other side: not a field every test
sets, but a *fake every test uses*, holding its own copy of an answer the
production adapter gives. The sweep found it and coverage would not have —
`RequiredTools` reported 100% covered on the compose adapter, because the
adapter's method *is* executed, by nothing that checks what it returns.

**What remains distrusted.**

- **No adapter has ever declined the capability in production.** The decline path
  is exercised by a wrapper type in a test, which is the honest way to model it
  and is still a model.
- **`runtime.declared` is registered only when a runtime is wired**, so a manager
  with no adapter reports nothing rather than reporting that. Consistent with the
  volume checks beside it, and untested.
- **The fake reports `docker` and `compose` whatever name it is given**, so a
  test can construct a runtime called `quadlet` that asks for Compose's tools.
  No test does; nothing stops one.

## Rules distilled

- **A fake that answers for an adapter is a second implementation of the
  answer.** If nothing compares the two, the sweep tests the fake and the
  shipped code is unmeasured — and coverage will still say 100%, because the
  method ran. (A-1)
- **`git checkout -- <file>` during a sabotage sweep restores to HEAD, and HEAD
  is not where you are.** The rule was recorded once against the tests a sweep
  demands; it applies to every uncommitted edit, and the second violation
  produced a commit message that was false. (A-2)
- **Two refusals about the same fact must exit with the same code.** An operator
  scripting against one of them has no way to learn there is a second. (A-3)
- **A leak with no name in it cannot be renamed away.** What moves is the
  decision, not the word — and that is why a vocabulary checker structurally
  cannot find these and a reader must. (D-014)
- **A removed key takes its meaning with it.** Deleting per-runtime key names
  deleted `project` from the new spelling, and the default that filled it in kept
  running — so the surface disappeared and the behaviour did not. (D-016)
- **A readiness gate that fires is a result, not an obstacle.** Three unsettled
  decisions cost three questions and one ruling each; the same three discovered
  inside the code would have cost a wave that had to be argued back out. (Wave
  27's plan)

## Carried into the next unit

- **`RuntimeSpec.Project` and the `COMPOSE_PROJECT` ABI** — D-016, the last of
  §2.2's unspelled leaks and the only item of P2 still open. It needs its own
  decision about whether a published ABI moves.
- **R-4: an option made explicit is not a change.** Comparing effective rather
  than declared options needs the adapter to resolve them, which is a capability
  and a decision row.
- **The acceptance script's `assert_running` has no settle window**, and flaked
  once on this PR. It counts running containers immediately after a stop.
- **The field-deprecation gap** — D-017. `runtime:` is deprecated, nothing warns,
  and the mechanism has to be chosen first.
- **A named removal release for `runtime:`**, without which "deprecated" is a
  word in a doc.
- **`Installation.Providers`** — still declared, still unwritten, now with a
  field doing its apparent job beside it (D-011).
- ~~**The rest of P2**: `installation import`, `doctor`, and §14's two unspelled
  leaks.~~ Three of four shipped in this wave; the fourth is D-016 above.
