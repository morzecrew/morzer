# RFC 0030 — Unit enablement is the operator's

- **Status:** 📝 Draft — design not locked, nothing scheduled. Written because
  the measurements in §3 were taken and are worth keeping; the decision they
  point at is §5 and it is deliberately still open. Row 2 was since answered by
  [#42](https://github.com/morzecrew/morzer/pull/42), which made a disabled unit
  visible; the other four rows are untouched by that and row 1 is now harder to
  leave open, not easier.
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

Nothing here is locked, and only row 2 has been answered — by #42, after this
table was written. The rest are the questions, with the trade-off each one turns
on, so that whoever picks this up is not re-deriving them.

| # | Question | Grade | The trade-off |
| --- | --- | --- | --- |
| 1 | Does the manager re-assert *enablement* on a unit that already exists? | OPEN | Enabling only units this run newly wrote makes `systemctl disable` stick — and means a unit left disabled by a half-finished install is never repaired, which is the case `init --repair` exists for. Enablement is either the operator's or the manager's; it cannot be both, and today it is the manager's — announced, since row 2, but still the manager's. |
| 2 | Should `UnitState.Enabled` be reported? | ✅ ANSWERED — yes, in [#42](https://github.com/morzecrew/morzer/pull/42) | Shipped as `<unit>: not enabled` on a loaded unit the supervisor wanted enabled, oneshots exempt. It was the smallest useful step and it decided nothing about row 1. Its risk was recorded here as hypothetical and is now live: an operator who *meant* to disable a unit gets a warning on every run, clearable only by letting the next reconciliation overrule them. That is the antipattern 0026's audit removed from this very check, arriving by a different door — and it is the strongest argument for answering row 1 rather than leaving it. |
| 3 | Do the generated units belong in `/etc/systemd/system`? | OPEN | The current choice is deliberate and documented: machine-specific files belong where local configuration lives. §3.2 measured its cost — it consumes the path a mask needs. `/usr/lib/systemd/system` would restore masking and drop-in overrides, and would make row 1 urgent rather than optional, because then a masked unit makes reconciliation fail. |
| 4 | Should an installation be able to declare "no scheduled backups"? | OPEN | A declarative flag (`policy.skip_scheduled_backups`, named for the unsafe direction as `SkipBackupBeforeUpdate` is) puts the fact in the installation, where `init --repair` reproduces it and 0027's desired-state story can carry it. Against: it is a *second* way to say something the operator can already say, and adding it without answering row 1 leaves the first way broken and the two disagreeing. |
| 5 | If backups are handled elsewhere, what does `doctor` say? | OPEN | A machine backed up at the storage layer never updates the last-backup timestamp, so `StaleBackupAfter` warns for ever. Suppressing that means the manager asserting a fact it cannot verify. Not suppressing it means a permanent warning — which is precisely how a check stops being read. |

**Row 4 is downstream of row 1**, and answering it first is the mistake this RFC
exists to prevent.

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
- **This RFC being read as a plan.** It is a record of measurements and open
  questions. Nothing in it is scheduled, and the code it describes is the code
  as it shipped.
