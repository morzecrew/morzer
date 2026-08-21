# Wave 31 · Where the units live

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

Branch `feature/wave-31-where-the-units-live`. RFC 0030 row 3, and the
reconciliation backlog: RFC 0023 row 14, outstanding since wave 27.

**Drift count: 0.** Nothing the RFCs settled was built otherwise. Row 3 was
`OPEN` and is now answered; row 14 was a proposal and is now a row.

## D-026 — Row 3 answered by pricing the move, not by preferring a directory

- **Touches:** RFC 0030 row 3 (`OPEN` → answered), §8.4
- **RFC said:** open. The row named what `/usr/lib/systemd/system` would buy —
  masking, drop-in overrides — and what it would make urgent, but not what it
  would break on a machine that already exists.
- **Built:** the units stay in `/etc/systemd/system`, and the constant is now
  pinned by a test.
- **Because:** two costs the row never priced, found by reading the adapter
  rather than the document.
  - **The old copy keeps winning.** systemd loads `/etc/systemd/system` above
    `/usr/lib/systemd/system` — read on systemd 261, systemd.unit(5): files
    higher in the list override files of the same name lower down. Every
    existing machine has its units in `/etc`, so after a move the manager would
    write to `/usr/lib` and systemd would go on loading the `/etc` copy. Every
    later change would look applied and not be, and nothing in the RFC said who
    removes the old file.
  - **The move re-enables what the operator switched off.** `InstallUnits`
    decides freshness by the file's presence in the unit directory, and
    `EnableNew` enables only the fresh ones — row 1's guarantee. After a move no
    unit is in the new directory, so every unit is fresh and every unit is
    enabled, silently reversing every `systemctl disable` on the machine. Row 1's
    harm, arriving once per machine through a migration.
- **Class:** spec-gap. Both were knowable before any code existed; the row was
  written about what the directory *means* and never about what changing it
  does. An open question priced on only one side reads as balanced.
- **Consequence:** `systemctl mask` stays unavailable on a generated unit,
  permanently rather than pending. The cost is bounded by rows 1 and 4, which
  between them give the operator two ways to say "off" that work — so masking is
  a mechanism for an intent already expressible.
- **Deliberately not applied:** a `doctor` check that notices an attempted mask.
  It would fire on no healthy machine, and it could not detect the case anyway —
  a refused `mask` leaves nothing behind to find. §3's argument against permanent
  warnings applies with more force to a check that cannot see what it claims to.

## D-027 — The unit directory was a default nothing tested

- **Touches:** RFC 0030 row 3, `internal/adapters/supervisor/systemd`
- **RFC said:** nothing; the constant is implementation.
- **Built:** `TestGeneratedUnitsLiveWhereAnAdministratorsUnitsLive`.
- **Because:** measured before writing it — changing `UnitDir` to
  `/usr/lib/systemd/system` and running the **entire** suite passes. Every
  construction of the supervisor in every test injects `WithUnitDir`, because
  relocating the directory is exactly what makes the adapter runnable without
  root. The seam that makes the tests possible is the seam that hides the value
  production uses.
- **Class:** spec-gap.
- **Consequence:** the path is now a decided value with a guard, and the failure
  message says to change the RFC first.

## D-028 — A test that would write into the source tree if it regressed

- **Touches:** wave 30's D-025 work, found by wave 31
- **RFC said:** nothing.
- **Built:** `TestTheBaselineWriteStopsWhenItCannotReadTheInstallation` now sets
  `Paths` to a temp directory.
- **Because:** `saveInstallation` writes its report file to
  `d.Paths.InstallationFile()` *before* it reaches the state store, and a zero
  `Paths` resolves that to a relative path. The test set no `Paths` because the
  guard means it never gets that far — but during the sabotage run that removed
  the guard, it serialised a blank installation into this repository's own
  `internal/lifecycle/ops/` directory. Found because the file was still sitting
  untracked in the working tree a day later.
- **Class:** spec-gap. The fixture was specified by what the passing path needs,
  and the failing path is the one the test exists for.
- **Consequence:** the artifact was evidence as well as litter — it is what a
  corrupted record actually looks like: `schema_version: 0`, empty id, empty
  product, carrying the baseline that was being adopted.

## D-029 — A distinction with only one half tested

- **Touches:** `internal/adapters/supervisor/systemd`, found by wave 31's sweep
- **RFC said:** nothing.
- **Built:** `TestRemoveUnitsReportsAFailureThatIsNotAMissingFile`.
- **Because:** `os.Remove` has three outcomes here and the code treats each
  differently — the file went away (removed), it was already gone (tolerated),
  anything else (reported). Two were covered:
  `TestRemoveUnitsStopsAndDisablesBeforeDeleting` writes the unit and deletes it
  successfully, and `TestRemoveUnitsToleratesAUnitThatWasNeverInstalled` takes
  the missing-file branch. The third — a removal that fails for any other reason
  — had no test, so deleting the branch that reports it survived the sweep. The
  why is the finding: a three-way outcome tested twice looks fully covered, and
  the untested one is the only branch that carries an error.

  Corrected in review: the first pass of this entry said all three existing
  tests took the tolerant branch, which is wrong — one takes the success path
  and a third refuses the name before removal is reached. The conclusion held
  and the evidence for it did not, which is the more embarrassing of the two.
