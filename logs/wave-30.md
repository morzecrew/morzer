# Wave 30 · The options as the runtime reads them

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

Branch `feature/wave-30-effective-runtime-options`. RFC 0023, against decisions
15 and 16. Closes R-4, carried from wave 28.

**Drift count: 0.** Nothing the RFC settled was built otherwise. One entry
(D-024), and it amends §6 rather than departing from a row — with the author's
ruling, before the code was written.

**The readiness gate did not fire.** Two load-bearing decisions rather than
three, so this was two questions and not a halt. Both were put before any code
existed and both came back as recommended. Worth recording that the gate not
firing is also a result: three waves of firing had made it the expected outcome,
and a practice whose alarm is always on is one nobody reads.

## D-024 — The comparison runs on resolved options, and §6 was measuring the wrong surface

- **Touches:** RFC 0023 decisions 15 and 16 (`LOCKED`), decision 7 (`LOCKED`),
  §6.
- **RFC said:** decision 16 refuses a release that changes a recorded option.
  It did not say *which* form of the option is compared, because until the
  adapter gained a default there was only one form.
- **Built:** `ports.OptionResolver`, an optional capability. Both the recorded
  baseline and the candidate go through it before they meet, so what is compared
  is what the runtime will read. A runtime that declines keeps the old
  declared-against-declared comparison.
- **Because:** an installation created with no `project` is already running
  under its product name, so a release writing that name out in full renames
  nothing — and the manager refused it, telling a vendor to restore a value that
  was never doing any work. The manager cannot see this alone: knowing that
  `project` falls back to the product is precisely the knowledge decision 7
  keeps out of these layers.
- **Class:** `spec-gap`.
- **Consequence:** the recorded baseline stays as the vendor declared it, so no
  schema bump, no migration, and `installation describe` publishes what it
  always did — at the cost that a future change to an adapter's default moves
  *both* sides of the comparison and a real rename would pass unnoticed. And the
  port gained its eighth optional capability, which is the second half of this
  entry.
- **Deliberately not applied:** recording the *effective* options at schema 11,
  put to the author. Refused because the migration cannot run where the state
  package lives — `internal/infra/state` has no adapter to ask — and because it
  changes a published artifact.
- **The second half.** §6's escape hatch fires when "the second adapter forces
  more than two new methods onto `ports.Runtime`". Measured while adding this
  one: **13 core methods and 8 optional capabilities**, against 12 and 5 when §6
  was written. One of its two spare method slots is spent (`Name()`, P2) and
  every other growth has gone where the instrument does not look. Amended to
  count both halves, with today's numbers as the baseline P3 is measured
  against. The condition is unchanged and still forward-looking — *what the
  second adapter forces* — so recording a larger surface does not fire the
  hatch; it makes it able to fire.
- **Proposed row (RFC 0023, row 20):** `LOCKED`. **Amendment to §6**, recorded
  in §13.

## Decision-row outcomes

**Ruled 2026-08-17, before any code was written.** Two questions, both accepted
as recommended.

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | 20 | **Accepted** | `LOCKED` | The comparison runs on resolved options; the installation keeps recording what was declared | D-024 |
| 0023 | §6 | **Accepted** | — | The escape hatch counts core methods and optional capabilities alike | D-024 |

**One alternative was declined:** recording effective options at installation
schema 11.

**Row 14 is still outstanding**, and was not put again for the third wave
running. It has now been carried longer than it took to build everything it
generalises, which is itself the argument for either putting it or dropping it.

## Self-audit — 2026-08-17

Scope: the whole branch — the port, the adapter, the comparison, the fake, the
shared battery, docs and records. `just ci` green at **86.5%** (floor 84),
`docs-check` 41 pages / 55 checks, `runtime-check` **17 mentions, 0 branches**,
unchanged: this wave added no runtime vocabulary above the adapters. Full
acceptance passed and the container lane passed, each run on its own.

**Sabotage sweep: 10 mutations, 10 killed — two only after being made
compilable, and one only after being made killable.**

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Major | **An adapter that resolved options in place broke no test.** The map handed to `ResolveOptions` is the installation's own recorded baseline, and `persistRuntimeBaseline` writes that map back — so a resolving *read* could have declared an adapter's default onto disk, turning a deployment that named no project into one that names its own default. The contract battery asserts non-mutation, but its real-adapter leg is behind the `docker` build tag and no untagged lane ran it. | Fixed — asserted in the adapter's own untagged test and at the ops boundary, where it holds for any runtime rather than this one |

**One process failure, recorded rather than hidden.** Acceptance was started
while the container lane was still running, and both failed — "cannot start
services" and "cannot restart services", one step apart in the same daemon. This
project already knows those two lanes cannot share Docker; running them
concurrently to save wall-clock is how that knowledge gets rediscovered. Both
pass run sequentially, and the first two runs are reported here rather than
quietly replaced by the second.

**What remains distrusted.**

