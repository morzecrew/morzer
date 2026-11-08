# RFC 0052 — Trusting a release's key on first use

- **Status:** 📝 Draft — proposed 2026-11-07, and ruled by the author
  2026-11-07: decision 13 as recommended. 2026-11-08, from the review:
  decision 1 narrowed to an `init` that creates an installation, and
  decision 15 accepted.
- **Scope:** How an installation comes to trust a vendor's release signing key
  without the operator typing it. A release may ship its minisign public key;
  an `init` given no `--signing-key` checks the release's signature against
  that key, trusts it, and requires a signature from every release after.
  Folded in: a trusted key, given or shipped, means a signature is required
  unless `--require-signature=false` says otherwise. Also folded in: a repair
  of an installation that exists is not a first use, and verifies against the
  installation's policy rather than its own flags. It covers `init`, its
  wizard and plan, the refusal a later release meets when it is signed by
  another key, and the vendor-side check that the shipped key signs the
  release. It does not change how `update` verifies, does not fetch keys from
  anywhere but the release, does not add keyless signing, and changes no
  manifest schema: the key is a file in the bundle, which older managers
  ignore.
- **Related:** [RFC 0004](0004-distribution-and-verification.md) (decisions
  5, 6, 7 and 24),
  [RFC 0022](0022-bootstrapping-the-manager.md) (decision 5, the embedded
  manager key),
  [`internal/lifecycle/ops/init.go`](../internal/lifecycle/ops/init.go),
  [`internal/adapters/verify/minisign/minisign.go`](../internal/adapters/verify/minisign/minisign.go),
  [`internal/domain/installation.go`](../internal/domain/installation.go),
  [`pages/docs/authoring/publishing.md`](../pages/docs/authoring/publishing.md)

---

## 1. Summary

A vendor adds `minisign.pub` to the bundle it signs. An operator then runs:

```sh
sudo morzer init --release oci://ghcr.io/morzecrew/dotdash:stable --recovery-recipient age1…
```

`init` finds the key in the release, checks the release's signature against
it, prints the key and its id, and records it as the installation's trusted
key with `require_signature: true`. Every later release must be signed by
that key, exactly as if the operator had passed `--signing-key` and
`--require-signature`.

The first install is as trustworthy as the registry it came from, which is
what an install without a key is today. Everything after it is protected
against a tag that moves to a release the vendor did not sign. Separately,
`--signing-key` alone now requires a signature; `--require-signature=false`
keeps today's "check if signed" behaviour for whoever wants it.

## 2. Motivation

The documented install of a signed product is:

```sh
sudo morzer init --release "oci://ghcr.io/morzecrew/dotdash:stable" \
  --require-signature --signing-key RWSo… --recovery-recipient age14…
```

The key is the vendor's public key, published "where your users will look for
it" (`pages/docs/authoring/publishing.md:139`). In practice it lives in the
vendor's CI variables, where `release verify` reads it, and an operator has
to find it, copy 56 characters, and remember a second flag that only means
something next to the first. Leaving both out is the easy path, and it
installs with **no signature check at all**: with no trusted key the
signature verifier has "no opinion" (`minisign.go`, `VerifySigned`, RFC 0004
decision 5), and the installation never acquires one.

What that costs is not the first install but the ones after it. An
installation following `stable` pulls whatever the tag points at, and an
unattended one applies it as root. The trusted key is what stops a tag
pushed by someone other than the vendor from being installed — and the
installations that most need it are the ones whose operators took the short
command.

## 3. Current state

- **Trust lives with the installation.** `Policy.SigningKeys` and
  `Policy.RequireSignature` (`internal/domain/installation.go:716-727`) are
  set at `init` from `--signing-key` and `--require-signature`
  (`internal/cli/commands.go:211-214`, `init.go:564-565`) and changed after
  through `morzer release trust add|remove|list` (RFC 0004 decision 24).
  The comment on `SigningKeys` states the rule this RFC has to answer: "a
  bundle naming the key that may sign it would be a bundle authorising
  itself".
- **No key, no check.** `VerifySigned` returns early with no keys
  (`minisign.go:70-81`). The checksum verifier still runs.
- **`--require-signature` without a key is refused before anything is
  created** (`init.go:115-120`), because no release could satisfy it.
  Nothing earlier steps create is unwound if `init` fails later: the
  directory, identity and signing-key steps have no compensation. The
  installation state is written after staging, though, and `init` runs again
  over what they left (`internal/infra/state/state.go:49-51`).
