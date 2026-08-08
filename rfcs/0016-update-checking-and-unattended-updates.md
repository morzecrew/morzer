# RFC 0016 — Update checking and unattended updates

- **Status:** 📝 Draft — design proposed
- **Scope:** Three separable capabilities, in increasing order of risk.
  `update --check` tells an operator a release exists, consuming
  `ReleaseSource.List` — a port method that is implemented, commented as
  existing for exactly this, and called by **nothing**. Channel following
  watches one mutable OCI tag through `Resolve`, which is what a vendor
  iterating against a sandbox actually needs and what `List` structurally cannot
  provide. And unattended apply, opt-in, refused unless the release **declares**
  it cannot require a human, with a relaxed variant confined to a declared
  **dev mode** that an installation can leave and can never enter. Covers the
  gate, the mode, the systemd timer, lock contention and the phone-home
  question. Does **not** add fleet rollout, canaries, auto-rollback on a later
  health regression, or a notification channel — that is
  [0015](0015-notifications.md), and P3 is gated on it.
- **Related:** [`internal/ports/source.go`](../internal/ports/source.go) (`List`,
  `Resolve`) ·
  [`internal/adapters/source/oci/oci.go`](../internal/adapters/source/oci/oci.go)
  (the only implementation of `List`) ·
  [`internal/domain/version.go`](../internal/domain/version.go)
  (`Compatibility`, `CheckUpgrade`, `AssessRollback`) ·
  [`internal/domain/installation.go`](../internal/domain/installation.go)
  (`Policy`, and the `SkipBackupBeforeUpdate` precedent) ·
  [`internal/adapters/supervisor/systemd/systemd.go`](../internal/adapters/supervisor/systemd/systemd.go)
  (the backup timer this copies) · [0001](0001-update-and-rollback.md) (the rule
  that the database is never rolled back automatically — the constraint this
  whole RFC bends around) · [0015](0015-notifications.md) (prerequisite for P3) ·
  [0014](0014-building-a-release-bundle.md) (the versions a channel points at) ·
  [0011](0011-bundled-container-images.md) (the customers this does **not**
  serve)

---

## 1. Summary

An operator running morzer has no way to learn that a new release exists. The
port method that would tell them has been implemented since
[0004](0004-distribution-and-verification.md) and has never been called.

This RFC wires it up, adds a cheaper mechanism for the sandbox loop that `List`
cannot serve, and then — separately, opt-in, and gated on a property the release
must declare about itself — lets an update apply without a human.

The load-bearing decision is the gate. Not "patch versions only", which is a
proxy: **a release is auto-applicable only if it declares `rollback_safe: true`
and its declared schema range leaves the previous release able to read what its
migrations produce.** That is the property that decides whether a failure needs
a person, and the manifest already carries it.

## 2. Motivation

**A built capability with no consumers.** `ReleaseSource.List` is on the port
([`source.go:39`](../internal/ports/source.go)). The OCI adapter implements it
against the registry tag list, and its comment says why it exists at all:
"This is the one transport that *can* answer, because a registry keeps a tag
list — which is why `List` is on the port at all"
([`oci.go:156-162`](../internal/adapters/source/oci/oci.go)). `local` and `https`
return `ErrUnsupported`. Nothing in `internal/cli` or `internal/lifecycle` calls
it. It was built for update checking and then not used.

**`status` and `doctor` know everything about the machine and nothing about the
world.** `doctor` checks secrets, storage, runtime, systemd units and backup
targets — secret rotation, free space, registry reachability, unit state. None
of them is "the release you are running is four versions behind."

**The alternative operators reach for is worse.** Absent a pull mechanism, the
way to update a customer VM on a schedule is CI reaching *in* over SSH. That
needs inbound access to a customer machine, a long-lived customer credential
sitting in the vendor's CI, and it does not work behind NAT. A pull is strictly
better on every one of those axes, which is the argument for building this at
all rather than an argument about convenience.

**And the sandbox loop is a different problem wearing the same clothes.** A
vendor pushing dev builds to a test VM wants "has the `dev` tag moved?", many
times a day. `List` cannot answer that: it parses each tag as a version and
**skips the ones that are not**, by design and with good reason
([`oci.go:160-161`](../internal/adapters/source/oci/oci.go)) — so a mutable
`dev` tag is invisible to it. That is not a defect in `List`; it means version
discovery and channel following are two operations, and only one of them exists.

