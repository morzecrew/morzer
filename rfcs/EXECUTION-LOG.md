# Execution log

Where building something disagreed with the design for it, written down at the
moment it happened. Nothing here is revised afterwards to agree with what was
later settled, and nothing here has been folded back into an RFC's own text.

The decision rows below are put forward for the author to accept or refuse.
Execution does not write them into a decision table itself.

This file is the one resident of `rfcs/` that carries no number and is not an
RFC. It has no status, and it never appears in the index table.

## Classes

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the RFC was right to be silent |
| `spec-gap` | Could have been known; the RFC was silent or at the wrong altitude | The design process missed something |
| `drift` | The RFC covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

---

# Wave 25 · The Podman host

Branch `feature/wave-25-podman-host`. RFC 0023 P1b, partially — §12 items 5 and 6
answered against a rootless Podman host, item 4 restated. No production code:
the deliverable is measurements and the record of them.

**Drift count: 0.** Nothing here was covered by an RFC and built otherwise —
helped considerably by this unit writing no code, which is worth saying rather
than letting a clean number imply more than it earned.

Four of the seven entries are `spec-gap` (D-003, D-004, D-005, D-007), recorded
as such rather than softened to `discovery`, which is the class an executor
grading their own work reaches for. Three are findings against the design
process: each was answerable from the repository before a Podman host existed,
and went unanswered because it sat on a list asserting that hardware was
required.

**D-007 is a finding against this unit**, and against D-003 specifically — a
result measured in one configuration and written down as "always". It came from
review rather than from the audit that preceded it, which is worth noting: the
audit checked whether each claim matched the run that produced it, and this claim
did. What it did not ask was whether the run covered the cases the claim spoke
for.

## D-001 — Podman does not exempt loopback from TLS; the ingest pull needs a flag

- **Touches:** RFC 0023 §12 item 6, §10's P3 bullet. Nothing in the decision
  table covers this — it was unlisted.
- **RFC said:** *"Whether 0011's in-process registry is reachable by Podman over
  plain HTTP. Not measured; 0012's TLS finding predicts it is not."*
- **Built:** nothing — this unit writes no code. **Measured:** a default `podman
  pull` puts a TLS ClientHello on the wire to `127.0.0.1` and fails with `http:
  server gave HTTP response to HTTPS client`. With `--tls-verify=false` it
  retries over HTTP and a pull from `ociserve` itself completes — blobs copied,
  `Mismatch()` nil, `ServeError()` nil. Docker, on the same listener, falls back
  to HTTP unprompted.
- **Because:** `containers/image` has no localhost exemption. Docker's daemon
  has one, and 0011's design was written against the runtime that does.
- **Class:** `discovery`. Nothing short of a Podman host settles it — 0012's
  prediction was a prediction, and it was right in direction and wrong about the
  cost.
- **Consequence:** the remedy is a flag on one command, not a `registries.conf`
  the manager would have to write into somebody's home directory. The flag is
  also the sharp edge: on the general `Pull` path it disables verification
  against a real registry. A flag that is correct on one command and a hole on
  the next is exactly the kind that gets hoisted to a struct field by a later
  refactor that sees it set in two places.
- **Proposed row (RFC 0023):** `ASSUMED` — the Quadlet ingest pulls with TLS
  verification disabled, scoped to the loopback ingest command alone. It is
  never carried on `Pull`, and never stored anywhere a second call site could
  reach it.

## D-002 — Podman ingests an OCI layout natively, so the registry may be unnecessary

- **Touches:** RFC 0011 decision 19; RFC 0023 §10's P3 bullet. Cross-document,
  and unlisted in both.
- **RFC said:** the rationale RFC 0011 adopted, written down at
  `internal/adapters/runtime/compose/ingest.go:32` — `docker load` discards the
  registry digest and `docker tag` refuses to create a digest reference, so
  *"What remains is a pull, which needs something to pull from."*
