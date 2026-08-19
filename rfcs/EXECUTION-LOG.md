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
- **R-4: an option made explicit is not a change.** Comparing effective rather
  than declared options needs the adapter to resolve them, which is a capability
  and a decision row.
- **The acceptance script's `assert_running` has no settle window**, and flaked
  once on this PR. It counts running containers immediately after a stop.
- **The field-deprecation gap** — D-017. `runtime:` is deprecated, nothing warns,
  and the mechanism has to be chosen first.
- **A named removal release for `runtime:`**, without which "deprecated" is a
  word in a doc.
- **`Installation.Providers`** — still declared, still unwritten, now with a
  field doing its apparent job beside it (D-011).
- ~~**The rest of P2**: `installation import`, `doctor`, and §14's two unspelled
  leaks.~~ Three of four shipped in this wave; the fourth is D-016 above.

# Wave 28 · What the runtime is told, and what that names

Branch `feature/wave-28-runtime-options`. RFC 0023 P2, the last item —
`RuntimeSpec.Project`, §2.2's remaining unspelled leak — plus the hazard found
underneath it.

**Drift count: 0.** Nothing the RFC settled was built otherwise. One entry
(D-018) departs from §4.1's shape, and §4.1 never described this surface at all.

**The readiness gate fired again, and paid again.** "Close the last leak" needed
three decisions the RFC does not settle: what a project *is* under `runtimes:`,
what protects a running installation from a changed one, and what becomes of the
published hook variable. All three were put to the author before any code
existed and all three were ruled, so the wave had none open. Two waves running:
the gate has now cost six questions and saved two re-scoped branches.

## D-018 — Per-runtime options are opaque, and the adapter validates them

- **Touches:** RFC 0023 §4.1, decision 10 (`LOCKED`), decision 7 (`LOCKED`).
- **RFC said:** a runtime declares files, and (decision 10) one key name for all
  runtimes. Nothing about settings.
- **Built:** `runtimes.<name>.options`, a map the manager bounds in shape —
  identifier keys, single-line values, 200 characters — and never reads. The
  compose adapter reads `project` from it and refuses keys it does not know,
  from `Validate`.
- **Because:** decision 10 removed the per-runtime *key name*, and `project`
  left with it (D-016). Putting it back as a typed field would put one runtime's
  vocabulary in the shape every runtime shares, which is what decision 10 took
  `units:` out of. A map is the only form that survives a second runtime with a
  different answer to the same question.
- **Class:** `spec-gap`.
- **Consequence:** a manifest surface exists whose *meaning* the manager cannot
  check. An unknown key is refused only by the adapter, only from `Validate` —
  the path `apply`, `doctor` and `release verify --render-check` take, and not
  every path. A vendor who mistypes an option and never runs those sees nothing.
- **Deliberately not applied:** a uniform `project` key on the declaration, put
  to the author beside the map. Refused for the reason above. Also refused: no
  surface at all, which would have made adopting `runtimes:` impossible for any
  vendor whose project is not their product name without a volume migration.
- **Proposed row (RFC 0023, row 15):** `LOCKED` — **accepted 2026-08-16**.

## D-019 — The installation records what the runtime was told

- **Touches:** RFC 0023 decision 3 (`LOCKED`), RFC 0018 decision 1.
- **RFC said:** nothing. The runtime is immutable per installation; what the
  runtime was *told* is not mentioned anywhere.
- **Built:** `Installation.RuntimeOptions` at installation schema 10, and a
  release that changes any of them is refused — before the operation in `apply`,
  `update` and `rollback`, and again inside `runtimeConfig`, which no path
  bypasses.
- **Because:** the options name durable things. Measured on this host:
  `--project-name alpha` resolves a volume named `alpha_data` and `beta`
  resolves `beta_data`. So a changed project is a deployment pointed at storage
  nothing has ever written to, with the operator's data still on the disk and
  nothing referring to it — and nothing else in the manager would notice. The
  backup that runs next captures the new empty volumes; `doctor` reports them
  covered.
- **Class:** `discovery`. The hazard existed before `runtimes:` — a vendor
  editing `runtime.project` between two releases has always been able to do
  this — and only building the new spelling made it visible, because the
  documented migration performs it.
- **Consequence:** every option is treated as durable, including ones no runtime
  has heard of, because the manager cannot tell which are. Refusing a harmless
  change costs a message; permitting a harmful one costs the data. An
  installation created before schema 10 has no baseline and adopts what it is
  *currently running* on its next converge — never what a candidate release
  proposes, which would record the change as the baseline and defeat the check
  on the one operation that needs it.
- **Deliberately not applied:** warning instead of refusing, put to the author.
  Refused because an unattended apply has nobody reading the warning.
- **Proposed row (RFC 0023, row 16):** `LOCKED` — **accepted 2026-08-16**.

## D-020 — The hook ABI is two lists

- **Touches:** RFC 0023 §2.2, RFC 0007 §13 and its three-ABI table.
- **RFC said:** P1's inventory named the shape and left the decision to P2 —
  *"the variable stays for Compose installations and is absent under another
  runtime, which makes it a runtime-supplied variable rather than a core one"*.
- **Built:** exactly that. `ports.HookVarSupplier`; the compose adapter supplies
  `COMPOSE_PROJECT`; `ports.HookEnv` no longer has a field for it.
- **Because:** renaming was never available — the name is what every vendor hook
  already writes — and the core ABI was promising a value one runtime cannot
  mean.
- **Class:** `spec-gap`.
- **Consequence:** the hook ABI is no longer one list, which RFC 0007's gate
  assumed. `docs-check` gained `checkRuntimeHookVars`, verified by perturbation:
  renaming the documented variable fails the build. Without it this fix would
  have created the ungated ABI RFC 0007 §13 built these gates to end.
- **Proposed row (RFC 0023, row 17):** `LOCKED` — **accepted 2026-08-16**.

## Decision-row outcomes

**Ruled 2026-08-16, before any code was written.** Three questions, three
accepted, each carrying a declined alternative.

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | 15 | **Accepted** | `LOCKED` | Per-runtime settings are an opaque `options` map; the adapter validates them | D-018 |
| 0023 | 16 | **Accepted** | `LOCKED` | The installation records the options it was created with; a release that changes them is refused | D-019 |
| 0023 | 17 | **Accepted** | `LOCKED` | `COMPOSE_PROJECT` is supplied by the runtime, not promised by the core ABI | D-020 |

**Row 14 is still outstanding** — wave 27 proposed "where the manager would
state which runtime a machine uses, it asks the adapter", and this wave is three
more instances of it: the project, the option vocabulary, and the hook variable.
It has not been put again, because a proposal repeated louder is still one
proposal.

**Three alternatives were declined and are recorded here, since nothing else
carries a refusal.** A uniform `project` key on the declaration. Warning instead
of refusing on a changed option. And documenting the migration without changing
the manager, which would have left the pre-existing half of the hazard — a
vendor editing `runtime.project` — exactly as it was.

## Self-audit — 2026-08-16

Scope: the whole branch, code, tests, docs, schemas and records. `just ci`
green at **86.5%** (floor 84), full acceptance, all three demo lanes, and
`docs-check` at 41 pages / **55** checks — one more than before, because the
runtime half of the hook ABI needed a gate of its own. `runtime-check` **17
mentions (7 port-shaped, 2 compose-shaped, 8 catalogue), 0 branches** — down
from 18/3, and the fall is the point: an inventory that only grows is a list
nobody has to shrink.

**Two lanes are reported rather than claimed.** `just test-docker` has one
failing test and `just coverage-union` cannot finish, both for the same reason
and neither for this branch's: another project's development server on this
host holds `127.0.0.1:18443`, which `TestTheStatementCarriesNamesAndTheBound
AndNoValues` needs. Every other test in the container lane passes, and the five
update tests that failed on an earlier run — port 18080, held at the time by a
different process — pass in isolation now that it is free. Killing somebody
else's process to make a lane green is not a way to make a lane green.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Major | `apply --dry-run` on an installation created before schema 10 **wrote the baseline** and reported that it had changed nothing. A plan that writes state is the one thing a plan may not be. | Fixed — adoption skipped on a dry run, asserted |
| A-2 | Major | The refusal fired inside a step, so the engine compensated and the operation exited 11 ("back where it started") — burying the exit code and the remedy in a record. A precondition reported as a step failure is a precondition nobody can act on. | Fixed — asked before the operation in all three converge paths; the in-step check stays as the unbypassable one |
| A-3 | Medium | The first version documented that a `project:` left beside `runtimes:` would be *ignored*. That is the hazard with a comment on it. | Fixed — refused, naming where the value goes |
| A-4 | Low | The end-to-end test edited the release in place, which trips the digest guard first, so it was asserting a different refusal than it claimed. | Fixed — the disagreement is driven from the state side, and the comment says why |
| A-6 | Medium | Two files behind the `docker` build tag still used the removed `RuntimeConfig.Project`, and every untagged lane was green. | Fixed — both updated; the tagged build is now part of the pass |
| A-5 | Major | **Deleting the update path's adoption killed nothing.** The whole path a vendor actually ships a rename through — a new bundle, arriving by `update` — was untested, and so was the ordering that makes it work: adopting from the *candidate* would record the change as the baseline and refuse nothing. | Fixed — an update that renames is refused end to end, and both mutations now fail it |

**Sabotage sweep: 11 mutations, 11 killed — one only after being made
killable — plus the new `docs-check` gate verified by perturbation.**

**A-1 and A-2 are the same kind of finding**: both are about *where* a correct
check runs rather than whether it is correct. Neither would have been found by
reading the diff, and both were found by tests written to assert the behaviour a
user sees.

**What remains distrusted.**

- **An unknown option is refused only from `Validate`.** Paths that never call
  it carry the typo silently until one does.
- **No second adapter has ever supplied a hook variable or declined an option**,
  so both halves of the new capability are exercised against one implementation
  and a fake modelled on it.
- **The adoption of a baseline is a state write on a converge path.** It is
  skipped on a dry run and idempotent afterwards, and it is still a write that
  did not happen before this wave.
- **A docker-tagged file went uncompiled by every untagged build.**
  `test/dockerlab` and one `_docker_test.go` still referenced the removed field
  and `just ci` was green throughout; only the container lane found them. Any
  build-tagged tree is invisible to the fast loop.

## Review findings — 2026-08-16

Five findings on PR #52, four valid and one acknowledged out of scope. Three of
the four were the same seam: the baseline was derived and written in one step.

| # | Severity | Finding | Status |
|---|---|---|---|
| R-1 | Critical | An update whose current release could not be resolved **skipped the derivation silently**, so the baseline stayed nil, read as "created before schema 10", and waved the rename through. On the update path — which is how a vendor actually ships one. | Fixed — the resolution error is returned; reproduced red first |
| R-2 | Major | A rollback `--dry-run` skipped the derivation along with the write, so the plan accepted a target the operation would then refuse. | Fixed — derivation is pure, and the plan compares what the run compares |
| R-3 | Major | The baseline write ran **before the deployment lock**, so it could put back fields a concurrent `config set` had just changed. | Fixed — a read-modify-write under the lock that yields to whatever it finds |
| R-4 | Major | An installation created without an explicit project records `{}`; a later release that makes the *same* value explicit is refused as a change, though the namespace is identical. | Acknowledged, out of scope — see below |
| R-5 | Minor | `rfcs/INDEX.md` named `COMPOSE_PROJECT` where the variable is `<PRODUCT>_COMPOSE_PROJECT`. | Fixed |

**R-1, R-2 and R-3 are one defect wearing three hats**, and the shape is worth
keeping: *deriving* a value and *recording* it are different acts with different
rules. Derivation must happen on every path, including a plan; recording must
happen on none of the read paths, and only under the lock. Fusing them made each
rule break the other — the dry-run exception took the derivation with it, the
write escaped the lock because the derivation had to happen early, and the path
that could not derive silently recorded nothing at all.

**R-4 is real and is not fixed here.** Comparing *effective* options rather than
declared ones means asking the adapter to resolve them — only it knows that
`project` falls back to the product name — which is a fourth capability on a
port whose surface this wave has already grown twice, and a decision row nobody
has ruled. The refusal is in the safe direction, names the key, and a vendor can
clear it by leaving the redundant value out. Carried.

**One process failure, recorded rather than fixed quietly.** `git checkout --`
during the sweep ate the three uncommitted fixes for R-1 to R-3, and they had to
be written twice. That is the third time this project's own rule — *commit
before you sabotage* — has been broken by the same command, in the same way.

**One CI flake, not this branch's.** `Acceptance (real Docker)` failed on
`assert_running 0` immediately after `docker compose -p demo stop` reported both
containers stopped; a re-run of the same commit passed, and the same script
passes locally. The assertion has no settle window between the stop and the
count. Carried as a fragility rather than fixed here.

