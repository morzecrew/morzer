# settings-after-init · RFC 0042 P1, four settings `init` used to fix

On branch `settings-after-init` from `8ebd593`, building
[RFC 0042](../rfcs/0042-settings-an-operator-changes-after-init.md) P1 as ruled
by the author on 2026-10-31. That covers:

- `release.require_signature`, refused without a trusted key and refused off while `update.auto_apply` is on;
- turning it off journalled as a `config` record with the flag `release.require_signature=false`;
- `installation.domains`, validated, then recorded, rendered and every running service re-created with `compose up --force-recreate`, as one journalled `config` operation;
- `retention.releases` and `retention.backups`, whole numbers of at least 1;
- the tests of §6 and the docs of §7.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Five things the RFC leaves to the build, decided while planning and before any
code.

Decision 3 has the setting refuse "before the lock", but the two rules it names
cross two settings. Settings apply in name order, so `config set
update.auto_apply=false release.require_signature=false` would reach the
signature first and be refused for the very state the same command ends.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-31T20:14:00Z
attempt: 1
claim: the signature rules cross two settings, so checking them inside one setting's Apply refuses a command that turns both off together
evidence: internal/lifecycle/ops/settings.go:391
action: decided
proposal: ASSUMED — the signature rules are checked over the installation the whole command produces, before the lock and again under it; `update.auto_apply=true` without a required signature is refused there too, with the hint to require one.
```

§5.3 says `unset` returns to the manifest's retention, but a setting's `Read`
sees only the installation, and the manifest's count needs a release loaded.

```divergence
decision: 7
grade: ASSUMED
class: spec-gap
at: 2026-10-31T20:15:00Z
attempt: 1
claim: `config get retention.releases` cannot show the manifest's count without loading a release, which `config settings` does not need today
evidence: internal/lifecycle/ops/settings.go:36
action: departed
proposal: ASSUMED — `retention.releases` and `retention.backups` read as the recorded count, and empty when unset, as `backup.schedule` reads empty when it takes the supervisor's default; the description says unset follows the release.
```

Decision 6 says "a host name, or an `https://` URL" without saying what a host
name admits.

```divergence
decision: 6
grade: ASSUMED
class: spec-gap
at: 2026-10-31T20:16:00Z
attempt: 1
claim: what counts as a host name, and whether duplicates compare by case, is not settled
evidence: rfcs/0042-settings-an-operator-changes-after-init.md:162
action: departed
proposal: ASSUMED — a host name is dot-separated labels of letters, digits and inner hyphens, at most 63 characters each and 253 in all, or an IP address; a port or a path needs the `https://` form; a URL carries no user name; duplicates compare without case, as DNS does.
```

§5.2 renders the configuration of the installed release, and says nothing of an
installation with none.

```divergence
decision: 9
grade: ASSUMED
class: spec-gap
at: 2026-10-31T20:17:00Z
attempt: 1
claim: a domain change on an installation with no release installed has nothing to render or re-create
evidence: rfcs/0042-settings-an-operator-changes-after-init.md:172
action: departed
proposal: ASSUMED — with no release installed, a domain change records the installation and nothing else, and the next `update` renders it.
```

The journalled domain change is a `config` operation that renders, which is the
kind RFC 0025 §4.1 attests and every operation notifies. The RFC names neither.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-31T20:18:00Z
attempt: 1
claim: whether a journalled setting change attests and notifies is not settled
evidence: internal/lifecycle/ops/config.go:189
action: decided
proposal: ASSUMED — a journalled setting change notifies as `config set` does; one that rendered the configuration attests as `config set` does, and the signature record alone, which renders nothing, does not.
```

The `LOCKED` row this branch touches, honored:

```divergence
decision: 2
grade: LOCKED
kind: resolved
at: 2026-10-31T20:19:00Z
attempt: 1
claim: the signature requirement changes through the setting `release.require_signature`
evidence: internal/lifecycle/ops/settings.go:52
action: decided
```

**Drift count: 0.**
