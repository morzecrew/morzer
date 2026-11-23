# RFC 0037 — Freshness of a signed release

- **Status:** ✅ Complete — P1 shipped 2026-11-02 in #254, and P2 2026-11-23
  in #411 and #412. Proposed, and ruled by the author 2026-11-01: §11's
  `OPEN` rows as recommended. The entries P1's build and review wrote in
  [logs/version-floor.md](../logs/version-floor.md) are decisions 18–22. P2
  was gated on a vendor asking, and the author ruled 2026-11-22 to build it
  now: decision 23 lifts the gate, and decisions 24–32 settle what the design
  left open. Decision 33 was added on #410's review. Decisions 34–42 record
  what P2's build and review added, from
  [logs/channel-index.md](../logs/channel-index.md).
- **Scope:** What stops a correctly signed but *stale* release from being
  installed, or from being the newest thing a machine ever learns of. Covers
  two threats SECURITY.md lists as one known gap: a rollback to an older signed
  release, and a freeze, where a channel keeps serving an old release so the
  machine never hears of a newer one. Proposes refusals that need nothing from
  a vendor (version floors per installation and per channel), and a vendor opt-in signed channel index
  with a sequence and an expiry for the freeze case. The minisign verifier, the
  channel follower (`update --check`, `--stage`, `--unattended`) and the update
  candidate record are touched; the bundle format and `SHA256SUMS` signature do
  not change. A machine installing from a path gets the installed floor and
  nothing else: freeze detection is out of its reach by construction, because
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
- Nothing changes for a vendor who does nothing, beyond the refusals in the first
  goal. An air-gapped operator installing from a path meets only the installed
  floor, which `--allow-older` overrides.

**Non-goals**

- **A full TUF implementation.** Root rotation, delegations and threshold
  signatures solve problems a single-vendor, single-key channel does not have.
  The index here borrows TUF's timestamp idea and nothing else.
- **Freeze detection for offline installs.** A machine that installs from a path
  has no view of what is current; claiming to detect a freeze there would be
  false. The installed floor still applies to it (§5.1).
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
- **A path is a source like any other for the installed floor.** `morzer update
  ./bundle` with a release older than the one running is refused as any other
  reference is, so an air-gapped machine gets the same downgrade refusal. A path
  is never a channel, so it has no channel mark.
- **A channel is named by its reference with credentials removed.** The key in
  `channel-marks.json`, and every refusal or journal entry that names a channel,
  goes through `ports.RedactRefCredentials` first, as `FollowChannel` already
  does with the reference it reports. A channel reference may carry a password
  in its userinfo, and this file must not become a copy of it.
- `update --allow-older` (name `ASSUMED`) installs a release below either floor,
  and is journalled. It is the escape hatch for a vendor who withdrew 1.4.0 and
  told customers to go back to 1.3.x.

**Crash safety.** Nothing here couples two writes that a crash could split.

- The installed floor is not stored apart from the release it guards: it is the
  version in `current-release.json`, which the state store writes atomically
  (`atomicfs.WriteFile`) as part of activation. There is no second file whose
  write could lag the activation and let a later update accept an older release.
- A channel mark is written after the candidate's bundle is verified and
  *before* the candidate record (`update-candidate.json`), both atomically. A
  crash between the two leaves a mark with no candidate, and the next poll stages
  the same version again, which the mark admits. The other order could leave a
  staged candidate whose channel mark was never raised, so mark-first is the
  contract. A crash before either write leaves both as they were.

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
  That `sequence` is higher than the highest it has accepted, or equal to it with
  byte-identical content, so a replayed older index is refused. The machine keeps
  the digest of the index it accepted to make the equal case checkable. A
  *different* index carrying an accepted sequence is refused and reported as the
  vendor's publishing error: otherwise an attacker could replay an older signed
  index that reused the number, with the older tag it matches. That `expires_at` is in the future. That
  `release.digest` matches the bundle the channel tag resolves to, so the index
  and the tag cannot be mixed from different moments.