## Rules distilled

- **A key removed from a shape takes its meaning with it, and the default that
  filled it keeps running.** Deleting per-runtime key names deleted `project`
  from the new spelling while `ApplyDefaults` still supplied one — so the
  surface disappeared and the behaviour did not. (D-016 → D-018)
- **A documented migration is code.** "Move the files and delete the block"
  executed exactly as written renames every volume a deployment owns; nothing in
  the manager was wrong, and the instruction was. (D-019)
- **A precondition discovered inside a step is reported as a step failure.**
  Compensation rewrites the outcome, and the exit code an operator scripts
  against becomes "nothing happened". Ask before the operation; keep the
  in-operation check for the paths that bypass the door. (A-2)
- **A build tag hides a compile error from every lane that does not set it.**
  Two files referencing a removed field survived `go build ./...`, `go vet` and
  `just ci`; the fast loop cannot see a tagged tree, so the tagged build is its
  own check. (A-6)
- **A plan must be audited for writes, not just for output.** The adoption was
  correct, idempotent and invisible — and it happened during `--dry-run`. (A-1)
- **The path a defect actually arrives by is the one to test.** Every refusal
  here was covered from the `apply` side, and the sweep found the `update` side
  — a new bundle from a vendor — carrying no test at all. That is how the
  hazard reaches a real machine. (A-5)
- **When a fix moves an ABI, check whether it moved out of a gate.** Making the
  hook variable runtime-supplied would have silently created a second,
  undocumented ABI beside the one RFC 0007 built gates for. (D-020)

## Carried into the next unit

- **The field-deprecation gap** — D-017. `runtime:` is deprecated, nothing warns,
  and the mechanism has to be chosen first. This is now the only thing between
  P2 and complete.
- **A named removal release for `runtime:`**, without which "deprecated" is a
  word in a doc.
- **Row 14, outstanding since wave 27** — the generalisation this wave supplied
  three more instances of.
- **`Installation.Providers`** — still declared, still unwritten (D-011).
- **P1b item 4** — the `EnvironmentFile`-on-tmpfs measurement, still behind a
  bootable venue, and now the only thing before **P3, the Quadlet adapter**.
- ~~**`RuntimeSpec.Project` and the `COMPOSE_PROJECT` ABI**~~ — closed by this
  wave.

# Wave 29 · Deprecating a field

Branch `feature/wave-29-deprecating-a-field`. RFC 0023 P2's last item — D-017,
the field-deprecation gap — and the second thing carried with it, a named
removal release for `runtime:`.

**Drift count: 0.** Nothing the RFC settled was built otherwise. Decision 9 said
`runtime:` "stays readable and deprecated"; it stays readable, and it is now
deprecated in the software rather than in a document.

**The readiness gate fired for the third wave running**, and for the third time
it was the whole of the wave's design. "Close the deprecation gap" needed three
decisions the RFC does not settle: when `runtime:` stops being read, whether the
project's own scaffold keeps writing it, and where a field-level warning is
allowed to appear. All three were put to the author before any code existed. Two
came back as recommended and one did not — the removal release is **0.4.0**, not
the 1.0.0 that was proposed — which is the argument for asking rather than
assuming: the wave would otherwise have shipped a date nobody chose, written
into a warning vendors are meant to plan against.

## D-021 — A field deprecation names its removal release and warns in three places

- **Touches:** RFC 0023 decision 9 (`LOCKED`), RFC 0018 decision 1.
- **RFC said:** `runtime:` "stays readable and deprecated", with the cost
  recorded as "two spellings to maintain until a named removal release". The
  release was never named and nothing warned (D-017).
- **Built:** `domain.FieldRemovalRelease = "0.4.0"` and
  `Manifest.DeprecatedFields()`, surfaced by `release verify`, `init` and
  `update` and by nothing else.
- **Because:** the only deprecation mechanism this project had is keyed by
  `api_version`, and a field cannot be a map key — a field is deprecated by
  being written at all, which only the manifest can answer. The three surfaces
  are the moments somebody can act: a vendor before publishing, an operator
  while choosing. Every other command meets the same manifest again with no
  choice available, and `release.Load` already refuses to be that place for the
  api_version warning, in prose, for this reason.
- **Class:** `spec-gap`.
- **Consequence:** a manifest surface now carries an expiry date that the
  software states. An operator who never runs `init` or `update` between now and
  0.4.0 is never told — which is the accepted cost of not warning on every load,
  and is bounded by the fact that 0.4.0 refuses at `update` rather than breaking
  a running deployment.
- **Deliberately not applied:** a `doctor` check, put to the author. Refused
  because every installation that exists runs a `runtime:` bundle, so the check
  would warn on every machine, permanently, about a file the operator cannot
  change — which is how a project teaches people to ignore `doctor`.
- **Proposed row (RFC 0023, row 18):** `LOCKED`.

## D-022 — The scaffold writes the current spelling and declares the manager it needs

- **Touches:** RFC 0023 decisions 8–10 and 15 (`LOCKED`), RFC 0013 §5.5, RFC
  0018 decision 1.
- **RFC said:** nothing about the scaffold. `runtimes:` was specified; that
  `morzer release new` still emitted `runtime:` was noticed by nobody.
- **Built:** the scaffold emits `runtimes.compose` with `project` under
  `options`, and stamps `compatibility.min_manager_version: 0.3.0`. The
  authoring tutorial, which taught the deprecated block, teaches the current
  one.
- **Because:** a project that warns about a field its own scaffold writes has
  deprecated nothing. The floor is not bookkeeping beside it: `runtimes:` is an
  unknown field to every released manager, and under strict decoding an unknown
  field refuses the whole manifest — so without the floor a vendor's customer is
  told about a typo instead of an upgrade requirement, which is precisely the
  failure RFC 0018 decision 1 exists to convert.
- **Class:** `spec-gap`.
- **Consequence:** a bundle scaffolded today cannot be installed by any released
  manager, because the manager it needs is not released. That is correct and it
  is also new: `release new` previously produced something 0.1.0 could install.
- **Proposed row (RFC 0023, row 19):** `LOCKED`.

## D-023 — A manager built between tags cannot be compared against a version floor

- **Touches:** RFC 0018 decision 1. **Against an earlier unit, found by this
  one.**
- **RFC said:** a lenient preamble reads `min_manager_version` before strict
  decoding, so "a future manifest field is a legible upgrade requirement rather
  than a report about a typo". It says nothing about what the manager's own
  version is.
- **Built:** `checkManagerVersion` declines the comparison when the running
  manager's version carries a prerelease.
- **Because:** the version is `git describe --tags`, which derives from the
  *last* tag — so the build that first understands a new field reports itself as
  a prerelease of the release *before* the one that ships it. Measured on this
  tree: it added `runtimes:` and calls itself `0.2.0-9-g8c5a81c`, which semver
  orders below the `0.3.0` floor its own scaffold writes. The shipped binary
  therefore refused the bundle `morzer release new` had just written, reporting
  it as "a bug in the scaffold", and `release verify` exited 9.
- **Class:** `spec-gap`. The mechanism was correct for every case that existed
  when it was designed, because until this wave nothing in the tree declared a
  floor at all.
- **Consequence:** a developer on an untagged build gets the strict decoder's
  unknown-field error rather than the clearer one. That is the trade the
  function already makes for every other question it cannot answer honestly, and
  it is the right side of it: the alternative refuses a manifest this build can
  in fact read.
- **Proposed row (RFC 0018):** the floor is not enforced against a manager whose
  own version is a prerelease. **Not yet ruled** — proposed here and nowhere
  else.

## Decision-row outcomes

**Ruled 2026-08-17, before any code was written.** Three questions; two
accepted as recommended and one decided against the recommendation.

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | 18 | **Accepted** | `LOCKED` | `runtime:` stops being read in 0.4.0; the warning appears at `release verify`, `init` and `update` and nowhere else | D-021 |
| 0023 | 19 | **Accepted** | `LOCKED` | `release new` writes `runtimes:` and stamps the `min_manager_version` it needs | D-022 |
| 0018 | — | **Proposed** | — | A version floor is not enforced against a manager whose own version is a prerelease | D-023 |

**The recommendation that lost.** 1.0.0 was proposed for the removal, on the
argument that the deprecated spelling should go before the release that freezes
the surface. **0.4.0 was chosen.** Recorded because nothing else carries a
refusal, and because the shorter clock changes what the next wave owes: the
removal is two minors away rather than an era away.

**Two alternatives were declined.** A `doctor` check for the deprecation, and
leaving the removal release unnamed while warning anyway.

**Row 14 is still outstanding**, and was not put again — a proposal repeated
louder is still one proposal.

## Self-audit — 2026-08-17

Scope: the whole branch — the domain surface, three call sites, the scaffold,
two documentation pages, and the tests. `just ci` green at **86.5%** (floor 84),
`docs-check` 41 pages / 55 checks, `runtime-check` **17 mentions, 0 branches**,
unchanged: this wave added no runtime vocabulary above the adapters.

**Sabotage sweep: 22 mutations, 22 killed — three only after being made
killable, and five added by the review rounds.** Full acceptance passed. The container lane failed once on
`TestTCPProbeAgainstRedis` and passed on a re-run; it is a settle-window
fragility of that test rather than anything this branch touches — see *Carried*.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Major | **The shipped binary refused its own scaffold.** `morzer release new` reported "a bug in the scaffold" and `release verify` exited 9, because the floor the scaffold stamps is above the version an untagged build reports for itself. | Fixed — D-023, verified red |
| A-2 | Major | **Deleting `min_manager_version` from the scaffold killed nothing.** The half of the ruling the question was actually about had no test, so the floor could have been dropped by any later edit in silence. | Fixed — the floor is asserted from the scaffold's real output, and so is the refusal an older released manager gets |
| A-3 | Medium | Every test left `managerVersion` at zero, and zero is the one value that skips the comparison — so the entire floor mechanism was inert under `go test` while broken in the binary. This is why A-1 survived to the point of being measured by hand. | Fixed — the new tests drive the loader with a non-zero version, in three positions relative to the floor |
| A-4 | Low | `init --dry-run` reports a plan for a bundle it never opens: the product name and domain in its closing line come out empty, and the deprecation warning cannot reach it. Pre-existing, and this wave is what made it visible. | Open — recorded below and carried; the fix moves init's fetch out of the operation |

**A-1 and A-2 are one finding seen from two ends.** The sweep found that nothing
held the floor; running the real binary found that the floor was wrong. Neither
half was visible from the diff, and the sweep alone would have produced a test
pinning the broken behaviour.

**What remains distrusted.**

- **The removal itself is unbuilt.** 0.4.0 is a string in a warning and a date
  in a document; nothing fails when it arrives, and nothing tests what happens
  the day `runtime:` stops being read.
- **A scaffolded bundle cannot be installed by any released manager**, because
  the manager it declares does not exist yet. Correct, and untested end to end —
  there is no released 0.3.0 to test against.
- **`git describe` versions understate by construction**, and the fix here
  declines rather than corrects. A tagged 0.3.0 satisfies the floor; a build one
  commit later does not, and now silently skips the check instead.
- **`init --dry-run` does not warn**, and cannot. The warning is published from
  `stepStageRelease`, and the engine returns from its plan branch before any
  step runs — so an init plan says nothing about a deprecated bundle while an
  update plan does. `update` can warn because `resolveUpdateTarget` materialises
  the bundle *before* the engine; `init` fetches inside the step. Making the two
  agree means moving init's fetch out of the operation, which changes what a
  plan does on the network and is not this wave's to do.

**One process failure, recorded rather than fixed quietly.** `git checkout --`
ate the D-023 fix during the verify-red step — the **fifth** time this command
has destroyed uncommitted work on this project, and the second time in three
waves. "Commit before you sabotage" was followed for the sweep and then broken
by a fix written *after* the commit. The sweep now restores by writing back the
file's saved contents rather than by asking git what HEAD says.

## Review findings — 2026-08-17

One finding on PR #53, and it was valid.