## 3. Current state

Verified against `86da400`.

| Fact | Where |
| --- | --- |
| `List` is on the port, implemented only by OCI, and **has no callers** | [`source.go:39`](../internal/ports/source.go), [`oci.go:162`](../internal/adapters/source/oci/oci.go) |
| `List` skips tags that are not semver, so a mutable channel tag is invisible to it | [`oci.go:160-161`](../internal/adapters/source/oci/oci.go) |
| `Resolve` returns version, digest and size **without downloading**, and is already called by `fetch` and `update` | [`source.go`](../internal/ports/source.go), [`release.go:292`](../internal/cli/release.go) |
| `update` requires an explicit ref or `--to`; there is no "latest" selection anywhere | [`update.go`](../internal/lifecycle/ops/update.go) (`resolveUpdateTarget`) |
| `Compatibility` carries `RollbackSafe`, `DatabaseSchemaMin`, `DatabaseSchemaMax`, and `CheckUpgrade`/`AssessRollback` are written and tested against them | [`version.go:262-264`](../internal/domain/version.go), [`compatibility_test.go`](../internal/domain/compatibility_test.go) |
| `AssessRollback` reads `current.RollbackSafe` — the flag of the release being rolled **off** | [`version.go:356`](../internal/domain/version.go) |
| `release fetch` verifies against installation policy, including signature, before the bundle lands, and removes it on failure | [`release.go:320-335`](../internal/cli/release.go) |
| The systemd adapter already generates a **timer** — `OnCalendar`, `Persistent=true`, `RandomizedDelaySec=900` | [`systemd.go:312-322`](../internal/adapters/supervisor/systemd/systemd.go) |
| `RestartPreventExitStatus=12`: exit 12 is requires-manual-intervention and must not restart-loop | [`systemd.go:283`](../internal/adapters/supervisor/systemd/systemd.go), [`errors.go:323`](../internal/domain/errors.go) |
| `Policy` already carries `RequireSignature`, `SigningKeys`, `SkipBackupBeforeUpdate`, `RetainReleases` | [`installation.go:103-131`](../internal/domain/installation.go) |
| Nothing prunes the release store after an update; it defers to a manual `release prune` | [`update.go:718`](../internal/lifecycle/ops/update.go) |

Two of these are the design, not background. The timer means the scheduling
mechanism is a **sibling of a working one**, including `RandomizedDelaySec`,
which already solves the thundering-herd problem when many customers poll one
registry. And `RestartPreventExitStatus=12` is the codebase's existing statement
of the principle this RFC has to obey: *a system that needs a human must stop and
wait for one.*

## 4. Goals / Non-goals

**Goals**

- An operator can learn that a release exists, from `status`, `doctor`, or a
  command.
- A vendor can point a sandbox at a channel and have it follow, cheaply.
- An update can apply without a human **only** when the release declares that a
  failure will not require one.
- Nothing here changes what `update` does once it starts.

**Non-goals**

- **Fleet orchestration.** Waves, canaries, per-customer scheduling. One machine
  decides for itself.
- **Auto-rollback on a later health regression.** `update` already compensates a
  failed update; a product that degrades an hour later is a different problem.
- **A notification channel.** [0015](0015-notifications.md).
- **Changing the update operation.** The gate decides *whether* to invoke it.
  Everything inside stays as [0001](0001-update-and-rollback.md) shipped it.
- **Making the database rollback-able.** [0001](0001-update-and-rollback.md)
  decided it is not, deliberately. This RFC works within that, which is why the
  gate looks the way it does.

## 5. Design

### 5.1 `update --check` — version discovery

```
morzer update --check [ref]
```

Calls `List`, filters to versions the installed release's compatibility admits
(`CheckUpgrade`, already written), and reports the highest. `--json` carries the
structured answer for a script.

It also becomes a `doctor` check, `release.update-available`, at **info** rather
than warn — being behind is not a fault, and a check that warns forever until an
operator updates is how a green report stops being read.

`local` and `https` return `ErrUnsupported` from `List`, and the command reports
that honestly: "this transport cannot enumerate versions." Reporting "up to
date" for a source that cannot answer would be the worst possible outcome of
this feature.

### 5.2 Channel following — a different operation

```yaml
update:
  channel: oci://registry.example/demo/bundle:dev
```

