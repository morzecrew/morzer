# RFC 0028 — The machine's signing identity

- **Status:** 🚧 In progress — **P1 shipped 2026-08-13**, in the same wave as
  its first consumer (0025 P1), as §9 required. The key, the format, schema 6
  and its bump-only migration, succession across a rebuild and the `doctor`
  check are in. **P2 (rotation) is unscheduled** and wanted the first time
  somebody believes a host was compromised.
- **Scope:** One per-installation Ed25519 key, minted at `init`, that lets a
  machine sign statements about itself — the capability [0024](0024-the-support-bundle.md),
  [0025](0025-attesting-an-installation.md) and [0026](0026-fleet-as-a-read-model.md)
  each assumed existed and none of which can proceed without. Covers the key's
  lifecycle, its format, what a signature by it proves, and what happens to a
  chain of signed artifacts when a machine is rebuilt. Deliberately not release
  signing (that is the vendor's, and [0004](0004-distribution-and-verification.md)
  decision 8 keeps it off deployment hosts), not a PKI, not key escrow, and not
  a second encryption identity.
- **Related:** [`internal/adapters/verify/minisign`](../internal/adapters/verify/minisign)
  (the verifier, and the comment this RFC has to answer),
  [`internal/lifecycle/ops/init.go`](../internal/lifecycle/ops/init.go)
  (`stepCreateIdentity`), [`internal/domain/export.go`](../internal/domain/export.go),
  [`internal/domain/installation.go`](../internal/domain/installation.go)
  (`InstallationSchemaVersion`), [0003](0003-secrets-recovery-and-onboarding.md)
  (the age identity and its protections), [0017](0017-recovery-artifacts.md)
  (export, import, and what a rebuilt machine keeps)
- **Origin:** Written 2026-08-12, out of the review of 0024–0026: all three
  locked a decision on "signed with the machine identity", and the review found
  that no such capability exists.

---

## 1. Problem

Three RFCs adopted in the same change assert the same thing, and it is not true:

- 0024 decision 4 — the support bundle is *"signed with the machine identity"*.
- 0025 §4.3 — the attestation is *"signed with the machine identity — the same
  age/minisign material 0003 and 0004 already manage"*.
- 0026 decision 6 — a fleet row is *"signed with the machine identity"*, and
  §9 relies on that signature to detect one machine overwriting another's row.

Measured 2026-08-12:

- **The machine identity is an age identity** — an X25519 key used to *decrypt*
  this installation's secret state. [`ports/secrets.go`](../internal/ports/secrets.go)
  calls it a decryption identity throughout; [`init.go:241`](../internal/lifecycle/ops/init.go)
  creates it as *"the machine's age key"*. age is an encryption format. It is
  not a signature scheme and has no signing operation.
- **Nothing in the manager signs anything.** There is no `Sign` outside tests.
- **The one signing-adjacent component is explicitly verification-only**, and
  says so in a comment that this RFC has to answer rather than ignore
  ([`minisign.go:16`](../internal/adapters/verify/minisign/minisign.go)):

  > Verification only. The manager never signs — see RFC 0004 decision 8: the
  > signing key belongs in a vendor's release pipeline, and building signing into
  > the manager would invite that key onto a deployment host.

So each of those three RFCs has a phase that cannot start. 0025 is the worst
case: its whole artifact is a signed statement, so it cannot begin at all.

## 2. Why this does not reverse 0004 decision 8

The objection above is correct and this RFC keeps it. What it protects is a
*vendor's release signing key* — one key whose signatures every customer trusts,
whose compromise forges releases for all of them, and which therefore must not
sit on any deployment host. Nothing here puts that key anywhere.

The key this RFC mints answers a different question, and the difference is the
whole argument:

| | The vendor's release key (0004) | This installation's key (0028) |
| --- | --- | --- |
| Asserts | "this release is what we published" | "this machine says this about itself" |
| Trusted by | every operator who installs the product | whoever is reading this machine's artifacts |
| Blast radius if stolen | every installation, forever | this machine's own statements |
| What an attacker gains | the power to forge releases | the power to lie about a host they already own |

That last row is the one that settles it. An attacker who can read this key has
root on the machine, and a machine's assertions about itself are worth exactly
as much as the machine — which is what 0025 §4.3 already says the bound is. The
key adds no authority an attacker with root does not already have; it adds the
ability for *everyone else* to detect statements that did not come from this
machine at all.