- **A repair verifies against its flags, not the policy.** `init --repair
  --release` builds the stage step's expectation from `--signing-key` and
  `--require-signature` (`init.go:718-722`), then carries the existing
  policy (`init.go:632`). A repair given no key therefore stages a release
  unchecked, even on an installation that requires a signature.
- **`--signing-key` without `--require-signature`** checks a signed release
  and installs an unsigned one (`publishing.md:150-151`).
- **Unattended apply needs a required signature**
  (`installation.go:1151-1155`).
- **The signature covers `SHA256SUMS`, which covers every other file in the
  bundle** (`internal/release/sums.go:40`). A key file at the bundle root is
  therefore inside what the signature signs, with no change to the build.
- **Nothing refuses an extra file at the bundle root.** An older manager
  installs a bundle carrying `minisign.pub` and ignores it.
- **`init` verifies in its stage step** (`init.go:716-729`) and records which
  key verified as a `domain.SignatureCheck` on the release record.
  `update` verifies against the installation's policy only, with the comment
  "The policy comes from the installation, never from the bundle"
  (`update.go:686-695`).
- **Precedent for an anchor that is not fetched.** RFC 0022 decision 5
  embeds the manager's own key in the install script, because "fetching the
  key you verify against proves only that the server was self-consistent".
  This RFC accepts exactly that limit for the first release, and says so
  (§5.4).

## 4. Goals / Non-goals

**Goals**

- A signed product installs with signature checking on, and without the
  operator typing its key.
- Every release after the first is held to the key the first one was signed
  with.
- A trusted key means a required signature, whichever way it arrived.
- An operator who wants the first install verified too can still pass the
  key, and nothing about that path gets weaker.

**Non-goals**

- **Verifying the first install without a key from elsewhere.** Nothing the
  release carries can do that; §5.4 is honest about it.
- **Changing what `update` trusts.** The policy stays the installation's. A
  later release's key file changes nothing (§5.3).
- **Keyless signing.** RFC 0004 decision 7 stands: cosign is a large
  dependency, and verification needs an online transparency log that an
  offline install does not have.

## 5. Design

### 5.1 The key a release ships

A signed bundle may carry its public key as `minisign.pub` at the bundle
root: the file `minisign -G` writes, an `untrusted comment:` line and the
key. The vendor commits it to the bundle source tree; `release build` sums it
like any other file, so the signature covers it.

One key per file. A bundle that ships a key file declares that it is signed.
Where `init` would trust the key (§5.2), a key file that does not parse, or a
bundle carrying one with no `SHA256SUMS.minisig`, is refused: either the
pipeline is broken or the signature was stripped. Where `init` ignores the
shipped key, because it was given `--signing-key` or
`--require-signature=false`, it ignores the whole file. The release is then
judged as if it shipped no key.

### 5.2 Trusting it at `init`

The stage step does the following after the checksum check when three
conditions hold:

- `init` creates an installation, because no installation state exists,
  whether or not `--repair` was passed (`init.go:88-97`);
- it is given no `--signing-key`;
- `--require-signature` is not `false`.

A repair of an installation that exists is not a first use (§5.4).

1. reads `minisign.pub` from the fetched release, if there is one;
2. verifies `SHA256SUMS.minisig` against it, refusing a release whose
   signature does not verify against the key it ships;
3. passes it on, and the write step records it as `policy.signing_keys` with
   `policy.require_signature: true`.

What happens by case:

| `init` flags | Signed, ships its key | Signed, ships no key | Unsigned |
| --- | --- | --- | --- |
| none | checked against the shipped key; trusted; required | installs, unchecked; warns, naming `--signing-key` | installs; warns that later releases will not be checked |
| `--signing-key K` | checked against `K`; shipped key ignored; required | checked against `K`; required | refused |
| `--signing-key K --require-signature=false` | checked against `K`; not required | checked against `K` | installs, as today |
| `--require-signature` | as "none" | refused: nothing to check against | refused |
| `--require-signature=false` | checksum only; nothing trusted | as today | as today |

"Unsigned" means a release with neither `SHA256SUMS.minisig` nor
`minisign.pub`. A release that ships a key with no signature is refused in
the rows that would trust it, "none" and `--require-signature`. The other
rows ignore its key file, so for them it is "Unsigned" (§5.1).

The up-front refusal of `--require-signature` with no key (`init.go:115`)
stays for an `init` given no `--release`. That `init` has no stage step and
no release to ask, so it is still refused before anything is created. When a
release is given, the refusal moves to the stage step, where it can tell a
release that ships a key from one that does not. That step runs after the
directories and the machine key exist, as a failed fetch or signature check
does today. A corrected `init` then runs again over them (§3).

