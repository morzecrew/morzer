# RFC 0026 — Fleet as a read model

- **Status:** 📝 Draft — decision 1 is what the RFC exists to preserve; the timer
  is deliberately the last phase.
- **Scope:** Making several machines visible without a control plane: each
  installation publishes one small object at a stable key through 0009's
  existing target registry, and a stateless command lists, verifies and renders
  them. Covers the payload, the reader, staleness, absence via a roster, and the
  dev-mode drop. Deliberately not an agent, not a listener, not a database, not a
  web UI in this repository, and — decision 2 — never a way to *act* on a remote
  machine.
- **Related:** [`internal/lifecycle/ops/recovery.go`](../internal/lifecycle/ops/recovery.go)
  (the dev-mode drop as it exists today),
  [`internal/cli/installation.go`](../internal/cli/installation.go) (`ls` without
  a Docker call), [0009](0009-backup-targets.md) (the target scheme registry),
  [0017](0017-recovery-artifacts.md) (single-file fetch, preserved installation
  id), [0020](0020-several-installations-on-one-machine.md) (`ls`, and the
  row-carrying-its-problem rule), [0025](0025-attesting-an-installation.md)
  (optional, as a richer payload)
- **Origin:** Drafted 2026-08-10; adopted 2026-08-12 with §10's measurements taken
  against the code.

---

## 1. Problem

0020 makes several installations **on one machine** visible. Nothing makes several
machines visible, and the question arrives immediately after: an operator or
vendor with twelve deployments wants one screen showing what version each is on,
whether it is healthy, and which one stopped reporting three weeks ago.

The obvious answer is a control plane. That answer is the end of this project.

A control plane means: a long-lived privileged process on every managed machine;
inbound connectivity to hosts whose selling point is that they are on someone
else's network; a second source of truth about installation state, which will
disagree with the first exactly when it matters; and an authorisation model,
because the moment the console can *act* it needs to know who may act. Each of
those individually contradicts a decision this project has already made
deliberately.

The interesting observation is that **the valuable half does not require any of
it**. "What is running where" is a read. Reads do not need a control plane; they
need a place to put facts and a way to read facts back.

## 2. Current state

- 0009 built a `BackupTarget` port with a scheme registry — `file://`, `ssh://`,
  `s3://` — and it *"mirrors the release-source registry byte for byte rather than
  inventing a second shape."*
- 0025, if it ships, is the third consumer of that shape and already flags that a
  fourth method means extracting `ports.ObjectStore` rather than widening
  `BackupTarget`.
- 0017 added a **single-file fetch** on the target port, so importing identity from
  a bucket costs kilobytes rather than the whole archive, *"a small addition
  precisely because `List` already reads one named object per remote backup through
  the same helper."*
- **`ls` reads state files alone** — measured, §10.2:
  [`installation.go:31`](../internal/cli/installation.go) says *"no Docker call,
  no lock, no network"*, and `--status` is the opt-in that costs one Docker call
  per row. That is what makes decision 8 achievable.

That is: a place to put facts, a way to list them, a way to read one back, and a
computation that does not need the runtime — already built, already tested against
real S3 and real ssh, already carrying its own credential story. This RFC is
mostly a payload and a renderer.

## 3. Design

### 3.1 Installations publish; nothing polls them

After any operation that changes state, and on a timer (a sibling of the backup
timer, which 0015 noted is already the one thing that runs with nobody watching),
each installation writes **one small object at a stable key**, replacing in place:

```text
fleet/<product>/<installation-id>/status.json
```

