# RFC 0016 — Update checking and unattended updates

- **Status:** ✅ Complete — P1 shipped 2026-08-08: `update --check`, the
  `update.check` setting, and the refusals that make a check say "I could not
  ask" rather than "up to date". **P2 and P3 shipped 2026-08-10**: channel
  following with `ChannelPeeker`, staging, the release-notes rendering that
  carries [0002](0002-rich-terminal-renderer.md) P5, the auto-apply gate,
  `database_schema_produces`, dev mode at schema 5, and the update timer.
  **Design locked** 2026-08-08. Every question in §10 is
  resolved into §11; §5.2's cost model was wrong and is amended in §13 with the
  measurement that replaced it.
- **Scope:** Three separable capabilities, in increasing order of risk.
  `update --check` tells an operator a release exists, consuming
  `ReleaseSource.List` — a port method that is implemented, commented as
  existing for exactly this, and called by **nothing**. Channel following
  watches one mutable OCI tag through `Resolve`, which is what a vendor
  iterating against a sandbox actually needs and what `List` structurally cannot
  provide. And unattended apply, opt-in, refused unless the release **declares**
  that a failure will not need a database restore, with a relaxed variant confined to a declared
  **dev mode** that is fixed when the installation is created and never
  transitions in either direction. Covers the gate, the mode, the systemd timer,
  lock contention and the phone-home question — and carries
  [0002](0002-rich-terminal-renderer.md) P5, whose "gated on a bundle shipping a
  `RELEASE.md`" could not open by itself and whose payoff is precisely a staged
  update awaiting a decision (§5.7). Does **not** add fleet rollout, canaries,
  auto-rollback on a later health regression, or a notification channel — that is
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
  serve) · [0003](0003-secrets-recovery-and-onboarding.md) (`installation
  import`, the second creation path) · [0009](0009-backup-targets.md) (the
  export that carries backup credentials into it) ·
  [`internal/cli/installation.go`](../internal/cli/installation.go) ·
  [`internal/release/load.go`](../internal/release/load.go) (strict decoding,
  which prices the new manifest field)

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
migrations produce.** That is the property deciding whether a failure could need
a *database restore* — the one decision [0001](0001-update-and-rollback.md)
refuses to make automatically — and the manifest already carries it. It does not
promise no human is ever needed; §5.3 is explicit about the paths it cannot see.

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