Stated as a rule, because it is the line to hold: **a key that lets a machine
speak for itself may live on that machine. A key that lets it speak for others
may not.**

## 3. Current state

- `init` runs `stepCreateIdentity`, which calls `Secrets.EnsureIdentity` and
  verifies the result is mode `0400`. Not compensable on purpose: *"deleting an
  identity is how secret state becomes permanently unreadable."*
- `Paths.AgeIdentityFile()` is `<etc>/age/identity`. There is one key path today.
- `InstallationSchemaVersion` is **5**.
- The export ([`domain/export.go`](../internal/domain/export.go)) carries the
  installation, the *encrypted* secret state and the recipient list — and
  deliberately **not** the machine's private age key. A rebuilt machine decrypts
  with the offline recovery key and becomes a *new* recipient. §5.3 is the
  consequence of that for signing.
- [`github.com/jedisct1/go-minisign`](../go.mod) is already a direct dependency,
  used by the verifier. Measured: it **can sign** — `PrivateKey.Sign(data,
  SignOptions{Hashed: true})` — and it **cannot generate or encode a private
  key**. §5.2 is where that lands.

## 4. Goals / Non-goals

**Goals**

1. A machine can produce a detached signature over bytes it is about to hand to
   somebody else, and a third party can check it without running `morzer`.
2. The public half travels with the installation, so a verifier can learn it
   from the export, from `status --json`, or from the artifact's own metadata.
3. A rebuilt machine is honestly a *different signer*, and that is visible
   rather than silent.

**Non-goals**

- **Release signing.** §2. Reopened by nothing.
- **A PKI, a CA, or cross-machine trust.** Whoever reads a machine's artifact
  decides whether to trust that machine's key, exactly as they decide whether to
  trust its statements. A hierarchy would be asserting something about machines
  this project does not know.
- **Escrow or recovery of the signing key.** Losing it costs the ability to sign
  *new* artifacts; old signatures stay verifiable against the recorded public
  key. That is a small enough loss that adding a recovery path — one more secret
  to protect, in an export — is the worse trade. Contrast the age identity,
  whose loss is unrecoverable and which 0003 therefore surrounds with ceremony.
- **Encrypting anything.** This key signs. The age identity encrypts. One key,
  one job.

## 5. Design

### 5.1 One key, minted at `init`, beside the age identity

`Paths.SigningKeyFile()` — `<etc>/signing/identity.key`, mode `0400`, minted by
a new step next to `stepCreateIdentity` and idempotent in the same way.

Its own directory rather than `<etc>/age/`, because that directory's name states
what is in it and a signing key is not an age key. A reader who greps for how
this machine's secrets are protected should not have to know that "age" grew a
second meaning.

### 5.2 minisign format, so the verifier is a tool operators already have

The signature is minisign's, in prehashed (`ED`) mode:

```console
$ minisign -Vm support-acme-....tar.zst -P RWQf6L...
Signature and comment signature verified
```

That is worth more than a bespoke envelope for one reason: **the project already
teaches operators to run exactly this command** to verify a release, and the
installation page carries it. A second signature format would be a second thing
to learn for the same gesture.

`go-minisign` signs but does not generate or encode a private key, so P1 owns
constructing the secret-key file: `crypto/ed25519.GenerateKey`, then minisign's
unencrypted secret-key layout (kdf algorithm `0x0000`, no scrypt). **This is the
one piece of format work in the RFC and it is the piece to distrust** — §7 says
how it is tested, and the test is interoperation with the real `minisign` binary
rather than with our own reader.

No passphrase. A passphrase on a key that a systemd timer must use unattended is
a passphrase written down next to the key.

### 5.3 A rebuilt machine is a new signer, and says so

`installation import` does not carry the private age key (§3), and it will not
carry the private signing key either: an export that reaches a bucket must not
contain a key that can sign as the machine.

So a rebuilt machine mints a fresh signing key, and the chain of anything it has
signed before is broken — which is 0025's continuity problem in a second costume.
The answer is the same shape as 0017's for the installation id: **carry the
public half and record the succession.**

- The export carries the *public* signing key of the machine that produced it.
- On import, that key is written to the new installation's
  `signing.previous_keys`, newest first.
