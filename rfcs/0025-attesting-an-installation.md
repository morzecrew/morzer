# RFC 0025 — Attesting an installation

- **Status:** 🚧 In progress — **P1–P3 shipped (2026-08-13)**. P1 emits a
  signed in-toto statement; P2 attests failures and compensations; P3 is
  `morzer attest verify`, with signature outcomes, chain continuity and
  `--against-live`.
  **Decision 8 is answered and the RFC lives**: an image swapped behind the
  manager's back makes `--against-live` fail, asserted in `test/suite`
  (`TestAgainstLiveFailsWhenAnImageWasSwappedByHand`), and so does an attested
  service that is gone. P4 — pushing statements through 0009's registry,
  `attest log`, and the `doctor` check for an unpushed directory — remains, as
  does emission from `update`, `rollback` and `restore`.
- **Scope:** A portable, signed, third-party-readable statement of what the
  manager did — one in-toto Statement per lifecycle operation, emitted on failure
  as well as success, appended locally and optionally pushed through 0009's target
  registry, with a verifier whose `--against-live` mode can fail for a real
  reason. Deliberately not a transparency log, not TPM binding, not policy
  enforcement, and not an assertion about anything the manager did not do.
- **Related:** [`internal/domain/operation.go`](../internal/domain/operation.go),
  [`internal/ports/runtime.go`](../internal/ports/runtime.go) (`ImageInspector`),
  [0004](0004-distribution-and-verification.md) (content digest, minisign),
  [0009](0009-backup-targets.md) (the target scheme registry),
  [0011](0011-bundled-container-images.md) (digest identity of images),
  [0014](0014-building-a-release-bundle.md) (reproducible builds, version identity),
  [0017](0017-recovery-artifacts.md) (encrypt-to-a-recipient, one-producer discipline)
- **Origin:** Drafted 2026-08-10; adopted 2026-08-12 with §11's measurements taken
  against the code.

---

## 1. Problem

The manager verifies a signature, pins a digest, refuses an unverified image,
journals eleven steps, and checks afterwards that they took. Then all of that
evidence stays on the machine, in a format nothing outside this project reads, and
dies with the disk.

An operator who must demonstrate — to an auditor, a customer, or their own change
board — that *the software running in production is the software the vendor
signed, installed by a process that was checked at each step* reconstructs it by
hand from `status`, the journal, and human trust. The manager, which knows the
answer exactly, contributes a screenshot.

The gap is not evidence collection. It is that the evidence has no **portable,
signed, third-party-readable form**.

## 2. Why this is the direction most at risk of being theatre

Stated first, because it is the honest framing and because a later section would
read as a caveat rather than a constraint.

Attestation is a domain full of artifacts that assert things nobody checks. It is
extremely easy to ship a JSON file that says everything is fine, put a signature
on it, and have accomplished a marketing claim.