```text
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

**The installation records where its current release came from.** It does not
today — nothing persists the ref an operator passed to `update` — so `--check`
with no argument has nothing to query, and the `doctor` check cannot run
unattended at all, which is the context that makes it useful.

One string, written **when a release becomes current**, not when a candidate is
staged. An earlier draft said "when a release is staged", which would have had
`--check` and the `doctor` check querying the *candidate's* source while the
installed release was still the old one — reporting on a release nobody is
running. A staged candidate needs no recorded ref of its own: it was fetched
from the configured channel (§5.2) moments earlier, and that is where it is
still reachable.

It rides the schema bump [0015](0015-notifications.md) is already making rather
than needing one of its own; see §5.4 on why that matters for `mode` too. If
this ships before 0015, it carries the bump instead.

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

**The digest `Resolve` returned is what gets fetched.** A channel is a *mutable*
tag by construction, so the tag can move between the resolve and the fetch —
and `update` does not close that today: `resolveUpdateTarget` sets
`ref.Digest = opts.ExpectDigest` ([`update.go:215`](../internal/lifecycle/ops/update.go)),
which is empty unless the operator passed `--digest`, so the fetch is unpinned
and only the *version* is compared afterwards. On the interactive path that is
tolerable — a human typed a reference seconds ago. On a polling loop watching a
tag that exists to move, it means the manager can install a bundle it never
resolved and never compared. So channel following passes the resolved digest
into the fetch and refuses a staged bundle whose digest differs from the one the
decision was made against.

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
| The target declares **`database_schema_produces`** | new field; absent means not auto-applicable |
| The **current** release can read that schema | `current.DatabaseSchemaMax >= target.DatabaseSchemaProduces` |
| `policy.require_signature` is true and `policy.signing_keys` is non-empty | `Policy` |
| The pre-update backup is not disabled | `!policy.skip_backup_before_update` |

The first four are one idea, and its name has to be exact: **could this update
end needing a database restore?** Not "could a human be required" — that was the
first draft's phrasing and it claims more than the gate delivers. An update can
still reach `requires-manual-intervention` through failures nothing here
inspects: a migration hook that exits non-zero, a health check that never
passes, a converge the engine cannot compensate. Those paths already stop and
wait for a person ([`errors.go:323`](../internal/domain/errors.go),
`RestartPreventExitStatus=12`), and this gate does not and cannot promise they
will not happen.

What it does promise is narrower and is the one that matters for running
unattended: **the failure will not be the unrecoverable kind.** A failed
unattended update that compensates back to the previous release is an incident
report; one that needs a restore decision at 03:00 is the thing
[0001](0001-update-and-rollback.md) refuses to make automatically. The gate
bounds the second, not the first.

That is precisely what `AssessRollback` answers
([`version.go:356-373`](../internal/domain/version.go)) — it sets
`RestoreRequired` when the installed release's migrations are one-way, or when
the previous release cannot read the current schema. The gate is that assessment
run *predictively*, before the update.

**Running it predictively needs a number the manifest does not currently
carry**, and that gap is where a first draft of this design went wrong. The
obvious stand-in is the target's `database_schema_min` — the lowest schema it
supports, which its migrations must therefore reach. But a release whose
migrations go *beyond* its own declared minimum slips straight through, and the
whole argument for this gate is that it rests on a declaration rather than a
proxy. Using a proxy for half of it is incoherent.

So the manifest gains one integer:

```yaml
compatibility:
  rollback_safe: true
  database_schema_min: 12
  database_schema_max: 14
  database_schema_produces: 14   # what my migrations leave the database at