| # | Severity | Finding | Status |
|---|---|---|---|
| R-2 | Medium | Codecov named `warnDeprecations` at 44% patch, and the uncovered lines were the **api_version branch** — dead in every test because `DeprecatedAPIVersions` is empty, on this path and on the one the code was moved from. A detection branch nothing runs is one nobody knows works, and this one only ever runs on the day it matters. | Fixed — the branch is driven by injecting a stale version, as `manifest_test.go` already does; both it and the nil-bus guard are sabotaged and killed |
| R-3 | Minor | `TestBothKindsOfDeprecationAreReported` asserted only that **two** warnings were published, which an implementation emitting the same warning twice also satisfies — the test's name claimed more than its assertion checked. | Fixed — both warnings are named; the mutation that published the api_version one twice is now killed |
| R-4 | Minor | The api_version tests overwrote a package-global map entry and deleted it unconditionally, so a pre-existing entry would be destroyed on the way out. | Fixed — a helper saves and restores. Not reproducible today, and deliberately so: the map is empty by construction, and "the map is empty" is a property of production code the test should not depend on |
| R-1 | Major | The untagged-build exemption (D-023) was written as **"any prerelease"**, which also exempts a deliberately versioned one — `0.2.0-rc.1` really is older than a 0.3.0 floor. The comment justifying it claimed the strict decode would still refuse such a bundle; that holds only when the floor stands in for an unknown field. A vendor may raise it for a *behavioural* reason, and then the manifest parses on the old manager and this check is the only thing refusing it. | Fixed — the exemption matches the shape `git describe` produces and nothing else; reproduced red first |

**R-1 is a defect in the reasoning rather than in the code**, which is the kind
worth writing down. D-023 was found by measuring the real binary, and the fix
was written against the one case measurement had produced. "Any prerelease"
covered that case and a second one nobody had asked about — and the comment
beside it asserted a safety property that was true of the measured case only.
The narrow shape was in the reviewer's first suggestion.

## Rules distilled

- **A fix written from one measurement generalises to exactly one case.** The
  untagged-build exemption was correct for the build that produced it and wrong
  for `0.2.0-rc.1`, because "the stamp understates" and "this build is older"
  wear the same syntax. Name the shape you measured, not the category it is
  in. (R-1)
- **A deprecation with no removal release is a complaint.** "Deprecated" tells a
  vendor something will happen; only a version tells them when, and the warning
  has nowhere to put a date the project never chose. (D-021)
- **Warn where the reader can still act, and nowhere else.** A vendor before
  publishing and an operator while choosing a bundle can both do something; the
  same manifest met on every later command cannot be changed by anybody reading
  the message. (D-021)
- **A project that scaffolds the field it deprecates has deprecated nothing** —
  and the assertion that keeps it honest is that the scaffold's own output
  produces no warning, not that the template looks right. (D-022)
- **A version derived from the last tag understates every build after it.**
  Anything comparing the manager's own version against a floor is comparing
  against a number that is wrong by one release for the entire development
  cycle. (D-023)
- **A check that is skipped by its zero value is inert in every test that does
  not set it.** `managerVersion` defaulted to zero, zero meant "decline", and a
  whole mechanism passed its tests while failing in the binary. Set the
  production value in at least one test, or the seam is the thing under test.
  (A-3)
- **`git checkout --` restores to HEAD, so it eats every fix written after the
  last commit.** Restore a mutated file from its own saved bytes; the sweep must
  not consult git at all. (fifth occurrence)
- **An assertion that something has gone away is a timeout, not a signal, and a
  timeout is a race with whatever else the machine is doing.** Two now in this
  project — a container count after `compose stop`, and a TCP probe after a
  shutdown — both green alone and both failing under a full lane. The published
  port outlives the process behind it. (carried from wave 28, second instance
  here)

## Carried into the next unit

- **The removal of `runtime:` in 0.4.0** — now a dated commitment rather than an
  open question, and nothing yet enforces the date.
- **Row 14, outstanding since wave 27.**
- **The RFC 0018 proposal from D-023**, unruled.
- **`Installation.Providers`** — still declared, still unwritten (D-011).
- **P1b item 4** — the `EnvironmentFile`-on-tmpfs measurement, still behind a
  bootable venue, and still the only thing before **P3, the Quadlet adapter**.
- **R-4 from wave 28** — an option made explicit with the adapter's own default
  is refused as a change; needs a resolve-options capability and a decision row.
- **The acceptance suite's `assert_running` has no settle window**, carried from
  wave 28 as a fragility — and **`TestTCPProbeAgainstRedis` is a second of the
  same kind**, found by this wave's container lane. It waits 30 seconds for a
  shut-down Redis to stop accepting connections and failed at 30.8s under a full
  lane, then passed alone in 0.6s. A published port keeps accepting while the
  proxy is torn down, so the probe is measuring Docker's teardown rather than
  the service. Neither is this branch's, and both are now two instances of one
  shape: an assertion about a service going away, with a timeout instead of a
  signal.
- **`init --dry-run` plans against a bundle it has not read** (A-4), which is why
  it cannot carry the deprecation warning and why its closing line names no
  product.
- ~~**The field-deprecation gap (D-017)**~~ — closed by this wave. **P2 is
  complete.**

# Wave 30 · The options as the runtime reads them

Branch `feature/wave-30-effective-runtime-options`. RFC 0023, against decisions
15 and 16. Closes R-4, carried from wave 28.

**Drift count: 0.** Nothing the RFC settled was built otherwise. One entry
(D-024), and it amends §6 rather than departing from a row — with the author's
ruling, before the code was written.

**The readiness gate did not fire.** Two load-bearing decisions rather than
three, so this was two questions and not a halt. Both were put before any code
existed and both came back as recommended. Worth recording that the gate not
firing is also a result: three waves of firing had made it the expected outcome,
and a practice whose alarm is always on is one nobody reads.

## D-024 — The comparison runs on resolved options, and §6 was measuring the wrong surface

- **Touches:** RFC 0023 decisions 15 and 16 (`LOCKED`), decision 7 (`LOCKED`),
  §6.
- **RFC said:** decision 16 refuses a release that changes a recorded option.
  It did not say *which* form of the option is compared, because until the
  adapter gained a default there was only one form.
- **Built:** `ports.OptionResolver`, an optional capability. Both the recorded
  baseline and the candidate go through it before they meet, so what is compared
  is what the runtime will read. A runtime that declines keeps the old
  declared-against-declared comparison.
- **Because:** an installation created with no `project` is already running
  under its product name, so a release writing that name out in full renames
  nothing — and the manager refused it, telling a vendor to restore a value that
  was never doing any work. The manager cannot see this alone: knowing that
  `project` falls back to the product is precisely the knowledge decision 7
  keeps out of these layers.
- **Class:** `spec-gap`.
- **Consequence:** the recorded baseline stays as the vendor declared it, so no
  schema bump, no migration, and `installation describe` publishes what it
  always did — at the cost that a future change to an adapter's default moves
  *both* sides of the comparison and a real rename would pass unnoticed. And the
  port gained its eighth optional capability, which is the second half of this
  entry.
- **Deliberately not applied:** recording the *effective* options at schema 11,
  put to the author. Refused because the migration cannot run where the state
  package lives — `internal/infra/state` has no adapter to ask — and because it
  changes a published artifact.
- **The second half.** §6's escape hatch fires when "the second adapter forces
  more than two new methods onto `ports.Runtime`". Measured while adding this
  one: **13 core methods and 8 optional capabilities**, against 12 and 5 when §6
  was written. One of its two spare method slots is spent (`Name()`, P2) and
  every other growth has gone where the instrument does not look. Amended to
  count both halves, with today's numbers as the baseline P3 is measured
  against. The condition is unchanged and still forward-looking — *what the
  second adapter forces* — so recording a larger surface does not fire the
  hatch; it makes it able to fire.
- **Proposed row (RFC 0023, row 20):** `LOCKED`. **Amendment to §6**, recorded
  in §13.

## Decision-row outcomes

**Ruled 2026-08-17, before any code was written.** Two questions, both accepted
as recommended.

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | 20 | **Accepted** | `LOCKED` | The comparison runs on resolved options; the installation keeps recording what was declared | D-024 |
| 0023 | §6 | **Accepted** | — | The escape hatch counts core methods and optional capabilities alike | D-024 |

**One alternative was declined:** recording effective options at installation
schema 11.

**Row 14 is still outstanding**, and was not put again for the third wave
running. It has now been carried longer than it took to build everything it
generalises, which is itself the argument for either putting it or dropping it.

## Self-audit — 2026-08-17

Scope: the whole branch — the port, the adapter, the comparison, the fake, the
shared battery, docs and records. `just ci` green at **86.5%** (floor 84),
`docs-check` 41 pages / 55 checks, `runtime-check` **17 mentions, 0 branches**,
unchanged: this wave added no runtime vocabulary above the adapters. Full
acceptance passed and the container lane passed, each run on its own.

**Sabotage sweep: 10 mutations, 10 killed — two only after being made
compilable, and one only after being made killable.**

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Major | **An adapter that resolved options in place broke no test.** The map handed to `ResolveOptions` is the installation's own recorded baseline, and `persistRuntimeBaseline` writes that map back — so a resolving *read* could have declared an adapter's default onto disk, turning a deployment that named no project into one that names its own default. The contract battery asserts non-mutation, but its real-adapter leg is behind the `docker` build tag and no untagged lane ran it. | Fixed — asserted in the adapter's own untagged test and at the ops boundary, where it holds for any runtime rather than this one |

**One process failure, recorded rather than hidden.** Acceptance was started
while the container lane was still running, and both failed — "cannot start
services" and "cannot restart services", one step apart in the same daemon. This
project already knows those two lanes cannot share Docker; running them
concurrently to save wall-clock is how that knowledge gets rediscovered. Both
pass run sequentially, and the first two runs are reported here rather than
quietly replaced by the second.

**What remains distrusted.**

- **The recorded baseline is what the vendor declared**, by decision. If an
  adapter ever changes a default, `resolve(recorded)` moves with it and both
  sides of the comparison shift together — a real rename would pass unnoticed.
  This is the accepted cost of not bumping the schema, and nothing detects it.
- **One implementation.** Only Compose implements `OptionResolver`; the decline
  path is exercised by a wrapper that hides the capability, not by a second
  adapter that genuinely has no defaults.
- **A pure capability's conformance is gated behind Docker.** `ResolveOptions`
  needs no daemon, but the battery's real-adapter leg is docker-tagged as a
  whole, so the fast loop cannot see it. That is precisely why A-1 survived.

## Rules distilled

- **A comparison between what somebody typed and what the machine does needs a
  translator, and only one layer has it.** Declared and effective are different
  values, and the layer holding the record is deliberately the one that cannot
  tell them apart. (D-024)
- **An instrument that measures one half of a surface reports health while the
  other half grows.** §6 counted methods; seven of the eight things added to the
  port since were capabilities. Ask what a threshold *cannot* see before
  trusting that it has not been crossed. (D-024)
- **A fake that duplicates an adapter's rule needs a battery that asks both the
  same question.** The lifecycle layer may not import an adapter, so its tests
  run against a copy of the rule — and a copy that drifts makes those tests
  agree with a manager that refuses the wrong releases. (D-024)

## Carried into the next unit

- **The §6 baseline, now recorded**: 13 core methods and 8 optional
  capabilities. P3 is measured against it, and the hatch can finally fire.
- **Row 14, outstanding since wave 27** — not put again for the third wave
  running. It has now been carried longer than it took to build everything it
  generalises, which is the argument for either putting it or dropping it.
- **The RFC 0018 proposal from wave 29's D-023**, still unruled.
- **The removal of `runtime:` in 0.4.0** — a dated commitment nothing enforces.
- **`Installation.Providers`** — still declared, still unwritten (D-011).
- **P1b item 4** — the `EnvironmentFile`-on-tmpfs measurement, still behind a
  bootable venue, and still the only thing before **P3, the Quadlet adapter**.
- **Two settle-window fragilities**, carried from waves 28 and 29: the
  acceptance suite's `assert_running`, and `TestTCPProbeAgainstRedis`.
- **`init --dry-run` plans against a bundle it has not read** (wave 29 A-4).
- ~~**The contract battery cannot exercise a pure capability without Docker**~~ —
  closed in review, see D-025.
- ~~**R-4, an option made explicit with the adapter's own default**~~ — closed by
  this wave.

## Review round, PR #54 — appended after the group closed

The sections above were written when the branch was pushed. What follows was
found afterwards, by review, and is appended rather than folded in: an entry
moved up into the execution record would claim the wave noticed it, and the
wave did not.

**Drift count: still 0** — nothing here was settled by the RFC and built
otherwise. Both findings are gaps the RFC never covered.

## D-025 — A boundary that trusted its adapters, found in review

- **Touches:** RFC 0023 decision 20, PR #54 review round 2
- **RFC said:** nothing; the port documents the rule and the battery checks it
- **Built:** `resolveRuntimeOptions` now hands the adapter a copy, and the
  option half of the contract battery runs untagged against the real adapter