The test this RFC binds itself to, in decision 8: **an attestation is worth
shipping only if `morzer attest verify` can fail for a reason that is not
corruption, and there is a fault-injection case that makes it fail.** If the only
way to get a failure is to edit the file, the artifact carries no information and
the RFC is closed as rejected rather than shipped as designed. This project has
already found two ports fully specified and never exercised (0015's `Notifier`,
0021's `Logs`); a verifier that cannot fail is the same defect written in
advance.

## 3. Current state

The material is already there, which is what makes this cheap and also what makes
it tempting to overstate:

- 0004: one content digest per release regardless of transport; minisign
  verification with `require_signature` as a working control; a pinned key.
- 0011: images identified by digest; an absent bundled image is a **refusal**
  rather than a pull, precisely so a digest-pinned deployment cannot converge on
  unverified bytes.
- 0014: every build gets a distinct version; `+metadata` refused because it
  silently bypassed the never-republish guard.
- 0005: signed reproducible builds of the manager itself.
- The journal: ordered steps with outcomes, per operation — but **durations, not
  timestamps** (§11.1, and it changes §4.1).

What is missing is a statement that binds them together and leaves the machine.

## 4. Design

### 4.1 One statement per lifecycle operation

After `apply`, `update`, `rollback`, `restore` and `config` complete — **and after
they fail** (decision 3) — the manager emits an
[in-toto](https://in-toto.io/) Statement.

- **Subject:** the release. Name, version, and the 0004 content digest.
- **PredicateType:** `https://morzecrew.github.io/morzer/attestation/v1`.
- **Predicate:** the operation record.

Predicate contents, in outline:

```text
operation: { id, kind, started, ended, outcome }
installation: { id, product, mode, manager_version }
release: { from_version, to_version, source_scheme, content_digest }
verification: {
  signature_required, signature_verified, key_id,
  digest_pinned, digest_matched
}
images: [ { ref, digest, origin: registry|bundle } ]
config: { parameter_names: [...], rendered_digest }
steps: [ { id, outcome, duration_ms } ]
```

**`steps[]` carries durations, not start and end times**, because that is what
[`domain.StepRecord`](../internal/domain/operation.go) records:
`{ID, Status, DurationMS, Idempotent, Message, Error}`. The draft wrote
`{ name, outcome, started, ended }` and §11.1 found no such fields. Two ways out,
and P1 picks one: use the durations the journal has, or add timestamps to
`StepRecord` and accept that P1 grows a journal change. **The predicate above
takes the first**, on the grounds that a duration plus the operation's own start
is enough to place a step in time, and that widening a persisted record to feed a
new artifact is the tail wagging the dog.

### 4.2 The predicate type is morzer's own, deliberately

Not `https://slsa.dev/provenance/v1`.

SLSA Provenance describes **how an artifact was built**. This describes **how an
artifact was deployed**. They are different claims about different events, and
reusing the build predicate would produce a document that validates against a
well-known schema while asserting something the schema does not mean — a
statement that is wrong in a way that reads as right, which is worse than an
unfamiliar predicate that is correct.

Deployment attestation is not a settled area. The document says so rather than
implying a standard exists behind it.

### 4.3 What the signature proves, written into the format

Signed — by what, §4.8 leaves open. Whatever the answer, the bound is the same
and is stated in the document rather than around it:

> This attestation proves that a process holding this installation's identity
> asserted these facts. It does not prove the facts, and it does not prove the
> host was not compromised before the assertion was made.

That paragraph is a **field in the document**, not a line in the docs. 0013 exists
because `release verify` printed `bundle is valid` for a bundle that could not
render; the same failure here would be an attestation read as a guarantee about
the world. An artifact that travels must carry the bound on its own claim.

### 4.4 Config: names always, values never, drift detectably

Parameter *names* are safe and useful. Values are not — 0007's parameters include
ports and hostnames, and 0015 already refused to let vendor-controlled output
leave the machine.

A hash of the rendered configuration would detect drift, and an unsalted one over
a small parameter space (a port number, a boolean) is brute-forceable. So:
`rendered_digest` is computed with a **per-installation salt stored in state**.
It therefore detects change on one machine over time — which is the audit question
— and is deliberately *not* comparable across machines, which is a capability
being given up on purpose rather than an oversight.

**No such salt exists today** (§11.3). It has to be minted at `init`, and
`installation import` must carry it, or a rebuilt machine mints a new one and
breaks its own chain continuity — the exact failure 0017 found for the
installation id and solved by preserving it.

### 4.5 Emitted on failure

A successful update is the least interesting event to an auditor. A failed update
that rolled back is the one they ask about. `outcome` carries `success`,
`failed`, `compensated`, and the step list shows where it stopped.

This is also the honesty test of the whole feature: a system that attests only
its successes is a system that attests nothing.

### 4.6 Local first, pushed second, and a failed push does not fail the operation

Statements append to `<root>/attestations/` and, if targets are configured, push
through **0009's target registry** — the same `file://`, `ssh://`, `s3://` scheme
dispatch, reused rather than re-shaped.

0009 made a failed backup push **fail the backup**, because reporting success for
data that is only on the doomed machine is exactly what it existed to prevent.
This RFC inverts that, and owes the reason: a backup that did not leave is a data
loss risk that has already materialised; an attestation that did not leave is a
gap in a record whose local copy still exists and can be re-pushed. Failing an
`update` because a log shipper was down would be the notification anti-pattern
0015 spent a whole section avoiding.

A failed push is a `warn` event, which 0015's default `min_level: error` means is
not notified — and that is the right default, stated so it is a decision rather
than an accident.

### 4.7 Reading them back

- `morzer attest log` — the local chain, newest first.
- `morzer attest verify <file|dir>` — signature, chain continuity (each statement's
  `from_version` matches its predecessor's `to_version`), and, with `--against-live`,
  whether the running deployment matches the newest successful statement.

`--against-live` is the mode that can fail for a real reason (decision 8): images
swapped by hand, a container started outside the manager, a config edited in
place. That is the check an auditor actually wants and the one an operator will
find genuinely useful the first time somebody `docker exec`s into production.


### 4.8 The signing model is unresolved, and it is not a detail

Every statement about a signature in this RFC rests on a capability the manager
does not have. Measured 2026-08-12:

- The **machine identity is an age identity** — an X25519 *encryption* key, used
  to decrypt this installation's secrets ([`internal/ports/secrets.go`](../internal/ports/secrets.go)).
  It is not a signing key and age is not a signature scheme.
- **Nothing in the manager signs anything.** `minisign` appears only as a
  *verifier* ([`internal/adapters/verify/minisign`](../internal/adapters/verify/minisign)),
  and [0004](0004-distribution-and-verification.md) decision 8 deliberately
  refuses a `morzer sign` verb: signing happens in the release pipeline, where
  the key lives.

So "signed with the machine identity" is not a thing that can be implemented
today, and the honest options are all consequential: mint a per-installation
Ed25519 signing key at `init` (a new key with a new lifecycle, a new thing
`installation import` must carry, and a new thing to lose), reuse the age
identity through a construction age was not designed for (no), or drop the
signature and say the artifact is unauthenticated (which changes what it is
worth). **[RFC 0028](0028-the-machines-signing-identity.md) answers this**, and this
RFC is its first consumer: 0028 P1 and 0025 P1 land in the same wave, so the key
is exercised by an artifact rather than by its own tests alone. 0028 §5.3 also
answers what happens to `attest verify`'s chain across a rebuild — the machine is
honestly a new signer and the predecessor's public key is recorded.

## 5. Decisions

| # | Decision | Grade | Why |
| --- | --- | --- | --- |
| 1 | Own predicate type, versioned by URI | LOCKED | §4.2. Reusing SLSA's build predicate would validate against a schema while asserting something it does not mean. |
| 2 | The claim's bound is a field in the document, not documentation | LOCKED | §4.3. An artifact that travels must carry the bound on its own claim. |
| 2a | What signs the statement | LOCKED | [0028](0028-the-machines-signing-identity.md)'s per-installation minisign key. P1 lands with 0028 P1 — the attestation is that key's first consumer, which is why 0028 is not scheduled ahead of this. |
| 3 | Emitted on failure and compensation, not only on success | LOCKED | §4.5. A system that attests only its successes attests nothing. |
| 4 | Parameter names yes, values never, rendered digest salted per installation | LOCKED | §4.4. An unsalted digest over a port number is brute-forceable. |
| 5 | Reuses 0009's target registry unchanged | LOCKED | Third consumer of that shape (release sources → backup targets → attestations); if it needs a fourth method, extract `ports.ObjectStore` rather than widening `BackupTarget`, and record it here. |
| 6 | A failed push warns; it does not fail the operation | LOCKED | §4.6, and the asymmetry with 0009 is stated in the reference documentation, not just here. |
| 7 | Attestations are never encrypted | LOCKED | They contain no secrets by construction (decision 4), and an audit artifact that requires a key to read is one an auditor will not read. This constrains §4.4 permanently: if a future field would need encrypting, it does not belong in the statement. |
| 8 | `attest verify --against-live` must have a fault-injection case that makes it fail before P3 is called complete | LOCKED | §2. If none can be constructed, the RFC closes as rejected. |
| 9 | `steps[]` carries the journal's durations rather than new timestamps | ASSUMED | §4.1 and §11.1. Reversed only if P1 finds a duration insufficient to place a step for an auditor. |
| 10 | The per-installation salt is minted at `init` and carried by `installation import` | LOCKED | §4.4, §11.3. A re-minted salt breaks chain continuity on exactly the machine that most needs it. |

## 6. Non-goals, and what reopens each

- **A transparency log (Rekor / sigstore).** Third-party timestamping is the thing
  that would upgrade "the machine asserts" to "the machine asserted *by this
  time*". It also requires network egress from installations whose defining trait
  is air-gapping. *Reopens if:* an auditor or customer names monotonic time as a
  requirement — at which point the honest design is an optional target, not a
  default.
- **TPM / measured boot binding.** *Reopens if:* a customer's control framework
  requires hardware root of trust, and they have the hardware.
- **Attesting the host** — kernel, packages, users. That is the OS's job and a
  manager that claimed it would be asserting things it cannot check.
- **Policy enforcement** ("refuse to apply unless the previous attestation is
  clean"). Attractive and out of scope: it turns a record into a gate, and gates
  that fail closed on evidence problems stop production for bookkeeping reasons.
  *Reopens if:* someone wants it in **dev mode** (0016) first, where the blast
  radius is a sandbox.
- **Attesting anything the manager did not do.** A container someone started by
  hand is *reported by* `--against-live` as a mismatch and is never attested.

## 7. Tests

- P2 rides the fault-injection suite 0001 already runs at eleven points: each
  injected failure should produce a statement whose step list stops where the
  injection did. §11.4 flags that "largely free" rests on the harness being able
  to assert on emitted artifacts, which is unmeasured.
- Decision 8's failing case is the test the RFC lives or dies by, and it is
  written before the verifier is called complete.
- A JSON Schema for the predicate, generated the way 0004 generates the
  manifest's, so the document and the code cannot drift.

## 8. Docs

A reference page for the predicate, carrying §4.3's bound verbatim, and the
asymmetry with 0009's push failure (decision 6) stated where an operator reads
about targets rather than only here.

## 9. Phasing

- **P1 — The statement, signed.** Predicate schema, emission on success only,
  local append, the salt from decision 10. Generated JSON Schema alongside.
  **And the signing, because there is no unsigned half of this to ship**:
  [0028](0028-the-machines-signing-identity.md) P1 lands here (decision 2a), the
  statement carries the signature and the public key that verifies it, and
  `attest verify` — P3's command — is preceded in P1 by whatever verification the
  emission tests need to prove the signature is real rather than recorded. An
  unsigned statement is the theatre §2 warns about, so it is not a phase.
- **P2 — Failure and compensation paths.** Emission from the fault-injection suite;
  each injected failure produces a statement whose step list stops where the
  injection did.
- **P3 — `attest verify`, including `--against-live` and its failing case**
  (decision 8). The RFC lives or dies here.
- **P4 — Push through 0009's registry, `attest log`, `doctor` check for an
  attestation directory that is not being pushed.**

## 10. Risks

- **Theatre** (§2), bounded by decision 8.
- **Overclaim** (§4.3), bounded by decision 2 — and the failure mode is that a
  reader ignores the bound field, which no format can prevent.
- **The step list is the vendor's names.** Hook names come from the bundle, so a
  statement carries vendor-chosen strings into an audit artifact. They are names,
  not output (0015's distinction), but they are still uncontrolled and must be
  length-bounded and character-classed.
- **Volume.** One statement per operation is small; `config` operations are
  frequent. Retention policy mirrors backups', and the local directory needs a
  prune the way releases do.

## 11. What this draft owed a measurement

Taken 2026-08-12.

1. **Whether the journal records step start/end times, or only outcomes.**
   **Neither, exactly.** [`domain.StepRecord`](../internal/domain/operation.go) is
   `{ID, Status, DurationMS, Idempotent, Message, Error}` — a duration in
   milliseconds, no timestamps. The predicate sketch in the draft assumed
   `started`/`ended`, so §4.1 is corrected and decision 9 records the choice:
   use the duration rather than widen a persisted record for a new artifact.
2. **Whether running image digests are retrievable through `ImageInspector`.**
   The capability **exists** as an optional interface
   ([`runtime.go:391`](../internal/ports/runtime.go)) and nothing currently
   declines it, but what it returns was not read closely enough to say whether
   `--against-live` gets digests or only references. P3 confirms before
   decision 8's deadline is set.
3. **Whether a per-installation salt already exists in state.** **It does not** —
   no occurrence of a salt anywhere in `internal/domain` or `internal/lifecycle`.
   It must be minted, which makes decision 10 mandatory rather than a detail:
   `installation import` has to carry it or a rebuilt machine breaks its own
   chain.
4. **Whether 0001's fault-injection harness can assert on emitted artifacts or
   only on outcomes.** Not measured. P2 is described as "largely free" on the
   assumption that it can, and that assumption is the kind 0008 §17 was three
   times optimistic about — so P2's estimate is unreliable until someone reads
   the harness.

## 12. Amendments

*(Empty.)*
