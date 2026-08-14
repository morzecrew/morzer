# RFC 0024 — The support bundle

- **Status:** 🚧 In progress — **P1–P3 and P5 shipped (2026-08-13)**. `morzer
  support bundle` and `--preview` produce a plaintext archive from eleven
  components, the inclusion list is generated from the code into
  [the reference page](../pages/docs/reference/support-bundle.md), container
  logs land bounded, per service and scrubbed, and `support redact --check`
  points the same redactor at a file an operator was going to send by hand.
  §11.3's measurement was taken and is in §12 A4. Two things moved during
  execution: redaction applies to **every** component rather than to the one
  class whose bytes are raw (A3), which is what §11.1's eager-capture defect
  becomes at the scale of an archive; and P5 shipped ahead of P4 because
  decision 7 is `LOCKED` and says it ships alongside the bundle, which §8
  contradicted (A6). **P4a shipped 2026-08-14**: a manifest declares who a
  bundle is for and the archive is encrypted to them alone. §10's manifest
  window **closed** — v0.1.0 and v0.1.1 are tagged — so the field went to
  `extensions."morzer.dev/support"`, measured against a released binary rather
  than argued (A10). **P4b — signing and `support inspect` — is unscheduled**
  and no longer blocked: 0028 P1 shipped, so the signer exists (A11).
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
worth). **[RFC 0028](0028-the-machines-signing-identity.md) answers this**: a
per-installation Ed25519 key in minisign format, minted at `init`, whose
signature proves that a process holding this installation's key produced the
bytes — and nothing more. P4 depends on 0028 P1.

## 4. Decisions

| # | Decision | Grade | Why |
| --- | --- | --- | --- |
| 1 | A verb, not a flag | LOCKED | §3.1. Four new semantics on a diagnostic command make `doctor`'s contract a sentence with an exception in it. |
| 2 | Allowlist inclusion, enumerated as an ABI, shrinking only on a version bump | LOCKED | §3.2. 0015's rule one level up: a component added later is not exported until someone classifies it. |
| 3 | Encrypted to vendor recipients only when declared; plaintext and loud when not | LOCKED | §3.4. Refusing to produce a plaintext archive would break the forum case, which is the case §2 rests on. |
| 3a | A *malformed* `support.recipients` is a refusal, never a fall back to plaintext | LOCKED | "Declared but unparseable" is not "absent". Falling back would produce a plaintext archive for the operator who most clearly asked for an encrypted one, and it would do it quietly — an intent-guard where an outcome-guard is needed. `--preview` prints the recipient fingerprints so the target is verifiable before the archive exists. |
| 4 | What the signature is, and what it proves | LOCKED | The bound is [0028](0028-the-machines-signing-identity.md) §5.5 verbatim: *a process holding this installation's signing key produced these bytes*. Not that the archive came from that machine — a copied key signs from anywhere — not that the operator did not edit it first, and not who the operator is. An overstated signature is worse than none, the same failure 0013 exists to fix in a different costume. The signer is 0028's per-installation minisign key; P4 lands after 0028 P1. |
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

- **P1 — The inventory and the classification.** ✅ Shipped 2026-08-13. Every candidate component listed,
  each marked include/redact/never, each with the reason. No code. The output is
  the reference page that decision 2 freezes.
- **P2 — `support bundle` and `--preview`** ✅ Shipped 2026-08-13, plaintext, no encryption, operator
  audience only — **and no container logs**. §9 says P2 without P3 is a leak
  generator with a progress bar, and shipping the one raw-vendor-bytes component
  before the phase that proves redaction works would be exactly that. Everything
  else in the allowlist is manager-produced and already structured, so P2 is
  still usable for a forum post, which is the point of §2. `--no-logs` is the
  default until P3 lands, and then it becomes a flag.
- **P3 — Redaction under test.** ✅ Shipped 2026-08-13. Drive the redactor over synthetic logs seeded
  with every secret shape 0008 found it missing — a `Stringer`, a struct, an
  `error` wrapping a value — and fail the build if any survives. This phase is
  the one that decides whether the feature is safe; P2 without it is a leak
  generator with a progress bar.
