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

## Review findings — 2026-08-16

Six findings on PR #50, all valid, none refuted. Two were raised by both bots.
The first two matter most, because each meant an accepted decision did not hold
in code.

| # | Severity | Finding | Status |
|---|---|---|---|
| R-1 | Critical | `stepWriteInstallation` ran before `stepStageRelease`, the only thing that puts the release into engine state — so `runtimeForNewInstallation` found none and **every installation recorded an empty runtime**, read as the legacy one. The feature recorded nothing. | Fixed — staging moved first; ordering asserted on the step list |
| R-2 | Critical | `Validate` required `providers.runtime.name` unconditionally while `ApplyDefaults` deliberately leaves it empty for two runtimes, so **a two-runtime manifest could not validate** and decision 8's shape did not exist. | Fixed — required only where one name can be true |
| R-3 | Major | An empty runtime key validated; `runtimeForRelease` recorded `""`; `RuntimeName()` read that as the legacy runtime, so a release declaring something else installed as Compose with every later message agreeing. | Fixed — empty and whitespace keys refused |
| R-4 | Major | Nothing refused an installation whose runtime this manager has no adapter for, so the recorded runtime was a label (decision 5). | Fixed — `ports.Runtime` gains `Name()`; the comparison is two values, not a literal |
| R-5 | Major | `checkReferencedFiles` walked only the deprecated block, so a `runtimes:` release loaded clean with a missing file and failed three steps into a deployment. | Fixed — walks every declared runtime, naming the vendor's own spelling |
| R-6 | Medium | The no-progress guard's test started at schema 1, which falls to the switch default and returns before the loop body runs twice — it never reached the guard, so a case that stopped advancing would still hang while the test stayed green. | Fixed — every supported schema migrated forward; the timeout is the assertion |

**Both critical findings are the same shape, and it is the shape this branch's
own audit was blind to.** R-1 and R-2 each passed every test written for them,
because each test exercised the piece rather than the path: `runtimeForRelease`
was tested directly and never through an `init`, and the two-runtime manifest
was asserted to leave a field empty without ever being asked to load. The seam
extracted to make a decision testable is the seam that stopped the decision
being tested where it runs.

**Port growth, recorded so §6's count stays honest.** `ports.Runtime` gains
`Name()`, its thirteenth method. §6's escape hatch counts methods forced by the
*second adapter*; this one is forced by decision 5's refusal, and the alternative
was comparing against a literal above `internal/adapters` — the branch decision 7
forbids.

**R-7, taken after the round rather than carried.** `Installation.Validate`
ignored `Runtime` entirely, so a hand-edited state file naming a runtime that
does not exist loaded clean.

The answer is that "validate the runtime" is two questions and only one of them
belongs here. **Whether a name is well-formed** is a grammar, and the domain
already has that shape for images, parameters and product names — so
`ValidRuntimeName` joins them, rejecting empty, padded, capitalised,
underscored, over-long, and anything carrying a terminal escape. **Whether a
runtime exists** is not a fact this layer has, and any answer shaped as a list
of known names is the runtime catalogue above `internal/adapters` that decision
7 exists to prevent; the well-formed-but-wrong name is refused by R-4's adapter
comparison, which is the only place that knows.

The security half is what made it worth doing now rather than later: the value
is read from a file an operator may have hand-edited and is printed back in
error messages, so a name carrying an escape sequence is a diagnostic that moves
the cursor — the same shape as the bounds on fleet rows and attested text, and
the same argument.

**The limit is asserted, not merely described.** A test requires that `quadlt`
*passes* domain validation, so a later reader who adds a catalogue here has to
delete a test that says why there isn't one.

**A process failure worth recording against this round.** Five of the six thread
replies cited commit hashes written from memory rather than read from `git log`;
four of them pointed at nothing. Corrected on the threads. A reviewer following a
fabricated reference finds no commit and has no way to tell a wrong hash from a
fix that was never made.

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
- **A seam extracted for testability is a seam where the production path stops
  being tested.** Both critical review findings sat exactly there: the unit
  passed, and nothing asked whether the caller reached it. (R-1, R-2)
- **A test that asserts a field is empty has not asserted the object is
  usable.** The two-runtime manifest satisfied its test and could not load.
  (R-2)
- **"Validate X" is often two questions at two layers.** Split them before
  reaching for the check: the shape of a value is usually knowable where it is
  defined, and its truth usually is not. (R-7)
- **Assert the limit of a check, not just its coverage.** A test requiring that
  a plausible-looking wrong value *passes* is what stops the next reader
  "fixing" the omission by adding the thing the architecture forbids. (R-7)
