# Release trust keys · trusted release signing keys change after `init`

From the ERP bundle's requests (item 8), on branch `release-trust-keys` from
`bd74239`. The request asked for `config set policy.signing_keys+=<key>` and
cited RFC 0028 P2; that RFC is the machine's own key, and trusted *release*
keys belong to RFC 0004, which made them an `init`-time value and never said
how they change. `config set` has no list syntax: `policy.signing_keys+=X`
parses as a setting named `policy.signing_keys+`. The author approved
`morzer release trust add|remove|list` on 2026-10-15, keys parsed when added,
and a refusal to remove the last key while signatures are required or
auto-apply is on, following the `secret recipients` and `backup target`
subcommands.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T05:49:02Z
attempt: 1
claim: RFC 0004 made the trusted keys an init-time value and never said how they change, so rotating a vendor key meant re-creating the installation; the author approved a subcommand surface on 2026-10-15
evidence: pages/docs/operating/changing-configuration.md:91
action: decided
proposal: ASSUMED — RFC 0004 decision 24: trusted release keys change after `init` through `morzer release trust add|remove|list`. A key is parsed when it is added, and the last key cannot be removed while `policy.require_signature` is set, which `update.auto_apply` requires.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T05:49:02Z
attempt: 1
claim: only the minisign verifier adapter can parse a key, and the operations layer imports no adapter; a key is parsed through an optional verifier capability, KeyChecker, which the chain forwards to its members, in the shape `init` already uses to ask the secrets port whether a recipient is usable
evidence: internal/adapters/verify/minisign/minisign.go:135
action: decided
proposal: ASSUMED — a verifier that can judge a key says so through ports.KeyChecker; a verifier without the capability accepts, as before. Optional rather than on ports.Verifier, because the checksum verifier has no keys to judge.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T05:49:02Z
attempt: 1
claim: `backup target add` refuses a target it already has; adding a key that is already trusted is a no-op that says so and exits 0, because a rotation is a two-step procedure an operator re-runs, and the re-run should not fail at the step that already worked
evidence: internal/lifecycle/ops/targets.go:267
action: decided
proposal: ASSUMED — `release trust add` is idempotent; `remove` of an untrusted key is an error naming the trusted ones, because a typo there would otherwise read as success while the old key stays trusted.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T05:49:02Z
attempt: 1
claim: the change is written under the deployment lock and not journaled, as installation settings and backup targets are: it converges nothing and has nothing to compensate
evidence: internal/lifecycle/ops/settings.go:279
action: decided
proposal: ASSUMED — trust changes take the lock and write the state; no engine operation, no journal record.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T05:49:02Z
attempt: 1
claim: Installation.Validate already refuses require_signature with no keys and auto_apply without require_signature, so removing the last key is refused at the write either way; the remove command refuses first, with a hint naming auto_apply when it is on, and before a plan returns, since a plan saying "would remove" would be a plan that lies
evidence: internal/domain/installation.go:927
action: decided
proposal: ASSUMED — the last-key refusal is one condition, require_signature, because auto_apply implies it; Validate stays the backstop.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T05:49:02Z
attempt: 1
claim: init checks only that a --signing-key was given, so a malformed key passed init and failed at the first verification, as a refusal to install a bundle rather than as a configuration error; RFC 0004 decision 6 refuses an unsatisfiable policy at load and spoke only of an absent key
evidence: internal/lifecycle/ops/init.go:103
action: decided
proposal: ASSUMED — decision 6 extended: a key that does not parse is refused where it is written, at `init` and at `release trust add`, before anything is created.
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-15T05:53:15Z
attempt: 1
claim: the entry above says a malformed key failed as a refusal to install a bundle rather than as a configuration error; wrong, since the verifier's parseKeys already named it an invalid key by position; what was wrong is when, at the first update, after init had built the installation around it
evidence: internal/adapters/verify/minisign/minisign.go:148
action: decided
proposal: ASSUMED — the entry above stands; only its claim is corrected.
```

Built as planned. The capability is at `internal/ports/source.go`, answered by
the minisign verifier and forwarded by the chain; `init` asks it at
`internal/lifecycle/ops/init.go:112`; the three operations are in
`internal/lifecycle/ops/trust.go`, with the idempotent add at :53, the
last-key refusal at :133 and both writes under the lock at :61 and :102. The
installation's stored shape is unchanged, so there is no schema bump.

Not done, deliberately: `require_signature` itself is still fixed at `init`.
Turning it off is a security downgrade nobody asked for, and turning it on
needs a key first, which `release trust add` now provides; a setting for it is
a separate decision. Not tested: that `internal/cli/root.go` wires the chain
with the minisign member, which is what makes a key judged at all; the suite
builds the same chain itself.

Superseded within the same attempt: `test/clitest/trust_test.go` runs `release
trust add` and `init --signing-key` through the command line in-process, so the
wired chain is what judges the key there.

**Drift count: 0.**