- **P4a — `support.recipients` and encryption.** ✅ Shipped 2026-08-14. The declaration lives under `extensions."morzer.dev/support"` because the window §11.4 gave the top-level field closed at the first tag; the three candidate placements were measured against a `v0.1.1` binary rather than reasoned about (A10). A declaration that cannot be used is a refusal before anything is collected, never a plaintext fallback (decision 3a), and `--preview` prints the recipients in full because that is the only check available against a key that parses and belongs to the wrong party.
- **P4b — signing and `support inspect`.** Unscheduled, and no longer gated: 0028 P1 shipped, so decision 4's signer exists. Split from P4a because encryption decides who can read the archive and a signature decides what it proves, which are independent (A11). `inspect` belongs here rather than with encryption: its job is to verify as well as to list.
- **P5 — `support redact --check`.** ✅ Shipped 2026-08-13, ahead of P4: decision 7 is `LOCKED` and says it ships alongside the bundle (§12 A6).

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

- ~~Whether `support.recipients` can be added to the manifest after the pre-1.0
  sweep, or must arrive before the first tag (§11.4).~~ **Answered 2026-08-14**:
  it could not, and did not. The window closed at `v0.1.0`, and the field went
  to `extensions."morzer.dev/support"` — see A10 for the three placements and
  what a released manager does with each. Promoting it to the top level remains
  open, for a version willing to raise the manifest's floor.
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

**A1 — the archive's timestamp is RFC 3339's basic form (2026-08-13, P2).**

§3.1 specifies `support-<product>-<installation-id>-<rfc3339>.tar.zst`. The
timestamp is `20260813T174241Z` rather than `2026-08-13T17:42:41Z`, because a
colon is legal in a filename on every platform this runs on and is a trap on the
one journey this file is built to make: `scp support-…T17:42:41Z.tar.zst host:`
makes `scp` read everything before the first colon as a hostname. A filename
that breaks the tool an operator uses to send it is a bad filename for a file
whose only purpose is to be sent.

The reported path is also absolute, which §3.1 does not discuss. `--json` puts
it on stdout for something else to act on, and that something is not
necessarily standing in the directory the archive was written to. The
acceptance lane found this by reading `.data.path` from one directory after
writing from another.

**A2 — the redactor's version is the manager's (2026-08-13, P2).**

§3.3 says `meta.json` records "the redactor version". There is no such thing:
`logging.Redactor` is one type with no version of its own, and minting one would
create a number nobody remembers to bump — which is worse than none, because a
reader would trust it. The manager version identifies the redaction logic
exactly, since the redactor ships inside the binary, so that is what the field
carries and what the archive says.

**A3 — every component is redacted, not only the class named `redact`
(2026-08-13, P3).**

§3.2 classifies container logs as the component that goes "through 0021's
redactor", with everything else included as-is because it is manager-produced
and already structured. Execution applies the redactor to **every** component.

The reason is §11.1's defect one level up. A component's class describes where
its bytes came from; redaction is about *when* they were written, and most of
this archive was written long before the command ran. The journal is the clear
case: it is appended to across every operation the installation has ever run,
and `logging`'s own `TestRegisteringAfterWithIsAKnownLimit` pins that the log
handler redacts at capture time and keeps the clear copy. So a step message that
embedded a secret before that secret was registered is on disk in the clear, and
no correctness in the handler helps an archive assembled a month later.
Redacting at collection is what makes registration order stop mattering.

Two consequences worth stating. The per-file count in `meta.json` is meaningful
for every entry rather than for one, which is what makes a zero in that column
readable at all. And the count's honest bound narrowed: it means "no value this
installation *currently* holds appeared here", so a rotated-away credential is
not something it can recognise — the reference page says so.

Container logs keep a stricter rule than the rest, which §3.3 did not settle:
when the secret values cannot be loaded they are **omitted entirely** rather
than included unfiltered. 0021 lets `morzer logs` print an unscrubbed stream and
say so, because an operator reading their own terminal can decide what to do
with what they see. This artifact is read by somebody else, later, holding only
the file and `meta.json` — and decision 5 already refuses a flag that turns
redaction off, so shipping unredactable bytes by default would be that flag with
no way to turn it off.

**A4 — §11.3's measurement, taken (2026-08-13, P1).**