- **Class:** spec-gap.
- **Consequence:** a removal that genuinely fails now says so instead of
  reporting the unit gone. What a swallowed failure left behind is a unit file
  surviving the uninstall that claimed to remove it, which systemd goes on
  honouring — the same class as an old unit shadowing a new one (D-026), reached
  by a different route. The fixture is a non-empty directory standing where the
  unit file should be, which makes `os.Remove` fail for a reason that is not
  "not there", without root.

## D-030 — A row answered in three places and stale in three others

- **Touches:** RFC 0030 §5, §11, review round 1 of PR #55
- **RFC said:** row 3 is `OPEN`, in the decision table, in §5's preamble, in
  §11's phasing, in the status header, in the index entry and in the index row.
- **Built:** all six updated.
- **Because:** answering a row means editing the row, and a document repeats its
  own status wherever a reader might need it without scrolling. Review caught
  §5's preamble; re-grepping for the claim rather than trusting the fix caught
  §11's phasing, which no reviewer had flagged.
- **Class:** spec-gap — knowable, and knowable mechanically. The RFC's own
  redundancy is a feature for readers and a hazard for editors.
- **Consequence:** the check that finds this is grepping the *claim* after the
  change, not re-reading the diff. A diff shows what moved; only a search shows
  what should have moved and did not.

## Reconciliation — 2026-08-17

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0030 | 3 | **Accepted** | ✅ ANSWERED | Generated units stay in `/etc/systemd/system`; `systemctl mask` stays unavailable | D-026 |
| 0023 | 14 | **Accepted** | `ASSUMED` | Where the manager would state which runtime a machine uses, it asks the adapter; an optional capability is the mechanism | D-014 |

**RFC 0030 is complete.** Row 3 was its last open question, so answering it
closed the document: every row now carries an outcome. Its status, its index row
and its place in the live-design list were all updated in the same change, which
is the half of reconciliation that gets forgotten — a row answered in a table
while the header still says "in progress" is drift of the kind this log exists to
prevent, arriving in the document rather than in the code.

**Row 14 is closed after four waves.** It was proposed by wave 27 and carried by
28, 29 and 30, each of which added an instance rather than a reason. What settled
it was noticing that the three instances — `ToolRequirer`, `HookVarSupplier`,
`OptionResolver` — were reached independently and landed on the same mechanism,
which makes the row a record of a practice rather than the introduction of one.

**One alternative was declined:** moving the units to `/usr/lib/systemd/system`
to restore masking. Recorded here because a refusal is written down nowhere else,
and because the argument for it — the one the row itself makes — is good until
the migration is priced.

## Rules distilled

- **An open question priced on one side reads as balanced.** Row 3 named what
  moving would buy and never what it would break, and sat open for that reason
  rather than because the answer was hard. Price both sides before grading a row
  `OPEN`. (D-026)
- **A seam that makes a thing testable is a seam that hides what production
  uses.** Every test relocated the unit directory, which is what let them run
  without root — and left the real value unexercised by all of them. (D-027)
- **Grep the claim, not the diff.** A status repeated in six places is edited in
  three and stays wrong in the others; the diff looks complete because every
  line in it is right. (D-030)
- **A branch tested twice out of three reads as covered.** `os.Remove` is
  handled three ways and two had tests, so the only branch carrying an error was
  the one nobody had written. Count the outcomes, not the tests. (D-029)
- **A fixture is specified by the failing path, not the passing one.** A test
  whose guard holds never reaches the code that needed the fixture, so the
  omission only shows up the day the guard breaks. (D-028)
- **An artifact left in the working tree is evidence before it is litter.** The
  blank installation this repository was carrying is what the corruption looks
  like, and reading it was faster than reasoning about it. (D-028)

## Carried into the next unit

- ~~**Row 14, outstanding since wave 27**~~ — accepted this wave.
- **The RFC 0018 proposal from wave 29's D-023**, still unruled. Now the oldest
  outstanding proposal in this file.
- **The removal of `runtime:` in 0.4.0** — a dated commitment nothing enforces.
- **`Installation.Providers`** — still declared, still unwritten (D-011).
- **P1b item 4** — the `EnvironmentFile`-on-tmpfs measurement, still behind a
  bootable venue, and still the only thing before **P3, the Quadlet adapter**.
  Note P3 lands units of its own, and D-026 is now the precedent for where they
  go and why a move is expensive.
- **Two settle-window fragilities**, carried from waves 28 and 29: the
  acceptance suite's `assert_running`, and `TestTCPProbeAgainstRedis`.
- **`init --dry-run` plans against a bundle it has not read** (wave 29 A-4).
- **`saveInstallation` writes its report before the state store**, so a failed
  state write leaves a report that disagrees with it. Noticed while fixing D-028
  and not chased; it is a real ordering question, not a test artifact.