- **Measured:** `podman pull oci:<layout>` ingests a layout directly. The
  mechanism the package comment says does not exist, exists — under the other
  runtime.
- **Because:** `containers/image` supports the `oci:` transport as a first-class
  source. The Docker daemon does not, so the reasoning was sound for the runtime
  it was written against and does not transfer.
- **Class:** `discovery`.
- **Consequence:** P3 has a choice the design says it does not have. **Not
  established, and it is the half that decides:** whether the `oci:` transport
  preserves the manifest's pinned registry digest. The identity check was
  contaminated — the probe image was already in the store from an earlier
  `docker.io` pull, so its `RepoDigests` had accumulated six entries and proved
  nothing about what this pull recorded. Until that is measured on a clean
  store, the choice is open rather than made.
- **Proposed row (RFC 0011):** `OPEN` — whether a Quadlet ingest uses the
  in-process registry or the `oci:` transport, to be decided by 0023 P3 after
  digest fidelity is measured on a store that does not already hold the image.

## D-003 — Item 5's premise was void, and rootless inverts a property 0010 reads as a design choice

- **Touches:** RFC 0023 §12 item 5 and §2.3; RFC 0010. Unlisted.
- **RFC said:** *"Whether rootless Podman's volume paths break 0010's staging
  assumptions. Not measured; needs the same machine."*
- **Measured:** 0010 has no host-path assumption to break. `CaptureVolume` mounts
  the volume into a helper container read-only and takes the tar off *stdout*;
  the host path is never named. The round trip was run under rootless Podman with
  the adapter's own argv — a dotfile, a `0600` mode and a nested directory
  survived, and the restore's wipe-then-extract removed a planted file rather
  than merging around it.
- **Because:** the capture was already designed to avoid touching the volume
  directly, for an unrelated reason — a bind mount would have left a root-owned
  file in a directory the manager may not be root in.
- **Class:** `spec-gap`, not `discovery`. `CaptureVolume` could have been read at
  the time §12 was written, and reading it would have closed the item without a
  host. The measurement confirmed what a read would have established.
- **Consequence:** no design change is forced, which is the useful half. The
  other half is a property, not a path: a rootless volume lives under
  `~/.local/share/containers/storage/volumes/<name>/_data` and is readable by the
  manager's own user, where Docker's answers `Permission denied`.
  `CaptureVolume`'s comment reads *"the manager should not need to be able to
  touch the volume directly"* as a design choice; under rootless Podman it can,
  always. Anything relying on the manager being *unable* to read a volume is
  relying on the runtime, not on this design.
- **Proposed row (RFC 0010):** `ASSUMED` — the helper-container capture is the
  mechanism under every runtime, and the manager's inability to read a volume
  directly is a property of a rootful runtime rather than a guarantee this
  design makes.

**Refined by D-007.** The readability claim as written above is too broad, and
the proposed row's second clause is weaker than what the refinement supports.
Left standing rather than rewritten — the entry records what was believed when
it was written, and the correction is the next entry's job.

## D-004 — Item 4 needed a bootable venue, not a Podman host

- **Touches:** RFC 0023 §12 item 4, §10's P1b bullet, §14. Also this unit's own
  execution plan, which asserted the same thing in a worse form — that the
  measurement wanted a reboot of the development workstation.
- **RFC said:** *"Not measured — needs a machine with Podman, which this
  repository's test lanes do not have"*, and P1b is *"One task, not three"*.
- **Built:** item 4 restated in §12, §10 and §14, with the mechanism half and the
  ordering half named separately.
- **Because:** a host that is already up cannot be asked what its tmpfs held at
  boot. The blocker was never the presence of Podman — it was the ability to
  boot something on demand, which the grouping never named and which a
  workstation supplies worst of all.
- **Class:** `spec-gap`. The question is about tmpfs and systemd ordering, and
  both were knowable when §12 was written.
- **Consequence:** item 4 no longer implies interrupting whoever is using the
  development machine, which was the assumption that made it expensive and the
  reason it stayed open longest. Decision 2 (`LOCKED`) still governs the venue: a
  venue that is not itself rootless would measure a configuration decision 2
  excludes.