On the acceptance deployment, after init, apply, three configuration changes, a
backup, a restore, an update killed mid-flight, a resume and a refused rollback:

| | |
| --- | --- |
| Whole archive | **5,882 bytes** compressed |
| Journal | 10,539 bytes uncompressed, ~1KiB per operation |
| Container logs | 889 bytes, from two stub containers |

The journal needs no bound of its own: a machine at one operation a day reaches
a third of a megabyte in a year.

What the measurement cannot say is how loud a real product is — the acceptance
containers write in a whole run what a production service writes in a second. So
the log bound stayed reasoned rather than fitted: 2000 lines and 1MiB per
service, which at a 200-byte line is 400KiB before compression. Everything else
in the archive is small and roughly fixed, so that bound alone decides the
artifact's size.

**A6 — decision 7 outranked the phasing: `support redact --check` ships now
(2026-08-13, self-audit).**

The RFC says two different things about it. Decision 7 is graded `LOCKED` and
says it "ships alongside"; §8 lists it as P5, after the encryption phase. The
self-audit walked the decision table rather than the diff, which is what made
the contradiction visible at all — conformance looked perfect against §8.

Resolved in the `LOCKED` row's favour, and the row's own argument is why:
"cheap, and the feature most likely to actually prevent a leak". The archive is
safe by construction; the thing an operator pastes into a chat window is not,
and until this shipped the feature had nothing to offer that case. `--check` is
a required flag rather than a default because it is the only mode that exists —
the alternative, printing a redacted file to stdout, is an output surface
nothing has specified, and a command that rewrote an operator's file would
destroy the evidence they were about to send.

§8's P5 entry is therefore empty and stays as a record of the order that was
planned.

**A7 — three narrowings the self-audit found and closed (2026-08-13).**

- **`manager.json` carried the version and not the build.** §3.2 asks for
  "manager version and build", and this file is also the archive's statement
  about which redaction logic ran (A2) — a version alone cannot distinguish a
  release binary from a patched host. The commit and date are stamped at link
  time into the command layer, so they travel as an option rather than as new
  `Deps` fields: they are an input to one report, not a capability the lifecycle
  layer acquired.
- **A deployment that logged nothing produced neither a file nor an
  explanation.** Every other component states its gap in `meta.json`, and logs
  are the one place a missing file is most suspicious. "There was no output" is
  now said rather than left as an absence a reader has to interpret.
- **`--dir`, which §3.1 does not mention**, writes the archive somewhere other
  than the working directory. Recorded here because an unrecorded addition is
  the same defect as an unrecorded departure, in the other direction.

**A8 — the installation component is the *described* document (2026-08-13,
review).**

§3.2 says "installation state (redacted)", and the first pass marshalled the
`Installation` record. The parenthesis had no implementation.

`AttestationSalt` is what decides it. The salt makes the attestation's
configuration digest resistant to being brute-forced back over a small space of
ports and booleans, and [0027](0027-desired-state-in-a-repository.md)'s
`describe` excludes it by name, saying so where the field is defined:
"publishing it in a document meant for a git repository would make the digest it
salts brute-forceable again". This archive travels further than a repository —
§2's whole framing is that it is handed to a stranger — so the record was the
wrong artifact and `Installation.Describe` was the right one all along.

Two things follow. The reference page's reason for this component cites
`installation describe` being safe to commit, which was an argument for a
document the code was not producing; that is now true rather than aspirational.
And the secret *names* are best-effort here where `describe` refuses without
them — `describe`'s document is committed as a record and `secrets: []` would be
a false one, while this archive exists because something is already broken and a
store that will not open is one more thing its reader should see.

**A9 — `meta.json` is scrubbed, and does not list itself (2026-08-13, review).**

Every collected component passed through the redactor and the archive's own
index did not, because it is built after the loop that scrubs. It is also the
file most likely to carry a value: every omission reason is an arbitrary
collector's error message, produced by the state layer, the renderer, the
runtime or `doctor`, and any of those can quote something the redactor would
have removed from the file the error was about. A6's release problem is exactly
that shape. It is, on top of that, the file a reviewer opens to decide the
archive is safe.