Content: a schema version, product, version, mode (0016's `dev`/`prod`), manager
version, health summary, last operation with outcome and timestamp, drift
indicator, and the publish timestamp, and the signing key's public half — the
last of those so an operator reading one row can compare a fingerprint, *not* so
a reader can verify with it. Signed with
[0028](0028-the-machines-signing-identity.md)'s per-installation key, over the
bytes as published, and verified against the roster rather than against the row:
§3.6.

**The schema version is in the payload, not inferred.** §3.4 already promises
that an unknown schema version is a row carrying its problem, and a reader
cannot say that about a document that does not state its version.

**The publish timestamp is what stops an older row overwriting a newer one.**
The key is stable and the write replaces in place, so a slow publisher that
finishes after a fast one would otherwise silently install stale state as
current. A publisher reads the existing object first and declines to replace a
newer one; a reader that sees a row older than one it has already seen reports
it rather than accepting it.

Never: parameter values, hostnames unless opted in, container logs, anything
0024's allowlist would exclude. The payload is deliberately smaller than a support
bundle and smaller than an attestation; it is a *row*, not a record.

### 3.2 The reader is a command, not a service

`morzer fleet ls <target-url>` lists the prefix, fetches each object, verifies,
renders a table. Stateless. No daemon, no database, no cache. It runs on a laptop.

`--json` emits the same data, and that — per 0019's rule that the JSON contract is
the value and a view that reshaped it would make presentation changes breaking —
is where anyone wanting a dashboard starts. A static site generated from the same
objects is a fine thing for somebody to build; it is not in this repository.

### 3.3 Absence is the most important row, and it is the hard part

An installation that stopped publishing is what you actually want to see, and **an
object that was never written cannot announce itself**. Listing a prefix shows you
what is there, which is precisely the population that is fine.

So the reader takes an optional roster — `morzer fleet ls --expect roster.yaml` —
and reports three classes: reporting, stale (present but older than a threshold),
and **expected but absent**. Without a roster, only the first two are possible,
and the command says so in its output rather than presenting a partial table as a
complete one.

This is 0020's rule generalised: *an installation whose state will not parse is a
row carrying its problem, never a skipped row.* Here the same discipline extends
to an installation that produced no state at all.

### 3.4 The reader refuses to hide anything

An unparseable object, a bad signature, an unknown schema version: each is a row
carrying its problem. A fleet view that quietly drops what it cannot read is
worse than no fleet view, because it reports health it did not observe.

### 3.5 Dev-mode installations must not publish into a production prefix

The hazard exists already and is already mitigated once: `installation import`
keeps the original id, deliberately, and 0009 puts backup targets *and their
credentials* in the export — so a sandbox rebuilt from a production export would
hold the customer's bucket, the customer's credentials and a matching id, and
would push throwaway backups straight into them.

**Measured (§10.1): the mitigation shipped as a special case, not a list.**
[`modeForImport`](../internal/lifecycle/ops/recovery.go) ends with one line,
`inst.Backup.Targets = nil`, and it lives in 0017's import path rather than
0016's. So the generalisation this RFC proposes is real work rather than a
refactor of an existing list: **fleet targets must join a single drop list with a
single test**, or the second thing to drop is the thing somebody forgets.

That generalisation is worth more than the feature.

### 3.6 What is signed, and what verifies it

Two questions the first draft of this section left to the reader, both of which
have a wrong answer that looks right.

**The signature covers the object's bytes as published**, not the payload
re-serialised by the reader. A signature over "the JSON" is a signature over
whichever spelling of the JSON the verifier happens to reproduce — key order,
whitespace, number formatting, escaping — so it either needs a canonicalisation
spec that both ends implement identically, or it needs to not have that problem.
This does the second: the publisher signs the bytes it is about to write, the
detached signature is published beside them at
`fleet/<product>/<installation-id>/status.json.minisig`, and **the reader
verifies before it parses**. Nothing about the row's meaning can then depend on
a re-serialisation nobody agreed on.

**The key that verifies a row does not come from the row.** The payload carries
the public half, and that is a convenience for an operator reading one machine's
output — never the anchor. A row that carries the key that verifies it verifies
itself, which authenticates nothing against the threat §9 names: the other
machine writing to the same bucket holds a perfectly valid signing key, and it
would replace the row, the key and the signature together.

So the roster is the anchor. Decision 5 already makes a roster necessary for the
answer that matters — which installations are *absent* — and binding a public
key to an installation id there costs one field in a file the reader already
has to maintain. It is obtained the way a fingerprint always is, out of band:
`morzer installation describe` prints it on the machine itself.

The three cases a reader then distinguishes, all of them visible rows per
decision 4: verified against the roster; **signed by a key the roster does not
name**, which is the overwrite; and unsigned or unverifiable. A reader with no
roster reports that it cannot authenticate anything, in the same breath as it
reports that it cannot see absences — the two limitations have one cause.

## 4. Decisions

| # | Decision | Grade | Why |
| --- | --- | --- | --- |
| 1 | Push-only. The managed machine runs no listener and accepts no inbound connection | LOCKED | The decision the whole RFC exists to preserve; everything else is negotiable. |
| 2 | The reader cannot act | LOCKED | No `fleet update`, no `fleet exec`, no fan-out. If you want to update ten machines you run ten `morzer update`s over ssh, and the fact that this is tedious is load-bearing: it keeps the destructive path per-machine, per-decision, and locally journalled. |
| 3 | Reuses 0009's target registry unchanged | LOCKED | Extracting `ports.ObjectStore` if and only if a fourth method is needed (0025 decision 5). |
| 4 | Rows carry their problems | LOCKED | §3.4. A view that drops what it cannot read reports health it did not observe. |
| 5 | Absence requires a roster, and the absence of a roster is reported | LOCKED | §3.3. A partial table presented as complete is the failure mode of every fleet view. |
| 6 | Verification failure is a visible row, not an exclusion | LOCKED | §3.4. Whatever authenticates a row, a row that fails to authenticate is displayed carrying its problem. |
| 6a | What signs a row | LOCKED | [0028](0028-the-machines-signing-identity.md)'s per-installation minisign key, over the object's bytes as published — see §3.6. |
| 6b | The reader's trust anchor is the roster, never the row | LOCKED | §3.6. A row carrying the key that verifies it is a row that verifies itself, and the attacker §9 names — another machine with write access to the shared bucket — holds a valid key of its own. It would replace the row *and* the key, and the signature would check out. The roster decision 5 already requires is where a public key is bound to an installation id; a row signed by anything else is displayed carrying that problem, per decision 4. |
| 7 | Dev-mode drops fleet targets, via a single generalised drop list | LOCKED | §3.5, and §10.1 measured that no such list exists yet — so this decision creates it. |
| 8 | No new state | LOCKED | The published object is derived entirely from what `status`/`ls` already compute. §10.2 confirms `ls` needs no Docker call, so a publisher can report the case where the runtime is the problem. If publishing needs the manager to record something new, that is a signal the payload is too big. |

## 5. Non-goals, and what reopens each

- **An agent daemon.** Never. The timer that already exists is the scheduler.
- **A web UI in this repository.** `--json` is the contract; a viewer is somebody's
  static site. *Reopens if:* the JSON contract turns out to be unrenderable
  without server-side work, which would itself be evidence the payload is wrong.
- **Alerting.** 0015's notifier already runs per machine and is the right layer —
  the machine that failed knows first and has the context. A fleet-level alerter
  would fire on staleness, which is a different and much noisier signal.
  *Reopens if:* staleness proves to be the failure people actually miss.
- **Remote command execution, fan-out, RBAC, multi-tenancy.** All downstream of
  decision 2.
- **Aggregating across products or across customers into one view.** A vendor
  wanting this is asking for a SaaS, which is 0024's non-goal too and for the same
  reason.
- **Deduplicating or compacting the object stream.** Status replaces in place;
  there is no stream. *Reopens if:* 0025's attestations are published here too, at
  which point retention is a real question and this RFC has to answer it.

## 6. Tests

- The payload is derived from the same computation `ls` uses, asserted by a test
  that publishes with the daemon stopped — the case decision 8 exists for.
- Round-trip: publish, list, verify, render, with a tampered object and an
  unparseable object each producing a row rather than an omission (decision 4).
- **The overwrite, played out**: a second installation with its own valid key
  rewrites the first's object, its embedded public key and its signature. The
  row must be reported as signed by a key the roster does not name. A verifier
  that trusts the row's own key passes this scenario, which is what makes it the
  test decision 6b lives or dies by.
- A reader with no roster says it cannot authenticate, rather than showing rows
  as verified.
- The drop list from decision 7 gets one test covering every credential-bearing
  field, so a third thing to drop cannot be added without failing it.

## 7. Docs

`fleet ls` joins the command reference. The roster format is a reference page —
including the public key per installation and how it is obtained, since a roster
written without keys makes every row unauthenticatable — and the operating guide
says plainly what the absence of a roster means, because a reader who does not
know that reads a complete-looking table as complete.

## 8. Phasing

- **P1 — The payload.** Schema, derived purely from existing computation (decision
  8), signed, plus `fleet publish` as a manual command. Nothing scheduled. This is
  usable immediately by anyone with cron and it tests the payload before the
  timer exists.
- **P2 — `fleet ls`**, staleness, unparseable rows, `--json`.
- **P3 — The roster and absence reporting** (§3.3).
- **P4 — The timer**, as a sibling of the backup timer, and the generalised
  dev-mode drop list (§3.5).

Note the ordering: the timer is **last**. A scheduled publisher built before the
payload is stable would put badly-shaped objects in twelve buckets, and objects in
buckets are the one thing this design cannot recall.

## 9. Risks

- **The credential problem, again.** 0009 already hit it in its sharpest form:
  restoring from a bucket needs credentials that live in the state that is in the
  backup that is in the bucket. Fleet publishing is easier — it is write-only and
  needs no bootstrap — but it does mean a write credential for a shared bucket
  exists on every managed machine. Write-only, prefix-scoped credentials are the
  answer; whether `s3://` and `ssh://` targets can both express that scoping is
  §10.3's question, and if `ssh://` cannot, that is a documented limitation rather
  than a silently weaker guarantee.
- **A shared bucket means one machine can overwrite another's row.** Prefix
  scoping mitigates it where the transport supports it; where it does not, the
  signature detects it **only because §3.6 anchors verification in the roster**.
  An earlier draft of this section claimed the signature caught this on its own,
  and it does not: the machine doing the overwriting is a machine with a valid
  signing key, so a row it rewrites end to end — payload, embedded public key and
  signature — verifies perfectly against itself. That is the whole reason
  decision 6b exists, and it is worth stating as a risk rather than only as a
  decision, because "it's signed" is exactly the sentence that stops people
  asking *by whom*.
- **Scope pressure.** Every user of this will ask for decision 2 to be relaxed
  within a week. The refusal is the product.

## 10. What this draft owed a measurement

Taken 2026-08-12. The draft's list skipped item 3; the numbering is contiguous
here.

1. **Whether 0016 shipped its dev-mode credential drop as a special case or a
   list.** **A special case, and in 0017's code rather than 0016's.**
   `modeForImport` in
   [`internal/lifecycle/ops/recovery.go`](../internal/lifecycle/ops/recovery.go)
   sets `inst.Backup.Targets = nil` directly. There is no list to extend, so
   §3.5's generalisation is new work — and decision 7 now says so rather than
   describing it as a refactor.
2. **Whether `status`/`ls` compute the payload without a Docker call.**
   **Confirmed.** `installation ls` is documented and implemented as reading the
   state files alone — *"no Docker call, no lock, no network"* — with `--status`
   as the opt-in that costs one call per row. A publisher can therefore report
   the case where the runtime is the problem, which is the case worth reporting.
3. **Whether 0009's target port supports write-only, prefix-scoped credentials on
   both `s3://` and `ssh://`.** Not measured. 0009 §12 already records a listing
   prefix that *deleted a neighbouring backup*, so this class of defect is known
   to be live in this code, and P1 must answer it before anything writes to a
   shared bucket.
4. **Whether the installation id is stable across an `installation import`.**
   **Confirmed by construction:** the import path preserves it deliberately —
   that is exactly why `modeForImport` has to drop credentials. The id is
   therefore a usable bucket key, and the same property is what makes §3.5's
   hazard real.

## 11. Amendments

*(Empty.)*