### 5.3 A trusted key means a required signature

`--require-signature` becomes tri-state on `init`. Unset, it is `true`
whenever a key is trusted, given or shipped, and `false` when none is.
`--require-signature=false` is the explicit opt-out. An operator who pins a
key and still accepts unsigned releases can say so; the default no longer
hands the downgrade to anyone who strips a signature.

This changes `init` only. `release trust add` on an installation that does
not require signatures leaves the requirement as it is, as it does today.

### 5.4 What it does not protect, and what a later release cannot do

The first release is trusted on the strength of how it arrived: the
registry, the TLS connection to it, the directory it was copied from. An
attacker who controls that at the moment of the first install installs their
own release, signed by their own key, and the installation trusts that key.
That is no worse than today's install without a key, which trusts that
release and every one after it. It is worse than passing the vendor's key
from an independent channel, which stays the way to verify the first install.

`init` prints the key and its id, and the docs tell an operator who wants
the first install verified to compare them with the vendor's published key,
or to pass `--signing-key`.

After the first install the shipped key is never trusted again:

- `update` verifies against `policy.signing_keys`, as now. A later release's
  `minisign.pub` neither adds a key nor lowers the requirement.
- A later release signed by another key is refused as now. When it ships a
  key the installation does not trust, the refusal names that key's **id**
  and points to the vendor's published key and the rotation procedure in
  `publishing.md`. It does not print the full key, which would make trusting
  a hijacked one a copy-paste.
- `init --repair --release` of an installation that exists is not a first
  install. A repair with nothing to repair creates an installation
  (`init.go:92`, and the repair branch at `init.go:582` needs one), and is a
  first use like any other `init`. One that repairs never reads the
  shipped key, so a repaired installation without keys stays without them,
  as decision 13 has `update` leave one. Its stage step verifies against the
  installation's policy, as `update` does, instead of against its own flags
  alone (§3). Keys given with `--signing-key` are checked alongside the
  policy's and, as now, are not recorded. A signature is required when the
  policy or `--require-signature` requires one. Otherwise a repair could
  stage a release signed by a key the policy does not trust, and record that
  check as a success.

So "never from the bundle" holds for policy, always. A bundle supplies a
key only to an installation that has none, and only to raise the bar.

### 5.5 The wizard, the plan and the record

- **The wizard**, when it runs, asks whether to trust a key the release
  ships, defaulting to yes. It reads the release only as far as it already
  does (`releaseManifest`, `internal/cli/commands.go:1301-1314`). For a local
  bundle it reads the bundle in place, so the question shows the key and its
  id. For a remote one it reads only the manifest, so the question says that
  the key is printed when the stage step fetches the release. It asks only
  where the stage step would trust a key (§5.2). It does not ask after
  `--signing-key` or `--require-signature=false`, or on a repair of an
  installation that exists. The wizard reads a local bundle in place, and
  the stage step reads the copy it fetched. The wizard therefore passes on
  the id of the key it showed, and the stage step refuses a shipped key with
  any other id: a key that changed in between is not the one the operator
  approved. Answering no installs with checksum verification only, as
  `--require-signature=false` does. If `--require-signature` was given,
  answering no is refused instead, before anything is created, as a release
  that ships no key is (§5.2). The operator asked for a signature and turned
  down the only key there is.
- **`init --dry-run`**, where the stage step would trust a key (§5.2),
  shows the key of a local bundle as a line of the stage step. A plan does
  not fetch a remote bundle (`checkPlannedRelease`, `ops.go:355-360`). For a
  remote bundle, the plan says that the stage step will trust a key if the
  release ships one, and prints no key. Where the stage step would not
  trust one, the plan says nothing about a shipped key. In every case the
  plan does not check the signature, as it does not today
  (`init.go:190-193`).
- **The record.** The stage step's journal message names the key id and
  that it came from the release. `release trust list` shows the key like any
  other. JSON output carries the trusted key and its id. No new state field:
  the release record's `SignatureCheck` already says which key verified the
  first release.

### 5.6 The vendor side

- **`release verify`** without `--signing-key` checks a shipped key against
  the shipped signature, and says that this proves self-consistency only.
  With `--signing-key`, it also refuses a shipped key that differs from the
  one given, which catches a stale `minisign.pub` before it reaches a
  registry.
- **`publishing.md`** tells vendors to ship `minisign.pub` and still publish
  the key where users can compare it.

### 5.7 Alternatives considered