- **Because:** two findings, both correct, both against code this wave added:
  - `checkRuntimeOptions` passed `inst.RuntimeOptions` to the resolver
    directly. Every resolver in this repository copies before it writes, so
    the boundary looked correct under every test that used one — a test of the
    adapters wearing the shape of a test of the boundary. Reproduced with a
    resolver that writes in place: the installation's record gained a `project`
    it never declared, from a check that only asked a question, and
    `persistRuntimeBaseline` would have written it to disk.
  - `ResolveOptions` needs no daemon, but the real-adapter leg of the battery
    is behind `docker` wholesale, so the shared rule was only ever checked
    against the real adapter where Docker was present. This is the item this
    same wave recorded as carried; review found it independently, which is the
    argument for closing it now rather than later.
- **Class:** spec-gap — both were knowable before the code existed. The first
  is the sharper one: the wave's own sabotage sweep found the *adapter* could
  mutate and fixed it there, and stopped. It never asked what the layer would
  do if an adapter misbehaved anyway.
- **Consequence:** one map copied per comparison. The port now states the
  non-mutation rule and states that the manager does not rely on it.
- **Deliberately not applied:** the same review asked that row 20 be reopened —
  an adapter that changes its default between an installation's creation and
  its update moves both sides of the comparison together, and the volumes stay
  under the old default. That is real, it is the consequence row 20 already
  records, and it is the cost of the ruling that declined schema 11. It is not
  reversed in review. `TestADefaultThatChangesUnderAnInstallationIsNotDetected`
  now pins the permissive behaviour so the gap is a failing test the day
  somebody closes it, rather than a paragraph.

**Rule distilled:** *a contract the boundary cannot enforce is one every future
implementation can break* — and a suite whose only implementations are
well-behaved cannot tell a guarded boundary from a lucky one. (D-025)

# Wave 31 · Where the units live

Branch `feature/wave-31-where-the-units-live`. RFC 0030 row 3, and the
reconciliation backlog: RFC 0023 row 14, outstanding since wave 27.

**Drift count: 0.** Nothing the RFCs settled was built otherwise. Row 3 was
`OPEN` and is now answered; row 14 was a proposal and is now a row.

## D-026 — Row 3 answered by pricing the move, not by preferring a directory

- **Touches:** RFC 0030 row 3 (`OPEN` → answered), §8.4
- **RFC said:** open. The row named what `/usr/lib/systemd/system` would buy —
  masking, drop-in overrides — and what it would make urgent, but not what it
  would break on a machine that already exists.
- **Built:** the units stay in `/etc/systemd/system`, and the constant is now
  pinned by a test.
- **Because:** two costs the row never priced, found by reading the adapter
  rather than the document.
  - **The old copy keeps winning.** systemd loads `/etc/systemd/system` above
    `/usr/lib/systemd/system` — read on systemd 261, systemd.unit(5): files
    higher in the list override files of the same name lower down. Every
    existing machine has its units in `/etc`, so after a move the manager would
    write to `/usr/lib` and systemd would go on loading the `/etc` copy. Every
    later change would look applied and not be, and nothing in the RFC said who
    removes the old file.
  - **The move re-enables what the operator switched off.** `InstallUnits`
    decides freshness by the file's presence in the unit directory, and
    `EnableNew` enables only the fresh ones — row 1's guarantee. After a move no
    unit is in the new directory, so every unit is fresh and every unit is
    enabled, silently reversing every `systemctl disable` on the machine. Row 1's
    harm, arriving once per machine through a migration.
- **Class:** spec-gap. Both were knowable before any code existed; the row was
  written about what the directory *means* and never about what changing it
  does. An open question priced on only one side reads as balanced.
- **Consequence:** `systemctl mask` stays unavailable on a generated unit,
  permanently rather than pending. The cost is bounded by rows 1 and 4, which
  between them give the operator two ways to say "off" that work — so masking is
  a mechanism for an intent already expressible.
- **Deliberately not applied:** a `doctor` check that notices an attempted mask.
  It would fire on no healthy machine, and it could not detect the case anyway —
  a refused `mask` leaves nothing behind to find. §3's argument against permanent
  warnings applies with more force to a check that cannot see what it claims to.

## D-027 — The unit directory was a default nothing tested

- **Touches:** RFC 0030 row 3, `internal/adapters/supervisor/systemd`
- **RFC said:** nothing; the constant is implementation.
- **Built:** `TestGeneratedUnitsLiveWhereAnAdministratorsUnitsLive`.
- **Because:** measured before writing it — changing `UnitDir` to
  `/usr/lib/systemd/system` and running the **entire** suite passes. Every
  construction of the supervisor in every test injects `WithUnitDir`, because
  relocating the directory is exactly what makes the adapter runnable without
  root. The seam that makes the tests possible is the seam that hides the value
  production uses.
- **Class:** spec-gap.
- **Consequence:** the path is now a decided value with a guard, and the failure
  message says to change the RFC first.

## D-028 — A test that would write into the source tree if it regressed

- **Touches:** wave 30's D-025 work, found by wave 31
- **RFC said:** nothing.
- **Built:** `TestTheBaselineWriteStopsWhenItCannotReadTheInstallation` now sets
  `Paths` to a temp directory.
- **Because:** `saveInstallation` writes its report file to
  `d.Paths.InstallationFile()` *before* it reaches the state store, and a zero
  `Paths` resolves that to a relative path. The test set no `Paths` because the
  guard means it never gets that far — but during the sabotage run that removed
  the guard, it serialised a blank installation into this repository's own
  `internal/lifecycle/ops/` directory. Found because the file was still sitting
  untracked in the working tree a day later.
- **Class:** spec-gap. The fixture was specified by what the passing path needs,
  and the failing path is the one the test exists for.
- **Consequence:** the artifact was evidence as well as litter — it is what a
  corrupted record actually looks like: `schema_version: 0`, empty id, empty
  product, carrying the baseline that was being adopted.

## D-029 — A distinction with only one half tested

- **Touches:** `internal/adapters/supervisor/systemd`, found by wave 31's sweep
- **RFC said:** nothing.
- **Built:** `TestRemoveUnitsReportsAFailureThatIsNotAMissingFile`.
- **Because:** `os.Remove` has three outcomes here and the code treats each
  differently — the file went away (removed), it was already gone (tolerated),
  anything else (reported). Two were covered:
  `TestRemoveUnitsStopsAndDisablesBeforeDeleting` writes the unit and deletes it
  successfully, and `TestRemoveUnitsToleratesAUnitThatWasNeverInstalled` takes
  the missing-file branch. The third — a removal that fails for any other reason
  — had no test, so deleting the branch that reports it survived the sweep. The
  why is the finding: a three-way outcome tested twice looks fully covered, and
  the untested one is the only branch that carries an error.

  Corrected in review: the first pass of this entry said all three existing
  tests took the tolerant branch, which is wrong — one takes the success path
  and a third refuses the name before removal is reached. The conclusion held
  and the evidence for it did not, which is the more embarrassing of the two.
- **Class:** spec-gap.
- **Consequence:** a removal that genuinely fails now says so instead of
  reporting the unit gone. What a swallowed failure left behind is a unit file
  surviving the uninstall that claimed to remove it, which systemd goes on
  honouring — the same class as an old unit shadowing a new one (D-026), reached
  by a different route. The fixture is a non-empty directory standing where the
  unit file should be, which makes `os.Remove` fail for a reason that is not
  "not there", without root.

## D-030 — A row answered in three places and stale in three others

- **Touches:** RFC 0030 §5, §11, review round 1 of PR #55
- **RFC said:** row 3 is `OPEN`, in the decision table, in §5's preamble, in
  §11's phasing, in the status header, in the index entry and in the index row.
- **Built:** all six updated.
- **Because:** answering a row means editing the row, and a document repeats its
  own status wherever a reader might need it without scrolling. Review caught
  §5's preamble; re-grepping for the claim rather than trusting the fix caught
  §11's phasing, which no reviewer had flagged.
- **Class:** spec-gap — knowable, and knowable mechanically. The RFC's own
  redundancy is a feature for readers and a hazard for editors.
- **Consequence:** the check that finds this is grepping the *claim* after the
  change, not re-reading the diff. A diff shows what moved; only a search shows
  what should have moved and did not.

## Reconciliation — 2026-08-17

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0030 | 3 | **Accepted** | ✅ ANSWERED | Generated units stay in `/etc/systemd/system`; `systemctl mask` stays unavailable | D-026 |
| 0023 | 14 | **Accepted** | `ASSUMED` | Where the manager would state which runtime a machine uses, it asks the adapter; an optional capability is the mechanism | D-014 |

**RFC 0030 is complete.** Row 3 was its last open question, so answering it
closed the document: every row now carries an outcome. Its status, its index row
and its place in the live-design list were all updated in the same change, which
is the half of reconciliation that gets forgotten — a row answered in a table
while the header still says "in progress" is drift of the kind this log exists to
prevent, arriving in the document rather than in the code.

**Row 14 is closed after four waves.** It was proposed by wave 27 and carried by
28, 29 and 30, each of which added an instance rather than a reason. What settled
it was noticing that the three instances — `ToolRequirer`, `HookVarSupplier`,
`OptionResolver` — were reached independently and landed on the same mechanism,
which makes the row a record of a practice rather than the introduction of one.

**One alternative was declined:** moving the units to `/usr/lib/systemd/system`
to restore masking. Recorded here because a refusal is written down nowhere else,
and because the argument for it — the one the row itself makes — is good until
the migration is priced.

## Rules distilled

- **An open question priced on one side reads as balanced.** Row 3 named what
  moving would buy and never what it would break, and sat open for that reason
  rather than because the answer was hard. Price both sides before grading a row
  `OPEN`. (D-026)
- **A seam that makes a thing testable is a seam that hides what production
  uses.** Every test relocated the unit directory, which is what let them run
  without root — and left the real value unexercised by all of them. (D-027)
- **Grep the claim, not the diff.** A status repeated in six places is edited in
  three and stays wrong in the others; the diff looks complete because every
  line in it is right. (D-030)
- **A branch tested twice out of three reads as covered.** `os.Remove` is
  handled three ways and two had tests, so the only branch carrying an error was
  the one nobody had written. Count the outcomes, not the tests. (D-029)
- **A fixture is specified by the failing path, not the passing one.** A test
  whose guard holds never reaches the code that needed the fixture, so the
  omission only shows up the day the guard breaks. (D-028)
- **An artifact left in the working tree is evidence before it is litter.** The
  blank installation this repository was carrying is what the corruption looks
  like, and reading it was faster than reasoning about it. (D-028)

## Carried into the next unit

- ~~**Row 14, outstanding since wave 27**~~ — accepted this wave.
- **The RFC 0018 proposal from wave 29's D-023**, still unruled. Now the oldest
  outstanding proposal in this file.
- **The removal of `runtime:` in 0.4.0** — a dated commitment nothing enforces.
- **`Installation.Providers`** — still declared, still unwritten (D-011).
- **P1b item 4** — the `EnvironmentFile`-on-tmpfs measurement, still behind a
  bootable venue, and still the only thing before **P3, the Quadlet adapter**.
  Note P3 lands units of its own, and D-026 is now the precedent for where they
  go and why a move is expensive.
- **Two settle-window fragilities**, carried from waves 28 and 29: the
  acceptance suite's `assert_running`, and `TestTCPProbeAgainstRedis`.
- **`init --dry-run` plans against a bundle it has not read** (wave 29 A-4).
- **`saveInstallation` writes its report before the state store**, so a failed
  state write leaves a report that disagrees with it. Noticed while fixing D-028
  and not chased; it is a real ordering question, not a test artifact.

# Wave 32 · A plan that names what it plans

Branch `feature/wave-32-a-plan-that-names-what-it-plans`. RFC 0001 decision 12
applied to `init`, and the reconciliation backlog: RFC 0018's proposal from
wave 29's D-023.

**Drift count: 1** — D-031, against whichever wave first gave `init` a
`--dry-run`, found by this one. RFC 0001 decision 12 settled that a plan reads
the bundle at its **source**; `init --dry-run` read nothing at all.

## D-031 — `init --dry-run` planned against a bundle it never opened

- **Touches:** RFC 0001 decision 12, RFC 0002 §"Plan (`--dry-run`)"
- **RFC said:** `--dry-run` plans the convergence steps against the bundle at
  its source rather than its release-store destination, because nothing is
  staged during a plan.
- **Built (before this wave):** `init --dry-run` closed with this, captured
  rather than paraphrased — the trailing space is the evidence, so it is fenced
  rather than spanned:

  ```
  installation  created for 
  ```

  Two empty slots, and a creation claimed in the past tense printed directly
  beneath *this is a plan; nothing was changed*. In `--json`, `data.product`
  was `""`.
