# RFC 0042 — Settings an operator changes after init

- **Status:** 📝 Draft — proposed, and ruled by the author 2026-10-31: §11's
  `OPEN` rows as recommended, except that a domain change re-creates every
  running service (decision 4). P1 is ready to build
- **Scope:** Three values `init` fixes that nothing changes afterwards: whether
  a release signature is required, the installation's domains, and the release
  and backup retention counts. Each becomes changeable on a running
  installation through `morzer config set`. The RFC covers what each change
  validates, what it converges and what it refuses. It does not make `mode`,
  the runtime or its options changeable: those are immutable by RFC 0016, 0023
  and 0027, and stay so. It does not touch the trusted signing keys, which
  `release trust` already changes (RFC 0004 decision 24), or the profile, which
  `update --profile` already overrides. It issues no TLS certificates for a new
  domain. No installation schema bump: every field already exists.
- **Related:** [`internal/lifecycle/ops/settings.go`](../internal/lifecycle/ops/settings.go)
  (the settings registry);
  [`internal/lifecycle/ops/config.go`](../internal/lifecycle/ops/config.go)
  (`config set` for parameters);
  [`internal/lifecycle/ops/init.go`](../internal/lifecycle/ops/init.go)
  (what `init --repair` carries);
  [`internal/domain/installation.go`](../internal/domain/installation.go)
  (`Policy`, `Domains`, `Validate`);
  [`internal/lifecycle/ops/trust.go`](../internal/lifecycle/ops/trust.go);
  [0004](0004-distribution-and-verification.md) decision 24,
  [0015](0015-notifications.md), [0016](0016-update-checking-and-unattended-updates.md),
  [0027](0027-desired-state-in-a-repository.md) §4.3,
  [0030](0030-unit-enablement-is-the-operators.md).

---

## 1. Summary

`morzer config set` gains four settings:

- `release.require_signature` turns the signature requirement on or off. It
  needs a trusted key, and it cannot be turned off while `update.auto_apply` is
  on.
- `installation.domains` replaces the domain list. It renders the
  configuration again and re-creates every running service (decision 4).
- `retention.releases` sets how many releases the store keeps.
- `retention.backups` sets how many backups it keeps.

Before this, an installation created without `--require-signature` could never
turn on unattended updates, a domain could only change through an undocumented
side effect of `init --repair`, and the retention overrides could only be set
by editing the state file.

## 2. Motivation

- **Unattended updates are out of reach for most installations.**
  `update.auto_apply` needs `policy.require_signature`
  ([`installation.go:1022`](../internal/domain/installation.go)). Only `init`
  writes that field ([`init.go:564`](../internal/lifecycle/ops/init.go)), and
  `init --repair` carries the whole policy forward
  ([`init.go:632`](../internal/lifecycle/ops/init.go)). RFC 0004 decision 24
  says so outright: "Whether a signature is required stays fixed at `init`."
  An operator who trusted a vendor key after `init` with `release trust add`
  still cannot require it. The only way out is to edit
  `installation.yaml` by hand, which `doctor` then reports as drift
  ([`doctor.go:343`](../internal/lifecycle/ops/doctor.go)).
- **Domains change, and only a repair changes them.** A product gets renamed or
  gains a second hostname. `init --repair --domain` replaces the list
  ([`init.go:629-631`](../internal/lifecycle/ops/init.go)), but that is a
  side effect of a command for restoring missing directories. It is
  documented as nothing else, it renders nothing, and nothing says the running
  services still have the old value.
- **The retention overrides have no writer.** `policy.retain_releases` and
  `retain_backups` override the manifest's retention
  ([`installation.go:714-718`](../internal/domain/installation.go)) and are
  read at [`installation.go:903`](../internal/domain/installation.go) and
  [`:914`](../internal/domain/installation.go). No command writes either one.
  `update.check` was in the same position before the settings registry
  existed ([`settings.go:27-30`](../internal/lifecycle/ops/settings.go)).

## 3. Current state

- **The settings registry** maps a dotted name to `Read`, `Apply` and `Clear`
  ([`settings.go:52`](../internal/lifecycle/ops/settings.go)). `SetSettings`
  validates before the lock, re-applies under it against a fresh read, saves,
  and reconciles the units
  ([`settings.go:366-480`](../internal/lifecycle/ops/settings.go)). A setting
  "changes nothing that is running", and a change is not an engine operation
  and writes no journal record.
- **Parameters** go through `config set` too. A changed parameter renders the
  configuration again and re-creates the services it declares. "A parameter
  with no declared services needs a full `apply`, which the summary says out
  loud" ([`config.go:346-352`](../internal/lifecycle/ops/config.go)). The two
  kinds cannot collide, because a parameter name has no dot.
- **Domains reach the deployment three ways.** Templates read `.Domain` and
  `.Domains` ([`render.go:185-219`](../internal/adapters/render/gotemplate/render.go)).
  The services' environment carries `DOMAIN`
  ([`ops.go:1016-1017`](../internal/lifecycle/ops/ops.go)). `PublicURL` is
  the first domain, given `https://` unless it has a scheme
  ([`installation.go:889-897`](../internal/domain/installation.go)).
  **Nothing validates a domain.** `init` accepts any string.