- **Which digest it binds.** `release.digest` is the bundle's *content* digest:
  the transport-independent value `release build` and `release verify` report
  and `update --digest` pins ([0004](0004-distribution-and-verification.md)
  decision 4). It is not the OCI manifest digest a peek returns
  (`ChannelState.UpstreamDigest`). That one is the registry's, and it changes
  when identical content is pushed again, which `stageCandidate` already treats
  as no change. So the checks split by cost. The index's signature, sequence and
  expiry are checked at the peek, since they need no download. The content
  digest is compared once the candidate is fetched and its digest computed,
  before it is staged, and a mismatch refuses the candidate.
- **When it is checked: every poll, moved or not.** `FollowChannel` returns
  early when the upstream digest is unchanged, and a frozen channel is precisely
  one whose digest never changes. So the index is fetched and checked at the
  start of every poll, before that early return, and its `expires_at` is
  recorded in state, so `status` and `doctor` report expiry between polls too.
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
  older than the installed one is refused, from a registry and from a local path
  alike; `rollback` still works, and
  `--allow-older` installs and is journalled.
- Suite: `update --check` and a peek seeing a higher tag raise no mark, so a
  registry advertising `9.9.9` blocks nothing; switching to another channel
  starts from that channel's mark.
- Failure injection: a crash after the channel mark is written and before the
  candidate record, and one before either, each leave a state the next poll
  recovers from without admitting an older release.
- Suite: the channel marks survive a manager restart and an `installation
  export`/import (whether import carries them is §11 row 5).