- **Deliberately not applied:** the venue was not chosen. Naming one here would
  settle by implication a question that touches a `LOCKED` row, and the whole
  point of the row's grade is that the executor's confidence is what is under
  test.

## D-005 — The RFC asserts P1b gates P2; both answered measurements landed on P3

- **Touches:** RFC 0023 §10 and §12's closing paragraphs. Phasing prose — no
  decision row covers it.
- **RFC said:** P1b is *"the only thing between here and P2"*, repeated in three
  places including the index's routing line.
- **Built:** the claim left standing in the RFC and questioned here, rather than
  edited to match what execution now believes. The factual counts around it were
  updated; the claim itself was not.
- **Because:** item 5 bears on volume capture and item 6 on image ingest, and
  both are P3. Item 4 bears on §4.3's parameter delivery, also P3. What P1b
  actually feeds into P2 is `doctor`'s checks (§9) — to which this unit adds two,
  the lingering requirement and `net.ipv4.ip_unprivileged_port_start`. That is a
  real dependency and a much smaller one than "the only thing between".
- **Class:** `spec-gap`.
- **Consequence:** if the claim is wrong, P2 may start now and the programme is
  not blocked on a venue at all. Deliberately not decided here — a phase gate is
  the author's to move, and an executor who moves one has given themselves
  permission to start the next phase.
- **Proposed row:** none. This is a phasing claim rather than a design decision,
  and it belongs in §10's prose if the author agrees with it.

## D-006 — Rootless units need lingering, a precondition the RFC never names

- **Touches:** RFC 0023 §11 and §9. Unlisted — §11 names one rootless
  precondition, `net.ipv4.ip_unprivileged_port_start`, and §9 gives `doctor` one
  check, the declared runtime's presence.
- **RFC said:** nothing at all about the user manager's lifetime.
- **Measured:** a rootless user unit does not start at boot unless the account is
  lingering. `loginctl enable-linger` sets it; it is off by default. The
  development host has it on for reasons predating this work.
- **Because:** `systemd-logind` tears the user manager down at last logout
  unless lingering is set, so there is nothing alive at boot to start a
  `.container` under.
- **Class:** `discovery`. No reading of this repository surfaces it — it is a
  property of the runtime's host integration, and it took a rootless host to
  notice.
- **Consequence:** a second `doctor` check, and a worse failure shape than the
  first. A machine without lingering installs, runs, converges and passes every
  check the manager currently makes, and then does not come back after a reboot
  — the cause separated from the symptom by however long the machine stays up.
  It also contaminates §12 item 4's venue: the only host available has lingering
  on, so a measurement taken there cannot see the state a fresh machine is in.
- **Proposed row (RFC 0023):** `ASSUMED` — a rootless runtime requires the
  installation's account to be lingering, and `doctor` reports its absence.
  Whether `init` refuses outright is left to P2, where it belongs beside decision
  5's refusal shape rather than being settled by the phase that found it.

## D-007 — The manager reads only what container root wrote, which makes the helper necessary rather than merely correct

- **Touches:** RFC 0023 §12 item 5; RFC 0010. Refines D-003, raised in review of
  PR #49 by CodeRabbit, which asked for the readability claim to be scoped to the
  configuration it was measured in.
- **D-003 said:** under rootless Podman the manager can read a volume directly,
  *"always"*.
- **Measured 2026-08-16**, varying the one axis D-003 held fixed — the uid the
  writing container runs as:

  | Written by | Host owner | Manager reads it |
  |---|---|---|
  | container root | the invoking user | yes |
  | container uid 1000 | subuid `100999`, mode `0600` | `Permission denied` |

  The helper container captured both, being inside the namespace where those ids
  mean something.
- **Because:** rootless maps container uid 0 to the invoking user and every other
  container uid into the subordinate range. D-003 measured a `busybox` running as
  root and generalised from it.
