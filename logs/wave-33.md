# Wave 33 · The environment file at boot

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

Branch `feature/wave-33-the-environment-file-at-boot`. RFC 0023 P1b item 4.

A spike. The deliverable is a measurement, not an implementation — `irreducible`
in the classification below: no amount of design settles what systemd does at
boot, and a guess would have been carried into P3's unit graph where discovering
it wrong costs a released adapter.

**Drift count: 0.** Nothing was built that a document settled otherwise. No
production code changed.

## D-038 — Item 4 answered a different question than it asked

- **Touches:** RFC 0023 §4.3, §12 item 4, decision 21 (new)
- **RFC said:** the rendered `EnvironmentFile` "must survive a reboot before the
  unit starts or the unit fails on a cold boot", and called this "the single
  hardest problem in the RFC".
- **Measured:** a tmpfs is empty at every boot by definition. The file never
  survives a reboot and never had to. What decides the outcome is whether the
  unit reading it is ordered behind whatever renders it, and what an absent file
  does when it is not.
- **The mechanism**, on the host (systemd 261) and again in the venue:
  `EnvironmentFile=` fails the unit before the process runs
  (`Failed to load environment files`, `Result=resources`); `EnvironmentFile=-`
  starts it, reports success, and runs with the parameter empty.
- **The ordering**, across four independent boots: an unordered unit with the
  dash **started and finished before the render unit had written the file**, and
  systemd marked it active.

  ```
  12:19:48.417342  Starting product B (unordered, dash)...
  12:19:48.417797  Starting render parameters (stands in for apply --startup)...
  12:19:48.423114  Finished product B (unordered, dash).
  12:19:48.425017  Finished render parameters (stands in for apply --startup).
  ```

  `B_PORT=[]` on a unit reporting success is the whole finding.
- **Class:** `irreducible`, and it behaved like one. The item had been carried
  since wave 26 because it could not be reasoned about, and one boot settled it
  — while also reversing the premise the section was written on.
- **Consequence:** decisions 21 and 22. The prefix is the difference between a
  deployment that refuses and one that lies (21, LOCKED), and P3's generated
  units owe the file's presence before any unit that reads it starts (22,
  ASSUMED) — the invariant, not a mechanism, which is the correction D-042
  records. Compose satisfies it for free, because its product unit's
  `ExecStart` *is* `apply --startup`. **P1b is complete and P3 is no longer
  gated.**

## D-039 — The venue is not the one the item asked for

- **Touches:** RFC 0023 §12 item 4
- **RFC said:** a venue that can be booted on demand.
- **Built:** a privileged container running systemd as PID 1, not
  `systemd-nspawn`.
- **Because:** nspawn needs root, and this environment has no password-less
  sudo. The substitute runs the same systemd, mounts `/run` as a real tmpfs
  (`rw,nosuid,nodev,noexec,relatime,mode=755`), and executes a real boot
  transaction, which is what the ordering question needs. It boots in about a
  second, which is what the item actually wanted when it rejected a workstation
  — "the answer costs a reboot per attempt".
- **Class:** `spec-gap` in the item's own phrasing: it named a tool where it
  meant a property.
- **Consequence, stated rather than buried:** this is not bare metal. It answers
  unit ordering and unit-start semantics. It cannot speak to firmware, initrd,
  or real device mounts, and was asked nothing about them. A future item that
  needs those needs a different venue, and item 4's answer does not cover it.
- **Deliberately not applied:** waiting for hardware. The two halves item 4
  named were both answerable here, and holding a four-wave blocker for a venue
  that would answer the same questions identically is a cost with no finding
  attached.

## D-040 — The spike is kept, because a measurement nobody can re-run is a claim

- **Touches:** `spikes/environmentfile-at-boot/`
- **Built:** the venue, its units and a `run.sh`, committed.
- **Because:** every number in item 4 came out of it, and an RFC that cites
  measurements no reader can reproduce is asking to be trusted rather than
  checked. Re-run from a clean slate before committing: images removed, script
  run, same three outcomes.
- **Class:** `discovery` — only building it revealed how cheap repeated boots
  are, which is what makes keeping it worthwhile rather than archiving a
  transcript.
- **Consequence:** it is not wired into `just ci`. It needs a privileged
  container and answers a question that is now settled; running it on every
  commit would spend a minute to re-derive a locked decision.

## D-041 — A shell script the shell linter did not look at