- Index (if built): a fixture registry serving a valid index; a replayed index
  with a lower sequence; an expired one; one whose digest disagrees with the
  tag; a bad signature; a different index reusing an accepted sequence; and an
  index that expires while the channel tag stays unchanged, which must still
  produce the freeze signal. Each is refused or warned per the decisions, against the
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
| 1 | `ASSUMED` | **Build layer 1 (the installed floor and channel marks) now?** Recommended yes: it needs nothing from a vendor, turns the existing downgrade warning into a refusal, and closes the rollback attack within a channel for every machine that has fetched the newer release from it. Was `OPEN`; ruled by the author 2026-11-01: as recommended, layer 1 is built now as P1. |
| 2 | `ASSUMED` | **Build layer 2 (the signed index) now, or gate it on a vendor asking?** Recommended: gate it. It costs a vendor periodic re-signing, and no vendor has asked; the design here is enough to build from when one does. Was `OPEN`; ruled by the author 2026-11-01: as recommended, gated on a vendor asking. Superseded by decision 23. |
| 3 | `ASSUMED` | **An expired index: refuse to stage, or stage and warn?** Recommended: warn, in `status`, `doctor` and the tick's notification, and refuse only under unattended `auto_apply`. A freeze is a reason to look, and refusing manual updates would block the operator from installing the fix they went to get by hand. Was `OPEN`; ruled by the author 2026-11-01: as recommended. |
| 4 | `ASSUMED` | **Name and shape of the override.** Recommended `update --allow-older`, journalled, with no persistent setting: going back past what a machine has seen should be a decision made each time. Was `OPEN`; ruled by the author 2026-11-01: as recommended, `update --allow-older`, journalled, with no persistent setting. |
| 5 | `ASSUMED` | **Does an export carry the channel marks?** Recommended yes: a rebuilt machine that forgot its channel delivered 1.4.0 can be offered 1.3.0 again. Was `OPEN`; ruled by the author 2026-11-01: as recommended. |
| 6 | `ASSUMED` | A floor is a release *version*, not a time: versions already order releases, and a timestamp would be a second ordering that can disagree with the first. |
| 7 | `ASSUMED` | `rollback` is exempt from both floors, and moves the installed floor down with the release; it has its own schema checks, and going back is its purpose. |
| 8 | `ASSUMED` | The index is signed with the vendor's bundle key from `policy.signing_keys`, not a separate key, so a vendor has one key to protect and an installation one list to configure. |
| 9 | `ASSUMED` | On OCI the index is an artifact under `<channel>.index`, fetched in the same poll as the channel's peek. A channel without an index is followed as today. |
| 10 | `ASSUMED` | The index pins the release by its bundle content digest, never the registry's manifest digest, and the comparison happens after the fetch that computes it. A disagreement between the index and the fetched bundle is a refusal: a tag and an index read at different moments must not combine into a release neither named. |
| 11 | `ASSUMED` | The installed floor is the current release's version, read from the current-release record, and is source-independent because an installation is one product. Channel marks are kept per channel, so a switch to another channel is judged by that channel's own history. |
| 12 | `ASSUMED` | Only a bundle that was fetched and verified raises a channel mark. A peek and `update --check` see tags and manifest digests, so they raise nothing: a registry advertising a false high version must not be able to block real releases below it. |
| 13 | `ASSUMED` | A channel is identified by its reference after `ports.RedactRefCredentials`, in the stored key and in every message and journal entry that names it. |
| 14 | `ASSUMED` | The installed floor covers every install path, a local bundle path included, since a downgrade is the same act whatever the transport. A path has no channel mark, and freeze detection does not reach it. |
| 15 | `ASSUMED` | The installed floor is read from `current-release.json`, written atomically at activation, so no separate write can lag it. A channel mark is written before the candidate record, so a crash can leave a raised mark with no candidate, which the next poll repairs, and never a candidate without its mark. |
| 16 | `ASSUMED` | An index is accepted only with a higher sequence, or the same sequence and byte-identical content. A vendor publishes any change under a higher sequence, and two different indexes under one sequence are refused as a publishing error. |
| 17 | `ASSUMED` | The index is fetched and checked on every poll, before the unchanged-digest early return in `FollowChannel`, and its expiry is recorded in state; a freeze is a channel that never moves, so a check that runs only when it moves never sees one. |
| 18 | `ASSUMED` | *(2026-11-02, from the build; accepted 2026-11-02)* The channel marks are a file under the manager directory, written atomically by the lifecycle layer as the notification outbox is, rather than more methods on the state port. See [logs/version-floor.md](../logs/version-floor.md) (unlisted, attempt 1, 16:56, the marks file). |
| 19 | `ASSUMED` | *(2026-11-02, from the build; accepted 2026-11-02)* `update` refuses a release older than the one installed at exit 9, `incompatible`, as `upgrade_from` refuses: a release that cannot be installed over what is running. The channel's refusals keep exit 2, since they are a poll's outcome rather than an install. See [logs/version-floor.md](../logs/version-floor.md) (unlisted, attempt 1, 17:05, the exit code). |
| 20 | `ASSUMED` | *(2026-11-02, from the build and its review; accepted 2026-11-02)* **Refines decision 4.** `--allow-older` beside `--check`, `--stage` or `--unattended` is a usage error, and staging never installs below a channel's mark. A resumed update keeps `--allow-older` only for the release the interrupted run was installing. See [logs/version-floor.md](../logs/version-floor.md) (unlisted, attempt 1, 17:05, the modes; and unlisted, attempt 2, 17:34, which narrows the resume). |
| 21 | `ASSUMED` | *(2026-11-02, from the build and its review; accepted 2026-11-02)* **Refines decision 5.** An import replaces the marks with the export's, keyed again with credentials removed, and removes them when the export carries none. An export naming no channel, or a mark with no version, is refused before anything is touched. An export whose marks cannot be read is written without them, with a warning. See [logs/version-floor.md](../logs/version-floor.md) (unlisted, attempt 1, 17:05, the export; and unlisted, attempt 2, 17:34, which makes the import a replacement). |
| 22 | `ASSUMED` | *(2026-11-02, from the build and its review; accepted 2026-11-02)* `update --allow-older` and `rollback`, when they install a release below the followed channel's mark, lower that mark to the release they installed, in one write. The poll looks again at a refusal recorded no later than its channel's mark, which happens when the mark was lowered or replaced since, so a vendor's withdrawal does not leave the follower refusing the patches that follow; times are kept to the second, so a refusal recorded in the same second as its mark costs one extra fetch. The installation is read under the lock, so the channel lowered is the one followed when the operation finishes; other channels' marks are left alone. The lowering is best-effort: if it cannot be written after the operation succeeded, the operator is warned and the mark stays, and the follower's refusal names `--allow-older` as the way out. See [logs/version-floor.md](../logs/version-floor.md) (unlisted, attempt 3, 18:02, which supersedes attempt 1 at 16:56 and attempt 2 at 17:34). |
| 23 | `ASSUMED` | **Supersedes decision 2.** Layer 2, the signed index, is built now as P2, without waiting for a vendor to ask. Ruled by the author 2026-11-22. |
| 24 | `ASSUMED` | A vendor publishes an index with the manager's own command, `morzer release index <channel reference> --key <file>` (name `ASSUMED`), on the pattern of `state publish`. It resolves the bundle the channel tag points at and writes that bundle's content digest. It takes the issue time in milliseconds as the sequence, and refuses when that is not higher than the sequence of the index already published under the tag. It sets `expires_at` from `--valid-for`, seven days unless given, signs `index.json` with the key, and pushes it and its signature as one artifact under `<channel>.index`. `--dry-run` does everything but the push. Re-signing is the same command run again. The alternative, a guide to doing it with `jq`, `minisign` and `oras`, leaves each vendor's scheduled re-sign a script of their own. Ruled by the author 2026-11-22. |
| 25 | `ASSUMED` | **Refines decision 9.** A channel without an index is followed as today only while this installation has never accepted an index from it. Once it has, a poll that finds none judges the tag against the accepted index until that expires, by decision 10's comparison, and then reports decision 3's expired-index warning. Otherwise removing the index would undo it. A vendor who stops publishing an index is answered on each machine with `update --stage --forget-index` (name `ASSUMED`), which forgets that channel's accepted index and is journalled. Ruled by the author 2026-11-22. |
| 26 | `ASSUMED` | An installation with no key in `policy.signing_keys` cannot verify an index, so it does not consult one. The poll records that the channel publishes an index, and `doctor` reports that this machine cannot check it, with a hint to pin the vendor's key. An unattended install already needs a pinned key (`AssessUnattended`). Ruled by the author 2026-11-22. |
| 27 | `ASSUMED` | **Refines decision 8.** `release index` takes a minisign secret key file without a password, as `state publish` takes the authority's key, so a scheduled job can re-sign. A vendor who keeps a password on the key re-signs by hand, with `minisign` and `oras`, which the publishing guide shows. Ruled by the author 2026-11-22. |
| 28 | `ASSUMED` | The tick notifies once when the accepted index expires, and not on every poll after it, as a sync's expiry notifies once (`sync.go:581-590`). Agreed by the author 2026-11-22. |
| 29 | `ASSUMED` | A candidate refused because the tag and the accepted index disagree (decision 10) is fetched again once a newer index is accepted. The refusal records the index's sequence, and the poll looks again at a refusal recorded under an older one, as decision 22 has it look again after a mark was lowered. Otherwise a vendor who moves the tag before re-signing the index keeps every machine refusing that release until the next one. Agreed by the author 2026-11-22. |
| 30 | `ASSUMED` | Only the channel follower reads an index: the scheduled tick and `update --stage`. An `update` of a reference and `update --check` do not, which keeps decision 3's manual path open. `status` and `doctor` report what the last poll recorded. Agreed by the author 2026-11-22. |
| 31 | `ASSUMED` | **Refines decisions 5 and 21.** An export carries each channel's accepted index as recorded: its sequence, the digest decision 16 compares, the release it names and its expiry. An import replaces them as it replaces the marks. A rebuilt machine that forgot the sequence would accept a replayed older index. Agreed by the author 2026-11-22. |
| 32 | `ASSUMED` | An index's expiry is judged by the machine's clock, with no allowance for skew, as a sync document's expiry is (`sync.go:325`). Agreed by the author 2026-11-22. |
| 33 | `ASSUMED` | Decision 24's check and its push are not atomic. OCI distribution has no conditional tag write, so two runs of `release index` for one channel can both pass the sequence check, and the tag write that lands last wins. No mechanism the registry offers closes that, and the risk is accepted as RFC 0039 §9 accepted it for `release push`: the publishing guide runs every `release index` and `release push` of a repository in one CI concurrency group, so a scheduled re-sign never overlaps a release. An older index that lands last is refused as a replay by each machine that accepted the newer one (decision 16), and the next re-sign publishes a higher sequence. Added on #410's review, 2026-11-22. |
| 34 | `ASSUMED` | *(2026-11-22, from the build of #411 and its review)* **Refines decision 24.** `release index` compares its sequence only with a published index whose signature verifies against the public half of `--key` and which names the same channel. Any other index under the tag is replaced, with a warning: one that cannot be read, one another key signed, or one for another channel. A tag is writable by anyone who can push to the repository, so a planted index at a high sequence would otherwise stop the vendor re-signing until their clock passed it, as `state publish` counts only the serials its key signed. A read the registry refuses, or cannot answer, stops the command before anything is pushed: it says nothing about what is there. See [logs/channel-index.md](../logs/channel-index.md) (unlisted, attempt 2, 2026-11-22T23:40:54Z, which supersedes attempt 1 at 22:52:42Z). |
| 35 | `ASSUMED` | *(2026-11-22, from the build of #411)* The index is an artifact of type `application/vnd.morzer.channel.index.v1` with two layers: `index.json` as `application/vnd.morzer.channel.index.v1+json`, and its signature as `application/vnd.morzer.channel.index.v1.minisig`, each titled by its file name as `oras push` titles it. A machine selects the layers by type, and a hand-signed artifact (decision 27) must match. Each layer is read up to 64 KiB, and a layer over that is refused. See [logs/channel-index.md](../logs/channel-index.md) (unlisted, attempt 1, 2026-11-22T22:52:42Z, the media types). |
| 36 | `ASSUMED` | *(2026-11-23, from the build of #412)* An index the follower cannot accept counts as no index: its signature fails, it does not parse, it names another channel (decision 40), its sequence is lower than the accepted one, or it reuses the accepted sequence with other bytes (decision 16). Decision 25 then judges the tag against the accepted index if there is one, and a channel with none is followed as today. The reason is recorded, and `status` and `doctor` report it. Refusing to stage over it would add nothing, since whoever can plant a bad index can delete it. Ruled by the author 2026-11-23. See [logs/channel-index.md](../logs/channel-index.md) (unlisted, attempt 3, 2026-11-23T07:29:03Z, an index the follower cannot accept). |
| 37 | `ASSUMED` | *(2026-11-23, from the build of #412)* **Refines decisions 10 and 17.** Every poll compares the accepted index's release digest with the content digest already known for what the tag points at: the staged candidate's, or the installed release's when the channel delivered it. An unmoved tag is never fetched, so without this a registry serving the fresh index while it holds the tag still would never be caught, and the index would never expire. When they disagree, the tick does not install the staged candidate, and `status` and `doctor` report that the index names one release while the tag still serves another. Ruled by the author 2026-11-23. See [logs/channel-index.md](../logs/channel-index.md) (unlisted, attempt 3, 2026-11-23T07:29:03Z, an unmoved tag). |
| 38 | `ASSUMED` | *(2026-11-23, from the build of #412 and its review)* **Refines decision 3.** Under `auto_apply`, a release behind an expired index, or behind one that names another release, is staged and not installed, with that as the tick's reason, as the drift and stopped gates hold a release. The unattended update asks again under its deployment lock, judged against the accepted index of the channel the release was staged from and the keys in force then, and is refused before anything runs when the index holds it. It is refused too when `update.channel` names another channel by then; the next tick follows that one. An operator's update still does not read the index (decision 30). Ruled by the author 2026-11-23. See [logs/channel-index.md](../logs/channel-index.md) (unlisted, attempt 3, 2026-11-23T07:29:03Z, decision 3's refusal; and unlisted, attempt 4, 2026-11-23T13:41:00Z, the recheck under the lock). |
| 39 | `ASSUMED` | *(2026-11-23, from the build of #412 and its review)* **Refines decision 9.** With a key pinned, a registry that cannot be reached for the index fails the poll, as a failed peek does. A read the registry answers with a refusal other than 404 counts as no index (decision 36), and is recorded and reported. Without a pinned key the index is not consulted (decision 26), and a failed read only leaves it unrecorded. A channel whose tag cannot carry `.index` within the 128-character tag limit has no index, as a digest reference has none. Ruled by the author 2026-11-23. See [logs/channel-index.md](../logs/channel-index.md) (unlisted, attempt 3, 2026-11-23T07:29:03Z, a failed read; and unlisted, attempt 4, 2026-11-23T13:41:00Z, a tag too long for the suffix). |
| 40 | `ASSUMED` | *(2026-11-23, from the build of #412)* The follower accepts an index only when its `channel` is the followed reference's tag. Otherwise another channel's index, copied under this channel's index tag, would let whoever can push tags move a channel to a release the vendor published for another one. Agreed by the author 2026-11-23. See [logs/channel-index.md](../logs/channel-index.md) (unlisted, attempt 3, 2026-11-23T07:29:03Z, the `channel` field). |
| 41 | `ASSUMED` | *(2026-11-23, from the build of #412)* **Refines decision 17.** The accepted indexes are a file under the manager directory beside the channel marks, `channel-indexes.json`, keyed as the marks are and written atomically by the lifecycle layer, as decision 18 placed the marks. The same record holds what the last poll found, which `status` and `doctor` report: an index this machine cannot check, one it did not accept and why, and a tag that disagrees with it. See [logs/channel-index.md](../logs/channel-index.md) (unlisted, attempt 3, 2026-11-23T07:29:03Z, where the expiry is recorded). |
| 42 | `ASSUMED` | *(2026-11-23, from the build of #412 and its review)* **Refines decision 25.** `--forget-index` is a usage error without `--stage`. It forgets the followed channel's accepted index under the deployment lock, then polls. It writes its `release` journal record first and forgets the index only once the record is written. With no accepted index it forgets nothing, journals nothing and says so. A plan forgets nothing: it reports the sequence it would forget as `would_forget_index`, and polls as if that index were gone. Only expiry notifies (decision 28): a refused index is reported by `status` and `doctor`. Agreed by the author 2026-11-23. See [logs/channel-index.md](../logs/channel-index.md) (unlisted, attempt 3, 2026-11-23T07:29:03Z, `--forget-index`; and unlisted, attempt 4, 2026-11-23T13:41:00Z, its order and its plan). |

## 12. Phasing

- **P1 — the installed floor and channel marks** (§5.1), if row 1 is ruled
  yes. One PR: the channel-marks file, the refusal in `update`, staging and the tick, `--allow-older`, the
  export field, docs, and the SECURITY.md entry rewritten to what remains.
  *(2026-11-02: row 1 was ruled yes, and P1 was built in #254.)*
- **P2 — the signed index** (§5.2), gated on a vendor asking (row 2). The
  verifier side, the `status`/`doctor` lines, and the vendor's publishing guide.
  *(2026-11-22: decision 23 superseded row 2, and P2 is built now; decisions
  24–33 settle what it left open.)*
  *(2026-11-23: P2 was built in #411, the vendor's side, and #412, the
  machine's.)*
