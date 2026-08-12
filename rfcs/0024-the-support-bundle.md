# RFC 0024 — The support bundle

- **Status:** 📝 Draft — P1–P3 are buildable now and need no vendor to exist; P4
  is the vendor half and is one manifest field.
- **Scope:** One command that exports a complete, redacted, self-contained account
  of an installation — journal, `doctor` results, resolved manifest, config diff,
  version history, container state and bounded logs — in a form safe to hand to a
  stranger, optionally encrypted to vendor recipients. Covers what is included and
  what never is, the redaction path, the preview, and the signature's bound.
  Deliberately not an uploader, not telemetry, not licence enforcement, and not a
  vendor-side ingest service.
- **Related:** [`internal/infra/logging`](../internal/infra/logging),
  [0008](0008-test-coverage-program.md) (the redaction handler and what it was
  caught missing), [0015](0015-notifications.md) (the allowlist argument),
  [0017](0017-recovery-artifacts.md) (encrypt-to-a-recipient-who-is-not-this-machine),
  [0021](0021-into-the-running-deployment.md) (`logs`, and redaction on by default)
- **Origin:** Drafted 2026-08-10; adopted 2026-08-12 with §11's measurements taken
  against the code.

---

## 1. Problem

The project declares two audiences. The operator gets **fifty-seven commands
under eighteen top-level verbs** (measured 2026-08-12 from the generated command
index; the draft said fifteen). **The bundle vendor gets `release verify`,
`release pack`, `release build`, `release archive` — every one of which runs
before the bundle leaves the vendor's machine, and nothing at all after.**

That asymmetry is the whole problem. The moment a release is installed somewhere
the vendor does not control, the vendor's only channel back is the operator's
prose, and the operator's only tool for producing evidence is copying a terminal.
So the vendor asks for output; the operator pastes a screenshot of `doctor` with
the container names blurred; three round trips later somebody asks for the logs
and the operator pastes those too, unredacted, into a public forum.

Meanwhile the manager holds, already, in structured form: the journal of every
operation, `doctor`'s check results, the resolved manifest, the config diff, the
version history, health-check outcomes, container state and logs (0021), and a
redaction handler that 0008 hardened after catching it printing anything that was
not a `string` or an `error`.

The evidence exists. It has no export.

## 2. The gate this RFC has to dissolve first

0002 §13 records the rule: *a phase gated on a condition the project cannot itself
create is abandoned, not deferred.* A vendor-support feature written for vendors
who do not exist yet is exactly that shape, and this RFC would deserve the same
fate.

It escapes on a structural point rather than an optimistic one: **the operator is
also a consumer of this artifact.** An operator with no vendor at all — running a
bundle they wrote themselves, or asking a forum — needs the same thing: a
complete, redacted, self-contained account of what this installation is and what
it did, that is safe to hand to a stranger.

So the artifact is designed for *"safe to hand to a stranger"*. The vendor case is
the same archive with a recipient key on it. Nothing in P1–P3 requires a vendor to
exist, and the vendor-specific part (§3.4) is one manifest field.

## 3. Design

### 3.1 A command of its own, not a `doctor` flag

`morzer support bundle`. Not `doctor --support-bundle`.

`doctor`'s contract is *"report on this machine, now"*: it takes no lock, writes
nothing, and its output is a view. A support bundle has retention, redaction
policy, encryption recipients and a signature. Hanging those off a flag smuggles
four new semantics into a diagnostic command and makes `doctor`'s contract a
sentence with an exception in it. Same reasoning that gave `release pack` its own
verb rather than a flag on `build`.

The command produces
`support-<product>-<installation-id>-<rfc3339>.tar.zst` in the current directory.

### 3.2 Inclusion is an allowlist

0015 established the pattern for what leaves a machine: *an allowlist, not a
denylist, so a `Kind` added later is not forwarded until someone classifies it.*
The same rule, for the same reason, one level up.

Every component is named, classified and enumerated in the reference documentation
as an ABI — 0017 already learned that *whatever ships becomes a contract whatever
the docs say*, and here the contract runs in the other direction: an operator who
has read the list once will not re-read it, so the list may only ever shrink
without a version bump.

**Included:** manifest as resolved, installation state (redacted), parameter
*names* and values marked non-sensitive, the config diff §1 names as evidence,
journal, `doctor` output, version history, `ps`/health output, container logs
bounded by lines and bytes, manager version and build, host facts `doctor`
already collects.

**Never included, enumerated because the enumeration is the point:** the age
identity; secret ciphertext (it is useless to a vendor and it is the crown
jewels); backup target credentials (0009 put them in the export, so the export is
adjacent and must not be mistaken for includable); 0017's export component; the
recovery recipients' private material; anything under the render directory.

### 3.3 Logs go through 0021's redactor, and the archive records which one