- A verifier following a chain across a rebuild finds the predecessor's key in
  the installation it is verifying against, and can say "signed by this
  installation's key as of before the rebuild" rather than "unknown signer".

**A signature that verifies against a `previous_keys` entry is its own outcome,
never plain "valid".** The three a verifier reports are: signed by this
installation's current key; *signed by a predecessor of this installation*; and
unverifiable. Collapsing the middle one into the first is what would make
rotation useless — the reason to rotate is usually that the old key may be in
somebody else's hands, and a verifier that accepts a retired key without saying
so accepts new forgeries from whoever holds it, forever, in exchange for keeping
old artifacts readable.

**And that outcome establishes provenance only. It cannot establish that the
signature was made while the key was in service**, so it is not a weaker "valid"
— it is a different question answered.

The tempting rule is the one to state and reject: rotation records *when* a key
left service, so reject a predecessor signature over an artifact dated after that
point. It does not work here, because the date comes from the artifact and the
artifact is what the forger writes. Anyone holding a stolen key signs a
back-dated statement as easily as a current one. A rule that stops only the
attacker who neglects to set a timestamp is a rule that reads as a defence and
is not one — this RFC's own §2 argument about overstated guarantees, applied to
itself.

`previous_keys` carries a retirement time regardless, because an operator
reading their own history needs to know when the machine stopped signing with
what. It is history for a human, not a check, and the RFC says so where the
field is defined rather than leaving a reader to assume a recorded timestamp is
an enforced one.

So the operational consequence is the whole answer, and it is worth stating
plainly because it is what somebody actually has to do: **rotating after a
suspected compromise does not repair anything the old key signed.** Every
artifact bearing that key is suspect, including the ones that look old. An
operator who still vouches for some of them re-signs those; the rest stay
suspect. Rotation protects the future, and nothing in this design protects the
past.

That is a real limit rather than a deferred one, and closing it needs a chronology
an attacker cannot write — a countersignature from something that was not on the
machine, or a transparency log. Both are out of scope by decision 11, and neither
is worth building for a fleet of self-hosted installations before somebody has a
concrete reason.

What this does **not** do is prove the rebuild was legitimate. Anyone holding the
export can import it and mint a key that claims the same predecessor. The
succession record is provenance for an operator reading their own history, not
an authentication of the rebuild — and, per 0025 decision 2, that bound belongs
in the artifact rather than in this document alone.

### 5.4 The public key is installation state, not a file to find

`Installation` gains a `Signing` block, taking `InstallationSchemaVersion` from
5 to **6**:

```yaml
signing:
  public_key: RWQf6L…
  previous_keys:
    # retired_at is history for an operator reading their own timeline, and
    # deliberately not a check: §5.3 and decision 11 record why a verifier
    # cannot reject a signature for being dated after it.
    - key: RWTz9k…
      retired_at: 2026-08-12T09:14:00Z
      reason: rebuild        # or `rotation`
```

In the installation rather than only on disk, so it reaches `status --json`, the
export, and 0026's fleet row without any of them reading a key file.

The refusal this needs is narrower than "schema 6 requires a public key", and
§5.6 is why. What is refused is **disagreement**: a signing key file whose public
half is not what state records. That is a machine which would sign with one key
and tell everybody it signs with another, and its artifacts are attributable to
nobody. A machine with *no* key and no record of one is a different thing
entirely — it is every installation that existed before this RFC.

### 5.5 What a signature by this key proves

Written here once, and carried by every consumer per 0025 decision 2:

> This signature proves that a process holding this installation's signing key
> produced these bytes. It does not prove the bytes are true, it does not prove
> the machine was uncompromised when it signed, and it does not identify the
> operator.

### 5.6 Every existing installation reaches schema 6 without a key, and that is fine

`init` mints the key. Every installation that already exists does not run `init`
again, so this needs an answer, and the obvious one — *the migration mints it* —
does not fit the mechanism.

**Measured against the code.** [`migrateInstallation`](../internal/infra/state/state.go)
is `func(domain.Installation) (domain.Installation, error)`: a pure function
from one value to another. Every migration in it so far — 2→3, 3→4, 4→5 — is a
comment explaining that there is nothing to convert and one line bumping the
number, because a new field's zero value has always been the correct reading of
an installation written before that field existed. Minting a key is not that. It
needs a CSPRNG, a file created at `0400` in a directory that may not exist, and a
write that must not half-happen — none of which a function with that signature
can do, and threading a filesystem into it would make every future migration a
place where loading state can write to disk.