Following a channel is `Resolve` on a fixed reference, comparing the returned
digest against the installed release's. It does not enumerate anything, it does
not parse tags as versions, and it works with a mutable tag — which `List`
structurally cannot (§2).

**Mutable pointer, immutable versions.** The tag moves; the bundles behind it
each carry a distinct version, because [0014 §5.2](0014-building-a-release-bundle.md)
derives one per build. So the never-republish refusal is untouched: it is doing
its job on the versions, while the channel does its job on the pointer. This is
exactly how a container registry's own `latest` works, and it is why the two
designs compose instead of fighting.

The cost is one `Resolve` per tick. Whether that is affordable at a five-minute
cadence is a property of the vendor's registry, not of the manager — so there is
**no hardcoded interval floor**. What the documentation must say is that a
registry with pull-rate limits counts manifest requests, so a cadence chosen
without regard to the registry will exhaust a budget that other things need.

### 5.3 The auto-apply gate

An update is auto-applicable only if **all** of these hold:

| Condition | Source |
| --- | --- |
| `CheckUpgrade` reports no problems | Already run by `update` today |
| The target declares `rollback_safe: true` | `target.Compatibility.RollbackSafe` |
| The **current** release can still read the schema the target's migrations will produce | `current.DatabaseSchemaMax >= target.DatabaseSchemaMin` |
| `policy.require_signature` is true and `policy.signing_keys` is non-empty | `Policy` |
| The pre-update backup is not disabled | `!policy.skip_backup_before_update` |

The first three are one idea: **after this update, could a human be required?**
That is precisely what `AssessRollback` answers
([`version.go:356-373`](../internal/domain/version.go)) — it sets
`RestoreRequired` when the installed release's migrations are one-way, or when
the previous release cannot read the current schema. The gate is that assessment
run *predictively*, before the update, using the target's `database_schema_min`
as the stand-in for the schema its migrations will leave behind.

That stand-in is a pessimistic approximation and is named as one: the exact
post-migration schema is not knowable before the migration runs, and
`database_schema_min` is the lowest schema the target supports, which its
migrations must therefore reach. A release whose migrations go further than its
own declared minimum would defeat it — which is unresolved question 2.

**Why not a version range.** "Auto-apply patch releases" is a proxy for "this
release has no migrations", and it is a bad one in both directions: a patch
release can carry an emergency one-way migration, and a minor release often
carries none. The manifest already lets a vendor state the real property. Gating
on the proxy would mean ignoring a declaration in favour of guessing.

**Signature is required, not recommended.** Auto-apply hands the vendor
unattended root — hooks run as root, and an update runs the target bundle's
migrate hook. A compromise of the vendor's registry or key becomes root on every
customer machine with no human in the path; today the operator pasting a command
is a control, however weak. So enabling auto-apply with `require_signature:
false` is **refused**, in the same shape `--skip-backup` requires `--force`
today.

Anything that fails the gate is still **fetched and staged** — `release fetch`
already verifies against installation policy before the bundle lands
([`release.go:320-335`](../internal/cli/release.go)) — and then notified. The
risky, slow, credential-using part happens ahead of time; the human decides only
when downtime happens. That middle state is where most of the value is, and it
carries almost none of the risk.

### 5.4 Dev mode

A machine may declare itself a sandbox. Spelled as a field on `Installation`
rather than on `Policy`, because `Policy` is what `morzer config` may change and
this may not be:

```yaml
mode: dev        # absent means production
```

**Absence means production.** This is the rule
[`installation.go:120-131`](../internal/domain/installation.go) already states
for `SkipBackupBeforeUpdate`, in a comment worth quoting because it is the same
hazard: the field was originally named for the safe direction, "where the zero
value — a field absent from a hand-edited file, a record written before the
field existed — meant *do not back up*, and the one place a missing bool decides
something is the one place it must not decide that."

**It is chosen at `init` and it is one-way: an installation can leave dev mode
and can never enter it.** The asymmetry is the point. Dev mode's relaxations
mean the machine's history is untrusted — backups may have been skipped,
releases aggressively pruned — so a machine that has been running as production
must not be downgradable into a mode that tolerates data loss. And the drift
this guards against only goes one direction: sandboxes become demo boxes become
production, never the reverse. Leaving dev mode is a normal edit; entering it
requires a fresh `init`.

