# Wave 28 · What the runtime is told, and what that names

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

Branch `feature/wave-28-runtime-options`. RFC 0023 P2, the last item —
`RuntimeSpec.Project`, §2.2's remaining unspelled leak — plus the hazard found
underneath it.

**Drift count: 0.** Nothing the RFC settled was built otherwise. One entry
(D-018) departs from §4.1's shape, and §4.1 never described this surface at all.

**The readiness gate fired again, and paid again.** "Close the last leak" needed
three decisions the RFC does not settle: what a project *is* under `runtimes:`,
what protects a running installation from a changed one, and what becomes of the
published hook variable. All three were put to the author before any code
existed and all three were ruled, so the wave had none open. Two waves running:
the gate has now cost six questions and saved two re-scoped branches.

## D-018 — Per-runtime options are opaque, and the adapter validates them

- **Touches:** RFC 0023 §4.1, decision 10 (`LOCKED`), decision 7 (`LOCKED`).
- **RFC said:** a runtime declares files, and (decision 10) one key name for all
  runtimes. Nothing about settings.
- **Built:** `runtimes.<name>.options`, a map the manager bounds in shape —
  identifier keys, single-line values, 200 characters — and never reads. The
  compose adapter reads `project` from it and refuses keys it does not know,
  from `Validate`.
- **Because:** decision 10 removed the per-runtime *key name*, and `project`
  left with it (D-016). Putting it back as a typed field would put one runtime's
  vocabulary in the shape every runtime shares, which is what decision 10 took
  `units:` out of. A map is the only form that survives a second runtime with a
  different answer to the same question.
- **Class:** `spec-gap`.
- **Consequence:** a manifest surface exists whose *meaning* the manager cannot
  check. An unknown key is refused only by the adapter, only from `Validate` —
  the path `apply`, `doctor` and `release verify --render-check` take, and not
  every path. A vendor who mistypes an option and never runs those sees nothing.
- **Deliberately not applied:** a uniform `project` key on the declaration, put
  to the author beside the map. Refused for the reason above. Also refused: no
  surface at all, which would have made adopting `runtimes:` impossible for any
  vendor whose project is not their product name without a volume migration.
- **Proposed row (RFC 0023, row 15):** `LOCKED` — **accepted 2026-08-16**.

## D-019 — The installation records what the runtime was told

- **Touches:** RFC 0023 decision 3 (`LOCKED`), RFC 0018 decision 1.
- **RFC said:** nothing. The runtime is immutable per installation; what the
  runtime was *told* is not mentioned anywhere.
- **Built:** `Installation.RuntimeOptions` at installation schema 10, and a
  release that changes any of them is refused — before the operation in `apply`,
  `update` and `rollback`, and again inside `runtimeConfig`, which no path
  bypasses.
- **Because:** the options name durable things. Measured on this host:
  `--project-name alpha` resolves a volume named `alpha_data` and `beta`
  resolves `beta_data`. So a changed project is a deployment pointed at storage
  nothing has ever written to, with the operator's data still on the disk and
  nothing referring to it — and nothing else in the manager would notice. The
  backup that runs next captures the new empty volumes; `doctor` reports them
  covered.
- **Class:** `discovery`. The hazard existed before `runtimes:` — a vendor
  editing `runtime.project` between two releases has always been able to do
  this — and only building the new spelling made it visible, because the
  documented migration performs it.
- **Consequence:** every option is treated as durable, including ones no runtime
  has heard of, because the manager cannot tell which are. Refusing a harmless
  change costs a message; permitting a harmful one costs the data. An
  installation created before schema 10 has no baseline and adopts what it is
  *currently running* on its next converge — never what a candidate release
  proposes, which would record the change as the baseline and defeat the check
  on the one operation that needs it.
- **Deliberately not applied:** warning instead of refusing, put to the author.
  Refused because an unattended apply has nobody reading the warning.
- **Proposed row (RFC 0023, row 16):** `LOCKED` — **accepted 2026-08-16**.

## D-020 — The hook ABI is two lists

- **Touches:** RFC 0023 §2.2, RFC 0007 §13 and its three-ABI table.
- **RFC said:** P1's inventory named the shape and left the decision to P2 —
  *"the variable stays for Compose installations and is absent under another
  runtime, which makes it a runtime-supplied variable rather than a core one"*.
- **Built:** exactly that. `ports.HookVarSupplier`; the compose adapter supplies
  `COMPOSE_PROJECT`; `ports.HookEnv` no longer has a field for it.
- **Because:** renaming was never available — the name is what every vendor hook
  already writes — and the core ABI was promising a value one runtime cannot
  mean.
- **Class:** `spec-gap`.
- **Consequence:** the hook ABI is no longer one list, which RFC 0007's gate
  assumed. `docs-check` gained `checkRuntimeHookVars`, verified by perturbation:
  renaming the documented variable fails the build. Without it this fix would
  have created the ungated ABI RFC 0007 §13 built these gates to end.