It no longer lists itself, which is the honest resolution of a real problem
rather than a gap: a file cannot state its own redaction count, because the
count is known only once the file exists and scrubbing it changes the bytes the
count describes. A zero there would be precisely the misreading this feature is
arranged to prevent, so the count lives in the command's output — produced after
the scrub — and the file is an index of the components it describes.

**A5 — §3.2's "values marked non-sensitive" has no referent (2026-08-13, P2).**

The Included list says "parameter *names* and values marked non-sensitive",
which assumes a sensitivity marking on parameters. There is none, and its
absence is deliberate: `ParameterSpec`'s own contract is that a parameter is
**not** a secret — its value already reaches Compose as an environment variable,
`docker inspect`, `status --json` and the journal, and secrets have their own
declared, audited, tmpfs-rendered path that "must never become a second one".

So the qualifier had nothing to select on, and withholding parameter values
would have hidden the most common cause of a support question while protecting
nothing this archive does not already carry in the journal. Values are included,
and the reference page gives that argument rather than the RFC's phrasing.

The neighbouring worry resolved the same way and is worth recording because it
looked more serious: the configuration diff renders each target, and a rendered
target cannot embed a secret either — `templateData` puts secret *references* in
the render context, a name to the path of the rendered file, never a value.

**A10 — the manifest window closed, and `support.recipients` went to
`extensions` (2026-08-14, P4).**

§10 left this open with a deadline and §11.4 said the window "closes at the
first tag". It closed: `v0.1.0` and `v0.1.1` are tagged. So P4 landed the field
where 0018 §5.4 said an experimental manager field goes, and the choice was
measured rather than argued. Against a binary built from `v0.1.1`:

| Declaration | What a released manager does |
| --- | --- |
| top-level `support:` | `unknown field "support"` — the whole bundle is refused |
| top-level `support:` with `min_manager_version` raised | `requires morzer 0.2.0 or newer` — refused, but legibly |
| `extensions."morzer.dev/support"` | `bundle is valid` |

The first is a break with a message that names the wrong cause. The second is
honest and costs too much: a vendor could not offer encrypted support bundles
without dropping support for every older manager, over a diagnostics field. The
third costs nothing those operators had — a manager without P4 cannot encrypt
whatever the field is called — so the field's location only ever decided whether
the bundle installs.

The trade it does carry, recorded rather than buried: an old manager *tolerates*
the block and ignores it, so a vendor who declares recipients gets plaintext
archives from operators who have not upgraded. Those operators are told the
archive is plaintext on every run, which is the same sentence decision 3 already
required; what they are not told is that their vendor asked for more. Promoting
the field to the top level is the fix, and it belongs to whatever version is
willing to raise the manifest's floor.

An early measurement of the middle row was wrong and is worth recording as a
method note: the binary was built without its version ldflag, reported `dev`,
and the version gate silently did not fire — so `min_manager_version` looked
like it did nothing. A gate that compares versions cannot be measured with a
binary that does not know its own.

**A11 — P4 ships in two halves, and this is the first (2026-08-14).**

§8 lists P4 as one phase: recipients, encryption, signing and `support inspect`.
Encryption and signing turn out to be independent — 0028 P1 shipped, so nothing
blocks the signing half — and they answer different questions: encryption
decides who can read the archive, a signature decides what the archive proves.
This phase is recipients and encryption. Signing and `support inspect` are the
second half, and `inspect` belongs with signing rather than with encryption
because its job is to verify as well as to list.

**A12 — a release that will not resolve cannot be asked what it declares
(2026-08-14, P4).**

The design has a row for "declared" and a row for "not declared" and no row for
"cannot tell". That third state is reachable on exactly the machine this command
exists for: the release directory is unreadable or its digest no longer matches,
which is when somebody wants a support bundle most.

Refusing would take the tool away at that moment, so the archive is produced,
plaintext, with the reason stated as an omission in `meta.json` and in the
report. That is deliberately not silence: an operator whose vendor did declare
recipients would otherwise read the ordinary "this archive is not encrypted"
line as the ordinary state, when what actually happened is that the declaration
could not be read. Decision 3a governs a declaration that is present and
unusable; this is a declaration that could not be reached, and the two fail in
opposite directions on purpose.