Container logs are the single most useful component and the only one that is raw
vendor bytes. They are included, redacted by 0021's path, and the archive's
`meta.json` records the redactor version and the count of redactions applied per
file. A count of zero on a log file is not proof of cleanliness and the
documentation says so; it is, however, the thing a reviewer looks at first.

`--no-logs` exists. `--raw` does not — see decision 5.

### 3.4 Encrypted to the vendor, unreadable by the machine

The manifest may declare `support.recipients` — age recipients belonging to the
vendor. When present, the archive is encrypted to them **only**, exactly as 0017
decision 11 encrypts the export to the recovery recipients only: an archive
sitting in a ticket system, an email thread or an S3 bucket is then not readable
by the ticket system, the mail provider, or an attacker who has the live host.

When absent, the archive is plaintext, and the command says so on stderr in the
same breath as the path. It does not refuse — the operator posting to a forum is
the case that needs plaintext, and 0017 already found that encrypting to a key
that dies with the machine is *"the appearance of recovery with none of the
substance."* The inverse trap is available here and is avoided the same way.

### 3.5 Preview, because trust in redaction is not a thing to ask for

`morzer support bundle --preview` writes nothing and prints the component list
with per-file sizes and redaction counts. `morzer support inspect <file>` lists
what is in an archive already produced, decrypting if the caller holds a
recipient key.

An operator who cannot see what leaves will either send nothing or send
everything. Both are failures of this feature.

### 3.6 The manager does not upload

The archive is written to disk. There is no `--upload`, no vendor endpoint, no
retry queue. 0016 established that even *checking* for an update is a phone-home
and is therefore off by default; a diagnostic uploader is a considerably larger
one, and it would put an outbound HTTP client on the path of a command an operator
runs when things are already broken.


### 3.7 The signing model is unresolved, and it is not a detail

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
worth). **This is an OPEN decision and it blocks the phase that depends on it.**

## 4. Decisions

| # | Decision | Grade | Why |
| --- | --- | --- | --- |
| 1 | A verb, not a flag | LOCKED | §3.1. Four new semantics on a diagnostic command make `doctor`'s contract a sentence with an exception in it. |
| 2 | Allowlist inclusion, enumerated as an ABI, shrinking only on a version bump | LOCKED | §3.2. 0015's rule one level up: a component added later is not exported until someone classifies it. |
| 3 | Encrypted to vendor recipients only when declared; plaintext and loud when not | LOCKED | §3.4. Refusing to produce a plaintext archive would break the forum case, which is the case §2 rests on. |
| 3a | A *malformed* `support.recipients` is a refusal, never a fall back to plaintext | LOCKED | "Declared but unparseable" is not "absent". Falling back would produce a plaintext archive for the operator who most clearly asked for an encrypted one, and it would do it quietly — an intent-guard where an outcome-guard is needed. `--preview` prints the recipient fingerprints so the target is verifiable before the archive exists. |
| 4 | What the signature is, and what it proves | **OPEN** | The bound is settled: whatever signs it proves the archive came from a machine holding that key, *not* that the operator did not edit it first, and not who the operator is. An overstated signature is worse than none — the same failure 0013 exists to fix, in a different costume. What is **not** settled is the signer: §3.7 measured that no signing key exists on the machine. P4 cannot start until this is answered. |
| 5 | No `--raw` escape hatch | LOCKED | A flag that disables redaction will be the flag every support article tells operators to pass, and then redaction is a feature nobody uses. An operator who genuinely needs unredacted logs already has `morzer logs --no-redact` (0021) and is making that choice one file at a time. |
| 6 | The manager never transmits | LOCKED | §3.6. |
| 7 | `support redact --check <file>` ships alongside | LOCKED | So an operator can run the same redactor over a paste they were going to send anyway. Cheap, and the feature most likely to actually prevent a leak. |
| 8 | Which redactor the bundle uses | LOCKED | One — see §11.2. `logging.Redactor` has two entry points and there is no second implementation to reconcile. |

## 5. Non-goals, and what reopens each

- **Licence and entitlement gating in the manifest.** Deliberately excluded, and
  the argument is not "later". A manager that enforces entitlement has, as its
  failure mode, refusing to start a paying customer's production system because a
  clock skewed or a check failed closed — and the manager's whole boundary is that
  it coordinates and reverses, never that it withholds. A vendor who wants
  licensing already has hooks, which run inside their own product where the
  consequences of a failed check are theirs to choose.
  *Reopens if:* a vendor demonstrates a check that must happen before `apply` and
  therefore cannot live in a hook — at which point the honest shape is a
  *manifest-declared preflight the vendor writes*, not a licence subsystem.
- **Install heartbeat / phone-home telemetry.** Reopens as an extension of 0016's
  update check, which is already a consented, scheduled, off-by-default outbound
  call with a gate around it. Building a second one here would duplicate that
  gate badly.