`status` and `doctor` mark it permanently and prominently. Not a first-run
notice — a persistent line, because the failure mode is a machine nobody
remembers the provenance of.

Neither `mode` nor the `update` block bumps `InstallationSchemaVersion`. The
rule the field states is "bumped only when the on-disk shape changes in a way a
previous manager would **misread**"
([`installation.go:9`](../internal/domain/installation.go)), and both of these
misread in the safe direction: an older binary sees no `mode` and treats the
machine as production, and sees no channel and polls nothing — which it would
not do anyway, since it generates no timer. This is the opposite of
[0015 decision 9](0015-notifications.md), where absence means an operator is
silently not told, and the contrast is the point: the bump is a judgement about
which direction the omission fails in, not a reflex on every new field.

**What dev mode relaxes:**

- Poll cadence — no floor.
- Auto-apply without §5.3's rollback gate. The machine is disposable; that is
  what it is for.
- No maintenance window.
- `skip_backup_before_update` becomes permissible without `--force`.
- Aggressive retention, which also addresses a real problem: nothing prunes
  after an update ([`update.go:718`](../internal/lifecycle/ops/update.go)), and a
  fast dev loop accumulates one release directory per build — with an `images/`
  layout each, once [0011](0011-bundled-container-images.md) lands.
- Prerelease versions are admissible update candidates. In production the
  default is stable-only, since [0014](0014-building-a-release-bundle.md) makes
  every dev build a prerelease.

**What dev mode never relaxes: the verification chain.** Signature checking,
digest pinning, `SHA256SUMS` completeness — all unchanged. The sandbox's entire
value is fidelity; every relaxation of verification reduces what a successful
test proves, and the thing being rehearsed is the customer's install. Sign dev
builds with a dev key that the sandbox installation pins. That is one
`minisign -Sm` in CI, and it rehearses key handling too.

### 5.5 The timer

`<product>-update.service` (`Type=oneshot`) and `<product>-update.timer`,
generated by the same supervisor that already generates the backup pair
([`systemd.go:294-322`](../internal/adapters/supervisor/systemd/systemd.go)).
`Persistent=true` catches up a tick missed while the machine was off;
`RandomizedDelaySec` spreads customers across a window so a vendor's registry
does not see every installation at the same second.

**`OnCalendar` is the maintenance window.** No new mechanism: an operator who
wants updates only on Sunday mornings writes that expression. This is worth
stating because "add a maintenance window" is the obvious next feature request
and systemd already expresses it better than a config field would.

A host without systemd is not an error and does not get the feature — the same
way unit installation is already skipped when `Available()` returns false.

**Lock contention: back off, never queue.** A tick that cannot take the
operation lock logs and exits **0**. The next tick is soon, and an update
waiting behind an operator's interactive backup is an update that starts at an
unpredictable moment. Exiting non-zero would also fight `Restart=on-failure`.

Exit 12 handling is inherited: a run that ends in requires-manual-intervention
must not be retried by the supervisor
([`systemd.go:283`](../internal/adapters/supervisor/systemd/systemd.go)).

### 5.6 Checking is off by default

An update check contacts the vendor's registry, which reveals an IP, a
timestamp, and by inference an installed version. For a self-hosted product
whose customers chose self-hosting, turning that on by default would be a
phone-home nobody agreed to. It is opt-in, and the documentation says what it
discloses.

## 6. Tests

- **`--check` against a registry with several tags** — highest admissible
  version reported, non-semver tags ignored, a version the compatibility gate
  refuses excluded rather than offered.
- **A transport that cannot list reports so**, and does not report "up to date".
  This is the failure that would be worst and least visible.
- **The gate, as a table**: `rollback_safe: false` refuses; a schema range the
  previous release cannot read refuses; `require_signature: false` refuses to
  even enable; each of them staged-and-notified rather than silently skipped.
- **Enabling auto-apply without a signing key is refused at configuration
  time**, not at update time. A machine that accepts the setting and then always
  refuses to act is worse than one that refuses the setting.
- **Dev mode cannot be entered.** An installation without `mode: dev` cannot
  acquire it by `config set`, by hand-editing, or by import — three paths, three
  assertions. Hand-editing is the one that matters, since the file is
  operator-facing.
- **A tick that cannot take the lock exits 0** and journals why.
- **Channel following**: a moved tag with an unchanged digest is a no-op; a
  moved tag with a new digest is a candidate. The first assertion is what stops
  a poll from re-fetching every tick.