- **Because:** the summary read the installation out of engine state, and a plan
  runs no steps, so nothing had populated it.
- **Class:** `drift`. Decision 12 covered it and `init` was built otherwise —
  the only non-zero drift entry in this file, and it is recorded as such rather
  than softened into a gap. What makes it drift and not a gap: the rule existed,
  was written down, and applied to exactly this situation.
- **Consequence:** the product was never unknown. The CLI resolves it *before*
  the operation, from `--product` or from the manifest at the bundle's source,
  because every managed path derives from it — so it was already in `opts`. The
  fix reads what was in hand rather than adding a way to obtain it.
- **Measured, and what corrected the plan for this wave:** the same `--json`
  object reported `etc_dir` ending in `/etc/web` for the `web` bundle while
  `product` was empty. One value derived from the manifest and one blank, in one
  object — which is what proved the manifest had already been read and killed
  the assumption that this needed a way to read a manifest without staging.

## D-032 — A warning withheld from the only person still choosing

- **Touches:** RFC 0023 decision 18
- **RFC said:** the deprecation warns at `release verify`, `init` and `update` —
  the moments somebody can still act.
- **Built:** the warning lived inside `stepStageRelease` and read the *staged*
  copy, so `init --dry-run` — an operator deciding whether to install the bundle
  at all — was the one path that never carried it.
- **Because:** decision 18's whole argument is that the warning belongs where a
  choice is available. A plan is that moment in its purest form: nothing has
  been done yet and the operator is deciding.
- **Class:** `spec-gap`. The row named commands, and a plan is a mode rather
  than a command, so following it literally left the mode uncovered.
- **Deliberately not applied:** moving the warning out of the step for the real
  path too. There it reads the bundle *after* verification, and a bundle whose
  signature does not check out should not hand out advice about its fields on
  the way to being refused. A plan has no verified copy and is already a
  statement about the source, so the two paths read different copies for a
  stated reason rather than by accident.

## D-033 — Two verbs in one clause

- **Touches:** wave 29's D-021 work, found by this wave
- **Built:** `warnDeprecations` published `"this bundle uses " + f.Message()`,
  and `Message()` already opens with the field name — composing *this bundle
  uses `runtime` is deprecated and will stop being read in 0.4.0*.
- **Because:** the same `Message()` is printed bare by `release verify` and has
  always read correctly, which is what located the defect in the join rather
  than in the sentence. It shipped in wave 29 and is in that wave's own
  acceptance log verbatim, unread by its author.
- **Class:** `spec-gap`.
- **Consequence:** nothing parsed either string — no test, no script, no doc —
  which is why it survived. The summary line and the warning are both now
  asserted.

## Reconciliation — 2026-08-18

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0018 | 13 | **Accepted, narrowed** | `ASSUMED` | A version floor is not enforced against a manager stamped by `git describe` between tags (`<N>-g<sha>`, optionally `-dirty`); a deliberate prerelease such as `rc.1` stays subject to it | D-023 |

**The proposal could not be accepted as written, and that is the finding.**
D-023 proposed "the floor is not enforced against a manager whose own version is
a prerelease". True of the wave 29 implementation — and **PR #53's review killed
that form as too wide**, because `0.2.0-rc.1` is a prerelease that genuinely *is*
below a `0.3.0` floor. The code was narrowed to the exact `git describe` shape
two waves before this row was written into the RFC. Accepting it verbatim would
have made the document claim something wider than the code does.

## Self-audit — 2026-08-18

Scope: the whole branch — three commits, `init`'s summary and warning paths,
the RFC row, and the tests. Sabotage sweep of six mutations against the changed
surface.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Medium | Blanking `data.product` in the `--json` output killed no test. The text summary was asserted and the machine-readable field was not — and of the two, only the second is a contract. | Fixed — `TestAPlansJSONNamesTheProductAndNoInstallation`, re-run against the same mutation and killed |

**Sabotage sweep: 6 mutations, 6 killed** — one only after A-1 made it killable.

**Two lanes, and the first container run was red.** `TestTCPProbeAgainstRedis`
failed under the full lane and passed alone in 0.64s, which is the carried
fragility from wave 29 reproducing its own measurement (0.6s alone, 30.8s under
load). Nothing on this branch is near a health probe. Recorded rather than
replaced by the green re-run: it is now the **third** sighting of the shape, and
a fragility seen three times across three waves is a defect the project keeps
deciding not to fix.

## Review round, PR #56 — appended after the group closed

Four findings, all valid. Two were defects in this wave's own new code, and the
most useful one is D-035: it found that this wave fixed half of the thing it
existed to fix and wrote the other half down as acceptable.

**Drift count: still 1** — no new drift; D-034 and D-035 are gaps, D-036 is a
limitation now named rather than a change.

## D-034 — `--repair` reported as a creation, on both paths

- **Touches:** wave 32's own summary work, and whatever wave first added `--repair`
- **Built:** `init --repair` said `installation <id> created for <product>`, and
  the plan said `would create an installation` beside an empty installation id —
  for a record that already exists.
- **Because:** the summary had one sentence for two operations. This wave made
  the plan half state it more explicitly, which is what made two reviewers see it.
- **Class:** `spec-gap`, and **pre-existing**: the real path said "created" on
  `main` before this branch. Fixed on both paths rather than only the one
  reported, because fixing the plan alone would have left the operation lying
  and called the review answered.
- **Consequence:** an operator reading a plan to check they are repairing the
  right machine was reading the one line that did not distinguish repair from
  first install.

## D-035 — The plan warned about directories and stayed silent about archives

- **Touches:** D-032, this wave, found in review
- **Built:** `warnPlannedDeprecations` joined `manifest.yaml` onto the release
  path. `--release` names a directory *or* a `tar.zst`, so for an archive that
  produced `demo.tar.zst/manifest.yaml`, the load failed, and the error was
  swallowed.
- **Because:** measured, with a real archive: the plan printed no warning while
  the operation printed one, about the same bundle. Two answers to one question,
  decided by which shape the vendor happened to publish.
- **Class:** `spec-gap`, and the worst kind — **the limitation was written down
  as intentional.** The function's own comment said an archive "gets no warning
  rather than an error". D-032 exists because a plan withheld this warning; this
  wave fixed the directory case and documented the archive case as acceptable,
  which is a gap converted into prose instead of into code.
- **Consequence:** now routed through `ports.ReleaseSource`, which reads either
  shape. Local references only: a registry would mean a plan pulling a bundle
  over the network to phrase an advisory. **A remote reference still gets no
  warning** — carried below, and named rather than commented away this time.

## D-036 — The between-tags exemption also exempts a build that is behind

- **Touches:** RFC 0018 row 13, written this wave
- **Built:** row 13 now names the hole.
- **Because:** `isUntaggedBuild` matches `N-g<sha>`, and a build from an *older*
  branch is stamped identically — `0.1.0-5-gabc1234` — while being genuinely
  behind rather than ahead. `git describe` cannot separate them: both are "N
  commits past some tag", and which tag is the last one is the question.
- **Class:** `spec-gap` in the row I had just written. The row already said the
  stamp is derived and understates; it did not say the derivation is also
  ambiguous in the other direction.
- **Consequence:** bounded to builds from source — a released binary sits on a
  clean tag and is held to the floor. Closing it needs a *declared* version
  rather than a derived one, which is a change to how this project versions
  itself, not to this check. Not attempted under review.

## D-037 — A test that passed for the wrong reason

- **Touches:** D-035's fix, found by the coverage gate on PR #56
- **Built:** `TestAPlanDoesNotReachForARemoteBundle`, asserting on a counting
  source rather than on the output.
- **Because:** codecov reported the patch at 75%, and the uncovered lines were
  `warnPlannedDeprecations`'s decline branches. The clitest written to cover the
  remote case asserted "no warning appears" — and **that passes for two
  different reasons**: the scheme guard declining, or a `Fetch` that fails
  because nothing serves `oci://` in a test. Measured: with the guard deleted,
  that test still passed.
- **Class:** `spec-gap` in my own test. The decision is *a plan does not go to a
  registry to phrase an advisory*, and the only observable that separates it
  from "the pull failed" is whether the source was asked at all — which the
  output cannot show.
- **Consequence:** the clitest keeps the user-visible claim and the internal
  test pins the mechanism. The mutation that survived now dies.

## Rules distilled

- **An output assertion cannot tell a guard from a failure downstream of it.**
  Both produce silence. When a decision is "do not attempt X", the test has to
  observe the attempt, not the result. (D-037)
- **A limitation written into a comment is a gap that has stopped being
  counted.** The archive case was documented as acceptable in the same wave
  whose whole purpose was that a plan must not withhold this warning. Ask
  whether a comment explaining a restriction is describing a decision or
  excusing an omission. (D-035)
- **Fixing the half a reviewer saw leaves the other half lying.** `--repair`
  said "created" on the real path too, and only the plan was reported. (D-034)
- **A row is written the day you know least about it.** Row 13 was authored and
  reviewed in the same wave, and review found a direction the author had not
  considered. (D-036)
- **An assertion on the sentence is not an assertion on the contract.** The
  summary and `data.product` say the same thing to two different audiences, and
  the parsed one had no test. (A-1)
- **A carried proposal ages against the code it describes.** D-023 sat unruled
  for three waves while a review invalidated its wording, and nothing in the
  log's format shows that — the entry looks as fresh as the day it was filed.
  Re-read the code before accepting a proposal, not just the proposal. (D-023)
- **A rule written about commands does not cover modes.** Decision 18 named
  `verify`, `init` and `update`; `--dry-run` is a mode of one of them, and fell
  through. Ask which *modes* a rule about commands reaches. (D-032)
- **One object holding a derived value and a blank one is the fastest proof
  available.** `etc_dir: /etc/web` beside `product: ""` settled where the defect
  was, and cost one command. (D-031)
- **Prose defects survive because nothing parses prose.** Two verbs collided in
  a warning that shipped, ran in an acceptance log, and was read by nobody —
  including the author who wrote both. (D-033)

## Carried into the next unit

- ~~**The RFC 0018 proposal from wave 29's D-023**~~ — accepted, narrowed.
- **The removal of `runtime:` in 0.4.0** — a dated commitment nothing enforces.
  Now the oldest carried item in this file.
- **`Installation.Providers`** — still declared, still unwritten (D-011).
- **P1b item 4** — still behind a bootable venue, still the only thing before
  **P3, the Quadlet adapter**.
- **Two settle-window fragilities**, carried from waves 28 and 29.
- **`saveInstallation` writes its report before the state store** (wave 31).
- **A plan over a remote reference still carries no deprecation warning** — new
  in review (D-035). Local shapes are covered; `oci://` and `https://` are not,
  because a plan that pulls a bundle to phrase an advisory is a cost nobody
  asked a plan for. Named here rather than left in a code comment.
- **`operation.status` reports `succeeded` for a dry run whose steps are all
  `pending`**, new here and deliberately not fixed: it is a machine-readable
  field RFC 0026's read model may consume, so changing it is a design question
  rather than a bugfix.

# Wave 33 · The environment file at boot

Branch `feature/wave-33-the-environment-file-at-boot`. RFC 0023 P1b item 4.

A spike. The deliverable is a measurement, not an implementation — `irreducible`
in the classification below: no amount of design settles what systemd does at
boot, and a guess would have been carried into P3's unit graph where discovering
it wrong costs a released adapter.

**Drift count: 0.** Nothing was built that a document settled otherwise. No
production code changed.

## D-038 — Item 4 answered a different question than it asked

- **Touches:** RFC 0023 §4.3, §12 item 4, decision 21 (new)
- **RFC said:** the rendered `EnvironmentFile` "must survive a reboot before the
  unit starts or the unit fails on a cold boot", and called this "the single
  hardest problem in the RFC".
- **Measured:** a tmpfs is empty at every boot by definition. The file never
  survives a reboot and never had to. What decides the outcome is whether the
  unit reading it is ordered behind whatever renders it, and what an absent file
  does when it is not.
- **The mechanism**, on the host (systemd 261) and again in the venue:
  `EnvironmentFile=` fails the unit before the process runs
  (`Failed to load environment files`, `Result=resources`); `EnvironmentFile=-`
  starts it, reports success, and runs with the parameter empty.
- **The ordering**, across four independent boots: an unordered unit with the
  dash **started and finished before the render unit had written the file**, and
  systemd marked it active.

  ```
  12:19:48.417342  Starting product B (unordered, dash)...
  12:19:48.417797  Starting render parameters (stands in for apply --startup)...
  12:19:48.423114  Finished product B (unordered, dash).
  12:19:48.425017  Finished render parameters (stands in for apply --startup).
  ```

  `B_PORT=[]` on a unit reporting success is the whole finding.
