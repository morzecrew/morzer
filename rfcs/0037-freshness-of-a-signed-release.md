# RFC 0037 — Freshness of a signed release

- **Status:** 📝 Draft — proposed, **awaiting the author's rulings** on the
  `OPEN` decisions in §11; not scheduled
- **Scope:** What stops a correctly signed but *stale* release from being
  installed, or from being the newest thing a machine ever learns of. Covers
  two threats SECURITY.md lists as one known gap: a rollback to an older signed
  release, and a freeze, where a channel keeps serving an old release so the
  machine never hears of a newer one. Proposes refusals that need nothing from
  a vendor (version floors per installation and per channel), and a vendor opt-in signed channel index
  with a sequence and an expiry for the freeze case. The minisign verifier, the
  channel follower (`update --check`, `--stage`, `--unattended`) and the update
  candidate record are touched; the bundle format and `SHA256SUMS` signature do
  not change. Offline installs from a path are out of scope by construction:
  nothing on such a machine can tell what is current.
- **Related:** [SECURITY.md](../SECURITY.md) "Known gaps";
  [0004](0004-distribution-and-verification.md) (signature verification, §5.5);
  [0016](0016-update-checking-and-unattended-updates.md) (channels, the
  unattended gate); [0028](0028-the-machines-signing-identity.md) (why a
  machine's own key cannot vouch for a vendor's chronology);
  [`internal/lifecycle/ops/channel.go`](../internal/lifecycle/ops/channel.go),
  [`internal/lifecycle/ops/check.go`](../internal/lifecycle/ops/check.go),
  [`internal/domain/version.go`](../internal/domain/version.go),
  [`internal/adapters/verify/minisign/minisign.go`](../internal/adapters/verify/minisign/minisign.go);
  prior art: [The Update Framework](https://theupdateframework.io/) timestamp and
  snapshot roles.

---

## 1. Summary

A signature proves who made a release, not that it is the newest one. Today an
attacker who can choose which signed bundle a machine is offered can offer an
old one, as long as it is newer than what is installed. Or they can keep a
channel pointing where it was, and the machine never learns of the fix it is
missing.

This RFC proposes two layers. The first needs nothing from a vendor: outside
`rollback`, the machine refuses a release older than the one installed, and
refuses a channel anything older than the newest release it has already fetched
and verified from that channel. The second is opt-in for a vendor: a
small signed channel index carrying a sequence number and an expiry, which lets
a machine refuse a stale or replayed index and say how long it has been unable
to see a current one. The recommendation is to build the first now and the
second when a vendor asks for it.

## 2. Motivation

SECURITY.md states the gap:

> Nothing checks a signature's *freshness*. A correctly signed older release can
> be presented in place of a newer one; only `compatibility.upgrade_from` and a
> pinned `--digest` stand in the way.

Two concrete attacks hide in that sentence, and they need different defences.

- **Rollback.** A vendor ships 1.4.0 to fix a vulnerability in 1.3.0. A machine
  still on 1.2.0 asks its channel what to install. Whoever controls what the
  channel serves (a compromised registry, a mirror, a network position on a
  mirror without pinning) offers 1.3.0, which is signed, newer than 1.2.0, and
  vulnerable. Every check passes: the signature verifies, `upgrade_from`
  admits it, and it is an upgrade.
- **Freeze.** The same party keeps the channel pointing at 1.2.0. Every poll
  answers "unchanged", which is also what an honest quiet channel answers. The
  unattended timer reports nothing wrong for as long as the attacker likes.

Unattended updates make both worth closing. A human reading release notes might
notice a version number is behind; a timer will not.

## 3. Current state

Verified against `main` at 8b3c37e.

- **The signature carries no time the manager reads.** The bundle's signature is
  minisign over `SHA256SUMS`, inside the bundle, checked against
  `policy.signing_keys` ([`verify/minisign`](../internal/adapters/verify/minisign/minisign.go)).
  The manager reads only the verdict. Nothing in a bundle states when it was
  released in a form the manager compares.
- **A channel that moves backwards past the installed version is refused.**
  `stageCandidate` refuses a resolved version older than the current release:
  "the channel points at %s, older than the installed %s"
  ([`channel.go:336`](../internal/lifecycle/ops/channel.go)). A republished
  version with different content is refused too.
- **An explicit `update` to an older version only warns.** `domain.CheckUpgrade`
  reports "target version %s is older than installed %s; this is a downgrade" as
  a warning ([`version.go:389`](../internal/domain/version.go)), and the comment
  at `channel.go:339` says so: the refusal lives in the channel path because
  `update` would otherwise accept it.
- **Nothing remembers what was offered.** The update candidate records the last
  upstream digest seen, as a change token, and the staged candidate. Once a
  candidate is installed or retired, nothing records that a newer version was
  ever seen, so "older than the newest seen" is not a question the manager can
  ask.
- **`update --check` takes the highest tag it is shown.** It enumerates version
  tags through `Source.List` ([`check.go:105`](../internal/lifecycle/ops/check.go))
  and picks the highest admissible one. A registry that hides newer tags is
  indistinguishable from one that has none.
- **Unattended installs already demand a signature.** `AssessUnattended` refuses
  unless `policy.require_signature` is set with a pinned key
  ([`version.go:505`](../internal/domain/version.go)), so every release an
  unattended machine installs is signed, and the freshness gap is the one left
  in that path.
- **Only OCI follows channels.** `ChannelPeeker` is implemented by the OCI source
  alone; HTTPS names one bundle and has no `List`. So the channel path, and the
  threats above, are OCI's today.

## 4. Goals / Non-goals

**Goals**

- Outside `rollback`, a machine refuses a release older than the one installed,
  and refuses a channel a release older than one it already fetched and verified
  from that channel.
- A vendor who opts in can let machines detect a replayed or frozen channel, and
  an operator learns from `doctor` and `status` how long a machine has been
  unable to see a current index.
- Nothing changes for a vendor who does nothing, beyond the refusal in the first
  goal, and nothing changes for an air-gapped operator installing from a path.

**Non-goals**

- **A full TUF implementation.** Root rotation, delegations and threshold
  signatures solve problems a single-vendor, single-key channel does not have.
  The index here borrows TUF's timestamp idea and nothing else.
- **Freshness for offline installs.** A machine that installs from a path has no
  view of what is current; claiming to check freshness there would be false.
- **Protecting against a compromised vendor key.** Whoever holds it can sign a
  fresh index for an old release. Key compromise is the vendor's to handle by
  rotating `policy.signing_keys`.

## 5. Design

### 5.1 Layer 1: two version floors (no vendor change)

Two floors, each with a different identity, and neither raised by anything the
machine has not fetched and verified.

**The installed floor is the current release.** An installation is one product,
fixed at `init`, so its versions are comparable whichever source they came from.
The floor is the version in the current-release record, not a second copy of it:
`update` refuses a release older than the one running, where today it only warns
(`domain.CheckUpgrade`). `rollback` moves the floor down with the release, since
going back is a deliberate act.

**A channel mark is per channel.** For each channel the installation has
followed, the highest version whose bundle was **fetched and verified** from it,
in the manager's state beside the update candidate:

```json
// /var/lib/<product>/manager/channel-marks.json
{
  "oci://registry.example/demo/bundle:stable": {"version": "1.4.0", "at": "2026-10-29T08:00:00Z"},
  "oci://registry.example/demo/bundle:lts":    {"version": "1.3.5", "at": "2026-10-20T08:00:00Z"}
}
```

- **What raises a channel mark: a verified fetch, nothing less.** A candidate the
  channel follower staged, which means fetched, checksummed and signature-checked,
  raises that channel's mark. A channel peek and `update --check` do not: they
  see a tag and a manifest digest, never a verified bundle, and a registry that
  advertises a false `9.9.9` tag must not be able to block every real release
  below it. A prerelease raises a mark only on a dev installation, matching the
  rule that a production machine does not stage one.
- **What a channel mark refuses.** A candidate from that channel older than the
  channel's own mark: the channel delivered 1.4.0, and is now serving 1.3.0.
  Switching to another channel starts from that channel's mark, so moving from
  `stable` to `lts` is refused only if `lts` offers something older than the
  release installed.
- **A channel is named by its reference with credentials removed.** The key in
  `channel-marks.json`, and every refusal or journal entry that names a channel,
  goes through `ports.RedactRefCredentials` first, as `FollowChannel` already
  does with the reference it reports. A channel reference may carry a password
  in its userinfo, and this file must not become a copy of it.
- `update --allow-older` (name `ASSUMED`) installs a release below either floor,
  and is journalled. It is the escape hatch for a vendor who withdrew 1.4.0 and
  told customers to go back to 1.3.x.

This closes the rollback attack within a channel for any machine that has
fetched the newer release from it, and turns the existing downgrade warning into
a refusal. It does nothing for a machine that never fetched the newer release,
which is the freeze.

### 5.2 Layer 2: a signed channel index (vendor opt-in)

A vendor who wants freeze detection publishes, beside the channel, a small JSON
document signed with the same minisign key that signs their bundles:

```json
{
  "schema": 1,
  "channel": "stable",
  "sequence": 418,
  "issued_at": "2026-10-29T08:00:00Z",
  "expires_at": "2026-11-05T08:00:00Z",
  "release": {"version": "1.4.0", "digest": "sha256:…"}
}
```

- **Where it lives.** For OCI, an artifact under the tag `<channel>.index`,
  carrying `index.json` and `index.json.minisig` (mechanics `ASSUMED`). A
  channel with no index is followed exactly as today.
- **What the machine checks.** The signature, against `policy.signing_keys`.
  That `sequence` is not lower than the highest it has accepted, so a replayed
  older index is refused. That `expires_at` is in the future. That
  `release.digest` matches what the channel tag resolves to, so the index and
  the tag cannot be mixed from different moments.
- **What an expired index does.** It is the freeze signal. Whether it refuses
  to stage, or stages and warns, is the decision in §11 row 3. Either way
  `status` and `doctor` report "the channel's index expired N days ago", which
  is the line an operator needs.
- **What it costs a vendor.** Re-signing the index before each expiry, even when
  nothing was released. That means a scheduled job holding the signing key, or
  a long expiry that weakens the signal. The RFC states this rather than hiding
  it: it is the reason the layer is opt-in.

### 5.3 What each option covers

| | Rollback below installed | Rollback below what a channel delivered | Freeze | Vendor cost | Air-gapped cost |
| --- | --- | --- | --- | --- | --- |
| Today | channel path only; `update` warns | no | no | none | none |
| Installed floor (5.1) | yes | no | no | none | none |
| Installed floor and channel marks (5.1) | yes | yes, once the newer bundle was fetched | no | none | none |
| Signed index (5.2) | yes | yes | yes, within the expiry | periodic re-signing | none: no channel |

### 5.4 Alternatives considered

- **A timestamp in the bundle's signature.** Minisign's trusted comment is
  covered by the signature and could carry a release time. It would let a
  machine compare two bundles' ages, but it says nothing about bundles the
  machine was never shown, so it does not detect a freeze. The version already
  orders releases, so a timestamp adds a second ordering that can disagree with
  the first.
- **Expiry on the bundle itself.** Rejected: it would stop a valid release from
  installing offline months later, which is the air-gapped operator's normal
  case.
- **Asking a second source.** Comparing two mirrors' answers detects a freeze
  only when an attacker controls one of them, and it doubles the network surface
  of every tick.

## 6. Tests

- Suite: a channel whose staged 1.4.0 raised its mark, and which then serves
  1.3.0, is refused by staging and by the unattended tick; `update` to a release
  older than the installed one is refused; `rollback` still works, and
  `--allow-older` installs and is journalled.
- Suite: `update --check` and a peek seeing a higher tag raise no mark, so a
  registry advertising `9.9.9` blocks nothing; switching to another channel
  starts from that channel's mark.
- Suite: the channel marks survive a manager restart and an `installation
  export`/import (whether import carries them is §11 row 5).
- Index (if built): a fixture registry serving a valid index; a replayed index
  with a lower sequence; an expired one; one whose digest disagrees with the
  tag; a bad signature. Each is refused or warned per the decisions, against the
  real minisign verifier.

## 7. Docs

- SECURITY.md: the known-gap entry shrinks to what remains (a freeze on a
  channel without an index, and any machine that never saw the newer release),
  or moves to Closed gaps if both layers ship.
- The update reference: the two refusals, their messages, `--allow-older`.
- The publishing guide (vendor side): how to publish and re-sign an index, and
  what an expiry costs and buys.

## 8. Out of scope

- **HTTPS channels.** HTTPS has no channels today; if one is added, it can carry
  the same index.
- **Signing the index with the machine's key.** RFC 0028's key speaks for the
  machine, not the vendor, and cannot vouch for a vendor's chronology.
- **A transparency log.** It would also settle "was this release ever
  published", which the index does not; nothing here needs it yet.

## 9. Risks

- **A vendor withdraws a release and customers must go back.** The channel mark
  refuses that on every machine that fetched the withdrawn release, because it
  cannot be told from a rollback attack. `--allow-older` and `rollback` are the answers, and the
  message has to name them, or an operator in an incident will be stuck.
- **Expiry turns a vendor's outage into customers' warnings.** A vendor who stops
  re-signing makes every machine report a stale index. That is the signal
  working, but it lands on customers. A generous default expiry, and warning
  rather than refusing (row 3), limit the damage.
- **A channel mark is local state an attacker with root could lower.** Root is out of
  the threat model (SECURITY.md "What is not").

## 10. Unresolved questions

- The `OPEN` rows in §11 are the author's to rule. Nothing below them is
  load-bearing until they are.
- Whether the index should also list older versions still supported, so a
  machine can tell "older but still maintained" from "withdrawn". That is not
  needed for either attack; it is a later extension.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `OPEN` | **Build layer 1 (the installed floor and channel marks) now?** Recommended yes: it needs nothing from a vendor, turns the existing downgrade warning into a refusal, and closes the rollback attack within a channel for every machine that has fetched the newer release from it. |
| 2 | `OPEN` | **Build layer 2 (the signed index) now, or gate it on a vendor asking?** Recommended: gate it. It costs a vendor periodic re-signing, and no vendor has asked; the design here is enough to build from when one does. |
| 3 | `OPEN` | **An expired index: refuse to stage, or stage and warn?** Recommended: warn, in `status`, `doctor` and the tick's notification, and refuse only under unattended `auto_apply`. A freeze is a reason to look, and refusing manual updates would block the operator from installing the fix they went to get by hand. |
| 4 | `OPEN` | **Name and shape of the override.** Recommended `update --allow-older`, journalled, with no persistent setting: going back past what a machine has seen should be a decision made each time. |
| 5 | `OPEN` | **Does an export carry the channel marks?** Recommended yes: a rebuilt machine that forgot its channel delivered 1.4.0 can be offered 1.3.0 again. |
| 6 | `ASSUMED` | A floor is a release *version*, not a time: versions already order releases, and a timestamp would be a second ordering that can disagree with the first. |
| 7 | `ASSUMED` | `rollback` is exempt from both floors, and moves the installed floor down with the release; it has its own schema checks, and going back is its purpose. |
| 8 | `ASSUMED` | The index is signed with the vendor's bundle key from `policy.signing_keys`, not a separate key, so a vendor has one key to protect and an installation one list to configure. |
| 9 | `ASSUMED` | On OCI the index is an artifact under `<channel>.index`, fetched in the same poll as the channel's peek. A channel without an index is followed as today. |
| 10 | `ASSUMED` | The index pins the release by digest, and a disagreement between the index and the tag is a refusal: a tag and an index read at different moments must not combine into a release neither named. |
| 11 | `ASSUMED` | The installed floor is the current release's version, read from the current-release record, and is source-independent because an installation is one product. Channel marks are kept per channel, so a switch to another channel is judged by that channel's own history. |
| 12 | `ASSUMED` | Only a bundle that was fetched and verified raises a channel mark. A peek and `update --check` see tags and manifest digests, so they raise nothing: a registry advertising a false high version must not be able to block real releases below it. |
| 13 | `ASSUMED` | A channel is identified by its reference after `ports.RedactRefCredentials`, in the stored key and in every message and journal entry that names it. |

## 12. Phasing

- **P1 — the installed floor and channel marks** (§5.1), if row 1 is ruled
  yes. One PR: the channel-marks file, the refusal in `update`, staging and the tick, `--allow-older`, the
  export field, docs, and the SECURITY.md entry rewritten to what remains.
- **P2 — the signed index** (§5.2), gated on a vendor asking (row 2). The
  verifier side, the `status`/`doctor` lines, and the vendor's publishing guide.