- **Proposed row (RFC 0023, row 17):** `LOCKED` — **accepted 2026-08-16**.

## Decision-row outcomes

**Ruled 2026-08-16, before any code was written.** Three questions, three
accepted, each carrying a declined alternative.

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | 15 | **Accepted** | `LOCKED` | Per-runtime settings are an opaque `options` map; the adapter validates them | D-018 |
| 0023 | 16 | **Accepted** | `LOCKED` | The installation records the options it was created with; a release that changes them is refused | D-019 |
| 0023 | 17 | **Accepted** | `LOCKED` | `COMPOSE_PROJECT` is supplied by the runtime, not promised by the core ABI | D-020 |

**Row 14 is still outstanding** — wave 27 proposed "where the manager would
state which runtime a machine uses, it asks the adapter", and this wave is three
more instances of it: the project, the option vocabulary, and the hook variable.
It has not been put again, because a proposal repeated louder is still one
proposal.

**Three alternatives were declined and are recorded here, since nothing else
carries a refusal.** A uniform `project` key on the declaration. Warning instead
of refusing on a changed option. And documenting the migration without changing
the manager, which would have left the pre-existing half of the hazard — a
vendor editing `runtime.project` — exactly as it was.

## Self-audit — 2026-08-16

Scope: the whole branch, code, tests, docs, schemas and records. `just ci`
green at **86.5%** (floor 84), full acceptance, all three demo lanes, and
`docs-check` at 41 pages / **55** checks — one more than before, because the
runtime half of the hook ABI needed a gate of its own. `runtime-check` **17
mentions (7 port-shaped, 2 compose-shaped, 8 catalogue), 0 branches** — down
from 18/3, and the fall is the point: an inventory that only grows is a list
nobody has to shrink.

**Two lanes are reported rather than claimed.** `just test-docker` has one
failing test and `just coverage-union` cannot finish, both for the same reason
and neither for this branch's: another project's development server on this
host holds `127.0.0.1:18443`, which `TestTheStatementCarriesNamesAndTheBound
AndNoValues` needs. Every other test in the container lane passes, and the five
update tests that failed on an earlier run — port 18080, held at the time by a
different process — pass in isolation now that it is free. Killing somebody
else's process to make a lane green is not a way to make a lane green.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Major | `apply --dry-run` on an installation created before schema 10 **wrote the baseline** and reported that it had changed nothing. A plan that writes state is the one thing a plan may not be. | Fixed — adoption skipped on a dry run, asserted |
| A-2 | Major | The refusal fired inside a step, so the engine compensated and the operation exited 11 ("back where it started") — burying the exit code and the remedy in a record. A precondition reported as a step failure is a precondition nobody can act on. | Fixed — asked before the operation in all three converge paths; the in-step check stays as the unbypassable one |
| A-3 | Medium | The first version documented that a `project:` left beside `runtimes:` would be *ignored*. That is the hazard with a comment on it. | Fixed — refused, naming where the value goes |
| A-4 | Low | The end-to-end test edited the release in place, which trips the digest guard first, so it was asserting a different refusal than it claimed. | Fixed — the disagreement is driven from the state side, and the comment says why |
| A-6 | Medium | Two files behind the `docker` build tag still used the removed `RuntimeConfig.Project`, and every untagged lane was green. | Fixed — both updated; the tagged build is now part of the pass |
| A-5 | Major | **Deleting the update path's adoption killed nothing.** The whole path a vendor actually ships a rename through — a new bundle, arriving by `update` — was untested, and so was the ordering that makes it work: adopting from the *candidate* would record the change as the baseline and refuse nothing. | Fixed — an update that renames is refused end to end, and both mutations now fail it |

**Sabotage sweep: 11 mutations, 11 killed — one only after being made
killable — plus the new `docs-check` gate verified by perturbation.**

**A-1 and A-2 are the same kind of finding**: both are about *where* a correct
check runs rather than whether it is correct. Neither would have been found by
reading the diff, and both were found by tests written to assert the behaviour a
user sees.

**What remains distrusted.**

- **An unknown option is refused only from `Validate`.** Paths that never call
  it carry the typo silently until one does.
- **No second adapter has ever supplied a hook variable or declined an option**,
  so both halves of the new capability are exercised against one implementation
  and a fake modelled on it.
- **The adoption of a baseline is a state write on a converge path.** It is
  skipped on a dry run and idempotent afterwards, and it is still a write that
  did not happen before this wave.
- **A docker-tagged file went uncompiled by every untagged build.**
  `test/dockerlab` and one `_docker_test.go` still referenced the removed field
  and `just ci` was green throughout; only the container lane found them. Any
  build-tagged tree is invisible to the fast loop.

## Review findings — 2026-08-16