- **A vendor-side ingest service, portal, or ticket integration.** Different
  product, probably a different repository, and it is the thing that turns this
  project into a SaaS with a CLI attached.
- **Automatic bundle capture on failure.** Tempting and wrong: the moment an
  operation fails is the moment to stop doing things, and a failing `update` that
  also writes a 200MB archive has changed what failure means.
  *Reopens if:* `doctor` grows a "you should send this" hint, which is the useful
  90% at none of the cost.
- **Diffing two support bundles.** Reopens once anyone has two.

## 6. Tests

- P3 is the test phase and is described in §8; it is the phase that decides
  whether this feature is safe.
- The component enumeration is generated from the code rather than written twice,
  and joins `just docs-check` (§9).
- `support inspect` round-trips what `support bundle` produced, encrypted and
  plaintext, so the reader and the writer cannot drift.

## 7. Docs

A reference page enumerating every component with its classification — that page
is the ABI decision 2 freezes, and it is generated rather than maintained. The
operator-facing how-to is "what to send when you ask for help", and it says in the
first paragraph what the archive never contains.

## 8. Phasing

- **P1 — The inventory and the classification.** Every candidate component listed,
  each marked include/redact/never, each with the reason. No code. The output is
  the reference page that decision 2 freezes.
- **P2 — `support bundle` and `--preview`**, plaintext, no encryption, operator
  audience only — **and no container logs**. §9 says P2 without P3 is a leak
  generator with a progress bar, and shipping the one raw-vendor-bytes component
  before the phase that proves redaction works would be exactly that. Everything
  else in the allowlist is manager-produced and already structured, so P2 is
  still usable for a forum post, which is the point of §2. `--no-logs` is the
  default until P3 lands, and then it becomes a flag.
- **P3 — Redaction under test.** Drive the redactor over synthetic logs seeded
  with every secret shape 0008 found it missing — a `Stringer`, a struct, an
  `error` wrapping a value — and fail the build if any survives. This phase is
  the one that decides whether the feature is safe; P2 without it is a leak
  generator with a progress bar.
- **P4 — `support.recipients`, encryption, signing, `support inspect`.**
- **P5 — `support redact --check`.**

## 9. Risks

- **The archive becomes the leak.** Mitigated by P3 preceding P4 in importance,
  by `--preview`, and by the absence of `--raw`. Not eliminated. This is a feature
  whose failure mode is silent and permanent.
- **The eager-redaction defect is real and this RFC adds call sites** (§11.1).
  P3 has to cover it explicitly rather than assume 0008 closed it.
- **Size.** A container log stream is unbounded. Bounded by lines and bytes with
  the bound printed, and `doctor` should warn when the bound truncated something
  the vendor will probably ask for.
- **The enumeration becomes stale.** Same failure as 0006 solved for docs: this
  list is generated from the code, or it drifts. `just docs-check` already fails on
  an undocumented manifest field; this list joins it.

## 10. Unresolved questions

- Whether `support.recipients` can be added to the manifest after the pre-1.0
  sweep, or must arrive before the first tag (§11.4).
- The byte-size defaults for the log bound, which §11.3 says must be measured
  rather than guessed.

## 11. What this draft owed a measurement

Taken 2026-08-12.

1. **Whether `redactAttr`'s eager-redaction defect is reachable from the paths a
   support bundle would use.** **Confirmed live.**
   [`logging.go:207`](../internal/infra/logging/logging.go) — `WithAttrs` calls
   `h.redactAttr(a)` at the moment `WithAttrs` is called, storing the result. A
   value captured before its secret is registered is stored in the clear and
   emitted on every later record from that handler. 0008's finding stands, the
   design is still eager, and this RFC adds call sites — so P3 must seed a
   register-after-capture case specifically, not only the value-shape cases.
2. **What 0021 shipped as its redaction path, and whether it is a second
   redactor.** **It is not.** There is one type, `logging.Redactor`, with two
   entry points: `Apply(string)` for the slog handler and
   [`Stream(io.ReadCloser)`](../internal/infra/logging/stream.go), which 0021
   added for byte streams. **The phase order in §8 stands** — the draft's warning
   that a second redactor "changes the phase order" does not apply.
3. **The real size of a journal and of a bounded log capture on the acceptance
   deployment.** Not measured; it needs a populated acceptance installation and
   the numbers decide the defaults. Guessing them here would be the kind of
   estimate 0008 §17 was three times wrong about, so P1 measures them.
4. **Whether the manifest can carry `support.recipients` after the pre-1.0 sweep
   (0018), or whether it has to arrive before the first tag.** Not resolved.
   0018 is ✅ Complete and no tag exists yet, so the window is still open — but
   it closes at the first tag, which makes this the one item in this RFC with an
   external deadline. If it closes first, the field needs 0018's `extensions`
   namespace or it is a hard break.

## 12. Amendments

*(Empty.)*
