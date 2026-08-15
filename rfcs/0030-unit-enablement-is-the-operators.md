# RFC 0030 — Unit enablement is the operator's

- **Status:** 🚧 In progress — **rows 1, 4 and 5 answered and executed
  2026-08-15**; row 2 was answered by [#42](https://github.com/morzecrew/morzer/pull/42)
  and **row 3 (the unit directory) stays OPEN**. Reconciliation no longer
  re-asserts enablement on a unit that already exists, so `systemctl disable`
  is durable between repairs; `policy.skip_scheduled_backups` is the declarative
  way to have no backup timer at all; and `backup.freshness` honours that
  declaration instead of warning for ever. §8 is the design, §5 carries the
  grades.
- **Scope:** Who decides whether a generated systemd unit is *enabled*, and
  where the manager's authority over its own units ends. Covers the
  re-enablement the reconciliation performs, the directory the units are
  written to and what that directory costs, and whether an installation should
  be able to declare "no scheduled backups" at all. Deliberately not about what
  the units *contain* — that is the manager's, uncontested — and not about
  installing units on a machine that has none, which
  [0016](0016-update-checking-and-unattended-updates.md) and `init
  --install-units=false` already settled.
- **Related:** [`internal/adapters/supervisor/systemd/systemd.go`](../internal/adapters/supervisor/systemd/systemd.go)
  (`UnitDir`, `InstallUnits`), [`internal/lifecycle/ops/settings.go`](../internal/lifecycle/ops/settings.go)
  (`refreshUnits`, `unitParams`), [0016](0016-update-checking-and-unattended-updates.md)
  (the update timer, and the rule that a machine managing no units keeps
  managing none), [0026](0026-fleet-as-a-read-model.md) (the fleet timer, which
  widened where reconciliation runs from), [0027](0027-desired-state-in-a-repository.md)
  (whether this belongs in the installation document at all)
- **Origin:** Found 2026-08-14 while making the backup schedule settable. The
  question asked was "how should an operator turn scheduled backups off"; the
  answer turned out to be that they already can, and the manager undoes it.

---

## 1. Problem

`morzer config set backup.schedule=…` now moves the backup window, and unsetting
it returns the nightly default rather than turning backups off. The obvious next
question — *how do I turn it off* — has a surprising answer: **the operator
already can, with their own init system, and the manager overrules them.** Since
[#42](https://github.com/morzecrew/morzer/pull/42) it at least says so, which is
an improvement and not an answer: being told on every `doctor` run that a
decision you made is about to be reversed is not the same as it standing.

That is not a missing feature. It is the manager asserting authority it was
never given, over the one part of a unit that is unambiguously a local decision.

## 2. Current state

`refreshUnits` renders the unit set and calls `InstallUnits`, which writes each
file and then calls `systemctl enable` for every unit whose spec asks for it.
The set of unconditionally-enabled units is `<product>.service` and
`<product>-backup.timer`; the update and fleet pairs are *generated
conditionally* and removed when their precondition goes away, which is a
different and correct mechanism.

Reconciliation runs from `config set`, and — since 0026 P4 — from `backup target
add` and `backup target remove`.

`ports.UnitState` carries an `Enabled` field, parsed by the systemd adapter from
`UnitFileState`. When this RFC was drafted nothing read it, and a disabled unit
was invisible as well as overruled. **That half changed under it:** review of
[#42](https://github.com/morzecrew/morzer/pull/42) reached the same conclusion
independently, and `doctor`'s unit check now reports a loaded unit whose
supervisor asked for enablement and did not get it — `<unit>: not enabled`. The
oneshot services are exempt, because enabling one runs it at every boot.

So the state this RFC argues about is no longer "invisible and overruled". It is
**visible and overruled**, which sharpens row 1 rather than settling it: the
only thing that clears the new warning is the next reconciliation re-enabling
the unit, so an operator who meant to disable it is now told about it on every
`doctor` run *and* has their decision reversed on the next `config set`.

## 3. What was measured

Taken 2026-08-14 against systemd 261. The `systemctl` invocations are run with
`--root` against a scratch tree, which exercises the same enable/mask logic
without a live daemon; the reconciliation half is measured against the real
adapter through the lifecycle layer.

1. **The reconciliation re-enables on every run.** An unrelated
   `morzer config set update.check=true` issues:

   ```text
   systemctl daemon-reload
   systemctl enable demo.service
   systemctl enable demo-backup.timer
   systemctl stop/disable demo-update.service, demo-update.timer
   systemctl stop/disable demo-fleet.service, demo-fleet.timer
   ```

   So `systemctl disable --now demo-backup.timer` holds until the next setting
   change, target addition or target removal, and is then undone with no
   message.

2. **An operator cannot mask a morzer-managed unit at all.** This was the
   surprise, and it is the opposite of what was expected:

   ```text
   $ systemctl mask demo-backup.timer
   Failed to mask unit: File '/etc/systemd/system/demo-backup.timer' already exists
   exit 1
   ```

   `mask` is implemented as a symlink to `/dev/null` at
   `/etc/systemd/system/<unit>`, and that is exactly where `UnitDir` puts the
   generated file. The manager's unit occupies the path a mask needs.

3. **`mask --runtime` works and does not survive a reboot.** It writes to
   `/run/systemd/system`, which outranks `/etc` and is tmpfs. So the one
   spelling that takes effect is the one that silently stops taking effect the
   next time the machine restarts.

4. **The expected failure is unreachable in this arrangement.** With the unit in
   `/usr/lib/systemd/system` instead, masking succeeds, `is-enabled` reports
   `masked`, and a later `enable` fails with exit 1 — which would make `config
   set` fail outright. morzer does not use that arrangement, so the reasoned
   prediction that "masking breaks `config set`" is **false today**. It becomes
   true the moment §5's directory question is answered the other way, which is
   why it is recorded here rather than discarded.

**Net: there is no durable way for an operator to stop a morzer timer using
their own init system.** `disable` is undone, `mask` is refused, and
`mask --runtime` evaporates at the next boot.

## 4. Severity, stated honestly

Low, today, and it is worth saying why rather than implying urgency the
measurements do not support.

The only unconditionally-enabled units are the product's own service and the
backup timer, so every re-enablement errs toward *the product runs and it gets
backed up*. The dangerous direction is already handled by a different mechanism:
turning off `update.check` **removes** the update pair rather than re-enabling
it, and the fleet pair goes with the last target.

What is not low is the principle. A manager that quietly reverses an operator's
`systemctl disable` is a manager whose behaviour cannot be reasoned about from
the host's own tools, and the next unit added under the same rule may not point
in a safe direction.

## 5. Decisions

Rows 1, 4 and 5 were answered together on 2026-08-15, in that order, and the
order is the content: row 4 could only be answered once row 1 was, and §8.2
records why. Row 3 is untouched and stays OPEN. The trade-off column is left as
it was written, so that what was traded is legible next to what was chosen.

| # | Question | Grade | The trade-off |
| --- | --- | --- | --- |
| 1 | Does the manager re-assert *enablement* on a unit that already exists? | ✅ ANSWERED — no (§8.1) | Enabling only units this run newly wrote makes `systemctl disable` stick — and means a unit left disabled by a half-finished install is never repaired, which is the case `init --repair` exists for. Enablement is either the operator's or the manager's; it cannot be both, and today it is the manager's — announced, since row 2, but still the manager's. |
| 2 | Should `UnitState.Enabled` be reported? | ✅ ANSWERED — yes, in [#42](https://github.com/morzecrew/morzer/pull/42) | Shipped as `<unit>: not enabled` on a loaded unit the supervisor asked to have enabled, with the oneshot services exempt. It was the smallest useful step and it decided nothing about row 1. Its risk was recorded here as hypothetical and is now live: an operator who *meant* to disable a unit gets a warning on every run, clearable only by letting the next reconciliation overrule them. That is the antipattern 0026's audit removed from this very check, arriving by a different door — and it is the strongest argument for answering row 1 rather than leaving it. |
| 3 | Do the generated units belong in `/etc/systemd/system`? | OPEN | The current choice is deliberate and documented: machine-specific files belong where local configuration lives. §3.2 measured its cost — it consumes the path a mask needs. `/usr/lib/systemd/system` would restore masking and drop-in overrides, and would make row 1 urgent rather than optional, because then a masked unit makes reconciliation fail. |
| 4 | Should an installation be able to declare "no scheduled backups"? | ✅ ANSWERED — yes, `policy.skip_scheduled_backups` (§8.2) | A declarative flag (`policy.skip_scheduled_backups`, named for the unsafe direction as `SkipBackupBeforeUpdate` is) puts the fact in the installation, where `init --repair` reproduces it and 0027's desired-state story can carry it. Against: it is a *second* way to say something the operator can already say, and adding it without answering row 1 leaves the first way broken and the two disagreeing. |
| 5 | If backups are handled elsewhere, what does `doctor` say? | ✅ ANSWERED — it honours the declaration (§8.3) | A machine backed up at the storage layer never updates the last-backup timestamp, so `StaleBackupAfter` warns for ever. Suppressing that means the manager asserting a fact it cannot verify. Not suppressing it means a permanent warning — which is precisely how a check stops being read. |

**Row 4 is downstream of row 1**, and answering it first is the mistake this RFC
exists to prevent. It was answered second, and §8.2 records what that bought.

## 6. Non-goals, and what reopens each

- **Managing units on a machine that has none.** `init --install-units=false` is
  a supported choice and `refreshUnits` already honours it by refusing to act
  when `InstalledUnits` is empty. Untouched here.
- **Unit *contents*.** What goes in the file is the manager's, uncontested: it
  encodes the exit-12 rule, the schedules and the manager's own path. This RFC
  is only about enablement and location.
- **A general "morzer owns nothing" mode.** *Reopens if:* somebody wants to
  generate units and hand them over entirely, which is a packaging question
  rather than a lifecycle one.

## 7. Risks

- **Deciding row 4 alone.** A flag that turns the timer off, on a machine where
  `systemctl disable` is still undone, gives an operator two switches with
  different behaviour and no way to tell which one is in force.
- **Reading §4's "low severity" as "no action".** The severity is low because of
  which units happen to be unconditional today. That is a property of the
  current unit set, not of the design, and it changes silently when a unit is
  added.
- **This RFC being read as a plan.** ~~It is a record of measurements and open
  questions. Nothing in it is scheduled~~ — true until 2026-08-15, when rows 1,
  4 and 5 were answered and executed. §3's measurements are still the code as it
  shipped *before* that, which is what makes them worth keeping: they are the
  evidence for §8 rather than a description of the current behaviour.

## 8. The design

Answered 2026-08-15. Three rows, and they are one design: each of the second and
third is only available because the one before it was settled.

### 8.1 Enablement is the operator's between repairs (row 1)

`InstallUnits` takes the scope of its enablement from the caller:

```go
// EnableScope says which of the units in a call may be enabled.
type EnableScope int

const (
    // EnableNew enables only units this call created. The zero value.
    EnableNew EnableScope = iota
    // EnableAll re-asserts enablement on every unit whose spec asks for it.
    EnableAll
)

InstallUnits(ctx context.Context, units []Unit, scope EnableScope) error
```

Reconciliation — `config set`, `backup target add`, `backup target remove` —
passes `EnableNew`. `init`, including `init --repair --install-units`, passes
`EnableAll`.

So `systemctl disable demo-backup.timer` is durable: it survives every setting
change, and the one thing that reverses it is a command whose entire purpose is
to reverse things somebody broke. §3.1 measured the old behaviour; the new one
issues no `enable` at all on a reconciliation whose unit files already exist.

**Why this rather than keeping the authority and adding a switch.** The
alternative considered was to leave reconciliation as it was and give the
operator a morzer-shaped way to turn the timer off. It fails on §3's net
finding: `disable` is undone, `mask` is *refused* because the generated file
occupies the path a mask needs, and `mask --runtime` evaporates at the next
boot. A morzer-shaped switch leaves all three standing and only marks the rake.
A root user's explicit action on their own machine being silently reversed is
the part that cannot be reasoned about from the host's own tools, and §4 says
what makes that expensive: it is a property of which units happen to be
unconditional today, and it changes silently when a unit is added.

**What it costs, stated plainly.** A unit left disabled by a half-finished
install is no longer repaired by the next unrelated `config set`. That is the
case `init --repair` exists for, `doctor` already prints it as the remedy, and
the check from row 2 is what makes the state visible in the first place. The
zero value being `EnableNew` is deliberate: a caller added later that forgets to
say gets the conservative behaviour rather than the overruling one.

**What it is not.** Unit *contents* and unit *existence* are still fully the
manager's, reconciled on every run exactly as before. Enablement was a third
thing travelling on the same call, and this separates it — it does not weaken
convergence, it relocates the part of convergence that overrules a person.

### 8.2 The declaration removes the unit (row 4)

`policy.skip_scheduled_backups` — named for the unsafe direction, as
`SkipBackupBeforeUpdate` is, so that a field absent from a hand-edited file or a
record written by an older manager means *backups are scheduled*.

When it is set, the backup service and timer are not generated, and
reconciliation removes them — the mechanism `update.check=false` and the fleet
pair already use. Settable as `morzer config set backup.scheduled=false`;
unsetting returns to scheduled, which is the safe direction.

**This is what row 1 bought.** The recorded objection to a flag was that it
would be "a second way to say something the operator can already say", and it
was correct while `systemctl disable` was undone — two switches, different
behaviour, no way to tell which is in force. Now they say different things: the
declaration decides whether the unit *exists*, `disable` decides whether an
existing unit *runs*, and both are durable. That is one mechanism per question,
which is why answering row 4 first would have been the mistake §5 says it is.

### 8.3 `doctor` honours what it can verify (row 5)

`backup.freshness` warns when the newest backup is older than
`policy.stale_backup_after`. On a machine backed up at the storage layer that
warning is permanent, and a permanent warning is how a check stops being read.

It now reports, rather than warns, when the installation declares
`skip_scheduled_backups`. The distinction row 5 turns on is preserved exactly:
the manager is not asserting that backups happen elsewhere — it is declining to
assert that they do not, on the strength of a declaration the operator wrote and
it can read.

**`backup.target-freshness` deliberately does not honour it.** The declaration says no
backups are *scheduled*; it does not say none are taken. A backup that exists
and is on no target is still every copy of the data sitting on one machine, and
that is worth saying however the backup was started.

**The unit check needs no exception.** With the timer no longer generated,
nothing expects it, so `<unit>: not enabled` cannot fire for it. Row 2's warning
survives for the case it was written for — a unit this installation *does* want,
switched off — and it is now clearable in both directions: `init --repair
--install-units` re-enables it, or the declaration removes the want. Its remedy
line says both.

## 9. Tests

- The adapter, at the level the scope is implemented: installing a set twice
  issues `enable` on the first call and none on the second, and `EnableAll`
  issues it both times. This is where a regression would land.
- The fake supervisor conforms: an already-installed unit keeps its enablement
  across an `EnableNew` install, and neither implementation ever disables. A
  fake that resets enablement on every install would make every suite test agree
  with a production path that does not.
- Through the lifecycle: `systemctl disable` survives a `config set`, and
  `init --repair` re-enables. These are the two sentences §3.1 measured false.
- The declaration removes both units, `init --repair` reproduces it, and
  `backup.freshness` stops warning while `backup.target-freshness` does not.

## 10. Docs

The operating page on backups gains the two ways to stop scheduled backups and
what each one means. The `config set` reference gains `backup.scheduled`. The
installation reference gains the policy field.

## 11. Phasing

One wave. Row 1 must land before row 4 in the same change, not in a later one:
shipping the declaration onto a machine where `disable` is still undone is the
two-switch state §7 names as the risk.

Row 3 (the unit directory) stays OPEN and is deliberately not bundled. It is the
one row that changes where files are written, it would make masking work, and
§3.4 measured that it also makes a masked unit fail `config set` outright —
which is a new failure mode, on a machine the operator has already told to stop.
That deserves its own decision.