- **Keep the key in the README.** No code, and the documented path today.
  It keeps the long command, and an operator who skips it gets no checking at
  all, for good.
- **Keyless signing (cosign).** The reference itself names the identity, so
  there is no key to distribute. Rejected by RFC 0004 decision 7, for the
  dependency and for needing an online transparency log; nothing here
  reopens that.
- **Fetch the key from a URL or a well-known path at `init`.** It is a second
  channel only if it is a different host. Usually it is the vendor's own
  site or the same GitHub organisation, and it adds a network dependency
  that an offline install cannot meet. A key in the bundle works the same
  for every transport (RFC 0004 decision 22).
- **A manifest field instead of a file.** It would be validated and
  documented in the schema, but it needs a `min_manager_version` floor and
  a schema change for what is one more file covered by the signature.
- **Ask before trusting, always.** It is the SSH prompt, and a
  non-interactive `init` would then need a flag to say yes. The
  non-interactive default is what makes the short command work; the wizard
  asks.

## 6. Tests

- Suite tests for each row of the table in §5.2, against signed and unsigned
  fixture bundles, asserting the recorded policy and the refusals. Two
  further cases: a release that ships a key with no signature, refused under
  "none" and installed under `--require-signature=false`; and
  `--require-signature` with no `--release`, refused with nothing created.
- A release that ships one key and is signed by another is refused at
  `init`.
- `init --repair --release` on an installation with a key refuses a release
  that is signed by another key and ships that key. On an installation
  without keys, the repair leaves it without keys. With no installation, it
  trusts the shipped key as `init` does.
- After a first-use install, an `update` to a release signed by a different
  key, shipping that key, is refused, and the refusal names the key id, not
  the key.
- An installation without keys that updates to a release shipping a key
  stays without keys, and the update prints the notice decision 13
  describes, with the key id and not the key.
- `init --dry-run` of a local bundle shows the key; JSON output carries it.
  Of a remote one, it says a shipped key would be trusted and prints none.
- CLI tests for the tri-state `--require-signature`, and for the wizard's no
  being refused after an explicit `--require-signature`.
- A local bundle whose key changes between the wizard and the stage step is
  refused.
- `init --dry-run --signing-key K`, and a dry-run repair of an installation
  that exists, say nothing about a shipped key.
- `release verify` with and without `--signing-key` against a matching, a
  stale and a missing `minisign.pub`.
- Sabotage: trust the shipped key on `update`, or on a repair; skip the
  shipped key's signature check; leave the requirement off after trusting;
  verify a repair against its flags alone.

## 7. Docs

- `pages/docs/authoring/publishing.md`: ship `minisign.pub`; keep publishing
  the key; the rotation procedure, now also what a first-use installation
  sees when it is skipped.
- The install and `init` reference: the short command, what the printed key
  is for, and the §5.4 limit, worded as a limit. "Verified" must not be used
  of the first install.
- `pages/docs/operating/unattended-updates.md`: a first-use installation
  meets `update.auto_apply`'s requirement with no further flags, which is
  worth one sentence.
- Changelog: `Added` for first use, `Changed` for `--signing-key` implying a
  required signature.

## 8. Out of scope

- **Rotation signed by the old key.** A release signed by the trusted key
  could introduce its successor. Useful, and a protocol of its own; rotation
  stays `release trust add` then `remove`.
- **`release trust add --from-release`.** Trusting the key a release ships
  on an existing installation, as a command. Decision 13 keeps that a step
  the operator takes with the key in hand; a command that takes it from the
  release would be the adoption it ruled out, one keystroke later.
- **Simplifying `--recovery-recipient`.** The other long value in the same
  command, with different trade-offs.

## 9. Risks

- **Read as stronger than it is.** A first-use install is not verified; it
  is pinned. Mitigation: the output says it trusted a key from the release,
  the docs say what that means, and §5.4 is the wording to follow.
- **A vendor who rotates without the procedure.** Every first-use
  installation refuses the next release. That is the protection working, and
  the refusal says what happened and where to look.
- **`--signing-key` scripts change behaviour.** A script passing a key
  without `--require-signature` now refuses an unsigned release. Mitigation:
  `--require-signature=false`, and a `Changed` changelog entry.
- **Unattended apply on the strength of a registry.** A first-use
  installation can turn on `update.auto_apply` with no extra flag, which
  hands the vendor unattended root on a key the registry supplied. It is
  still a separate, explicit `config set`, and the docs say what it rests
  on.

## 10. Unresolved questions