- **The signature policy is enforced** at `release fetch`
  ([`fetch.go:49`](../internal/lifecycle/ops/fetch.go)), in `update`'s
  `verify-bundle`, including `update --to` from the store
  ([`update.go:641`](../internal/lifecycle/ops/update.go)), and at `init`.
  `apply` and `rollback` verify nothing.
- **`Validate` already refuses** a required signature with no key
  ([`installation.go:1065`](../internal/domain/installation.go)) and
  `auto_apply` without a required signature
  ([`installation.go:1022`](../internal/domain/installation.go)), wherever the
  state is written.
- **`release trust remove`** refuses to remove the last key while signatures
  are required ([`trust.go:119`](../internal/lifecycle/ops/trust.go)).

## 4. Goals / Non-goals

**Goals**

- One command changes each of the three on a running installation, with
  `--dry-run`.
- A value the machine cannot honour is refused before anything is written,
  with a hint naming the command that makes it possible.
- A domain change says plainly what it has and has not reached.

**Non-goals**

- **`mode`, the runtime and its options.** Immutable by RFC 0016, 0023
  decision 3 and 0027 §4.3, because each transition is dangerous in its own
  way. This RFC changes none of that.
- **The trusted keys.** `release trust` already changes them (RFC 0004
  decision 24).
- **Certificates.** morzer does not terminate TLS. A new domain needs whatever
  serves it to have a certificate, and that is the release's proxy or the
  operator's.

## 5. Design

### 5.1 `release.require_signature`

```text
morzer config set release.require_signature=true
morzer config set release.require_signature=false
```

- `true` needs at least one trusted key. `Validate` already refuses the state
  without one; the setting refuses first, with the hint `morzer release trust
  add <key>`.
- `false` is refused while `update.auto_apply` is on, as `Validate` already
  requires. The hint is to turn `update.auto_apply` off first.
- It applies from the next `release fetch` or `update`. The installed release
  keeps running, and nothing already in the store is checked again. An
  `update --to` to a stored release whose signature does not verify is then
  refused, because `verify-bundle` reads the policy at the time of the update.
- `release trust list` already reports the value.

### 5.2 `installation.domains`

```text
morzer config set installation.domains=demo.example,www.demo.example
morzer config unset installation.domains
```

- Dotted, as every setting is. A parameter name has no dot
  ([`settings.go:23-25`](../internal/lifecycle/ops/settings.go)), so a bare
  `domains` would take the name from a release that already declares a
  parameter called that.
- A comma-separated list replacing the whole list. Each entry is checked as a
  host name, or as an `https://` URL, which `PublicURL` already accepts. An
  empty entry, a duplicate, or a scheme other than `https` is refused.
- It renders the configuration again and re-creates every running service.
  No release declares which services read the domain, and `apply` would not
  reach them all: its `compose up` re-creates only a service whose Compose
  definition changed, so one that reads the domain from a rendered file keeps
  the old value (decision 4).
- It runs as an operation, not inside the setting's `Apply`, which only
  validates and records in memory and also runs on `--dry-run`
  (`settings.go:397`). `SetSettings` runs three steps under the lock,
  journalled as `config`, in the shape `config set` gives a parameter change
  (`config.go:342-353`):
  1. record the installation, compensated by writing back the one it replaced;
  2. `render-configuration`, the step `apply` runs, compensated by restoring
     the files it replaced;
  3. re-create every running service with `compose up --force-recreate`.
     `UpOptions` gains `ForceRecreate`. It is skipped when nothing is running,
     and when the product is stopped (RFC 0040): `start` runs `apply`, which
     starts every service with the new value.
- A render failure compensates step 1, so the recorded domains and the files
  never disagree. `--dry-run` plans the three steps and runs none, as the
  engine plans a parameter change (decision 9).
- `unset` empties the list, which renders as an installation created without a
  domain does.
- `init --repair --domain` keeps working, and the docs name `config set
  installation.domains` as the way to change them.

### 5.3 `retention.releases` and `retention.backups`

```text
morzer config set retention.releases=5 retention.backups=14
morzer config unset retention.releases
```

- A whole number of at least 1. `unset` returns to the manifest's retention.
  This is `retention.pre_update_backups`' shape, beside which they sit.
- They mean what the manifest's counts already mean. `retention.releases=N`
  keeps N releases beyond the current, previous and staged ones, which are
  never pruned (`prune.go:183-188`). `retention.backups=N` keeps the newest N
  backups, and `retention.pre_update_backups` keeps that many pre-update
  backups on top, whatever N says (`ports/backup.go:395-409`).
- Nothing is pruned when the value is set. Retention applies where it already
  does: releases after an update and at `release prune`, backups after a
  backup.

### 5.4 Alternatives considered

- **A `release trust require` subcommand** for the signature requirement, next
  to the keys it depends on. It is closer to where the keys live, but it adds a
  second place to change a single boolean, beside a registry built for
  exactly that. RFC 0004 decision 24 kept the keys out of `config set` because
  rotating a key takes two steps, which one assignment cannot express. A
  boolean takes one.