- **The recorded baseline is what the vendor declared**, by decision. If an
  adapter ever changes a default, `resolve(recorded)` moves with it and both
  sides of the comparison shift together — a real rename would pass unnoticed.
  This is the accepted cost of not bumping the schema, and nothing detects it.
- **One implementation.** Only Compose implements `OptionResolver`; the decline
  path is exercised by a wrapper that hides the capability, not by a second
  adapter that genuinely has no defaults.
- **A pure capability's conformance is gated behind Docker.** `ResolveOptions`
  needs no daemon, but the battery's real-adapter leg is docker-tagged as a
  whole, so the fast loop cannot see it. That is precisely why A-1 survived.

## Rules distilled

- **A comparison between what somebody typed and what the machine does needs a
  translator, and only one layer has it.** Declared and effective are different
  values, and the layer holding the record is deliberately the one that cannot
  tell them apart. (D-024)
- **An instrument that measures one half of a surface reports health while the
  other half grows.** §6 counted methods; seven of the eight things added to the
  port since were capabilities. Ask what a threshold *cannot* see before
  trusting that it has not been crossed. (D-024)
- **A fake that duplicates an adapter's rule needs a battery that asks both the
  same question.** The lifecycle layer may not import an adapter, so its tests
  run against a copy of the rule — and a copy that drifts makes those tests
  agree with a manager that refuses the wrong releases. (D-024)

## Carried into the next unit

- **The §6 baseline, now recorded**: 13 core methods and 8 optional
  capabilities. P3 is measured against it, and the hatch can finally fire.
- **Row 14, outstanding since wave 27** — not put again for the third wave
  running. It has now been carried longer than it took to build everything it
  generalises, which is the argument for either putting it or dropping it.
- **The RFC 0018 proposal from wave 29's D-023**, still unruled.
- **The removal of `runtime:` in 0.4.0** — a dated commitment nothing enforces.
- **`Installation.Providers`** — still declared, still unwritten (D-011).
- **P1b item 4** — the `EnvironmentFile`-on-tmpfs measurement, still behind a
  bootable venue, and still the only thing before **P3, the Quadlet adapter**.
- **Two settle-window fragilities**, carried from waves 28 and 29: the
  acceptance suite's `assert_running`, and `TestTCPProbeAgainstRedis`.
- **`init --dry-run` plans against a bundle it has not read** (wave 29 A-4).
- ~~**The contract battery cannot exercise a pure capability without Docker**~~ —
  closed in review, see D-025.
- ~~**R-4, an option made explicit with the adapter's own default**~~ — closed by
  this wave.

## Review round, PR #54 — appended after the group closed

The sections above were written when the branch was pushed. What follows was
found afterwards, by review, and is appended rather than folded in: an entry
moved up into the execution record would claim the wave noticed it, and the
wave did not.

**Drift count: still 0** — nothing here was settled by the RFC and built
otherwise. Both findings are gaps the RFC never covered.

## D-025 — A boundary that trusted its adapters, found in review

- **Touches:** RFC 0023 decision 20, PR #54 review round 2
- **RFC said:** nothing; the port documents the rule and the battery checks it
- **Built:** `resolveRuntimeOptions` now hands the adapter a copy, and the
  option half of the contract battery runs untagged against the real adapter
- **Because:** two findings, both correct, both against code this wave added:
  - `checkRuntimeOptions` passed `inst.RuntimeOptions` to the resolver
    directly. Every resolver in this repository copies before it writes, so
    the boundary looked correct under every test that used one — a test of the
    adapters wearing the shape of a test of the boundary. Reproduced with a
    resolver that writes in place: the installation's record gained a `project`
    it never declared, from a check that only asked a question, and
    `persistRuntimeBaseline` would have written it to disk.
  - `ResolveOptions` needs no daemon, but the real-adapter leg of the battery
    is behind `docker` wholesale, so the shared rule was only ever checked
    against the real adapter where Docker was present. This is the item this
    same wave recorded as carried; review found it independently, which is the
    argument for closing it now rather than later.
- **Class:** spec-gap — both were knowable before the code existed. The first
  is the sharper one: the wave's own sabotage sweep found the *adapter* could
  mutate and fixed it there, and stopped. It never asked what the layer would
  do if an adapter misbehaved anyway.
- **Consequence:** one map copied per comparison. The port now states the
  non-mutation rule and states that the manager does not rely on it.
- **Deliberately not applied:** the same review asked that row 20 be reopened —
  an adapter that changes its default between an installation's creation and
  its update moves both sides of the comparison together, and the volumes stay
  under the old default. That is real, it is the consequence row 20 already
  records, and it is the cost of the ruling that declined schema 11. It is not
  reversed in review. `TestADefaultThatChangesUnderAnInstallationIsNotDetected`
  now pins the permissive behaviour so the gap is a failing test the day
  somebody closes it, rather than a paragraph.

**Rule distilled:** *a contract the boundary cannot enforce is one every future
implementation can break* — and a suite whose only implementations are
well-behaved cannot tell a guarded boundary from a lucky one. (D-025)
