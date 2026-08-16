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

Three of the six entries are `spec-gap` (D-003, D-004, D-005) and all three are
findings against the design process rather than against this unit. Recorded as
such rather than softened to `discovery`, which is the class an executor grading
their own work reaches for: every one of them was answerable from the repository
before a Podman host existed, and the reason they were not answered is that each
sat on a list that said hardware was required.

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

## Decision-row outcomes

**Nothing has been ruled on yet.** D-001, D-002, D-003 and D-006 carry proposals
outstanding as of 2026-08-16; D-004 and D-005 propose no row. This section exists
without an accepted entry on purpose — a proposal nobody has accepted or refused
should be visible as such, and a log without this heading cannot tell that state
from a proposal that was quietly adopted.

| RFC | Row | Outcome | Grade | Decision | From |
|---|---|---|---|---|---|
| 0023 | — | Outstanding | — | Ingest pulls with TLS verification disabled, scoped to the loopback command | D-001 |
| 0011 | — | Outstanding | — | Registry or `oci:` transport for a Quadlet ingest, pending digest fidelity | D-002 |
| 0010 | — | Outstanding | — | Helper-container capture under every runtime; unreadability is the runtime's, not the design's | D-003 |
| 0023 | — | Outstanding | — | A rootless runtime requires a lingering account; `doctor` reports its absence | D-006 |

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
  readability were observed on this machine's setup, and neither was varied.

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
- **Whether P2 is gated at all** (D-005), which is the author's call and which
  changes what the next wave even is.
- **The development machine's Docker daemon is 29.6.2 while its client is
  29.7.2** — a live upgrade that has not been restarted into, caught in the
  User-Agent on the wire during D-001's measurement. `just test-docker` and `just
  acceptance` should be re-baselined after the next reboot, so that a Podman
  finding is never confused with a Docker upgrade regression.