- **Class:** `irreducible`, and it behaved like one. The item had been carried
  since wave 26 because it could not be reasoned about, and one boot settled it
  — while also reversing the premise the section was written on.
- **Consequence:** decisions 21 and 22. The prefix is the difference between a
  deployment that refuses and one that lies (21, LOCKED), and P3's generated
  units owe the file's presence before any unit that reads it starts (22,
  ASSUMED) — the invariant, not a mechanism, which is the correction D-042
  records. Compose satisfies it for free, because its product unit's
  `ExecStart` *is* `apply --startup`. **P1b is complete and P3 is no longer
  gated.**

## D-039 — The venue is not the one the item asked for

- **Touches:** RFC 0023 §12 item 4
- **RFC said:** a venue that can be booted on demand.
- **Built:** a privileged container running systemd as PID 1, not
  `systemd-nspawn`.
- **Because:** nspawn needs root, and this environment has no password-less
  sudo. The substitute runs the same systemd, mounts `/run` as a real tmpfs
  (`rw,nosuid,nodev,noexec,relatime,mode=755`), and executes a real boot
  transaction, which is what the ordering question needs. It boots in about a
  second, which is what the item actually wanted when it rejected a workstation
  — "the answer costs a reboot per attempt".
- **Class:** `spec-gap` in the item's own phrasing: it named a tool where it
  meant a property.
- **Consequence, stated rather than buried:** this is not bare metal. It answers
  unit ordering and unit-start semantics. It cannot speak to firmware, initrd,
  or real device mounts, and was asked nothing about them. A future item that
  needs those needs a different venue, and item 4's answer does not cover it.
- **Deliberately not applied:** waiting for hardware. The two halves item 4
  named were both answerable here, and holding a four-wave blocker for a venue
  that would answer the same questions identically is a cost with no finding
  attached.

## D-040 — The spike is kept, because a measurement nobody can re-run is a claim

- **Touches:** `spikes/environmentfile-at-boot/`
- **Built:** the venue, its units and a `run.sh`, committed.
- **Because:** every number in item 4 came out of it, and an RFC that cites
  measurements no reader can reproduce is asking to be trusted rather than
  checked. Re-run from a clean slate before committing: images removed, script
  run, same three outcomes.
- **Class:** `discovery` — only building it revealed how cheap repeated boots
  are, which is what makes keeping it worthwhile rather than archiving a
  transcript.
- **Consequence:** it is not wired into `just ci`. It needs a privileged
  container and answers a question that is now settled; running it on every
  commit would spend a minute to re-derive a locked decision.

## D-041 — A shell script the shell linter did not look at

- **Touches:** `justfile`, found while committing D-040
- **Built:** `just shellcheck` now covers `spikes/*/*.sh`.
- **Because:** the recipe checked `install.sh` and `.github/scripts/*.sh`, so it
  passed green over a script it had never read. Committing the spike added a
  shell script to the repository that the repository's own shell gate did not
  see — and `just shellcheck` reporting success is exactly what would stop
  anybody from checking by hand.
- **Class:** `spec-gap`. The recipe enumerated the two places shell scripts
  lived when it was written, which is a list that silently stops being complete.
- **Consequence:** verified by mutation rather than assumed — an unquoted
  `$(dirname $0)` in the spike is now caught (SC2046, SC2086), and was not
  before.

## D-042 — A spike locked a mechanism it had not built

- **Touches:** RFC 0023 decision 21 as first written, split into 21 and 22
- **Built (first):** one row, `LOCKED`, saying the parameter file is referenced
  without the `-` prefix **and** that any unit reading it is ordered behind
  whatever renders it.
- **Built (now):** two rows. 21 keeps the prefix, `LOCKED`. 22 states the
  invariant — a reader must not start before the file exists — as `ASSUMED`,
  with the mechanism left to P3.
- **Because:** the single row graded a measured fact and an unbuilt design
  obligation the same way. The prefix is systemd's behaviour, observed four
  times; ordering-behind-the-render is one mechanism among several, and it was
  inferred from the fixture that happened to be convenient to write. A generator
  emitting its own ordering, `RequiresMountsFor=`, rendering earlier in the
  boot, or Quadlet inlining values instead of referencing a file would all
  satisfy the same invariant. A spike that has built no adapter is the
  worst-placed thing to foreclose the design of one.
- **Class:** `spec-gap` in the row, and the failure mode `flag-dont-flip` names
  outright: grade inflation. LOCKED rows earn their force by being rare, and one
  sitting beside a row that did not need it weakens the one that did.
- **Consequence:** row 21 also gained a scope it was missing — it bans the
  prefix on the *parameter* file, not everywhere, because `-` stays right for a
  genuinely optional file such as an operator override drop-in. The first
  version would have forbidden a pattern nothing here objects to.
- **Found by:** the author's review of the proposal, which is the step this wave
  had skipped — see below.

## Reconciliation — 2026-08-19

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | 21 | **Accepted, scoped** | `LOCKED` | The `EnvironmentFile` carrying parameters is referenced without the `-` prefix | D-038 |
| 0023 | 22 | **Accepted, regraded** | `ASSUMED` | A reader must not start before the file exists; the mechanism is P3's | D-038, D-042 |

**This wave got the reconciliation backwards, and the entry above exists because
of it.** Rows 3, 13, 14 and 20 were each put to the author before they were
written. Row 21 was written straight into the RFC, `LOCKED`, on the strength of
a measurement — and a measurement is exactly what makes an executor most
confident and least inclined to ask. The author's ruling split it, downgraded
half, and scoped the half that survived, none of which the log would have
recorded had the row simply been committed.

**One alternative was declined:** locking the ordering mechanism now, on the
argument that P3 will need one anyway. Declined because "will need something
here" is not the same as "must use this", and the RFC has no way to distinguish
them once a row says LOCKED.

## D-043 — The spike claimed reproducibility and floated

- **Touches:** D-040, `spikes/environmentfile-at-boot/`, found in review
- **Built:** the base image pinned by digest, and a build-time assertion that
  the venue's systemd is the version the measurement was taken on.