- **Read the hash, never recall it.** A commit reference is a claim like any
  other, and one written from memory is unfalsifiable to the reader who follows
  it and finds nothing.
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

# Wave 27 · Import, doctor, and the leak with no name in it

Branch `feature/wave-27-import-and-doctor-runtime`. RFC 0023 P2, the rest of it
except one item: `installation import` carrying and refusing the kind, `doctor`
reporting it, and §2.2's second unspelled leak. `RuntimeSpec.Project` is not in
it, deliberately and by the author's ruling — see D-016.

**Drift count: 0** against this wave's RFC. One finding against wave 26 is
recorded here (D-016) and it is *not* drift: the RFC never settled what a
project means under `runtimes:`, so its absence is a gap rather than a
departure. What wave 26 missed was writing the gap down, which is what this
entry is.

**The readiness gate fired before any code was written, and that is the entry
worth reading first.** "The rest of P2" as a single unit needed three
load-bearing decisions the RFC does not settle — the import refusal, the
project question, and the deprecation mechanism — which is the threshold at
which `flag-dont-flip` says an RFC is not ready to execute as drawn. All three
were put to the author; one was ruled into the wave and two were ruled out of
it. The wave that ran had one.

## D-014 — The `tools.Docker` leak is closed by a capability, not a rename

- **Touches:** RFC 0023 §2.2, §4.5, §9. No decision row governs it: the leak was
  named and classified, and no fix was ever specified.
- **RFC said:** `ops/doctor.go` hard-codes `tools.Docker` in the branch that runs
  when there is no installation — *"the lifecycle layer stating which runtime
  this machine will use, in a sentence with no runtime's name in it."*
- **Built:** `ports.ToolRequirer`, an optional capability beside `RegistryProber`
  and `ImageInspector`. `doctor` asks the wired adapter which tools `init` will
  need; the compose adapter answers `docker` and `compose`.
- **Because:** §14 files both unspelled leaks under "the renames themselves are
  not P1's", and this one cannot be renamed — the sentence has no runtime's name
  in it, which is why the inventory could not hold it. What had to move was the
  *decision*, from the layer that must not make it to the layer that owns it.
- **Class:** `spec-gap`.
- **Consequence:** `doctor` on a machine with no installation now also checks the
  Compose CLI plugin, and **fails** on a host that has the daemon and not the
  plugin. That machine could never have completed an `init`, so this is a
  refusal moving earlier rather than a new restriction — but it is a new failing
  check on an existing surface and it is in the changelog as one.
- **Deliberately not applied:** two alternatives, each refused for a reason worth
  keeping. A `runtime name → tool` table in the tool catalogue, which decision 7c
  explicitly permits as data — refused because it puts the adapter's knowledge in
  a table the adapter cannot see, so the manager would be asserting what a
  runtime needs rather than asking. And a method on `ports.Runtime` — refused
  because §6's escape hatch counts methods, and spending the fourteenth on a
  diagnostic would blunt the test the RFC exists to run.
- **Proposed row (RFC 0023, row 14):** `ASSUMED` — where the manager would
  otherwise state which runtime a machine uses, it asks the adapter; an optional
  capability is the mechanism, so an adapter that cannot answer declines rather
  than stubs. **Outstanding.**

## D-015 — `installation import` refuses a runtime it cannot drive

- **Touches:** RFC 0023 §4.2, decision 3 (`LOCKED`), decision 5 (`LOCKED`).
- **RFC said:** *"`installation import` is the second creation path (0016 found
  this) and must carry the kind."* Nothing about what an import does when the
  kind names a runtime this binary has no adapter for.
- **Built:** refused before anything is created, naming both runtimes and the two
  ways forward, exiting 9.
- **Because:** the field already travelled — an export carries the installation
  whole — so carrying it was never the open question. The open question is that
  decision 3 makes the runtime immutable, so an imported record naming an
  undriveable runtime is a machine where `apply`, `update`, `status` and
  `restore` all fail and nothing can correct it short of deleting the
  installation.
- **Class:** `spec-gap`.
- **Consequence:** this is the only thing an import refuses about the *manager*
  rather than about the document, and it sits directly beside `ManagerVersion`,
  which the same file says is *"recorded for diagnosis, never enforced: refusing
  an export because it was written by a different manager is a refusal at the
  worst possible moment."* The distinction is real and is now written in both
  places: a version mismatch still leaves a working machine.