- **Prerelease admissibility** differs between modes, driven by
  [0014](0014-building-a-release-bundle.md)-shaped versions.

## 7. Docs

- `operating/updating.md` gains checking, channels and the unattended section —
  and the unattended section must lead with what it costs, not what it saves.
- A new `operating/unattended-updates.md` if that section outgrows the page: the
  gate, why it is the gate, the signature requirement, and the maintenance
  window as an `OnCalendar` expression.
- `authoring/publishing.md`: what `rollback_safe: true` now additionally means
  to a vendor. It has always gated `rollback`; it now also decides whether a
  release may install without a human, which raises the cost of declaring it
  carelessly. This is the most important documentation change in the RFC.
- `reference/installation-commands.md` and the installation-file reference: the
  `update` block and `mode`, including that `mode` cannot be turned on.

## 8. Out of scope

- **Fleet rollout, waves, canaries.** One machine decides for itself. What would
  change it: a vendor operating enough customer machines to need staged
  exposure, which is a control plane and not a CLI.
- **A vendor-side recall.** "Stop installing 1.4.2, it is bad" has no channel
  here beyond moving the tag back. A real recall needs the manager to consult
  something after it has already resolved a version, which is a new trust
  relationship.
- **Auto-rollback on a health regression after a successful update.** `update`
  compensates a failure during the operation; a product that degrades later is a
  monitoring problem.
- **Scheduling `doctor`.** A natural sibling — this RFC generates a timer and
  [0015](0015-notifications.md) forwards `check` events — and deliberately not
  bundled, because it needs deduplication policy that neither RFC has.
- **Bandwidth or storage policy for staged releases.** Staging every candidate
  fills a disk. Dev mode's aggressive retention covers the sandbox; production
  is bounded by how often a vendor releases.

## 9. Risks

- **Unattended root.** This is the risk; everything else is detail. An update
  runs the target bundle's hooks as root. The mitigations are the mandatory
  signature, the declared-property gate, and the fact that the gate defaults
  closed — and none of them helps against a vendor whose signing key is stolen.
  An operator for whom that is unacceptable should use §5.1 and §5.2 and never
  §5.3, which is why they are separately phased.
- **A vendor who declares `rollback_safe: true` carelessly.** The flag now
  carries more weight than it did when [0001](0001-update-and-rollback.md)
  introduced it, and its meaning has been widened without the vendors who
  already set it being asked. The documentation change in §7 is the only
  mitigation and it is weak; a stronger one would be a manifest `api_version`
  bump, which is a large hammer for one field's semantics.
- **Dev mode escaping to production.** Mitigated by the one-way rule and the
  permanent marker, not eliminated: a machine that was *always* dev mode and
  quietly became load-bearing is the case neither guard catches.
- **This serves the opposite customer from [0011](0011-bundled-container-images.md).**
  Polling needs registry credentials living permanently on the customer's
  machine — precisely what 0011 exists because vendors often cannot grant. The
  two serve disjoint customer sets. Worth noting that a bundle credential and an
  image credential are separate, so a public `oci://` bundle carrying private
  bundled images is coherent; but a customer who cannot reach a registry at all
  cannot use anything in this RFC.
- **A timer firing mid-session.** Bounded by the lock and the back-off, but an
  operator debugging at 03:00 will meet it once.
- **Notification is not guaranteed.** [0015 decision 6](0015-notifications.md)
  makes delivery at-most-once. An unattended update that needs a human may
  therefore need one who was never told. The journal is the durable record and
  the design must not pretend otherwise — which is why the gate refuses anything
  that *could* require a human, rather than allowing it and relying on the
  message.

## 10. Unresolved questions

1. **Should `--check` be able to run without a configured channel or source?**
   The installation does not record where the current release came from, so
   `--check` with no argument has nothing to query. Recording the source ref at
   install time is a small state addition with a schema bump, and it is what
   makes the `doctor` check work unattended. Leaning toward recording it.
2. **Is `database_schema_min` a sound stand-in for the post-migration schema?**
   It is the lowest schema the target supports, so its migrations must reach it
   — but a release whose migrations go *beyond* its declared minimum would slip
   past the gate. The alternative is a new manifest field stating what the
   migrations produce, which is more honest and asks vendors for something they
   have never been asked for.