- **Touches:** `justfile`, found while committing D-040
- **Built:** `just shellcheck` now covers `spikes/*/*.sh`.
- **Because:** the recipe checked `install.sh` and `.github/scripts/*.sh`, so it
  passed green over a script it had never read. Committing the spike added a
  shell script to the repository that the repository's own shell gate did not
  see — and `just shellcheck` reporting success is exactly what would stop
  anybody from checking by hand.
- **Class:** `spec-gap`. The recipe enumerated the two places shell scripts
  lived when it was written, which is a list that silently stops being complete.
- **Consequence:** verified by mutation rather than assumed — an unquoted
  `$(dirname $0)` in the spike is now caught (SC2046, SC2086), and was not
  before.

## D-042 — A spike locked a mechanism it had not built

- **Touches:** RFC 0023 decision 21 as first written, split into 21 and 22
- **Built (first):** one row, `LOCKED`, saying the parameter file is referenced
  without the `-` prefix **and** that any unit reading it is ordered behind
  whatever renders it.
- **Built (now):** two rows. 21 keeps the prefix, `LOCKED`. 22 states the
  invariant — a reader must not start before the file exists — as `ASSUMED`,
  with the mechanism left to P3.
- **Because:** the single row graded a measured fact and an unbuilt design
  obligation the same way. The prefix is systemd's behaviour, observed four
  times; ordering-behind-the-render is one mechanism among several, and it was
  inferred from the fixture that happened to be convenient to write. A generator
  emitting its own ordering, `RequiresMountsFor=`, rendering earlier in the
  boot, or Quadlet inlining values instead of referencing a file would all
  satisfy the same invariant. A spike that has built no adapter is the
  worst-placed thing to foreclose the design of one.
- **Class:** `spec-gap` in the row, and the failure mode `flag-dont-flip` names
  outright: grade inflation. LOCKED rows earn their force by being rare, and one
  sitting beside a row that did not need it weakens the one that did.
- **Consequence:** row 21 also gained a scope it was missing — it bans the
  prefix on the *parameter* file, not everywhere, because `-` stays right for a
  genuinely optional file such as an operator override drop-in. The first
  version would have forbidden a pattern nothing here objects to.
- **Found by:** the author's review of the proposal, which is the step this wave
  had skipped — see below.

## Reconciliation — 2026-08-19

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | 21 | **Accepted, scoped** | `LOCKED` | The `EnvironmentFile` carrying parameters is referenced without the `-` prefix | D-038 |
| 0023 | 22 | **Accepted, regraded** | `ASSUMED` | A reader must not start before the file exists; the mechanism is P3's | D-038, D-042 |

**This wave got the reconciliation backwards, and the entry above exists because
of it.** Rows 3, 13, 14 and 20 were each put to the author before they were
written. Row 21 was written straight into the RFC, `LOCKED`, on the strength of
a measurement — and a measurement is exactly what makes an executor most
confident and least inclined to ask. The author's ruling split it, downgraded
half, and scoped the half that survived, none of which the log would have
recorded had the row simply been committed.

**One alternative was declined:** locking the ordering mechanism now, on the
argument that P3 will need one anyway. Declined because "will need something
here" is not the same as "must use this", and the RFC has no way to distinguish
them once a row says LOCKED.

## D-043 — The spike claimed reproducibility and floated

- **Touches:** D-040, `spikes/environmentfile-at-boot/`, found in review
- **Built:** the base image pinned by digest, and a build-time assertion that
  the venue's systemd is the version the measurement was taken on.