- **Domains as a parameter the release declares.** The vendor would then say
  which services read it, and `config set` could re-create exactly those. But
  domains are the operator's, from `init`, and every release would need to
  declare the same parameter.
- **`init --repair` as the documented path.** It already works for domains.
  But it re-creates a layout, and making it a configuration command too gives
  it two jobs, one of which carries the whole policy forward unchanged.

## 6. Tests

- `config set release.require_signature=true` with no trusted key is refused
  with the hint. With a key, `release fetch` of an unsigned bundle is then
  refused.
- `release.require_signature=false` while `update.auto_apply` is on is
  refused. With `auto_apply` off, it is accepted.
- `installation.domains` renders the template's `.Domain` with the new value
  and re-creates the running services, which see the new `DOMAIN`. A failed
  render leaves the old domains recorded and the old files in place. With the
  product stopped, nothing is re-created. A bad entry is refused before
  anything is written.
- `retention.releases=0` is refused. Set to 1, the next update keeps one
  release beyond the current, previous and staged ones.
- Each with `--dry-run` writes nothing.

## 7. Docs

- `reference/parameters.md#changing-one-after-install`: the four settings, in
  the settings table.
- `operating/unattended-updates.md`: turning on `release.require_signature`
  after `init`, which replaces "re-create the installation".
- `reference/commands.md#init`: `--repair` restores a layout; `config set
  installation.domains` changes domains.

## 8. Out of scope

- **Journalling setting changes.** Settings write no journal record today
  (§3). Decision 5 asks whether turning off the signature requirement should be
  the first that does.
- **Changing the mode.** It stays immutable. A sandbox becoming production is a
  new installation and a `restore`, as now.

## 9. Risks

- **Turning off the requirement is a downgrade done by one command.** Refusing
  it while `auto_apply` is on covers the case where it matters most. Decision 5
  covers the record of it.
- **A domain change restarts the product.** Every running service is
  re-created, which is a short outage. The plan says so, and a domain change
  is rare and deliberate.

## 10. Unresolved questions

None. The author ruled §11's `OPEN` rows 2026-10-31.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | The four values become settings in the existing registry, through `config set` and `config unset`, each under a dotted name so none can shadow a release parameter: `release.require_signature`, `installation.domains`, `retention.releases` and `retention.backups`. No new command, no schema bump: every field exists. |
| 2 | `LOCKED` | **Where the signature requirement changes.** Recommended: the setting `release.require_signature`, since a boolean has none of the two-step rotation that kept the keys out of `config set` (RFC 0004 decision 24). Alternative: `release trust require [--off]`, next to the keys. Either way, this refines RFC 0004 decision 24's "stays fixed at `init`". Was `OPEN`; ruled by the author 2026-10-31: as recommended. |
| 3 | `ASSUMED` | Turning the requirement on needs a trusted key, and turning it off is refused while `update.auto_apply` is on. Both rules already hold in `Validate`, and the setting refuses with a hint before the lock. It applies from the next fetch or update and re-checks nothing already stored. |
| 4 | `ASSUMED` | **What a domain change converges.** Recommended: it renders the configuration and re-creates nothing; the summary says `apply` is needed, as for a parameter with no declared services (`config.go:346-352`). Alternative: it runs the whole `apply` itself, which reaches the services in one command but makes `config set` the first setting that restarts the product. Was `OPEN`; ruled by the author 2026-10-31: neither. It renders the configuration and re-creates every running service. No release declares which services read the domain, and `apply`'s `compose up` re-creates only those whose Compose definition changed, so neither option reaches a service that reads it from a rendered file. §5.2. |
| 5 | `ASSUMED` | **Whether turning off the signature requirement is journalled.** Recommended: yes, as a `config` record with the flag `release.require_signature=false`, so an incident review finds the downgrade. It would be the first setting with a journal record. Alternative: no record, as for every other setting today. Was `OPEN`; ruled by the author 2026-10-31: as recommended. |
| 6 | `ASSUMED` | Domains are validated when set: a host name, or an `https://` URL, with no empty entry and no duplicate. `init` keeps accepting what it accepts today, so an existing installation is not refused at load. |
| 7 | `ASSUMED` | `retention.releases` and `retention.backups` are whole numbers of at least 1. `unset` returns to the manifest's value, and nothing is pruned when one is set. |
| 8 | `ASSUMED` | `mode`, the runtime and its options stay immutable. This RFC does not reopen RFC 0016, 0023 decision 3 or 0027 §4.3. |
| 9 | `ASSUMED` | *(2026-10-31, from the review.)* A domain change runs as a journalled `config` operation of three steps: record the installation, render the configuration, re-create the running services. Each of the first two compensates, so a failed render leaves the recorded domains and the files as they were, and `--dry-run` plans the steps without running them. A setting's `Apply` stays validation and an in-memory record. |

## 12. Phasing

- **P1**: §5 in full, the decisions as ruled, the docs in §7. One PR.
- Nothing is demand-gated.