3. **Does entering dev mode need to be impossible, or merely loud?** Impossible
   is the safer rule and it forecloses a legitimate case: a production machine
   being deliberately retired into a test fixture. The escape hatch is a fresh
   `init`, which loses the installation id — acceptable for a machine being
   retired, and the question is whether anyone will disagree.
4. Should staged-but-not-applied releases count against `retain_releases`?
   Staging is what makes the middle mode valuable, and a retention policy that
   prunes the staged candidate makes it pointless.

## 11. Decisions

| # | Decision | Rationale and consequence |
| --- | --- | --- |
| 1 | Three separately phased capabilities: check, follow, apply | They differ by orders of magnitude in risk, and an operator who wants the first must not have to accept the third. |
| 2 | `--check` uses `List`; channel following uses `Resolve` | They are different operations: `List` skips non-semver tags by design, so it cannot follow a mutable channel. Consequence: a channel is a single ref, not a tag pattern. |
| 3 | A channel is a **mutable pointer to immutable versions** | Composes with [0014 §5.2](0014-building-a-release-bundle.md), which gives every build a distinct version, so the never-republish refusal keeps working untouched. |
| 4 | **No hardcoded poll interval floor** | The cost belongs to the vendor's registry, not the manager. Consequence: the documentation must state that rate-limited registries count manifest requests, because nothing enforces it. |
| 5 | Auto-apply is gated on the release **declaring** it cannot require a human — `rollback_safe: true` plus a schema range the previous release can still read — not on a version range | A version range is a proxy for "has no one-way migration"; the manifest states the real property. Consequence: `rollback_safe`'s meaning widens for vendors who already set it, which the docs must carry. |
| 6 | The gate is `AssessRollback` run **predictively**, with `database_schema_min` standing in for the post-migration schema | Reuses a written, tested function rather than a second compatibility judgement. Consequence: a pessimistic approximation that unresolved question 2 may replace with a declared field. |
| 7 | Enabling auto-apply **refuses** without `require_signature` and a pinned key | Unattended apply hands the vendor unattended root. Refused at configuration time, not at update time — matching how `--skip-backup` requires `--force`. |
| 8 | Anything failing the gate is **fetched, staged and notified**, never silently skipped | Moves the network, the credentials and the verification off the human's critical path while leaving the downtime decision with them. This is where most of the value is. |
| 9 | Dev mode is on `Installation`, not `Policy`, and **absence means production** | `Policy` is what `config` may change; this may not be. Absence-means-safe is the rule [`installation.go:120-131`](../internal/domain/installation.go) already learned the hard way for `SkipBackupBeforeUpdate`. |
| 10 | Dev mode is **one-way**: leavable, never enterable | Its relaxations make the machine's history untrusted, and sandbox→production drift only goes one direction. Consequence: retiring a production machine into a test fixture requires a fresh `init`. |
| 11 | Dev mode relaxes scheduling, downtime, backups and retention; it **never** relaxes verification | The sandbox's value is fidelity — a relaxed verification chain means the rehearsal is not of the thing being shipped. Consequence: dev builds must be signed, with a dev key. |
| 12 | `OnCalendar` **is** the maintenance window | systemd already expresses it better than a config field would, and the timer is a sibling of the backup timer that already ships. |
| 13 | A tick that cannot take the lock **exits 0** | The next tick is soon; queueing makes the start time unpredictable, and a non-zero exit would fight `Restart=on-failure`. |
| 14 | Update checking is **off by default** | It contacts the vendor's registry, which for a self-hosted product is a phone-home nobody agreed to. |

## 12. Phasing

- **P1 — `update --check`.** Consumes `List`, adds the `doctor` check and the
  `status` line. No new trust, no new mechanism, gated on nothing. This is the
  phase that turns an unused port method into a feature.
- **P2 — channel following and staging.** `Resolve` on a fixed ref, fetch and
  stage, notify. Gated on [0015](0015-notifications.md) for the notify half; the
  staging half is independently useful and can land first. Also the phase a
  vendor's sandbox actually needs.
- **P3 — unattended apply and dev mode.** Gated on
  [0015](0015-notifications.md) shipping, because an update that can end in
  requires-manual-intervention needs a way to ask for one.

P1 is worth shipping alone. P3 is the only phase carrying the risk in §9, and it
should stay behind an explicit opt-in even after it ships.