So the migration does what every migration here does: **nothing but the bump.**
A migrated installation is at schema 6 with an empty `signing` block, which
states the truth — this machine has never had a signing key.

The key is minted instead by the idempotent step from §5.1, which `init` runs and
which any operation that needs to sign runs first. Three consequences, each
deliberate:

- **An installation acquires a key the first time it needs one**, not on the
  upgrade that made keys possible. A manager upgrade that silently generates
  cryptographic material on a machine nobody asked is a surprise; producing a
  signed artifact is a request.
- **`doctor` reports a machine at schema 6 with no signing key** as information,
  not a failure. It becomes a warning only once the installation is configured to
  do something that signs — publishing a fleet row on a timer, say — because then
  it is a scheduled operation that will fail.
- **The refusal in §5.4 stays sharp** by being about disagreement rather than
  absence. Empty state with no key file is an un-minted machine; a key file whose
  public half is not the recorded one is a machine to stop.

What this costs is that `signing.public_key` is not a field a consumer may assume
is populated. Every consumer already has to handle its absence — 0026's roster
may name an installation that has never signed — so the alternative would have
bought nothing but a mandatory-looking field with an empty value in it.

## 6. Decisions

| # | Decision | Grade | Why |
| --- | --- | --- | --- |
| 1 | A per-installation Ed25519 signing key, minted at `init`, living on the machine | LOCKED | §2. A key that lets a machine speak for itself may live on that machine; 0004 decision 8 is about a key that speaks for others, and stands. |
| 2 | minisign format, prehashed mode | LOCKED | §5.2. `minisign -Vm` is a command this project already teaches, so verification needs no new tool and no `morzer`. |
| 3 | No passphrase | LOCKED | §5.2. A passphrase for an unattended signer is a passphrase stored beside the key. |
| 4 | The private key never leaves the machine — not in the export, not in a backup | LOCKED | An export travels to a bucket (0017); a signing key inside one signs as the machine for whoever finds it. |
| 5 | A rebuilt machine is a new signer; the public predecessor is recorded | LOCKED | §5.3. The alternative — carrying the private key — contradicts decision 4, and pretending the signer is unchanged is the silent version of the same thing. |
| 6 | The public key lives in installation state, schema 6 | LOCKED | §5.4. It has to reach `status`, the export and a fleet row without any of them opening a key file. |
| 7 | Losing the signing key is not recoverable and that is acceptable | LOCKED | Old signatures stay verifiable; only new ones are lost. A recovery path would add a second secret to protect for a much smaller loss than the age identity's. |
| 8 | The key is minted for every installation, including `--mode dev` | ASSUMED | Uniformity beats a conditional nobody remembers; a sandbox that cannot sign would be a sandbox whose artifacts differ in shape from production's. Reversed if a consumer finds a reason a sandbox must be unable to sign. |
| 9 | The 5→6 migration bumps the number and mints nothing | LOCKED | §5.6, measured: `migrateInstallation` is a pure value-to-value function and every migration in it is a comment plus a bump. Making it able to write a key file would make loading state a thing that can write to disk. The consequence this decision accepts, and that consumers must handle: `signing.public_key` may legitimately be empty. |
| 10 | A signature verifying against a retired key is a distinct verification outcome | LOCKED | §5.3. Folding it into "valid" means a rotation after a suspected compromise still accepts whatever the old key signs, which is the one case rotation exists for. |
| 11 | That outcome is provenance only; the RFC does not try to reject a retired-key signature by date | LOCKED | §5.3. The date comes from the artifact and the artifact is what a forger writes, so the rule would stop only an attacker who forgot to set a timestamp — a defence in appearance and not in fact, which is the failure §2 exists to avoid. The consequence is stated instead: rotation protects the future and nothing here protects the past. *Reopens if* an authenticated chronology arrives — a countersignature from off the machine, or a transparency log — at which point the rejection rule becomes implementable rather than decorative. |

## 7. Tests

- **Interoperation with the real `minisign` binary, in the container lane.**
  Mint a key, sign a file, and verify it with `minisign -Vm -P <pubkey>`. This is
  the test that matters: §5.2's format work is hand-written, and checking it with
  our own reader would prove only that we are self-consistent. Verified red by
  corrupting one byte of the signature and of the signed file in turn.