- **Because:** D-040's whole argument for committing the spike is that "a
  measurement nobody can re-run is a claim". It was built `FROM
  archlinux:latest` with an unpinned `pacman -Sy systemd`, so a re-run could
  quietly boot a different userspace and a different systemd, and report the
  result as the same experiment. That is worse than not keeping the venue: the
  second answer arrives wearing the first one's authority.
- **Class:** `spec-gap`, and a self-inflicted one — the entry arguing for
  reproducibility shipped the mechanism that would have broken it.
- **Consequence:** the venue is pinned to a digest and **fails the build** if
  systemd is not 261, rather than silently measuring something else. Verified by
  re-running the whole spike from a clean slate against the pinned image: same
  three outcomes, same ordering, which is the fourth boot and the one that makes
  the pin evidence rather than an assertion.
- **Also:** the runner used a fixed container name, so two invocations could
  destroy each other's container — or an unrelated one that happened to share
  the name. Now unique per run.

## D-044 — A row that would have sent P3 at the wrong mechanism

- **Touches:** RFC 0023 row 22, PR #59 review round 2
- **Built:** `RequiresMountsFor=` removed from row 22's list of mechanisms, with
  a note saying why it is not one.
- **Because:** row 22 lists the ways P3 might satisfy "the file exists before a
  reader starts", and `RequiresMountsFor=` is not one of them.
  systemd.unit(5): it "automatically adds dependencies of type `Requires=` and
  `After=` for all **mount units** required to access the specified path". That
  orders the reader after `/run` is *mounted*, which it already is when the race
  runs, and says nothing about whether anything has written into it. A P3 that
  picked it off this list would have shipped the exact race the row exists to
  prevent, believing the row had blessed it.
- **Class:** `spec-gap` in a row written one commit earlier. The row was
  authored to stop a spike foreclosing P3's design, and then offered P3 a
  mechanism that does not work — a list assembled from plausibility rather than
  from the manual page.
- **Consequence:** the wrong entry is named as wrong rather than deleted, since
  it is the one a reader is most likely to reach for independently.

## D-045 — Claiming more than the venue measured

- **Touches:** RFC 0023 row 21 and §12 item 4, and this log's distilled rules
- **Built:** three claims narrowed to the unit-level signal that was observed.
- **Because:** the spike measured that systemd reports a unit started while its
  parameter is empty. It did **not** measure what any health check does with an
  empty parameter — nothing here ran one. "Every health surface reports as fine"
  and "the one outcome nothing downstream can detect" were extrapolations, and
  they sat inside a LOCKED row, which is the worst place to keep an unmeasured
  claim: the row's authority is the measurement.
- **Class:** `spec-gap`. Review named two locations; there were three, the third
  being the distilled rule, which is the one that travels furthest.
- **Consequence:** the argument for LOCKED is unchanged and now rests only on
  what was seen — a false green at the unit level, with downstream detection
  depending on something validating the parameter, which nothing guarantees.

## D-046 — The same number, wrong in a fourth place

- **Touches:** wave 31's D-030, unapplied by this wave
- **Built:** every boot-count claim in the tree found by one grep and aligned.
- **Because:** the ordering boot count moved from three to four when pinning the
  venue forced a re-run, and it is written in five places. Review caught the
  RFC's copy, then the self-audit table's, then the distilled rule's — three
  rounds, each fixing the instance that was pointed at.
- **Class:** `drift` against this file's own recorded practice, which is the
  uncomfortable part. **D-030 distilled exactly this rule one wave ago** —
  *grep the claim, not the diff* — after a status was edited in three places and
  left stale in three others. It was written down, and then not applied to a
  number that had just changed.
- **Consequence:** the sweep is done properly now, across `rfcs/` and the
  spike's README. What the rule was missing is the trigger: it says what to do
  and not when, so it only fires for somebody already suspicious. The version
  worth keeping is that **a number that changes is a number to grep for**, at
  the moment it changes rather than at the moment somebody objects.

## Self-audit — 2026-08-19

Scope: the whole branch — one commit, no production code. A spike, so the audit
is of the *measurement*: whether the venue answers the question asked, whether
the RFC text claims more than was observed, and whether the numbers reproduce.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Medium | The claim "B started before the render" is a **race outcome, not a determinism**. Unordered units have no guaranteed order, and B won four times out of four, counting the post-pin re-run — but it could lose, in which case the unit succeeds with the correct value. Recorded, because it makes the hazard *worse* rather than milder: an intermittent silent misconfiguration is harder to catch than a reliable one. | Fixed — stated in item 4 and in row 21 |
| A-2 | Low | The venue's `/run` is mounted by Docker (`--tmpfs /run`), not by systemd as it would be on a host. The conclusion is unaffected — what matters is that it is a tmpfs and empty when the transaction begins, which held either way — but the mount's provenance differs from bare metal and the item should not imply otherwise. | Fixed — named in item 4's venue paragraph |
| A-3 | Low | The product units are `Type=oneshot` shell commands rather than containers. Deliberate: `EnvironmentFile` semantics are systemd's and do not depend on what `ExecStart` runs. Worth stating so a reader does not take the spike for a Quadlet rehearsal. | Fixed — stated in the spike's README |

**No sabotage sweep of production code**, because none changed. The one gate this
wave did touch was mutated instead: an unquoted `$(dirname $0)` in the spike is
caught by the extended `just shellcheck` and was not before (D-041).

**The container lane went red on its first run**, and it is recorded rather than
replaced by the green re-run. `TestTCPProbeAgainstRedis` failed under the full
lane and passed alone in 0.661s — the **third wave it has failed in (29, 32 and
33), and they are not consecutive**: waves 30 and 31 ran the lane clean. Counted
from the log rather than from memory, after review proposed "fourth" and the
entry it was correcting said "three consecutive" — both wrong, in different
directions. It failed here on a branch that changes no Go code at all. That it fails here, of all branches, is the
clearest evidence yet that it is measuring Docker's teardown rather than the
product, and it is now the longest-running unfixed finding in this file.

## Rules distilled

- **A measurement is when an executor is least inclined to ask.** Confidence
  earned by observing something transfers to the design conclusions drawn from
  it, and those are a different claim. Row 21 was written unruled precisely
  because the evidence behind it felt settled. (D-042)
- **Grade the fact and the obligation separately.** One row held systemd's
  observed behaviour and an unbuilt adapter's design, and gave both the grade
  the stronger half deserved. (D-042)
- **A race that keeps going the same way is still a race.** Four boots agreeing
  is not determinism, and the intermittent version of a silent misconfiguration
  is worse than the reliable one. (A-1)
- **A lint recipe that enumerates paths is a list that goes stale silently.**
  Adding a script to a repository does not add it to the gate, and the gate goes
  on reporting success. (D-041)
- **An unanswerable item is often a mis-asked one.** Item 4 was carried for
  seven waves as "can the file be read at boot". One boot showed the file is
  never there at boot, and the real question — who is ordered before whom — had
  been answerable all along. (D-038)
- **A silent success is worse than a loud failure, and costs one character.**
  `EnvironmentFile=-` turns a missing configuration into a running product that
  systemd reports as started. Whether anything downstream notices depends on
  something validating the parameter — which is a different claim, and not one
  this spike measured. (D-038)
- **An item that names a tool has hidden the property it needs.** "A machine
  with Podman", then "systemd-nspawn" — what it wanted was a boot that costs a
  second, and saying so would have unblocked it sooner. (D-039)
- **Keep the venue, not the transcript.** Numbers in a document are checkable
  only if the thing that produced them still runs. (D-040)
- **A kept venue must be pinned, or it re-runs a different experiment under the
  old one's name.** The entry arguing for reproducibility shipped a floating
  base image, which is the failure the entry existed to prevent. (D-043)
- **A number that changes is a number to grep for, when it changes.** D-030
  said grep the claim rather than the diff; this wave had the rule, changed a
  count, and still needed three review rounds to find all five copies. A rule
  without a trigger only fires for somebody already looking. (D-046)
- **A list of alternatives is a claim about each of them.** Row 22 offered P3
  four mechanisms and one of them does not work; naming it without checking the
  manual page would have blessed the race the row exists to prevent. (D-044)
- **A measurement's authority does not extend to what it did not measure.** The
  venue showed a false green at the unit level, and the prose turned that into
  a claim about every health surface — inside a LOCKED row. (D-045)
- **Count from the record, not from memory.** Two numbers in this wave were
  wrong in opposite directions — a boot count too low and a flake count too
  high — and a reviewer proposed a third that was wrong again. (D-043)

## Carried into the next unit

- ~~**P1b item 4**~~ — measured; **P3, the Quadlet adapter, is no longer gated**
  and is the whole of what remains in RFC 0023.
- **P3 owes the file's presence before any unit that reads it starts** (decision
  22), and owes it without the `-` prefix (decision 21). The mechanism is P3's
  to choose; the invariant is not. A constraint discovered before the adapter
  exists rather than after.
- **The removal of `runtime:` in 0.4.0** — a dated commitment nothing enforces.
  The oldest carried item, and untouched by this wave, which was the spike alone.
- **`Installation.Providers`** — still declared, still unwritten (D-011).
- **Two settle-window fragilities**, carried from waves 28 and 29;
  `TestTCPProbeAgainstRedis` has now failed in three waves — 29, 32 and 33 —
  which are not consecutive.
- **`saveInstallation` writes its report before the state store** (wave 31).
- **A plan over a remote reference still carries no deprecation warning** (D-035).
- **`operation.status` reports `succeeded` for a dry run whose steps are all
  `pending`** (wave 32).

# Wave 34 · The lane and the clock

Branch `feature/wave-34-the-lane-and-the-clock`. No RFC phase: this unit is the
carried list at the tail of wave 33, which no wave owned.

Scheduled ahead of RFC 0023 P3 deliberately, and the argument is the lane rather
than tidiness. P4 adds a second runtime's acceptance stage to a container lane
that has gone red in three of the last five waves; a red run there would then be
unattributable between the new adapter and the old fragility, and that ambiguity
costs more to resolve than the fixes cost to write.

**Drift count: 5** — D-048, D-052 and D-054 against wave 32; D-056 against
RFC 0014, pre-existing and shipped; D-057 against this wave's own first fix.

Written as 1 when the group was opened and corrected when it stopped being true,
which is the whole of D-046's rule applied to the number that measures this
practice. Four of the five were found while chasing something else — two CI
failures that both read as flakes, a fixture migration, and a test written for
the removal — and none was in the diff. That is the argument for the wave: a
unit scoped to a carried list found more than the list contained, because the
list was the part somebody had already noticed.

## D-047 — A plan's steps say `planned`, not `pending`

- **Touches:** no decision row in any RFC; the step-status vocabulary is
  internal and no document settles it. Carried from wave 32.
- **RFC said:** nothing.
- **Built:** `domain.StepPlanned`, written by `Engine.plan()`, and an explicit
  case in `FirstIncompleteStep` that refuses to resume from it.
- **Because:** `pending` means a step has not run *yet*. A plan's steps were
  never going to run, so the record was the one document in the system still
  claiming work was owed — beneath an operation status of `succeeded`, which is
  correct and stays, because planning is what succeeded. Changing the operation
  status instead would have erased the distinction between a plan that worked
  and a plan that failed validation.
- **Class:** `spec-gap`.
- **Consequence:** the value is its own rather than a reuse of `pending`, and
  that pays twice. `FirstIncompleteStep` treats `pending` as resumable, so a
  record whose steps are all pending reads as resumable from step 0 — a plan
  offered to `--resume` would run every step while reporting that it continued
  something. Unreachable today, because the engine returns from `plan()` before
  journaling and no record on disk can carry the status; now refused by
  construction rather than by that accident continuing to hold.
- **Deliberately not applied:** the plan already knows which steps will not run
  (`WillRun`, from each step's `Check`), and that could have been folded into
  the record as `StepSkipped`. It was not: `skipped` in a journaled record means
  a check reported the postcondition already held *during a run*, and reusing it
  for a prediction would put a claim about what happened into a document about
  what would. The event carries `WillRun` and `Reason` for the reader who wants
  it.
- **Proposed row:** none. A status vocabulary that no RFC settles does not
  become a decision table entry because one value was added to it; the argument
  for the value belongs where the value is, and it is in the doc comment.

## D-048 — The deferral rested on a premise that was false, and checkable

- **Touches:** wave 32's carried item, and its stated reason
- **Wave 32 said:** the dry-run status is *"a machine-readable field RFC 0026's
  read model may consume, so changing it is a design question rather than a
  bugfix"*, and left it.
- **Found:** 0026's read model cannot reach it. `Engine.Run` returns from
  `plan()` before `e.journal()` — under a comment that says so in as many words,
  *"a dry run plans and prints; it must not touch the journal"* — and
  `fleetLastOperation` sources the row from `State.LastOperation`, the journal.
  No dry-run record has ever been journaled, so no read model can consume one.
- **Class:** `drift` against wave 32. Not a wrong fix, a wrong reason: the
  premise was two file reads away in code that wave had open, and deferring on
  it converted a contained defect into a standing design question that was
  carried into two subsequent waves' *Carried* lists.
- **Consequence:** the fix was smaller than the deferral implied, which is the
  general shape worth noticing. **A deferral is a claim, and it is the kind
  nothing later re-examines** — a fix gets reviewed, a `Carried` bullet gets
  copied forward. This is the same failure as RFC 0023 §12 item 5, where "a host
  is needed for this" sat on the list defining what was unknown and went
  unattacked for four days; here it was "a consumer may read this", and it sat
  for two waves.

## D-049 — The flake was not a settle window, and the first diagnosis was wrong

- **Touches:** the fragility carried since wave 29; waves 29, 32 and 33
- **Planned:** the wave-34 plan named the *second* poll loop as the suspect —
  that the test waits up to 30s for Docker to stop accepting on the published
  port, and that teardown under load exceeds it.
- **Measured, and refuted:** teardown is **105ms idle and 119–133ms under CPU
  saturation** (24 spinners on 16 cores, five runs each). It is not the cause,
  and no amount of widening that window would have fixed anything.
- **Built:** `dockerlab.WaitGone`, and the test asks the container whether it
  stopped before asking the port.
- **Because:** the real cause is an ambiguity, not a window — the test had two
  windows already. `redis-cli shutdown` drops its own connection as the server
  goes down, so a non-zero exit is the ordinary outcome; the test therefore
  discarded the error, and in doing so made a `docker exec` that never reached
  the container indistinguishable from a shutdown that worked. When the exec
  missed, the test spent its whole 30s deadline watching a perfectly healthy
  Redis and then reported *"a stopped service was still reported healthy"*.
- **Reproduced:** by making the shutdown miss — **31.3s and those exact
  messages**, against the **30.8s** wave 32 recorded from the real failure. The
  duration is what identifies it: 30s is the deadline, and a test that fails
  because a service is genuinely unhealthy fails in under a second.
- **Class:** `discovery`.
- **Consequence:** the poll is still there and still 30s, because the port
  mapping does outlive the container. What changed is what a failure names. It
  had been reporting the prober for a fault in the fixture, which is why three
  waves each looked at it, found nothing wrong with health probing, and carried
  it. **An assertion that cannot tell which of two things failed will name the
  wrong one, and it will name the one under test** — that is what made this
  cost three waves rather than one.

## D-050 — `assert_running` counts before Docker has finished

- **Touches:** the fragility carried since wave 28
- **Built:** a 30s settle window inside the helper, at all seven call sites.
- **Because:** `docker compose stop` returns when it has *asked*; a container is
  reported running until its process is actually gone. The helper sampled once,
  immediately.
- **Class:** `spec-gap`.
- **Consequence:** the assertion is not weakened — a wrong count still fails, it
  merely has to still be wrong thirty seconds later. What it stops reporting is
  a fact about timing dressed as a fact about the deployment.

## D-051 — A plan over a remote reference will not warn, and that is now settled

- **Touches:** D-035, carried from wave 32
- **Put to the author, and refused:** whether `init --dry-run` should fetch an
  `oci://` or `https://` bundle so it can phrase a deprecation warning.
- **Because:** wave 32's reason holds and was never a compatibility argument —
  a plan is the cheap, side-effect-free path, and making it pull a remote
  artifact to phrase an advisory inverts what it is for. The measured absence of
  users, which decided the other two questions this wave, does not reach this
  one.
- **Class:** not a departure. Recorded so the item stops being carried.
- **Consequence:** closed as won't-fix rather than left open. Three waves of
  *Carried* lists is long enough for a bullet nobody intends to act on, and an
  item carried indefinitely is indistinguishable from one nobody has read.

## D-052 — `runtime:` stops being read in 0.3.0, and the grace period never existed

- **Touches:** RFC 0023 decision 18 (`LOCKED`), superseded by row 23
- **RFC said:** 0.4.0, so that 0.3.0 would be a release in which both spellings
  worked and a vendor could publish one bundle across the upgrade.
- **Found:** there is no such release and never was. `git show
  v0.2.0:internal/domain/manifest.go` has no `Runtimes` field — `runtimes:`
  ships for the first time in 0.3.0 — so 0.1.0 through 0.2.0 read only the old
  block and 0.3.0 reads only the new one. **No version reads both.** The window
  row 18 was buying did not exist when row 18 was written.
- **Built:** the removal, in 0.3.0, and put to the author as a *withdrawal*
  rather than a reschedule. Ruled on and accepted the same day.
- **Class:** `drift` against wave 32, and specifically against row 18's
  reasoning rather than its date. The row got the important half right — a
  deprecation without a clock is a word in a document — and then assumed the
  half it did not check.
- **Consequence:** priced rather than asserted. 11 amd64 downloads and 0 arm64
  across three releases, read from the release assets on 2026-08-19, most of
  them this project's own installer validation. The cost of the break is
  therefore near zero and the cost of carrying two spellings was not.
- **Also:** the author's ruling was "keep the mechanism, delete only the member",
  and the mechanism kept a global `FieldRemovalRelease` that only ever worked
  because there was exactly one member. Two fields deprecated in different
  releases cannot share it. **Left as it is deliberately** and recorded here
  instead: the shape to choose is the next deprecation's to force, and building
  it now would be a mechanism designed for a caller that has not arrived — which
  is the thing RFC 0015 and 0021 were both findings about.

## D-053 — Every fixture in the tree was written in the spelling being removed

- **Touches:** the whole test suite; RFC 0023 row 23
- **Found:** all three `testdata/*/manifest.yaml` bundles and every Go manifest
  fixture used `runtime:`. Nine packages went red on the removal.
- **Class:** `discovery`, and the most useful thing this wave measured. The
  fixtures being on the old spelling is the same fact as no released manager
  reading the new one, seen from inside the repository — the project had shipped
  `runtimes:` in wave 32 and was still testing almost everything through the
  block it had deprecated.
- **Consequence, and it is not bookkeeping:** two tests were passing for the
  wrong reason and one of them was hiding a live defect. `profilesFrom` read
  `manifest.Runtime.Profiles` directly, so from the moment `runtimes:` existed
  the `init` wizard offered **no profiles at all** to any bundle written in it —
  silently, because an empty list is also what a release with no profiles looks
  like. `TestProfilesComeFromTheBundle` passed throughout, because its fixture
  was written the old way. Migrating the fixture failed the test immediately;
  the fix is in the same wave.