- **Class:** `spec-gap` against D-003 rather than `discovery` — the mapping is how
  rootless works, and one probe run as a non-root user would have found it. The
  reviewer was right that a single configuration had been generalised; the
  correction is larger than the one requested, because the direction of the
  conclusion changes rather than its confidence.
- **Consequence:** the argument runs the other way from D-003's. A capture that
  read the host path directly would succeed against a product running as root and
  fail against one that drops privileges — the configuration a security-conscious
  vendor ships — so the helper container is **the only mechanism that works
  under rootless**, not merely one that still works. D-003's proposed row
  understates this, and the row below replaces it.
- **Proposed row (RFC 0010), superseding D-003's:** `ASSUMED` — the
  helper-container capture is the mechanism under every runtime, and under a
  rootless one it is the only mechanism: the manager's own credentials reach only
  files written by container root, so a host-path capture would silently depend
  on the product not dropping privileges.

## Decision-row outcomes — 2026-08-16

**One ruling, four proposals still outstanding.** D-005's phasing question was put
to the author and answered; D-001, D-002, D-003 and D-006 were not. The
outstanding rows are listed rather than omitted, because a proposal nobody has
accepted or refused should be visible as such — a log that only records
acceptances cannot tell that state from a proposal quietly adopted.

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | — | **Accepted** | — | P2 is not gated on P1b: it consumes none of the three measurements, and what it did need — the host precondition list for decision 5's refusal and §9's `doctor` — landed with items 5 and 6. P1b stays open on item 4, which gates P3. No decision row; §10's phasing prose carries it. | D-005 |
| 0023 | — | Outstanding | — | Ingest pulls with TLS verification disabled, scoped to the loopback command | D-001 |
| 0011 | — | Outstanding | — | Registry or `oci:` transport for a Quadlet ingest, pending digest fidelity | D-002 |
| 0010 | — | ~~Outstanding~~ **Superseded by D-007** | — | ~~Helper-container capture under every runtime; unreadability is the runtime's, not the design's~~ | D-003 |
| 0023 | — | Outstanding | — | A rootless runtime requires a lingering account; `doctor` reports its absence | D-006 |
| 0010 | — | Outstanding | — | Helper-container capture is the only mechanism under rootless: the manager's credentials reach only what container root wrote | D-007 |

**The alternative that was declined is worth recording**, since nothing else
would carry it: closing P1b outright by folding item 4 into P3, where its answer
is consumed. Refused because it recreates the grouping error D-004 had just
found — item 4 would again inherit a completion criterion belonging to other
work — and because a phase left open on one item is a visible statement that the
architecture test is not yet de-risked, which folding it away would erase.

## Audit findings — 2026-08-16

Scope: the whole branch as of `15ebb0f`, three files — `0023`, `INDEX.md` and
this log. No production code, so the audit is a fidelity pass: every claim traced
back to something actually run, every quotation to the file it is in, every
number to where it came from. Pinned to a commit rather than described as "the
branch", since the fixes below add one and a scope that drifts is a scope that
claims to have covered them.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | High | §12 item 5 said the round trip used "the adapter's own argv", but the probe ran a *tagged* `busybox` while the adapter pins `DefaultHelperImage` by digest. The argv matched; the image did not, so the sentence claimed a fidelity the run did not have. | Fixed — re-run with the pinned digest, same result, and §12 now names the constant |
| A-2 | Medium | D-002 attributed *"What remains is a pull…"* to `ociserve`'s rationale; it is in `internal/adapters/runtime/compose/ingest.go:32`. A reader checking the quote at the named place would not find it. | Fixed — attributed to the file and line |
| A-3 | Low | §11 stated lingering is "off by default" in the same breath as measured findings, so a reader would take it as measured. It is documented behaviour; what was measured is that this host has it *on*. | Fixed — the documented half and the measured half are now separated |

**No sabotage sweep.** There is no code on this branch to mutate, and a sweep
reported against prose would be theatre. The equivalent discipline here is A-1's:
re-running the measurement under the conditions the sentence claims, rather than
softening the sentence to match the run that happened.