```

**Absent means not auto-applicable.** No migration burden on any existing
vendor, no guessing, and it degrades in exactly the right direction: a vendor
who has never heard of unattended updates gets none, and one who wants them
states the fact that makes them safe.

**The field is not free, and the cost is not where it looks.** Manifest
decoding is strict — `ParseManifest` uses `DisallowUnknownField`
([`load.go`](../internal/release/load.go)) — so an older manager meeting a
manifest carrying this field **fails to load it**. `min_manager_version` is the
mechanism for exactly this, but it cannot help here: it is read by
`CheckUpgrade`, which runs on an already-loaded manifest, so the decode error
arrives first. The operator sees `unknown field "database_schema_produces"`
with a hint about typos, not "this release needs manager ≥ X".

That is a pre-existing property of the format rather than something this RFC
introduces — it is true of *any* new manifest field — but this is the first RFC
to add one since the constraint was noticed, so it is recorded here.

**Today it costs nothing, and that is the point.** Nothing has been released:
there are no older managers and no third-party manifests, so this field is free
in a way no field will be again. The constraint is an argument for adding it
**now** rather than a risk to accept — and, more usefully, an argument for
asking once before the first tag what *other* fields will be wanted, since after
that every one of them is a hard break with a misleading error. That sweep is
not this RFC's to run.

Two ways to soften the constraint permanently, neither in scope here: teach the
decoder to check `api_version` and `min_manager_version` before rejecting
unknown fields, or reserve an `extensions.`-style escape that strict decoding
already tolerates. Both are worth more before the first tag than after it.

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

**Mode is fixed at creation and never transitions** *through the manager*. Not
one-way — *no* way. The qualifier is load-bearing and was missing from the first
draft: `mode` is a field in a JSON file, so root can edit it, and no
schema-version check or `config` refusal detects a same-version value change.
Nothing here is tamper-evident.

That is not a hole this RFC should try to close, and adding a signed or
duplicated creation record to defend one boolean would be defending it against
an operator who can equally edit the recipient list, the backup targets, or the
installation id. Root on the machine is outside every threat model in this
corpus. The claim is therefore about the manager's own surfaces — `config set`,
`import`, every command — and §6's assertions are about those, not about
detecting a hand-edited state file.
An earlier draft of this section allowed dev → production on the reasoning that
only the reverse was dangerous. That is wrong; both directions are, in different
shapes:

| Transition | What breaks | When it surfaces |
| --- | --- | --- |
| production → dev | Real customer data now under relaxed rules — no pre-update backup, aggressive pruning, auto-apply skipping the gate | Immediately |
| dev → production | Untrusted history presented as trustworthy: you find out at rollback time that `previous` was pruned and no pre-update backup was ever taken | During an incident |

The second is quieter and lands when it costs most. Since neither is acceptable,
the field is immutable, which is also the simplest invariant to state and to
test: *mode is a property of the installation, chosen when the installation is
created.*

**Creation means `init` or `import`**, and the second is not an afterthought.
`installation import` reproduces `export.Installation` wholesale, so a
production export would rebuild as a production machine — silently blocking the
workflow that most wants a sandbox, since [0003](0003-secrets-recovery-and-onboarding.md)'s
`import → update → restore` is exactly how a vendor tests a customer's backup.
So `import --mode dev` exists, and the asymmetry lives there instead: **you may
demote at creation, never promote at all.** `import --mode production` from a
dev export is refused, because that is the deferred-risk transition wearing a
different hat.

**`import --mode dev` must drop the backup targets, and this is not optional.**
Import preserves the original installation id — deliberately, so a lost
machine's backups stay restorable — and [0009](0009-backup-targets.md) has the
export carry backup targets *with their credentials*, because a rebuilt machine
has to reach them. A sandbox imported from a production export would therefore
hold the customer's bucket, the customer's credentials, and a matching
installation id, and would push its own throwaway backups straight into them.
A sandbox that can write to production's backup target is worse than no sandbox.
So the targets are dropped, and the drop is reported rather than silent.

The cost of all this: **there is no path from sandbox to production.** Promotion
is backup → fresh `init` → restore, which already works today, and which is the
right amount of ceremony for a machine about to hold real data.

`status` and `doctor` mark it permanently and prominently. Not a first-run
notice — a persistent line, because the failure mode is a machine nobody
remembers the provenance of.

**`mode` must land at or after a schema bump**, and the reason is not the one
that first suggests itself. Reading is the safe direction: an older binary that
sees no `mode` treats the machine as production, which is stricter. *Writing* is
not. `config set` rewrites the state, unknown fields are dropped on the way
through, and a dev sandbox touched once by an older binary silently loses its
mode and thereafter presents as production — the deferred-risk row of the table
above, arrived at by accident.

What prevents that is that a manager refuses a state file from the future —
`migrateInstallation` is forward-only and `Installation.Validate` "rejects a
schema version from the future"
([`state.go:103-107`](../internal/infra/state/state.go)).

**So `mode` takes schema 5, and shares its version with nothing.** An earlier
draft had it ride [0015](0015-notifications.md)'s bump to 4 — "or carry the bump
itself if this RFC ships first" — which is worse than it looks. Two field sets
both called schema 4 means a manager implementing only one of them accepts a
state file written by the other, sees the version it knows, and rewrites it
dropping fields it has never heard of. That is the exact write-back hazard this
section is about, reintroduced by the fix for it. A schema version has to name
one shape.

`mode` arrives in P3, which is already gated on 0015 shipping, so 4-then-5 is
the order regardless.

**The recorded source ref (§5.1) needs no bump and takes none.** Losing it to an
older binary's write-back means `--check` stops answering until it is
reconfigured — a degradation an operator sees immediately, not a silent loss of
a protection they believed they had. That is the distinction the bump exists to
draw, and applying it reflexively to every new field is how schema versions stop
meaning anything.

The contrast with [0015 decision 9](0015-notifications.md) is worth keeping in
view: a bump is a judgement about which direction an omission fails in, not a
reflex on every new field — and the judgement has to consider the write path,
not only the read.

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

Retention has one exemption that is **not** mode-specific: a release **staged
but not applied** (§5.3) is exempt from `retain_releases` in either mode, the
way `current` and `previous` already are — the prune command exempts both
unconditionally ([`release.go`](../internal/cli/release.go)). Staging ahead of
the operator's decision is most of what makes the middle mode worth having, and
a retention pass that prunes the candidate it just fetched makes it pointless.
The exemption ends when the candidate is applied or superseded by a newer one.

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
phone-home nobody agreed to.

"Opt-in" needs a name, or it is a sentiment rather than a contract. The setting
is `update.check` on the installation, default **false**, and **absent means
false** — the same absence-means-safe rule `mode` follows (§5.4) and for the
same reason.

What it gates is equally explicit, because the natural reading is wrong in one
of the three cases:

| Path | Honours `update.check`? |
| --- | --- |
| `morzer update --check` | **No.** An operator typing the command *is* the consent; refusing it because a persisted flag is false would be the manager arguing with a direct instruction. |
| The `doctor` check and the `status` line | **Yes.** These run unprompted — under a timer, in a script, in someone's dashboard — so an unset flag must mean no network. |
| The background timer (§5.5) | **Yes**, necessarily: the timer is only installed when the setting is on. |

So the registry is unreachable by default and reachable when explicitly asked
for, which is the property the phone-home concern actually wants — not silence
in the face of a direct request.

### 5.7 Release notes, and where [0002](0002-rich-terminal-renderer.md) P5 finally lands

[0002](0002-rich-terminal-renderer.md) built `glamour` rendering for release
notes and shipped P1–P4, leaving P5 "gated on a bundle actually shipping a
`RELEASE.md`". That gate has never opened, and could not: no bundle ships a file
that no tooling creates, no scaffold writes and no page mentions.
[`release.ReleaseNotesFileName`](../internal/release/load.go) has been a
constant since, read by `release show` and by nothing else.

Two changes open it, and neither is in 0002:

- [0013 decision 14](0013-bundle-authoring-experience.md) has `release new`
  write a `RELEASE.md` stub, so bundles start carrying one by default.
- **This phase gives it a job.** A staged-but-unapplied release (§5.3) is
  exactly the moment an operator is deciding whether to accept downtime, and
  "what changes" is the question they are actually asking. `update --check` and
  the staged-release line in `status` render the incoming release's notes; the
  notification carries their first line.

So P5 ships here rather than in 0002. It is a renderer that already exists,
pointed at a file that will now exist, at the moment it is worth reading —
which is three things 0002 could not arrange on its own, and the reason a phase
sat unscheduled for months rather than being cheap and forgotten.

Nothing about a release *requires* notes: a bundle without `RELEASE.md` renders
nothing, exactly as `release show` behaves today.

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
- **`mode` cannot change after creation through any manager surface**, in
  either direction: not by `config set`, not by `import`, not by any command
  that writes the installation. Not a test that a hand-edited state file is
  detected — §5.4 explains why that is not claimed.
- **`import --mode dev` drops the backup targets**, asserted by importing an
  export that carries targets *with credentials* and checking the rebuilt
  installation has neither — and that the drop is reported, not silent. This is
  the assertion that stops a sandbox writing into production's bucket, so it
  must fail if the drop is ever removed for convenience.
- **`import --mode production` from a dev export is refused.**
- **The `database_schema_produces` gate, both halves**: a release declaring it
  is auto-applicable, and one omitting it is not. Without the second, the field
  could default to something permissive and no test would notice.
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
- `reference/manifest.md`: `database_schema_produces`, what declaring it opts
  into, and that omitting it is a valid and conservative choice.
  [0006](0006-documentation-site.md)'s `docs-check` fails the build on an
  undocumented manifest field, so this is gated rather than remembered.
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
  introduced it: it has always gated `rollback`, and now also decides whether a
  release may install without a human. Nothing is released, so no vendor is
  having the meaning changed under them — the widening is free *now* and would
  not have been later, which is the argument for doing it in this window. What
  remains is ordinary care: a vendor who sets the flag without reading what it
  means gets unattended installs they did not think about. §7's documentation
  change is the mitigation, and it is the most important one in the RFC.
- **Dev mode escaping to production.** Mitigated by immutability and the
  permanent marker, not eliminated: a machine that was *always* dev mode and
  quietly became load-bearing is the case neither guard catches. Immutability
  makes it *visible* — the machine still says `mode: dev` — which is the most a
  config field can do about an organisational problem.
- **A new manifest field will be a hard break for older managers — after the
  first release.** Strict decoding rejects an unknown field before
  `min_manager_version` is consulted, so the operator gets a message about typos
  rather than about versions. Today that costs nothing, since no released
  manager and no third-party manifest exist. The risk is therefore not this
  field but the **next** one: every manifest field added after the first tag
  pays this price, which is why §5.3 argues for a deliberate sweep before then
  and names the two ways to remove the wall permanently.
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

All four are resolved as of 2026-08-08 and recorded as decisions 15–18. Kept
here because what was open, and what answering it uncovered, is part of the
record.

1. ~~Can `--check` run without a configured channel or source?~~ → decision 15.
   The installation records the ref its current release came from. It rides
   [0015](0015-notifications.md)'s schema bump rather than needing one of its
   own.
2. ~~Is `database_schema_min` a sound stand-in for the post-migration schema?~~
   → decision 16. **No** — a release whose migrations go beyond its own declared
   minimum slips past, and a gate whose whole argument is "rests on a
   declaration, not a proxy" cannot use a proxy for half of itself. The manifest
   gains `database_schema_produces`; absent means not auto-applicable.
3. ~~Impossible to enter dev mode, or merely loud?~~ → decisions 17 and 18.
   Neither, as posed: the question assumed one dangerous direction and there are
   two, so the field is immutable and chosen at creation. Answering it surfaced
   `import` as a second creation path — and the backup-target hazard that comes
   with it (§5.4).
4. ~~Do staged releases count against `retain_releases`?~~ → decision 18's
   second half. Exempt, as `current` and `previous` already are.

What implementation is still free to settle: how a staged candidate is marked on
disk, and whether `--check`'s "cannot enumerate" is a distinct exit code.

## 11. Decisions

| # | Decision | Rationale and consequence |
| --- | --- | --- |
| 1 | Three separately phased capabilities: check, follow, apply | They differ by orders of magnitude in risk, and an operator who wants the first must not have to accept the third. |
| 2 | `--check` uses `List`; channel following uses `Resolve` | They are different operations: `List` skips non-semver tags by design, so it cannot follow a mutable channel. Consequence: a channel is a single ref, not a tag pattern. |
| 3 | A channel is a **mutable pointer to immutable versions** | Composes with [0014 §5.2](0014-building-a-release-bundle.md), which gives every build a distinct version, so the never-republish refusal keeps working untouched. |
| 4 | **No hardcoded poll interval floor** | The cost belongs to the vendor's registry, not the manager. Consequence: the documentation must state that rate-limited registries count manifest requests, because nothing enforces it. |
| 5 | Auto-apply is gated on the release **declaring** it cannot require a human — `rollback_safe: true` plus a schema the previous release can still read — not on a version range | A version range is a proxy for "has no one-way migration"; the manifest states the real property. Consequence: `rollback_safe`'s meaning widens — it has always gated `rollback` and now also decides whether a release may install unattended. Free to widen only because nothing is released; the docs must carry it regardless. |
| 6 | The gate is `AssessRollback` run **predictively**, over declared values only | Reuses a written, tested function rather than a second compatibility judgement, and keeps the gate free of estimates — see decision 16 for the value it needs and does not yet have. |
| 7 | Enabling auto-apply **refuses** without `require_signature` and a pinned key | Unattended apply hands the vendor unattended root. Refused at configuration time, not at update time — matching how `--skip-backup` requires `--force`. |
| 8 | Anything failing the gate is **fetched, staged and notified**, never silently skipped | Moves the network, the credentials and the verification off the human's critical path while leaving the downtime decision with them. This is where most of the value is. |
| 9 | Dev mode is on `Installation`, not `Policy`, and **absence means production** | `Policy` is what `config` may change; this may not be. Absence-means-safe is the rule [`installation.go:120-131`](../internal/domain/installation.go) already learned the hard way for `SkipBackupBeforeUpdate`. |
| 10 | ~~Dev mode is **one-way**: leavable, never enterable~~ — **superseded by decision 17** (2026-08-08) | Kept because the reasoning it got wrong is instructive: it saw only the production → dev danger and licensed the dev → production one, which is the transition whose cost lands during an incident. Original rationale: its relaxations make the machine's history untrusted, and sandbox→production drift only goes one direction. Consequence: retiring a production machine into a test fixture requires a fresh `init`. |
| 11 | Dev mode relaxes scheduling, downtime, backups and retention; it **never** relaxes verification | The sandbox's value is fidelity — a relaxed verification chain means the rehearsal is not of the thing being shipped. Consequence: dev builds must be signed, with a dev key. |
| 12 | `OnCalendar` **is** the maintenance window | systemd already expresses it better than a config field would, and the timer is a sibling of the backup timer that already ships. |
| 13 | A tick that cannot take the lock **exits 0** | The next tick is soon; queueing makes the start time unpredictable, and a non-zero exit would fight `Restart=on-failure`. |
| 14 | Update checking is **off by default** | It contacts the vendor's registry, which for a self-hosted product is a phone-home nobody agreed to. |
| 15 | The installation **records the ref its current release came from** | Without it `--check` has nothing to query and the `doctor` check cannot run unattended, which is the context that makes it useful. Rides [0015](0015-notifications.md)'s bump to schema 4; carries its own if this ships first. |
| 16 | The manifest gains **`database_schema_produces`**; **absent means not auto-applicable** | Replaces the `database_schema_min` stand-in, which a release migrating past its own declared minimum would slip through — and a gate arguing "declaration, not proxy" cannot use a proxy for half of itself. Fails closed, and asks nothing of a vendor who does not want unattended updates. Consequence: strict decoding means an older manager would **reject the manifest outright** with `unknown field`, and `min_manager_version` cannot soften it because `CheckUpgrade` runs after the decode — which costs nothing today (no release, no older managers) and prices every manifest field added after the first tag. §5.3 argues for a deliberate sweep before then. |
| 17 | `mode` is **immutable** — fixed at creation, no transition in either direction. **Supersedes decision 10** | Both directions are dangerous, differently: production → dev puts real data under relaxed rules immediately, dev → production presents untrusted history as trustworthy and surfaces during an incident. An immutable field is also the simplest invariant to test. Consequence: promotion is backup → fresh `init` → restore, and there is no shortcut. |
| 18 | Creation means `init` **or `import`**; `import --mode dev` is allowed and **drops the backup targets**, `import --mode production` from a dev export is refused. Staged-but-unapplied releases are exempt from retention | Import reproduces the export wholesale, which would otherwise block [0003](0003-secrets-recovery-and-onboarding.md)'s `import → update → restore` — the way a vendor tests a customer's backup. But an import keeps the original installation id and [0009](0009-backup-targets.md) puts targets *and credentials* in the export, so a sandbox would push throwaway backups into the customer's bucket under a matching id. Dropping them is not optional, and the drop is reported. Demote at creation, never promote. |
| 19 | [0002](0002-rich-terminal-renderer.md) **P5 ships in P2 of this RFC**, not in 0002 | Its gate — "a bundle actually shipping a `RELEASE.md`" — could not open by itself, since nothing created such a file. [0013 decision 14](0013-bundle-authoring-experience.md) makes bundles carry one and a staged update is where reading the notes matters. Consequence: 0002 stays ✅ Complete with P5 recorded as delivered elsewhere, rather than being reopened. |
| 20 | **Refines decision 5** (2026-08-09): the gate promises "**no database restore will be required**", not "no human will be required" | An update still reaches `requires-manual-intervention` through a failed migration hook, a health check that never passes, or a converge the engine cannot compensate — none of which this gate inspects. What it bounds is the *unrecoverable* failure, which is the one that cannot wait for morning. Consequence: unattended apply can still stop and page someone; it will not stop needing a restore decision. |
| 21 | **Refines decision 15** (2026-08-09): the recorded source ref is written when a release becomes **current**, never when a candidate is staged, and carries no schema bump | Writing it at staging would have `--check` and `doctor` reporting on a release nobody is running. And its loss to an older binary's write-back is a visible degradation (`--check` stops answering) rather than a silent one, which is the line schema bumps are supposed to draw. |
| 22 | Channel following **passes the resolved digest into the fetch** and refuses a staged bundle whose digest differs | A channel is a mutable tag by construction, and `resolveUpdateTarget` pins only `opts.ExpectDigest` ([`update.go:215`](../internal/lifecycle/ops/update.go)) — empty unless the operator passed `--digest`. Tolerable when a human typed a reference seconds ago; not when a loop is watching a tag that exists to move. |
| 23 | The setting is `update.check`, default **false**, absent means false. `update --check` **ignores** it; `doctor`, `status` and the timer **honour** it | "Opt-in" without a named setting is a sentiment. Refusing an explicitly typed command because a persisted flag is false would be the manager arguing with a direct instruction; running unprompted with the flag unset is the phone-home. |
| 24 | **Refines decision 17** (2026-08-09): `mode` is immutable **through the manager's surfaces**; a hand-edited state file is not detected and is not claimed to be | Defending one boolean with a tamper-evident record would defend it against an operator who can equally edit the recipient list, the backup targets or the installation id. Root on the machine is outside every threat model in this corpus. |

## 12. Phasing

- **P1 — `update --check`.** Consumes `List`, adds the `doctor` check and the
  `status` line. No new trust, no new mechanism, gated on nothing. This is the
  phase that turns an unused port method into a feature.
- **P2 — channel following and staging.** `Resolve` on a fixed ref, fetch and
  stage, notify. Gated on [0015](0015-notifications.md) for the notify half; the
  staging half is independently useful and can land first. Also the phase a
  vendor's sandbox actually needs. **Carries [0002](0002-rich-terminal-renderer.md)
  P5** — see §5.7.
- **P3 — unattended apply and dev mode.** Gated on
  [0015](0015-notifications.md) shipping, because an update that can end in
  requires-manual-intervention needs a way to ask for one.

P1 is worth shipping alone. P3 is the only phase carrying the risk in §9, and it
should stay behind an explicit opt-in even after it ships.

## 13. Amendments

Recorded on completion, 2026-08-10. The decision table is append-only; this
section records where execution diverged and why.

### §5.2 priced a tick at "one `Resolve`", and a Resolve is a download

The whole affordability argument for channel following was one sentence: "The
cost is one `Resolve` per tick." That is wrong, and it is wrong by the size of
the release.

`ports.ReleaseSource.Resolve` returns a `ResolvedRelease` carrying the bundle's
**content digest**, and a content digest is a property of the bytes. The OCI
source therefore pulls the layer to compute one
([`oci.go`](../internal/adapters/source/oci/oci.go)). A poll built on Resolve
downloads the entire release on every tick to discover that nothing has changed
— at the five-minute cadence §5.2 discusses, 288 times a day. The port's own doc
comment said "without downloading the payload", which is true of a directory and
false of a registry; it now says otherwise.

**What ships instead** is `ports.ChannelPeeker`, an optional capability: one
manifest request, no blob. It is optional rather than a method on
`ReleaseSource` because only a transport with a server-side identity for its
content can answer — a registry has a manifest digest, a directory has nothing
that changes when a file does not. A source that cannot peek is **refused by
name** rather than falling back to Resolve, which is the tempting move and the
one that reintroduces the bug silently.

The assertion is a byte count, not a digest
([`TestPeekReadsAManifestAndResolveReadsTheBundle`](../test/suite/oci_test.go)).
A Peek that downloaded the bundle to find the right answer would pass any test
written about its answer, which is precisely the failure.

Two consequences worth stating:

- **`ReleaseRecord` gains `UpstreamDigest`** — the registry's identity for what
  a release came from, distinct from the content digest and never compared with
  it. Without it, every tick after an install would re-fetch to discover it
  already had the release. It carries no schema bump, for decision 21's reason:
  losing it costs one fetch.
- **The peek returns the reference pinned by digest**, and staging fetches that
  rather than the tag. This is decision 22 implemented at the transport
  boundary, where reference syntax belongs.

### `update.check` shipped in P1 with no way to turn it on

The setting had a name, a default, a documented meaning, a `doctor` check and a
refusal whose hint told operators to "enable it with `morzer config`" — which
read and wrote *release parameters* exclusively. There was no surface for
installation-level settings at all, so P1's own opt-in could not be exercised
without hand-editing the state file.

P2 adds one, dispatched on the single thing that cannot collide: a parameter
name is `[a-z][a-z0-9_]*`, so a dotted name is never one. `update.check`,
`update.channel` and `update.auto_apply` are set with `morzer config set
update.check=true` and listed by `morzer config settings`.

The general shape is worth keeping: a decision that names a setting is not
shipped until something can write it, and "the operator edits the state file" is
not a surface — the manager rewrites that file on every operation.

### The update timer is generated from the setting, not from `init`

§5.5 described the timer beside the backup timer, which `init` installs. But the
channel is configured *later*, by an operator who has decided to follow one — so
units are reconciled when the setting changes: a channel installs the pair, and
clearing it removes them.

That made `ManagedUnitNames` load-bearing in a way it was not before. It is
deliberately the **superset** — every unit the supervisor may own, whether or
not this installation generates it — because it is what removal walks, and a
list narrowed to the current configuration would leave an orphaned timer polling
a channel nobody configures any more.

The port also gains `InstalledUnits`, which answers a question nothing else
could: whether this installation manages units at all. `init
--install-units=false` is a supported choice, and reconciliation that installed
units into a machine that deliberately has none would be the manager overruling
a decision somebody already made.

### `status` names the staged release; it does not print the notes

§5.7 said "`update --check` and the staged-release line in `status` render the
incoming release's notes". `--check` does. `status` shows the line and stops.

A `status` reading is a compact report of what is deployed and whether it works,
and a vendor's changelog inside it would push the service list and the health
results off a terminal — every time it is run, until somebody installs the
update. The notes are one command away, and the page says which one. The
divergence is recorded rather than quietly taken because the difference is
visible to anyone reading §5.7 beside the output.

### `release prune` moved into the lifecycle layer

Dev mode prunes after every update (§5.4), and the retention pass lived in
`internal/cli`. Two implementations of "what is unprunable" would drift, and the
staged-candidate exemption is exactly the kind of rule that would be added to one
of them. There is one `ReleaseEntry.Exempt()` now, and both callers ask it.