Five findings on PR #52, four valid and one acknowledged out of scope. Three of
the four were the same seam: the baseline was derived and written in one step.

| # | Severity | Finding | Status |
|---|---|---|---|
| R-1 | Critical | An update whose current release could not be resolved **skipped the derivation silently**, so the baseline stayed nil, read as "created before schema 10", and waved the rename through. On the update path — which is how a vendor actually ships one. | Fixed — the resolution error is returned; reproduced red first |
| R-2 | Major | A rollback `--dry-run` skipped the derivation along with the write, so the plan accepted a target the operation would then refuse. | Fixed — derivation is pure, and the plan compares what the run compares |
| R-3 | Major | The baseline write ran **before the deployment lock**, so it could put back fields a concurrent `config set` had just changed. | Fixed — a read-modify-write under the lock that yields to whatever it finds |
| R-4 | Major | An installation created without an explicit project records `{}`; a later release that makes the *same* value explicit is refused as a change, though the namespace is identical. | Acknowledged, out of scope — see below |
| R-5 | Minor | `rfcs/INDEX.md` named `COMPOSE_PROJECT` where the variable is `<PRODUCT>_COMPOSE_PROJECT`. | Fixed |

**R-1, R-2 and R-3 are one defect wearing three hats**, and the shape is worth
keeping: *deriving* a value and *recording* it are different acts with different
rules. Derivation must happen on every path, including a plan; recording must
happen on none of the read paths, and only under the lock. Fusing them made each
rule break the other — the dry-run exception took the derivation with it, the
write escaped the lock because the derivation had to happen early, and the path
that could not derive silently recorded nothing at all.

**R-4 is real and is not fixed here.** Comparing *effective* options rather than
declared ones means asking the adapter to resolve them — only it knows that
`project` falls back to the product name — which is a fourth capability on a
port whose surface this wave has already grown twice, and a decision row nobody
has ruled. The refusal is in the safe direction, names the key, and a vendor can
clear it by leaving the redundant value out. Carried.

**One process failure, recorded rather than fixed quietly.** `git checkout --`
during the sweep ate the three uncommitted fixes for R-1 to R-3, and they had to
be written twice. That is the third time this project's own rule — *commit
before you sabotage* — has been broken by the same command, in the same way.

**One CI flake, not this branch's.** `Acceptance (real Docker)` failed on
`assert_running 0` immediately after `docker compose -p demo stop` reported both
containers stopped; a re-run of the same commit passed, and the same script
passes locally. The assertion has no settle window between the stop and the
count. Carried as a fragility rather than fixed here.

## Rules distilled

- **A key removed from a shape takes its meaning with it, and the default that
  filled it keeps running.** Deleting per-runtime key names deleted `project`
  from the new spelling while `ApplyDefaults` still supplied one — so the
  surface disappeared and the behaviour did not. (D-016 → D-018)
- **A documented migration is code.** "Move the files and delete the block"
  executed exactly as written renames every volume a deployment owns; nothing in
  the manager was wrong, and the instruction was. (D-019)
- **A precondition discovered inside a step is reported as a step failure.**
  Compensation rewrites the outcome, and the exit code an operator scripts
  against becomes "nothing happened". Ask before the operation; keep the
  in-operation check for the paths that bypass the door. (A-2)
- **A build tag hides a compile error from every lane that does not set it.**
  Two files referencing a removed field survived `go build ./...`, `go vet` and
  `just ci`; the fast loop cannot see a tagged tree, so the tagged build is its
  own check. (A-6)
- **A plan must be audited for writes, not just for output.** The adoption was
  correct, idempotent and invisible — and it happened during `--dry-run`. (A-1)
- **The path a defect actually arrives by is the one to test.** Every refusal
  here was covered from the `apply` side, and the sweep found the `update` side
  — a new bundle from a vendor — carrying no test at all. That is how the
  hazard reaches a real machine. (A-5)
- **When a fix moves an ABI, check whether it moved out of a gate.** Making the
  hook variable runtime-supplied would have silently created a second,
  undocumented ABI beside the one RFC 0007 built gates for. (D-020)

## Carried into the next unit

- **The field-deprecation gap** — D-017. `runtime:` is deprecated, nothing warns,
  and the mechanism has to be chosen first. This is now the only thing between
  P2 and complete.
- **A named removal release for `runtime:`**, without which "deprecated" is a
  word in a doc.
- **Row 14, outstanding since wave 27** — the generalisation this wave supplied
  three more instances of.
- **`Installation.Providers`** — still declared, still unwritten (D-011).
- **P1b item 4** — the `EnvironmentFile`-on-tmpfs measurement, still behind a
  bootable venue, and now the only thing before **P3, the Quadlet adapter**.
- ~~**`RuntimeSpec.Project` and the `COMPOSE_PROJECT` ABI**~~ — closed by this
  wave.