- **Deliberately not applied:** importing with a warning, which was put to the
  author beside the refusal. Refused because the warning is read during an
  incident, by someone who then runs `apply` and gets a second, worse surprise.
- **Proposed row (RFC 0023, row 13):** `LOCKED` — **accepted 2026-08-16**, added
  with a back-link.

## D-016 — `runtimes:` cannot name a project, and one is supplied anyway

- **Touches:** RFC 0023 §2.2, §4.1, decision 10 (`LOCKED`). Against wave 26.
- **RFC said:** §4.1's sketch gives each runtime a file list and nothing else.
  §2.2 lists `RuntimeSpec.Project` as an unspelled leak and describes it as a
  field with three readers.
- **Found:** it is more than that now. `RuntimeDecl` has no `project` key, so a
  vendor on the new spelling cannot set one — and `ApplyDefaults` fills
  `m.Runtime.Project` from the product name **unconditionally**, including for a
  manifest that never wrote a `runtime:` block. That value still reaches
  `--project-name` and the `COMPOSE_PROJECT` hook ABI. The only way to name a
  project under `runtimes:` is to write a legacy block containing nothing but
  `project:`, which slips past the both-declared refusal because `isZero()`
  deliberately ignores the field.
- **Because:** decision 10 removed per-runtime key names, and `project` left with
  them without anybody saying so. Wave 26's log records the decision and not this
  consequence.
- **Class:** `spec-gap`. The RFC never settled what a project means under the new
  spelling, so this is not drift — wave 26 built nothing the RFC had decided
  otherwise. What it did was leave a surface nobody chose.
- **Consequence:** every release on the new spelling is grouped by its product
  name, silently, through a field on a deprecated block. 0021 already noticed the
  docs' teardown snippet works only because the example's project name happens to
  equal its product name; this makes that coincidence the rule, without saying so
  anywhere a vendor reads.
- **Not fixed here, by the author's ruling on 2026-08-16.** It is a published
  hook ABI, and moving it breaks bundles in the field — its own unit of work,
  with its own decision about whether the ABI moves, is renamed with the old name
  kept, or stays what it is. Widening this wave to include it would have made the
  wave the ABI change with a doctor check attached.
- **Proposed row:** none. The question is what a project *is* once a second
  runtime exists, and proposing an answer from inside a wave that is not doing
  the work would be the laundering this practice exists to prevent.

## D-017 — The deprecation of `runtime:` is deferred, with the reason

- **Touches:** RFC 0023 decision 9 (`LOCKED`), RFC 0018 decision 1. A departure
  from the execution plan rather than from a document.
- **Plan said:** wave 27 covers the rest of P2, and the carried list from wave 26
  names the field-deprecation gap as part of it.
- **Built:** nothing. `runtime:` remains deprecated in prose with no warning
  anywhere.
- **Because:** this project's only deprecation mechanism is keyed by
  `api_version` (`DeprecatedAPIVersions`), and this is a *field*. Where a
  field-level warning surfaces — bundle load, `release verify`, every operator
  command — is a design decision with a real cost attached: a warning on every
  manifest load is a warning about a file the operator did not write and cannot
  change, which is how a project teaches people to ignore its warnings.
- **Class:** `spec-gap`.
- **Consequence:** decision 9's cost — *"two spellings to maintain until a named
  removal release"* — is running with no clock on it and no signal to a vendor
  that one is running.
- **Proposed row:** none yet; the mechanism has to be chosen before a row can say
  anything. Carried.

## Decision-row outcomes

**Ruled 2026-08-16, before any code was written — which is the sequence wave
26's own log recorded itself for getting wrong.** Three questions, three
rulings, and two of them decided what the wave was rather than what it built.

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | 13 | **Accepted** | `LOCKED` | `installation import` refuses an export whose runtime this manager does not drive, before anything is created | D-015 |
| 0023 | — | **Deferred** | — | `RuntimeSpec.Project` is its own unit of work; the finding is logged and the ABI decision is not taken here | D-016 |
| 0023 | — | **Deferred** | — | The deprecation of `runtime:` waits on a field-level mechanism being chosen | D-017 |
| 0023 | 14 | **Outstanding** | `ASSUMED` | Where the manager would state which runtime a machine uses, it asks the adapter through an optional capability | D-014 |

**Three alternatives were declined and are recorded here, since nothing else
carries a refusal.** Importing with a warning instead of a refusal — refused
because the warning is read mid-incident by somebody who then runs `apply`.
Doing the `project` work inside this wave — refused because a published-ABI
change with a doctor check attached is an ABI change nobody reviewed as one.
Warning about `runtime:` on every manifest load — refused because it is a
warning about a file the operator did not write and cannot change.