None. The author ruled decision 13 on 2026-11-07, and on 2026-11-08 accepted
the review's narrowing of decision 1 and decision 15.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `LOCKED` | An `init` that creates an installation, given no `--signing-key`, and not told `--require-signature=false`, trusts the key a signed release ships and requires a signature from then on, without asking unless the wizard runs. It is the premise: reversing it to an always-ask or a flag brings back the long command this RFC exists to remove. Narrowed from the review to an `init` that creates an installation, leaving a repair to decision 15; accepted by the author 2026-11-08. |
| 2 | `ASSUMED` | The key ships as `minisign.pub` at the bundle root, the file `minisign -G` writes, one key. A file rather than a manifest field: no schema change, no version floor, older managers ignore it, and the signature already covers it through `SHA256SUMS`. |
| 3 | `ASSUMED` | A release that ships a key must verify against it. A key that does not parse, a signature that does not verify against the shipped key, or a shipped key with no signature is refused at `init`, because a release that ships a key declares that it is signed. This applies only where `init` would trust the key: given `--signing-key` or `--require-signature=false`, `init` ignores the whole file. |
| 4 | `ASSUMED` | An explicit `--signing-key` wins: the shipped key is ignored and the signature must verify against the given one. The independent channel is never overridden by the bundle. |
| 5 | `ASSUMED` | `--require-signature` is tri-state on `init`: unset means `true` whenever a key is trusted, given or shipped; `--require-signature=false` opts out and disables trust on first use. `release trust add` does not change the requirement. |
| 6 | `ASSUMED` | `--require-signature` with no key is refused in the stage step, not before it, and only when the release ships no key. An `init` given no `--release` has no stage step, so it is still refused before anything is created, as now. |
| 7 | `ASSUMED` | After the first install the shipped key is never trusted: `update` verifies against `policy.signing_keys` only, and a later release's key file changes neither the keys nor the requirement. "Never from the bundle" stays true of policy. |
| 8 | `ASSUMED` | A later release signed by an untrusted key it ships is refused with that key's id, never the full key, and a pointer to the vendor's published key and the rotation procedure. The full key in a refusal would make trusting a hijacked key a copy-paste. |
| 9 | `ASSUMED` | `init` prints the trusted key and its id and says it came from the release; the docs never call a first-use install verified. |
| 10 | `ASSUMED` | The wizard asks before trusting, defaulting to yes. It shows the key only for a local bundle; for a remote one it says the stage step will print it. Answering no installs with checksum verification only, unless `--require-signature` was given, in which case no is refused. `--dry-run` shows the key for a local bundle and says one would be trusted for a remote one, which a plan does not fetch. JSON carries it. Both the wizard and the plan speak only where the stage step would trust a key. The stage step refuses a shipped key other than the one the wizard showed. |
| 11 | `ASSUMED` | No new state field. The journal names the key id and its source, `release trust list` shows the key, and the release record's `SignatureCheck` says which key verified the first release. |
| 12 | `ASSUMED` | An unsigned release, or a signed one that ships no key, still installs with no flags, with a warning. Refusing would break every unsigned product's first command for a protection its vendor has not opted into. |
| 13 | `ASSUMED` | Whether an existing installation without keys trusts the key of the first release that ships one. **Recommended: no.** `update` prints a notice with the key id and the `release trust add` and `config set release.require_signature=true` commands, and trusts nothing itself: an unattended timer adopting whatever a moved tag ships would pin an attacker's key without anyone looking. The alternative is automatic adoption at the next interactive `update`. Was `OPEN`; ruled by the author 2026-11-07: as recommended. |
| 14 | `ASSUMED` | `release verify` checks a shipped key against the shipped signature, saying it proves self-consistency only, and with `--signing-key` refuses a shipped key that differs from the given one. |
| 15 | `ASSUMED` | *(2026-11-08, from the review)* `init --repair` of an installation that exists is not a first use; one with nothing installed creates an installation and is. A repair never reads the shipped key. Its stage step verifies against the installation's policy, plus any `--signing-key` it is given, which it does not record, as now. A signature is required when the policy or `--require-signature` requires one. Today a repair given no key verifies nothing, even where the policy requires a signature (`init.go:718-722`, `init.go:632`). Accepted by the author 2026-11-08. |

## 12. Phasing

- **P1, the manager:** §5.1–§5.5 and decisions 1–13 and 15, with the
  operator docs. One PR.
- **P2, the vendor side:** §5.6 and decision 14, with `publishing.md`. It can
  land before P1 so vendors ship the key first; nothing in P1 depends on it.