**What remains distrusted.**

- **Item 4, entirely.** Nothing about it was measured.
- **D-002's digest fidelity**, which is the half that decides whether the `oci:`
  transport can replace the registry. Stated as unmeasured in the entry itself.
- **Every measurement here is hand-run against one host, and nothing re-runs
  them.** They are reproducible from what §12 records, which is not the same as
  being watched: if Podman's loopback behaviour changes, no lane goes red. This
  is the same distinction §12's own closing paragraph draws about CI, and it
  applies to the measurements as much as to a job.
- **One host, one storage configuration.** The rootless volume path and its
  readability were observed on this machine's setup. **One axis has since been
  varied — the uid the writing container runs as — and varying it reversed the
  conclusion (D-007).** The storage driver, the graph root's filesystem and the
  subuid range's size have not been varied, and the same lesson applies to each:
  a result from one configuration is a result about that configuration.

## Rules distilled

- **A phase boundary drawn around a shared blocker holds only as long as the
  blocker was identified correctly for every member.** Two of P1b's three items
  closed within an afternoon of a Podman host existing; the third was never
  blocked on one, and inherited a completion criterion that had nothing to do
  with it. (D-004)
- **An unknown a reader could close by reading the code should not be sitting on
  the list of what is unknown.** It costs more than a wrong answer, because the
  list is cited as the definition of what is not yet known. (D-003)
- **Verify an identity on a clean store.** An artifact already present from
  another source makes an accumulating field agree with any hypothesis. (D-002)
- **A flag that is correct on one command and a hole on the next must never
  become a field.** The refactor that hoists it will be reading two call sites
  that set it, not the one that must not. (D-001)
- **Update the counts, propose the claim.** When a document's facts go stale and
  its argument becomes doubtful in the same edit, only the first is the
  executor's to change. (D-005)
- **"Does the claim match the run?" is a weaker question than "does the run cover
  what the claim speaks for?"** The audit asked the first of D-003 and got yes;
  the second would have caught it, and review did. A measurement generalised past
  the one configuration it was taken in fails no check that compares it to its own
  evidence. (D-007)
- **A precondition whose absence survives every check is worse than one that
  fails loudly**, and it is found by asking what happens after a reboot rather
  than by testing the install. Lingering passes install, converge and `doctor`,
  and shows up as a machine that did not come back. (D-006)

## Carried into the next unit

- **§12 item 4**, and with it the venue question against decision 2 (`LOCKED`).
  The venue must be rootless to satisfy that row, and must be able to boot
  *without* lingering already set, or it answers a different question than the
  one asked (D-006).
- **Digest fidelity of Podman's `oci:` transport**, measured on a store that does
  not already hold the image — the half of D-002 that decides it.
- ~~**Whether P2 is gated at all** (D-005), which is the author's call and which
  changes what the next wave even is.~~ **Ruled 2026-08-16: it is not.** The next
  wave is RFC 0023 P2 — the manifest's runtime dimension and decision 8 — and it
  starts with a constraint this unit did not go looking for: `Providers.Runtime`
  is a single `Provider`, so decision 8's better-reading option cannot express a
  bundle declaring two runtimes, which §4.1 requires and decision 4 assumes.