- The reverse direction too: a key produced by the `minisign` binary is usable by
  this code, so the format work is checked from both sides.
- Round-trip through the export: public key out, predecessor recorded on import,
  asserted against a real rebuild in the recovery scenario the acceptance suite
  already runs.
- The three states §5.6 separates, because the refusal is about disagreement and
  it would be easy to write one that refuses absence instead: a key file whose
  public half is not the recorded one is refused; a schema-5 installation
  migrates to 6 with an empty `signing` block and keeps working; and the minting
  step run on that machine produces a key and records it, leaving the
  installation indistinguishable from one `init` minted.
- A signature made with a retired key verifies as *signed by a predecessor*
  rather than as valid (decision 10), asserted by rotating and then verifying an
  artifact signed before the rotation — a verifier that reports both cases
  identically passes every other test in this list.
- And the same outcome for an artifact signed with the retired key *after* the
  rotation, dated after `retired_at`. It is the same "signed by a predecessor",
  not a rejection, which is decision 11 written as a test so that a later reader
  who assumes the timestamp is enforced finds out here rather than from an
  incident.
- The key file is `0400` and its directory is not world-readable, asserted the
  way `stepCreateIdentity`'s `Verify` already asserts it for the age identity.

## 8. Docs

The security policy gains the key: what it is, what a signature by it proves
(§5.5 verbatim), what losing it costs, and — the sentence an operator needs at
the moment they are rotating — that rotation protects future artifacts and
repairs nothing the old key signed. The installation reference gains
`signing.public_key` and `previous_keys`, saying where `retired_at` is history
rather than a check. The recovery guide gains the sentence that a rebuilt machine
signs with a new key.

## 9. Phasing, and why it is not scheduled ahead of its consumer

- **P1 — The key and the signer.** Minting as an idempotent step (§5.1, §5.6),
  storage, mode, schema 6 and its bump-only migration, the public key in state
  and in the export, the succession record on import, `doctor` check, and the
  interop tests in §7.
- **P2 — Rotation.** `installation rotate-signing-key`: mint, push the old public
  key onto `previous_keys`, keep old signatures verifiable. Wanted the first time
  somebody believes a host was compromised.

**P1 ships in the wave that has its first consumer, and not before.** This
project has twice found a mechanism built ahead of its caller —
[0015](0015-notifications.md) found `Notifier` fully specified with no
implementation, [0021](0021-into-the-running-deployment.md) found `Logs` and
`Exec` fully implemented with no caller — and a signing key with nothing to sign
would be the third. The first consumer is 0025 P1, which cannot start without it,
so the two land together and the key is exercised by an artifact rather than by
its own tests alone.

What this RFC delivers on its own is the *decision*: 0024, 0025 and 0026 can each
turn their OPEN signing decision into a reference to this document, and 0026 in
particular can stop treating its overwrite defence as unresolved.

## 10. Risks

- **The secret-key format is hand-written** (§5.2). Mitigated by testing against
  the real binary in both directions, and bounded: it is one file layout, written
  once, and a mistake makes signatures that fail to verify rather than signatures
  that verify wrongly.
- **A machine that signs is a machine with a key to lose.** Accepted by decision
  7, and the loss is bounded to future signatures.
- **Succession is provenance, not authentication** (§5.3). The risk is a reader
  taking it for more than it is, which is why the bound is a field in every
  consumer's artifact rather than a paragraph here.
- **A stolen key stays useful to whoever stole it, and rotation does not change
  that** (§5.3, decision 11). The dangerous version of this risk is not the
  attacker — it is an operator who rotates, believes the incident closed, and
  leaves artifacts signed by the old key in circulation as though the rotation
  retroactively vouched for them. The docs (§8) carry the sentence rather than
  only the decision table, because the person who needs it is reading a recovery
  guide at the time.
- **Schema 6 is a state migration**, and 0023 P2 wants one too. If both land in
  the same window they should be one bump, not two.
- **`signing.public_key` may be empty on a real installation** (§5.6, decision 9),
  and the risk is a consumer written against the happy machine — one that treats
  the field as always present, and produces a fleet row or an attestation
  claiming a signer that is the empty string. Every consumer needs the absent
  case in a test, not in a comment.

## 11. Amendments

*(Empty.)*