- **The rule underneath it:** *a fixture written in the deprecated form tests
  the deprecated path.* A project that deprecates a surface and leaves its
  fixtures on it has not started migrating, it has only announced one — and its
  suite is measuring the path it intends to delete.

## D-054 — The path-join bug was fixed in one place and left in two

- **Touches:** wave 32's D-034; `internal/cli/commands.go`,
  `internal/cli/init_wizard.go`
- **Wave 32 found and fixed:** `warnPlannedDeprecations` joined `manifest.yaml`
  onto `--release`, which for an archive produces
  `demo.tar.zst/manifest.yaml` — a path that does not exist.
- **Found here:** the same join, unchanged, at `commands.go:62` and
  `init_wizard.go:293`. Byte-identical in `v0.2.0`, so it is shipped.
  Measured: **`morzer init --release <bundle>.tar.zst` without `--product`
  fails with `cannot read <archive>/manifest.yaml`** — on a valid archive, for
  both a plan and a real install. The archive is the shape a vendor publishes,
  so this is the primary install path.
- **Class:** `drift` against wave 32. Not for fixing the instance it found —
  that was right — but for not grepping for the others, which is **the rule this
  file distilled one wave later** as D-046: *a claim that changes is a claim to
  grep for.* The same failure, one wave apart, in the opposite direction: D-046
  was a number restated in prose, this is a mechanism restated in code.
- **Consequence:** not fixed here. It is a third defect found by a wave that was
  scoped to a carried list, and fixing it inside this one would make the wave
  unreviewable. Carried, named, and measured, which is the most a wave that
  cannot afford it should do.

## D-055 — A plan does not validate the bundle it plans against

- **Touches:** RFC 0001 decision 12; found while writing the removal's tests
- **Found:** `init --dry-run --product demo --release <legacy bundle>` reports
  *"would create an installation"* for a release the very next command refuses.
  The plan is refused only when `--product` is **absent** — because the CLI then
  has to read the manifest to learn the name, and validation comes with the
  read. An incidental mechanism, not a check.
- **Class:** `spec-gap`. RFC 0001 decision 12 settled that a plan reads the
  bundle at its source, which wave 32 built; it did not settle that a plan
  *validates* what it read, and nothing does.
- **Consequence:** the shape wave 32 named — *two answers to one question,
  decided by which shape the vendor published* — has reappeared as *decided by
  which flags the operator passed*. Recorded in the test that meets it, so the
  next reader finds it at the assertion rather than in this file.

## D-056 — `release build` copies the vendor's `.git` into the bundle it publishes

- **Touches:** RFC 0014; `internal/infra/atomicfs/copy.go`
- **Found:** chasing a CI failure that read as a settle-window flake —
  `cannot open .../bundle/.git/objects/maintenance.lock`, racing git's own
  background maintenance. The lock was the symptom. **The bundle walk does not
  exclude `.git`**, and nothing in the tree does.
- **Measured, end to end, with a seeded credential:** `release build` on a
  git-tracked bundle wrote a `SHA256SUMS` of 55 entries of which **42 were
  `.git/`**, including `.git/config` and the whole of `.git/objects`; `release
  archive` then packed all 42 into the published `tar.zst`. The signature chain
  is *signature → SHA256SUMS → every file*, and "every file" had come to mean
  the vendor's repository.
- **Not exotic:** `--version-from-git` requires the bundle to be a git repo, so
  this is the workflow the flag exists for, and this project's own test creates
  a repo at the bundle root — which is how CI met it.
- **Class:** `drift` against RFC 0014, which never distinguished a bundle
  *source tree* from what *ships* from it. Pre-existing: `v0.2.0`'s `copy.go`
  has no filter either.
- **Consequence:** wave 35, with an RFC 0014 amendment, ruled by the author on
  2026-08-19. Not fixed here — the exclusion list, whether `.gitignore` is
  honoured, and what a stricter builder does to bundles already published are
  four decisions, and a security fix reviewed under a debt wave's title is
  reviewed by nobody. **Nobody has leaked anything**: the same 11 downloads that
  priced D-052 price this.

## Rules distilled

- **A deferral is a claim, and nothing later re-examines it.** A fix gets
  reviewed; a *Carried* bullet gets copied forward. Wave 32 deferred the dry-run
  status because a read model "may consume" it, and the read model reads the
  journal a dry run never enters — two file reads away, carried for two waves.
  (D-048)
- **A grace period is a claim about what some released binary can read, and it
  is checkable against the tags.** Row 18 named a removal release on sound
  reasoning and assumed the release before it was a migration window. `git show
  <tag>:<file>` settles that in one command. (D-052)
- **A fixture written in the deprecated form tests the deprecated path.** A
  project that deprecates a surface and leaves its fixtures on it has announced
  a migration rather than started one, and its suite is measuring the path it
  means to delete. Migrating the fixtures is what turns the announcement into a
  test — here it exposed a wizard that had offered no profiles to any current
  bundle since the day the spelling landed. (D-053)
- **An assertion that cannot tell which of two things failed will name the
  wrong one — and it names the one under test.** The Redis probe reported "a
  stopped service was still reported healthy" when the shutdown had not landed,
  so three waves each looked at health probing, found it correct, and carried
  the finding. (D-049)
- **Fixing the instance is half the fix; the other half is the grep.** D-046
  distilled this for a number and this wave found it true of a mechanism: wave
  32 fixed one path-join and left two, one of which breaks the primary install
  path. (D-054)
- **A flake in a lane is a hypothesis about the lane, not a fact about it.**
  Both of this wave's flakes were real defects wearing a flake's clothes — a
  swallowed error, and a `.git` directory in a published archive. (D-049, D-056)

## Carried into the next unit

- **The `.git` leak — wave 35**, with an RFC 0014 amendment (D-056). Ruled.
- **The path-join in `commands.go` and `init_wizard.go`** (D-054). `init
  --release <archive>` without `--product` is broken on a shipped release.
- **A plan does not validate the bundle it plans against** (D-055).
- **`FieldRemovalRelease` is a single-member design with no members** (D-052).
  The next field deprecation is what should force its shape.
- **`Installation.Providers`** — still declared, still unwritten (D-011). Ruled
  2026-08-19: **not gated on P3**, which was this file's own framing and did not
  survive checking. The state decodes with plain `json.Unmarshal`, so removing
  it needs no migration; what gates it is whether `installation describe`'s
  output may lose a field, which is RFC 0027's question. Wave 36.
- ~~**The removal of `runtime:` in 0.4.0**~~ — done, one release early (D-052).
  The oldest carried item in this file, closed.
- ~~**Two settle-window fragilities**~~ — both closed (D-049, D-050), and
  D-049's first fix was itself wrong (D-057): it named the cause correctly and
  left it in place.
- ~~**A plan over a remote reference carries no deprecation warning**~~ — closed
  as won't-fix (D-051).
- ~~**`operation.status` reports `succeeded` for an all-`pending` dry run**~~ —
  fixed (D-047).
- **`saveInstallation` writes its report before the state store** (wave 31).
  Untouched, and now the oldest carried item in this file.

## Reconciliation — 2026-08-19

| RFC | row | outcome | grade | decision | from |
|---|---|---|---|---|---|
| 0023 | 23 | **Accepted** | `LOCKED` | `runtime:` stops being read in 0.3.0; a withdrawn compatibility promise rather than a moved date, because no released manager reads `runtimes:` | D-052 |
| 0023 | 18 | **Superseded** | `LOCKED` | Left in the table unedited. An append-only table that rewrites the row it replaces has stopped recording that a decision changed | D-052 |
| 0014 | — | **Deferred to wave 35** | — | A bundle source tree is not what ships from it; the exclusion list, `.gitignore`, and already-published bundles are wave 35's to settle | D-056 |
| 0027 | — | **Ruled, unscheduled** | — | `Installation.Providers` is 0027's question, not 0023's, and not gated on P3 | D-011 |
| — | — | **Refused** | — | Making `init --dry-run` fetch a remote bundle to phrase a deprecation advisory | D-051 |

No row is proposed for `StepPlanned` (D-047): a step-status vocabulary that no
RFC settles does not become a decision row because one value was added to it.
The argument for the value belongs where the value is.

## Self-audit — 2026-08-19

Scope: the whole branch — the engine's plan record, two test lanes, the manifest
removal and every fixture behind it, three documentation pages and the
changelog. `just ci` green at **86.6%** (floor 84), `docs-check` 41 pages / 55
checks, acceptance passed, container lane run.

**Sabotage sweep: 9 mutations, 8 killed.** The survivor is `case StepPlanned:`
in `FirstIncompleteStep`, and the why is the finding rather than the survival:
it is behaviourally identical to the `default:` branch beside it, which also
refuses. It exists so that branch's comment — *"a status this build does not
recognise"* — stays true of a status this build defines. Recorded rather than
deleted, and recorded rather than counted as covered.

| # | Severity | Finding | Status |
|---|---|---|---|
| A-1 | Major | **The `init` wizard offered no profiles to any bundle on the current spelling**, since the day that spelling landed. `profilesFrom` read the deprecated block directly, and an empty list is also what a release with no profiles looks like, so it failed silently. | Fixed — D-053, verified red the moment the fixture moved |
| A-2 | Major | **`init --release <archive>.tar.zst` without `--product` is broken on a released binary** — the same path-join wave 32 fixed in one place and left in two. | Open — D-054, carried |
| A-3 | Major | **`release build` writes the vendor's `.git` into the bundle it publishes**, `.git/config` included, and `release archive` ships it. | Open — D-056, wave 35, ruled |
| A-4 | Medium | **A plan does not validate the bundle it plans against**, and is refused only when `--product` is absent, by accident of the CLI needing the manifest for the name. | Open — D-055, recorded at the assertion that meets it |
| A-5 | Medium | The legacy refusal emitted a **second error contradicting the first**: a vendor who wrote `runtime:` was told they had declared no runtime. | Fixed, with the test that was missing for it |
| A-6 | Low | Four symbols left with no callers by the removal — `legacyProjectOption`, `RuntimeSpec.isZero`, and the suite's `warnings`/`contains`. **Found by lint, not by reading the diff**, which is the argument for the linter: none appears in any hunk that stopped using it. | Fixed |
| A-7 | Low | `DeclaredRuntimes` now returns the manifest's own map rather than a fold-built one, and `RuntimeConfig.Options` carries it to every adapter method uncloned. **Pre-existing and unchanged in practice**: every manifest that still loads was already on the spelling that took this path, and the fold's fresh map only ever protected the spelling being deleted. | Open — observation, not this wave's to decide |

**A-1 is the wave's most useful finding and it was free.** Nothing looked for it;
migrating a fixture off a deprecated spelling failed the test that had been
guarding it, and the defect was underneath. The generalisation is in *Rules
distilled*, and it is the one worth carrying: a suite whose fixtures use the
deprecated form is measuring the path the project intends to delete.

**Three of the four Majors were found while chasing something else** — two CI
failures that both read as flakes, and a fixture migration. None was in the
diff. That is the argument for treating a red lane as a hypothesis rather than
an inconvenience, and it is why this wave was scheduled before RFC 0023 P3
rather than after.

## D-057 — The first fix made the flake diagnosable, not fixed

- **Touches:** D-049, this wave, corrected by this wave
- **D-049 said:** `dockerlab.WaitGone` was the fix — ask the container whether
  it stopped before asking the port.
- **Found, by running the lane it was written for:** the test failed again, and
  the message was the new one — *"the request to stop it did not land, which is
  a fault in the fixture and not in whatever is being probed"*. So the diagnosis
  was **confirmed in the wild rather than only in simulation**, and the fix
  addressed the wrong half: it made the failure name its cause and left the
  cause in place.
- **Built:** `dockerlab.Stop`, which stops the container from outside. Nothing in
  the probe's claim needs the service to stop itself — what is asserted is that
  a TCP check reports a vanished port as refused, and how it went is the
  fixture's business. `docker exec` has to schedule a process in a container on
  a busy host; `docker stop` has no such step.
- **Class:** `drift` against this wave. The evidence for the *diagnosis* was
  strong — a simulated miss reproduced the recorded 30.8s to within half a
  second — and I let that stand in for evidence about the *remedy*, which it
  never was. Attributing a failure correctly and preventing it are two changes,
  and the first one feels like both.
- **Consequence:** the full lane is green and **one green run is weak evidence
  for a flake that failed in three of six waves.** What is worth more is that
  the mechanism is gone rather than widened: there is no longer an in-container
  step that can fail to land, so the failure mode is removed by construction
  instead of given a longer deadline. If it returns it will be something else,
  and it will say so.