## Self-audit — 2026-08-16

Scope: the whole branch, four commits, ~520 lines across 12 files, code, tests,
docs and records. `just ci` green at **86.4%** (floor 84), `just
coverage-union` **87.6%** (floor 86), `just runtime-check` **18 mentions / 0
branches** — unchanged, because closing this leak removed no *name*, which is
the whole point of D-014. Full acceptance, all three demo lanes, `just
test-docker` green on the first run.

**Sabotage sweep: 12 mutations, 12 killed — one only after being made
killable.**

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Major | Dropping `compose` from the compose adapter's tool list **survived**. Everything above the adapter sees that list through the fake, which carries its own copy, so the shipped adapter's answer was asserted by nothing. | Fixed — the adapter's own test, plus a test that every tool it names has a probe in the catalogue |
| A-2 | Major | `git checkout -- doctor.go`, undoing a mutation, restored the file to HEAD and **ate an uncommitted fix** — and the commit that followed claimed the fix as done. The comment on `drivesRuntime` said one implementation served three callers while `doctor` still had its own copy. | Fixed and recorded in the commit that restored it |
| A-3 | Medium | The import refusal raised an installation error, the kind the same comparison uses mid-operation, while the schema-from-the-future refusal on the same path exits 9. Two exit codes for "this document needs a different manager". | Fixed — incompatible, with the exit code asserted |
| A-4 | Low | `TestDoctorAsksTheRuntimeWhichToolsInitWillNeed` asserted against the fake's own list, so a fake advertising nothing would have passed by never entering the loop. | Fixed — the list is required non-empty first |

**A-1 is the one to carry.** It is the injected-seam failure this project has
already recorded once, arriving from the other side: not a field every test
sets, but a *fake every test uses*, holding its own copy of an answer the
production adapter gives. The sweep found it and coverage would not have —
`RequiredTools` reported 100% covered on the compose adapter, because the
adapter's method *is* executed, by nothing that checks what it returns.

**What remains distrusted.**

- **No adapter has ever declined the capability in production.** The decline path
  is exercised by a wrapper type in a test, which is the honest way to model it
  and is still a model.
- **`runtime.declared` is registered only when a runtime is wired**, so a manager
  with no adapter reports nothing rather than reporting that. Consistent with the
  volume checks beside it, and untested.
- **The fake reports `docker` and `compose` whatever name it is given**, so a
  test can construct a runtime called `quadlet` that asks for Compose's tools.
  No test does; nothing stops one.

## Rules distilled

- **A fake that answers for an adapter is a second implementation of the
  answer.** If nothing compares the two, the sweep tests the fake and the
  shipped code is unmeasured — and coverage will still say 100%, because the
  method ran. (A-1)
- **`git checkout -- <file>` during a sabotage sweep restores to HEAD, and HEAD
  is not where you are.** The rule was recorded once against the tests a sweep
  demands; it applies to every uncommitted edit, and the second violation
  produced a commit message that was false. (A-2)
- **Two refusals about the same fact must exit with the same code.** An operator
  scripting against one of them has no way to learn there is a second. (A-3)
- **A leak with no name in it cannot be renamed away.** What moves is the
  decision, not the word — and that is why a vocabulary checker structurally
  cannot find these and a reader must. (D-014)
- **A removed key takes its meaning with it.** Deleting per-runtime key names
  deleted `project` from the new spelling, and the default that filled it in kept
  running — so the surface disappeared and the behaviour did not. (D-016)
- **A readiness gate that fires is a result, not an obstacle.** Three unsettled
  decisions cost three questions and one ruling each; the same three discovered
  inside the code would have cost a wave that had to be argued back out. (Wave
  27's plan)

## Carried into the next unit

- **`RuntimeSpec.Project` and the `COMPOSE_PROJECT` ABI** — D-016, the last of
  §2.2's unspelled leaks and the only item of P2 still open. It needs its own
  decision about whether a published ABI moves.
- **The field-deprecation gap** — D-017. `runtime:` is deprecated, nothing warns,
  and the mechanism has to be chosen first.
- **A named removal release for `runtime:`**, without which "deprecated" is a
  word in a doc.
- **`Installation.Providers`** — still declared, still unwritten, now with a
  field doing its apparent job beside it (D-011).
- ~~**The rest of P2**: `installation import`, `doctor`, and §14's two unspelled
  leaks.~~ Three of four shipped in this wave; the fourth is D-016 above.