- **Because:** D-040's whole argument for committing the spike is that "a
  measurement nobody can re-run is a claim". It was built `FROM
  archlinux:latest` with an unpinned `pacman -Sy systemd`, so a re-run could
  quietly boot a different userspace and a different systemd, and report the
  result as the same experiment. That is worse than not keeping the venue: the
  second answer arrives wearing the first one's authority.
- **Class:** `spec-gap`, and a self-inflicted one — the entry arguing for
  reproducibility shipped the mechanism that would have broken it.
- **Consequence:** the venue is pinned to a digest and **fails the build** if
  systemd is not 261, rather than silently measuring something else. Verified by
  re-running the whole spike from a clean slate against the pinned image: same
  three outcomes, same ordering, which is the fourth boot and the one that makes
  the pin evidence rather than an assertion.
- **Also:** the runner used a fixed container name, so two invocations could
  destroy each other's container — or an unrelated one that happened to share
  the name. Now unique per run.

## D-044 — A row that would have sent P3 at the wrong mechanism

- **Touches:** RFC 0023 row 22, PR #59 review round 2
- **Built:** `RequiresMountsFor=` removed from row 22's list of mechanisms, with
  a note saying why it is not one.
- **Because:** row 22 lists the ways P3 might satisfy "the file exists before a
  reader starts", and `RequiresMountsFor=` is not one of them.
  systemd.unit(5): it "automatically adds dependencies of type `Requires=` and
  `After=` for all **mount units** required to access the specified path". That
  orders the reader after `/run` is *mounted*, which it already is when the race
  runs, and says nothing about whether anything has written into it. A P3 that
  picked it off this list would have shipped the exact race the row exists to
  prevent, believing the row had blessed it.
- **Class:** `spec-gap` in a row written one commit earlier. The row was
  authored to stop a spike foreclosing P3's design, and then offered P3 a
  mechanism that does not work — a list assembled from plausibility rather than
  from the manual page.
- **Consequence:** the wrong entry is named as wrong rather than deleted, since
  it is the one a reader is most likely to reach for independently.

## D-045 — Claiming more than the venue measured

- **Touches:** RFC 0023 row 21 and §12 item 4, and this log's distilled rules
- **Built:** three claims narrowed to the unit-level signal that was observed.
- **Because:** the spike measured that systemd reports a unit started while its
  parameter is empty. It did **not** measure what any health check does with an
  empty parameter — nothing here ran one. "Every health surface reports as fine"
  and "the one outcome nothing downstream can detect" were extrapolations, and
  they sat inside a LOCKED row, which is the worst place to keep an unmeasured
  claim: the row's authority is the measurement.
- **Class:** `spec-gap`. Review named two locations; there were three, the third
  being the distilled rule, which is the one that travels furthest.
- **Consequence:** the argument for LOCKED is unchanged and now rests only on
  what was seen — a false green at the unit level, with downstream detection
  depending on something validating the parameter, which nothing guarantees.

## D-046 — The same number, wrong in a fourth place

- **Touches:** wave 31's D-030, unapplied by this wave
- **Built:** every boot-count claim in the tree found by one grep and aligned.
- **Because:** the ordering boot count moved from three to four when pinning the
  venue forced a re-run, and it is written in five places. Review caught the
  RFC's copy, then the self-audit table's, then the distilled rule's — three
  rounds, each fixing the instance that was pointed at.
- **Class:** `drift` against this file's own recorded practice, which is the
  uncomfortable part. **D-030 distilled exactly this rule one wave ago** —
  *grep the claim, not the diff* — after a status was edited in three places and
  left stale in three others. It was written down, and then not applied to a
  number that had just changed.
- **Consequence:** the sweep is done properly now, across `rfcs/` and the
  spike's README. What the rule was missing is the trigger: it says what to do
  and not when, so it only fires for somebody already suspicious. The version
  worth keeping is that **a number that changes is a number to grep for**, at
  the moment it changes rather than at the moment somebody objects.

## Self-audit — 2026-08-19

Scope: the whole branch — one commit, no production code. A spike, so the audit
is of the *measurement*: whether the venue answers the question asked, whether
the RFC text claims more than was observed, and whether the numbers reproduce.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Medium | The claim "B started before the render" is a **race outcome, not a determinism**. Unordered units have no guaranteed order, and B won four times out of four, counting the post-pin re-run — but it could lose, in which case the unit succeeds with the correct value. Recorded, because it makes the hazard *worse* rather than milder: an intermittent silent misconfiguration is harder to catch than a reliable one. | Fixed — stated in item 4 and in row 21 |
| A-2 | Low | The venue's `/run` is mounted by Docker (`--tmpfs /run`), not by systemd as it would be on a host. The conclusion is unaffected — what matters is that it is a tmpfs and empty when the transaction begins, which held either way — but the mount's provenance differs from bare metal and the item should not imply otherwise. | Fixed — named in item 4's venue paragraph |
| A-3 | Low | The product units are `Type=oneshot` shell commands rather than containers. Deliberate: `EnvironmentFile` semantics are systemd's and do not depend on what `ExecStart` runs. Worth stating so a reader does not take the spike for a Quadlet rehearsal. | Fixed — stated in the spike's README |

**No sabotage sweep of production code**, because none changed. The one gate this
wave did touch was mutated instead: an unquoted `$(dirname $0)` in the spike is
caught by the extended `just shellcheck` and was not before (D-041).

**The container lane went red on its first run**, and it is recorded rather than
replaced by the green re-run. `TestTCPProbeAgainstRedis` failed under the full
lane and passed alone in 0.661s — the **third wave it has failed in (29, 32 and
33), and they are not consecutive**: waves 30 and 31 ran the lane clean. Counted
from the log rather than from memory, after review proposed "fourth" and the
entry it was correcting said "three consecutive" — both wrong, in different
directions. It failed here on a branch that changes no Go code at all. That it fails here, of all branches, is the
clearest evidence yet that it is measuring Docker's teardown rather than the
product, and it is now the longest-running unfixed finding in this file.

## Rules distilled

- **A measurement is when an executor is least inclined to ask.** Confidence
  earned by observing something transfers to the design conclusions drawn from
  it, and those are a different claim. Row 21 was written unruled precisely
  because the evidence behind it felt settled. (D-042)
- **Grade the fact and the obligation separately.** One row held systemd's
  observed behaviour and an unbuilt adapter's design, and gave both the grade
  the stronger half deserved. (D-042)
- **A race that keeps going the same way is still a race.** Four boots agreeing
  is not determinism, and the intermittent version of a silent misconfiguration
  is worse than the reliable one. (A-1)
- **A lint recipe that enumerates paths is a list that goes stale silently.**
  Adding a script to a repository does not add it to the gate, and the gate goes
  on reporting success. (D-041)
- **An unanswerable item is often a mis-asked one.** Item 4 was carried for
  seven waves as "can the file be read at boot". One boot showed the file is
  never there at boot, and the real question — who is ordered before whom — had
  been answerable all along. (D-038)
- **A silent success is worse than a loud failure, and costs one character.**
  `EnvironmentFile=-` turns a missing configuration into a running product that
  systemd reports as started. Whether anything downstream notices depends on
  something validating the parameter — which is a different claim, and not one
  this spike measured. (D-038)
- **An item that names a tool has hidden the property it needs.** "A machine
  with Podman", then "systemd-nspawn" — what it wanted was a boot that costs a
  second, and saying so would have unblocked it sooner. (D-039)
- **Keep the venue, not the transcript.** Numbers in a document are checkable
  only if the thing that produced them still runs. (D-040)
- **A kept venue must be pinned, or it re-runs a different experiment under the
  old one's name.** The entry arguing for reproducibility shipped a floating
  base image, which is the failure the entry existed to prevent. (D-043)
- **A number that changes is a number to grep for, when it changes.** D-030
  said grep the claim rather than the diff; this wave had the rule, changed a
  count, and still needed three review rounds to find all five copies. A rule
  without a trigger only fires for somebody already looking. (D-046)
- **A list of alternatives is a claim about each of them.** Row 22 offered P3
  four mechanisms and one of them does not work; naming it without checking the
  manual page would have blessed the race the row exists to prevent. (D-044)
- **A measurement's authority does not extend to what it did not measure.** The
  venue showed a false green at the unit level, and the prose turned that into
  a claim about every health surface — inside a LOCKED row. (D-045)
- **Count from the record, not from memory.** Two numbers in this wave were
  wrong in opposite directions — a boot count too low and a flake count too
  high — and a reviewer proposed a third that was wrong again. (D-043)

## Carried into the next unit

- ~~**P1b item 4**~~ — measured; **P3, the Quadlet adapter, is no longer gated**
  and is the whole of what remains in RFC 0023.
- **P3 owes the file's presence before any unit that reads it starts** (decision
  22), and owes it without the `-` prefix (decision 21). The mechanism is P3's
  to choose; the invariant is not. A constraint discovered before the adapter
  exists rather than after.
- **The removal of `runtime:` in 0.4.0** — a dated commitment nothing enforces.
  The oldest carried item, and untouched by this wave, which was the spike alone.
- **`Installation.Providers`** — still declared, still unwritten (D-011).
- **Two settle-window fragilities**, carried from waves 28 and 29;
  `TestTCPProbeAgainstRedis` has now failed in three waves — 29, 32 and 33 —
  which are not consecutive.
- **`saveInstallation` writes its report before the state store** (wave 31).
- **A plan over a remote reference still carries no deprecation warning** (D-035).
- **`operation.status` reports `succeeded` for a dry run whose steps are all
  `pending`** (wave 32).