- **The development machine's Docker daemon is 29.6.2 while its client is
  29.7.2** — a live upgrade that has not been restarted into, caught in the
  User-Agent on the wire during D-001's measurement. `just test-docker` and `just
  acceptance` should be re-baselined after the next reboot, so that a Podman
  finding is never confused with a Docker upgrade regression.

---

# Wave 26 · The manifest's runtime dimension

Branch `feature/wave-26-manifest-runtime-dimension`. RFC 0023 P2, partly — the
manifest and state halves; `installation import`, `doctor`, and §14's two
unspelled leaks are not in it.

**Drift count: 0.** Nothing the RFC settled was built otherwise. Two entries
below depart from §4.1's *sketch* rather than from a decision row, which is a
different thing and is said so in each.

## D-008 — Decision 8 resolved against the option the RFC preferred

- **Touches:** RFC 0023 §4.1, decisions row 8 (`OPEN` → proposed `LOCKED`).
- **RFC said:** either `runtimes:`' keys are the declaration, or
  `providers.runtime.name` stays the selector — *"The second reads better and is
  a smaller change."*
- **Built:** the first. `runtimes:`' keys are the declaration;
  `providers.runtime.name` is derived from them for a single-runtime release and
  left empty for a two-runtime one.
- **Because:** `Providers.Runtime` is a single `Provider` beside `secrets`,
  `backup` and `health`. It holds one value, and §4.1 requires a bundle to be
  able to declare two runtimes — which decision 4 then has `--render-check`
  render both of. The preferred option cannot express the case the same section
  mandates.
- **Class:** `spec-gap`. The struct has looked like this since before the RFC;
  the preference was formed against the YAML sketch rather than the type.
- **Consequence:** the manifest's hardcoded `"compose"` default is gone —
  §2.1's second expensive leak, and the one §12.2 said decided the RFC's cost.
  `providers.runtime.name` is now derived or empty, never invented.
- **Proposed row (RFC 0023, row 8):** `LOCKED` — as built above.

## D-009 — `runtimes:` is added; `runtime:` stays readable

- **Touches:** RFC 0023 §4.1. Put to the author before any code was written.
- **RFC said:** the block *"lands as a replacement of the existing block before
  the first tag rather than an addition after it."*
- **Built:** an addition after it. Both spellings parse, a manifest declaring
  both is refused, and the legacy block folds into the map on read.
- **Because:** the premise expired. 0.1.0 and 0.2.0 are cut, and under strict
  decoding a replacement makes `runtime:` an unknown field — every bundle
  already built stops parsing, to buy a tidier surface. The author ruled on the
  alternatives rather than the executor.
- **Class:** `spec-gap`.
- **Consequence:** two spellings until a named removal release, and **no
  `api_version` bump**: 0018 decision 1's `min_manager_version` carries the cost,
  which is the mechanism it exists to be. `DeprecatedAPIVersions` stays empty —
  it is keyed by api_version and this is a *field* deprecation, which has no
  mechanism today and did not grow one here.
- **Proposed row (RFC 0023, row 9):** `LOCKED` — as built above.

## D-010 — One `files` key per runtime, against §4.1's sketch

- **Touches:** RFC 0023 §4.1's YAML example; decision 7 (`LOCKED`).
- **RFC said:** `quadlet: {units: [app.container, ...]}` beside
  `compose: {files: [...]}`.
- **Built:** `files` for every runtime.
- **Because:** deciding whether `units` or `files` is the legal key means asking
  which runtime the block belongs to, and a branch on a runtime's name above
  `internal/adapters` is exactly what decision 7 forbids and what
  `tools/runtimecheck` fails the build over. A `LOCKED` row outranks a sketch in
  a design section, so this is a departure from the illustration rather than a
  conflict needing a halt.
- **Class:** `discovery`. The collision is only visible once the validator is
  written against the rule.
- **Consequence:** a vendor writes `runtimes.quadlet.files: [app.container]`.
  Those are files, so the name is honest; what they *mean* stays the adapter's.
- **Proposed row (RFC 0023, row 10):** `LOCKED` — as built above.

## D-011 — A fourth zero-caller shape, in the field this feature wanted

- **Touches:** RFC 0023 §1; `internal/domain/installation.go`. Unlisted.
- **RFC said:** nothing. §1 indicts the shape in general — 0015 found a port
  with no implementations, 0021 methods with no callers.
- **Found:** `Installation.Providers` is declared, serialised, and **never
  written and never read** by anything, tests included. It also carries two
  contradictory documented meanings: `describe.go` says *"declared by the release
  manifest, not chosen by the operator"*, and `repair_test.go` says *"from the
  flags: which adapters to use is what `init` decides"*. It comes from neither.
- **Built:** a new `Installation.Runtime`, schema 8 → 9, rather than reusing it.
- **Because:** an older manager reading `Providers.Runtime` finds a name it
  understands and no reason to stop — and it has one adapter, so it would drive
  a Quadlet installation with Compose. The bump is what makes that a refusal,
  and it is for the *read* path, which none of bumps 5–8 were.
- **Class:** `spec-gap` against the codebase rather than against this RFC.
- **Consequence:** `Installation.Providers` is still unwritten and now has a
  neighbour that does its apparent job. It should be deleted or given a meaning;
  this wave did neither, because removing a serialised field is its own schema
  question.
- **Proposed row (RFC 0023, row 11):** `LOCKED` — as built above.

## D-012 — A multi-runtime release is refused at `init`, pending P3

- **Touches:** RFC 0023 §4.1, decision 5 (`LOCKED`). Unlisted as a row.
- **RFC said:** *"A bundle declaring both carries both sets."* It does not say
  how the manager picks which one to install with.
- **Built:** `init` records the runtime when a release declares exactly one, and
  refuses when it declares several, naming P3.
- **Because:** choosing means knowing which runtime this manager can drive, and
  the only answers available today are a branch on a runtime's name — forbidden
  by decision 7 — or a name injected at the composition root that every test
  would set and no test would exercise as production leaves it. Refusing costs a
  bundle nobody ships yet; either alternative costs the architecture test.
- **Class:** `spec-gap`.
- **Consequence:** the manifest can express a two-runtime release before the
  manager can install one. That gap closes with the second adapter.
- **Proposed row (RFC 0023, row 12):** `ASSUMED` — a release declaring several
  runtimes is refused at `init`. Graded `ASSUMED` rather than `LOCKED` because it
  expires: P3 brings a second adapter and with it a real basis for choosing.

## D-013 — The state migration loop could hang rather than refuse

- **Touches:** `internal/infra/state/state.go`. Pre-existing; no RFC covers it.
- **Found by sabotage**, and not in the way a sweep usually reports: the mutation
  that stopped `case 8` advancing the schema version did not fail a test, it hung
  the run until the timeout killed it. `migrateInstallation` loops while the
  version is below current, so a case that does not raise it never terminates.
- **Built:** a progress check that refuses when a pass raises nothing.
- **Because:** every load of installation state goes through this loop. A
  mistyped case number is therefore a manager that stops responding on an
  operator's machine, not one that says what is wrong — and it would present as
  a hang with no output, which is the hardest failure to diagnose remotely.
- **Class:** `discovery`.
- **Consequence:** the failure is now a sentence naming the schema version that
  made no progress. The guard costs one comparison per migration pass.

## Decision-row outcomes

**Ruled 2026-08-16. All four proposals accepted, and a fifth row added for
D-012 at the author's direction.** D-013 proposes nothing — it is a defect fixed
in the code it belongs to.

**Recorded against this section's own process failure:** rows 8–11 were written
into the RFC's decision table as `LOCKED` in the same pass that amended the
phasing, before any of them had been put to the author. The ruling below makes
them legitimate; it does not make the sequence correct, and the entry stays here
because a log that only records outcomes cannot show that a proposal was adopted
before it was offered. D-012's row was the one written in the right order — put
first, accepted, then added.

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | 8 | **Accepted** | `LOCKED` | `runtimes:`' keys are the declaration; the provider name is derived or empty | D-008 |
| 0023 | 9 | **Accepted** | `LOCKED` | `runtimes:` added, `runtime:` deprecated and still read, no api_version bump | D-009 |
| 0023 | 10 | **Accepted** | `LOCKED` | One `files` key per runtime; per-runtime key names cannot be validated without a forbidden branch | D-010 |
| 0023 | 11 | **Accepted** | `LOCKED` | The runtime is a new installation field at schema 9, not `Providers` | D-011 |
| 0023 | 12 | **Accepted** | `ASSUMED` | A release declaring several runtimes is refused at `init`, until P3 gives a basis for choosing | D-012 |

**Two alternatives were declined and are recorded here, since nothing else
carries a refusal.** Keeping §4.1's `units:` key per runtime, which would have
let `compose: {units: [...]}` pass domain validation and fail only at the
adapter — refused because it moves the error further from the vendor who caused
it. And deleting `Installation.Providers` in this wave rather than leaving it
beside its replacement — refused because removing a serialised field is its own
schema question, and it is carried forward instead.

## Audit findings — 2026-08-16

Scope: the whole branch as of `8fc06c2`, six commits. The findings below were
produced by the project's own guards and by the sabotage sweep, not by reading.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | High | Normalising the legacy block into a field during `ApplyDefaults` made it a snapshot: `Validate` called on its own saw an empty map and checked no paths, so `/etc/passwd` in `runtime.files` passed. A path-escape check that holds only when another method ran first is not a check. | Fixed — `DeclaredRuntimes` derives on every call; test asserts the check without `ApplyDefaults` |
| A-2 | High | `buildInstallation` set the runtime from the release on **every** `init`, including `--repair`, so a vendor changing runtimes between releases would have a repair re-point an installation whose volumes belong to the old one — decision 3's transition, by the back door. | Fixed — carried from existing state; behavioural test added |
| A-3 | Medium | `migrateInstallation` hangs rather than refusing when a case fails to advance the version (D-013). | Fixed — progress guard |
| A-4 | Low | The refusal for a manifest declaring no runtime named only `runtimes`, sending a vendor using the legacy spelling to look for a block they do not have. | Fixed — names both spellings |

**Sabotage sweep: 8 mutations, 8 killed** — but only after two of them were
made killable. `repair rebuilds the runtime` **survived**: the repair
classification table asserts that somebody wrote a *reason*, not that the code
matches it, so the carry had no behavioural test. And `case 8 does not advance`
killed by timeout rather than by assertion, which is A-3 demonstrating itself.

**What remains distrusted.**

- **P2 is not finished.** `installation import` does not carry the runtime,
  `doctor` does not report it, and §14's two unspelled leaks are untouched. Each
  is named in §10's P2 bullet rather than left to be discovered.
- **No adapter has ever been selected by this machinery.** Every path is
  exercised with one runtime present, so "refuses the runtime it cannot run" is
  tested against a release that declares a runtime nobody can run, not against a
  machine that lacks one.
- **`Installation.Providers` is still unwritten**, and now sits beside a field
  doing its apparent job (D-011).

## Rules distilled

- **A validator that reads a normalised field is only as good as the guarantee
  that normalisation ran.** Derive on read, or the check silently becomes a
  check of stale state. (A-1)
- **A table that records a reason is documentation, not a test.** If a
  classification says "carried", something must fail when it stops being
  carried. (A-2)
- **A loop whose exit condition is only changed inside its own body needs a
  progress guard**, or a missing case is a hang rather than an error — and a
  hang on a load path is the least diagnosable failure there is. (D-013)
- **A design sketch loses to a `LOCKED` row.** When the illustration cannot be
  implemented without violating a decision, the illustration is what gives way,
  and the departure is recorded rather than argued. (D-010)

## Carried into the next unit

- **The rest of P2**: `installation import`, `doctor`, and §14's two unspelled
  leaks (`RuntimeSpec.Project`, `doctor.go`'s hard-coded `tools.Docker`).
- **`Installation.Providers`** — delete it or give it a meaning. Deleting it in
  this wave was put to the author on 2026-08-16 and declined: removing a
  serialised field is its own schema question, and widening the branch after its
  audit is how an audited branch stops being audited.
- **The field-deprecation gap**: `runtime:` is deprecated and nothing warns. The
  only deprecation mechanism is keyed by `api_version`, and this is a field.
- **A named removal release for `runtime:`**, without which "deprecated" is a
  word in a doc.
